package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/adapter"
	claudeadapter "github.com/dakshjotwani/gru/internal/adapter/claude"
	"github.com/dakshjotwani/gru/internal/attention"
	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/controller"
	claudecontroller "github.com/dakshjotwani/gru/internal/controller/claude"
	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/command"
	"github.com/dakshjotwani/gru/internal/env/host"
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

// stateDir returns the directory Gru uses for server.yaml, logs, and the
// default db_path. Falls back to $HOME/.gru when GRU_STATE_DIR is unset.
// Overriding via env is what lets a gru-on-gru minion run a second server
// against its own state dir without touching the parent's ~/.gru/.
func stateDir() string {
	if d := os.Getenv("GRU_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), ".gru")
}

// writePortFile atomically writes "host:port" to path via a tmp file + rename.
// The port is always written as 127.0.0.1:<port> so callers get a usable URL
// even when the listener is bound to [::] or :0.
func writePortFile(path string, port int) error {
	payload := fmt.Sprintf("127.0.0.1:%d\n", port)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(payload), 0o644); err != nil {
		return fmt.Errorf("write tmp port file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename port file: %w", err)
	}
	return nil
}

func runServer(portFilePath string) error {
	cfgPath := filepath.Join(stateDir(), "server.yaml")
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

	// Build the env.Registry so LaunchSession can route per-launch to the
	// adapter declared in a spec file. "host" stays the default for launches
	// that don't carry an EnvSpec, preserving v1 behavior.
	envReg := env.NewRegistry()
	envReg.Register(host.New())
	envReg.Register(command.New())

	// Derive the port from cfg.Addr so minions get told where to POST their
	// hook events. cfg.Addr is ":7777" / ":17777" / "host:port" — strip the
	// leading colon and any host prefix to land on the port alone.
	listenPort := "7777"
	if cfg.Addr != "" {
		addr := cfg.Addr
		if idx := strings.LastIndex(addr, ":"); idx >= 0 {
			addr = addr[idx+1:]
		}
		if addr != "" {
			listenPort = addr
		}
	}

	ctrlReg := controller.NewRegistry()
	ctrlReg.Register(claudecontroller.NewClaudeController("localhost", listenPort, envReg, "host"))

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
	// Wire attention-engine rescoring into the supervisor tick so the
	// staleness signal ramps up for silent sessions (otherwise the score
	// only changes on hook arrival).
	sv.SetAttentionRescorer(&attentionRescorer{store: s, pub: pub, engine: attnEngine})
	go sv.Run(serverCtx)

	mux := http.NewServeMux()

	// gRPC + grpc-web via connect-go (single port, no Envoy).
	grpcPath, grpcHandler := gruv1connect.NewGruServiceHandler(svc,
		connect.WithCompressMinBytes(1024),
	)
	mux.Handle(grpcPath, grpcHandler)

	// Hook event ingestion (plain HTTP POST).
	mux.Handle("POST /events", ingestionHandler)

	// WebSocket terminal: streams a tmux pane over a PTY.
	mux.Handle("GET /terminal/{session_id}", server.NewTerminalHandler(s))

	// First listener follows cfg.Addr directly so --port-file + :0 flows
	// keep working (the bound port is captured from it). Additional
	// listeners on the same mux are opened on the resolved bind addresses
	// using the first listener's port.
	firstLn, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	boundPort := firstLn.Addr().(*net.TCPAddr).Port

	if portFilePath != "" {
		if err := writePortFile(portFilePath, boundPort); err != nil {
			_ = firstLn.Close()
			return fmt.Errorf("write port file: %w", err)
		}
	}

	// h2c enables HTTP/2 cleartext (required for gRPC without TLS).
	// CORS wraps the mux so OPTIONS preflights are answered first.
	httpServer := &http.Server{
		Handler: h2c.NewHandler(server.CORS(mux), &http2.Server{}),
	}

	log.Printf("gru server bound on %s (mode=%s, db=%s)", firstLn.Addr(), cfg.Bind, cfg.DBPath)

	// Fan out to additional interfaces per cfg.Bind, reusing the bound port.
	extra, err := server.ResolveBindAddrs(serverCtx, cfg.Bind, fmt.Sprintf("%d", boundPort))
	if err != nil {
		_ = firstLn.Close()
		return fmt.Errorf("resolve bind addrs: %w", err)
	}
	for _, a := range extra {
		// Skip if it's the same IP we already bound (e.g. cfg.Addr was
		// 127.0.0.1:<port>). A listen on an already-bound address
		// would fail; better to detect via the first listener's address.
		if a == firstLn.Addr().String() {
			continue
		}
		ln, err := net.Listen("tcp", a)
		if err != nil {
			// An extra interface failing is a warning, not a fatal — the
			// first listener is still good. Typical failure: tailscale
			// detected but the returned IP isn't bindable (e.g. the
			// operator has `tailscale up` pending).
			log.Printf("warning: skip extra listener on %s: %v", a, err)
			continue
		}
		log.Printf("gru server also listening on %s", ln.Addr())
		go func(ln net.Listener) {
			if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("extra listener %s exited: %v", ln.Addr(), err)
			}
		}(ln)
	}

	return httpServer.Serve(firstLn)
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

// attentionRescorer adapts the attention engine + store to supervisor's
// AttentionRescorer interface. The supervisor calls Rescore on every tick so
// long-silent sessions drift up the queue via the staleness ramp.
type attentionRescorer struct {
	store  *store.Store
	pub    *ingestion.Publisher
	engine *attention.Engine
}

func (r *attentionRescorer) Rescore(ctx context.Context, sessionID string) {
	snap := r.engine.Recompute(sessionID)
	// Nothing to do if we never saw an event for this session — the engine
	// returns a zero snapshot and we'd just overwrite score=0 with score=0.
	if snap.Score == 0 && len(snap.Signals) == 0 {
		return
	}
	if _, err := r.store.Queries().UpdateSessionAttentionScore(ctx, store.UpdateSessionAttentionScoreParams{
		AttentionScore: snap.Score,
		ID:             sessionID,
	}); err != nil {
		log.Printf("attention: rescore %s: %v", sessionID, err)
		return
	}
	r.pub.Publish(&gruv1.SessionEvent{
		Type:      "attention.rescored",
		SessionId: sessionID,
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
	var portFilePath string
	c := &cobra.Command{
		Use:   "server",
		Short: "Start the gru server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(portFilePath)
		},
	}
	c.Flags().StringVar(&portFilePath, "port-file", "",
		"After bind, atomically write 'host:port' to this path. Required for "+
			"ephemeral-port flows (addr: :0) so callers can discover the real port.")
	return c
}
