package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/adapter"
	claudeadapter "github.com/dakshjotwani/gru/internal/adapter/claude"
	"github.com/dakshjotwani/gru/internal/attention"
	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/controller"
	claudecontroller "github.com/dakshjotwani/gru/internal/controller/claude"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/journal"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/supervisor"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"github.com/spf13/cobra"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func runServer() error {
	cfgPath := filepath.Join(os.Getenv("HOME"), ".gru", "server.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Ensure DB directory exists.
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0700); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	pub := ingestion.NewPublisher()
	adapterReg := adapter.NewRegistry()
	adapterReg.Register(claudeadapter.NewNormalizer())

	ctrlReg := controller.NewRegistry()
	ctrlReg.Register(claudecontroller.NewClaudeController(cfg.APIKey, "localhost", "7777"))

	svc := server.NewService(s, pub)
	svc.SetControllerRegistry(ctrlReg)

	// Build the attention engine from config-provided weights. Any zero
	// field falls back to the documented defaults.
	attnWeights := attention.DefaultWeights()
	cw := cfg.Attention.Weights
	if cw.Paused != 0 {
		attnWeights.Paused = cw.Paused
	}
	if cw.Notification != 0 {
		attnWeights.Notification = cw.Notification
	}
	if cw.ToolError != 0 {
		attnWeights.ToolError = cw.ToolError
	}
	if cw.StalenessCap != 0 {
		attnWeights.StalenessCap = cw.StalenessCap
	}
	if start, full, err := cw.ParseStalenessDurations(); err != nil {
		log.Printf("attention: ignoring bad staleness duration: %v", err)
	} else {
		if start != 0 {
			attnWeights.StalenessStart = start
		}
		if full != 0 {
			attnWeights.StalenessFull = full
		}
	}
	attnEngine := attention.New(attnWeights)
	ingestionHandler := ingestion.NewHandlerWithAttention(s, adapterReg, pub, attnEngine)

	// Start process liveness supervisor in the background.
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Ensure the journal singleton is up before the supervisor begins reconciling.
	// A failure here is logged but non-fatal — the supervisor will retry with
	// backoff on the next reconcile tick.
	if err := journal.Ensure(serverCtx, s, ctrlReg, cfg); err != nil {
		log.Printf("journal: ensure failed at startup: %v (supervisor will retry)", err)
	}

	sv := supervisor.New(
		&supervisorStoreAdapter{store: s},
		&supervisorPublisherAdapter{pub: pub},
		10*time.Second,
	)
	sv.SetJournalRespawner(&journalRespawner{store: s, reg: ctrlReg, cfg: cfg})
	go sv.Run(serverCtx)

	mux := http.NewServeMux()

	// gRPC + grpc-web via connect-go (single port, no Envoy).
	grpcPath, grpcHandler := gruv1connect.NewGruServiceHandler(svc,
		connect.WithCompressMinBytes(1024),
	)
	mux.Handle(grpcPath, server.BearerAuth(cfg.APIKey, grpcHandler))

	// Hook event ingestion (plain HTTP POST).
	mux.Handle("POST /events", server.BearerAuth(cfg.APIKey, ingestionHandler))

	// WebSocket terminal: streams a tmux pane over a PTY.
	// Auth via ?token= query param (browsers can't set headers on WS upgrades).
	mux.Handle("GET /terminal/{session_id}", server.NewTerminalHandler(cfg.APIKey, s))

	// h2c enables HTTP/2 cleartext (required for gRPC without TLS).
	// CORS wraps the mux so OPTIONS preflights are answered before BearerAuth runs.
	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: h2c.NewHandler(server.CORS(mux), &http2.Server{}),
	}

	log.Printf("gru server listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
	log.Printf("API key: %s", cfg.APIKey)
	return httpServer.ListenAndServe()
}

// supervisorStoreAdapter adapts *store.Store to supervisor.SessionStore.
type supervisorStoreAdapter struct {
	store *store.Store
}

func (a *supervisorStoreAdapter) ListLiveSessions(ctx context.Context) ([]supervisor.LiveSession, error) {
	rows, err := a.store.Queries().ListSessions(ctx, store.ListSessionsParams{
		ProjectID: "",
		Status:    "",
	})
	if err != nil {
		return nil, err
	}
	var live []supervisor.LiveSession
	for _, r := range rows {
		if r.Status != "running" && r.Status != "starting" && r.Status != "idle" && r.Status != "needs_attention" {
			continue
		}
		ls := supervisor.LiveSession{
			ID:     r.ID,
			Status: r.Status,
			Role:   r.Role,
		}
		if r.TmuxSession != nil {
			ls.TmuxSession = *r.TmuxSession
		}
		if r.TmuxWindow != nil {
			ls.TmuxWindow = *r.TmuxWindow
		}
		if r.LastEventAt != nil {
			if t, err := time.Parse(time.RFC3339, *r.LastEventAt); err == nil {
				ls.LastEventAt = &t
			}
		}
		// Look up the latest event type only for running sessions — the
		// staleness heuristic is the only consumer and it only fires on
		// running. Skipping this query for other statuses keeps reconcile
		// cheap.
		if r.Status == "running" {
			if ev, err := a.store.Queries().GetLatestEventForSession(ctx, r.ID); err == nil {
				ls.LastEventType = ev.Type
			}
		}
		live = append(live, ls)
	}
	return live, nil
}

func (a *supervisorStoreAdapter) UpdateSessionStatus(ctx context.Context, sessionID, status string) error {
	_, err := a.store.Queries().UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		Status: status,
		ID:     sessionID,
	})
	return err
}

// supervisorPublisherAdapter adapts *ingestion.Publisher to supervisor.EventPublisher.
type supervisorPublisherAdapter struct {
	pub *ingestion.Publisher
}

// journalRespawner adapts the journal package to supervisor.JournalRespawner.
type journalRespawner struct {
	store *store.Store
	reg   *controller.Registry
	cfg   *config.Config
}

func (r *journalRespawner) RespawnJournal(ctx context.Context) error {
	_, err := journal.Spawn(ctx, r.store, r.reg, r.cfg)
	if err != nil {
		log.Printf("journal: respawn failed: %v", err)
	}
	return err
}

func (a *supervisorPublisherAdapter) PublishExit(_ context.Context, e supervisor.ExitEvent) {
	eventType := "session.crash"
	if e.NewStatus == "completed" {
		eventType = "session.end"
	} else if e.NewStatus == "killed" {
		eventType = "session.killed"
	}
	a.pub.Publish(&gruv1.SessionEvent{
		Type:      eventType,
		SessionId: e.SessionID,
		Timestamp: timestamppb.Now(),
	})
}

func (a *supervisorPublisherAdapter) PublishStatusChange(_ context.Context, e supervisor.StatusChangeEvent) {
	eventType := ""
	switch e.NewStatus {
	case "needs_attention":
		eventType = "notification.needs_attention"
	default:
		return
	}
	a.pub.Publish(&gruv1.SessionEvent{
		Type:      eventType,
		SessionId: e.SessionID,
		Timestamp: timestamppb.Now(),
	})
}

func newServerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Start the gru server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer()
		},
	}
}
