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
	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/controller"
	claudecontroller "github.com/dakshjotwani/gru/internal/controller/claude"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/supervisor"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"github.com/spf13/cobra"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
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
	ingestionHandler := ingestion.NewHandler(s, adapterReg, pub)

	// Start process liveness supervisor in the background.
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	sv := supervisor.New(
		&supervisorStoreAdapter{store: s},
		&supervisorPublisherAdapter{pub: pub},
		10*time.Second,
	)
	go sv.Run(serverCtx)

	mux := http.NewServeMux()

	// gRPC + grpc-web via connect-go (single port, no Envoy).
	grpcPath, grpcHandler := gruv1connect.NewGruServiceHandler(svc,
		connect.WithCompressMinBytes(1024),
	)
	mux.Handle(grpcPath, server.BearerAuth(cfg.APIKey, grpcHandler))

	// Hook event ingestion (plain HTTP POST).
	mux.Handle("POST /events", server.BearerAuth(cfg.APIKey, ingestionHandler))

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
		if r.Status != "running" && r.Status != "starting" {
			continue
		}
		ls := supervisor.LiveSession{
			ID:     r.ID,
			Status: r.Status,
		}
		if r.TmuxSession != nil {
			ls.TmuxSession = *r.TmuxSession
		}
		if r.TmuxWindow != nil {
			ls.TmuxWindow = *r.TmuxWindow
		}
		live = append(live, ls)
	}
	return live, nil
}

func (a *supervisorStoreAdapter) MarkSessionErrored(ctx context.Context, sessionID string) error {
	_, err := a.store.Queries().UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		Status: "errored",
		ID:     sessionID,
	})
	return err
}

// supervisorPublisherAdapter adapts *ingestion.Publisher to supervisor.EventPublisher.
type supervisorPublisherAdapter struct {
	pub *ingestion.Publisher
}

func (a *supervisorPublisherAdapter) PublishCrash(_ context.Context, e supervisor.CrashEvent) {
	a.pub.Publish(&gruv1.SessionEvent{
		Type:      "session.crash",
		SessionId: e.SessionID,
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
