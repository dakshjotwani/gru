package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/artifacts"
	"github.com/dakshjotwani/gru/internal/ingest"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/controller"
	claudecontroller "github.com/dakshjotwani/gru/internal/controller/claude"
	"github.com/dakshjotwani/gru/internal/devices"
	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/command"
	"github.com/dakshjotwani/gru/internal/env/host"
	"github.com/dakshjotwani/gru/internal/journal"
	"github.com/dakshjotwani/gru/internal/publisher"
	"github.com/dakshjotwani/gru/internal/push"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/supervisor"
	"github.com/dakshjotwani/gru/internal/tailer"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"github.com/spf13/cobra"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
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

	// State pipeline rev 2: the publisher tails the events projection
	// (written by per-session tailers) instead of being pushed to by an
	// HTTP handler.
	pub := publisher.NewPublisher(s)

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

	// Artifact manager: on-disk under <stateDir>/artifacts, default caps
	// from the design doc. The HTTP upload + download handlers and the
	// gRPC List/Delete handlers all share this single manager so caps
	// and on-disk lifecycle are consistent.
	artifactRoot := filepath.Join(stateDir(), "artifacts")
	artifactMgr, err := artifacts.NewManager(s, artifactRoot, artifacts.DefaultCaps(), pub)
	if err != nil {
		return fmt.Errorf("init artifact manager: %w", err)
	}
	svc.SetArtifactManager(artifactMgr)
	// Boot-time orphan sweep: removes session-id directories whose row is
	// gone, and bin/tmp files without a matching artifact row. Errors are
	// logged inside the sweeper; not fatal to startup.
	if err := artifactMgr.SweepOrphans(context.Background()); err != nil {
		log.Printf("artifacts: boot sweep: %v", err)
	}

	// Start process liveness supervisor in the background.
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Tailer manager — the producer side of the state pipeline. Spawns
	// one goroutine per non-terminal session that tails Claude's
	// transcript JSONL, applies state derivation, and writes both the
	// events projection and the derived sessions row in one
	// transaction.
	// NOTE: tailer.NewManager appends ".gru/notify/" and
	// ".claude/projects/" to the path it's given — pass the *user's
	// home dir*, not the gru state dir. Passing stateDir() (which is
	// already ~/.gru) once produced doubled-up paths like
	// ~/.gru/.gru/notify/ and the tailers read empty files.
	homeDir, _ := os.UserHomeDir()
	tailerMgr := tailer.NewManager(s, pub, homeDir)
	svc.SetTailerManager(tailerMgr)
	if err := tailerMgr.Start(serverCtx); err != nil {
		log.Printf("tailer manager start: %v (continuing; sessions will be tailed on next launch)", err)
	}
	defer tailerMgr.StopAll()

	// Run the publisher's fan-out loop.
	go pub.Run(serverCtx)

	// Ensure the journal singleton is up before the supervisor begins reconciling.
	if err := journal.Ensure(serverCtx, s, ctrlReg, cfg); err != nil {
		log.Printf("journal: ensure failed at startup: %v (supervisor will retry)", err)
	}

	// Supervisor → tailer routing in rev-3: the supervisor appends a
	// process_exited event to the per-session ingest log via
	// ingest.Append; the tailer reads its log on the next drain and
	// applies derivation. Same path as Claude hooks; durable across
	// server restarts (the supervisor's reconcile loop also re-emits
	// on startup if the row is still non-terminal).
	sv := supervisor.New(
		&supervisorStoreAdapter{store: s},
		func(sessionID string, ev ingest.Event) error {
			return ingest.Append(homeDir, sessionID, ev)
		},
		10*time.Second,
	)
	sv.SetJournalRespawner(&journalRespawner{store: s, reg: ctrlReg, cfg: cfg})
	go sv.Run(serverCtx)

	mux := http.NewServeMux()

	// gRPC + grpc-web via connect-go (single port, no Envoy).
	grpcPath, grpcHandler := gruv1connect.NewGruServiceHandler(svc,
		connect.WithCompressMinBytes(1024),
	)
	mux.Handle(grpcPath, grpcHandler)

	// Note: /events HTTP endpoint is gone in rev 2 — Gru no longer
	// receives hook posts over HTTP. Producers (Claude Code) write
	// directly to filesystem files (transcript JSONL + the residual
	// notify file); tailers read.

	// Artifact upload (multipart) + download (capability-token GET). Both
	// share the artifact manager wired above. The download path is the
	// only credential — anyone with the URL can fetch the bytes, anyone
	// without cannot. CORS-wide so iframes from opaque origins (sandbox="")
	// can fetch.
	mux.Handle("POST /artifacts", ingestion.NewArtifactUploadHandler(artifactMgr))
	mux.Handle("GET /artifacts/{token}", ingestion.NewArtifactDownloadHandler(artifactMgr))

	// WebSocket terminal: streams a tmux pane over a PTY.
	mux.Handle("GET /terminal/{session_id}", server.NewTerminalHandler(s))

	// Device registry + action endpoints for Web Push notifications.
	// The action resolver re-uses the gRPC SendInput handler so approve/
	// deny from the lock screen maps to a normal tmux send-keys.
	deviceReg := devices.NewRegistry(s.Queries())
	devices.Register(mux, devices.HandlerDeps{
		Registry: deviceReg,
		Resolve: func(ctx context.Context, sessionID, text string) error {
			_, err := svc.SendInput(ctx, connect.NewRequest(&gruv1.SendInputRequest{
				SessionId: sessionID,
				Text:      text,
			}))
			return err
		},
		Lookup: func(ctx context.Context, eventID string) (string, bool) {
			row, err := s.Queries().GetEvent(ctx, eventID)
			if err != nil {
				return "", false
			}
			return row.SessionID, true
		},
	})

	// Auto-generate VAPID keys on first boot. The keys are written back
	// to server.yaml so they survive restarts; rotating them would
	// invalidate every registered device, so we keep them stable.
	if cfg.Push.VAPIDPrivate == "" || cfg.Push.VAPIDPublic == "" {
		priv, pub, err := push.GenerateVAPIDKeys()
		if err != nil {
			return fmt.Errorf("generate VAPID keys: %w", err)
		}
		cfg.Push.VAPIDPrivate = priv
		cfg.Push.VAPIDPublic = pub
		if err := cfg.Save(cfgPath); err != nil {
			log.Printf("warning: persist VAPID keys to %s: %v (they'll regenerate on next boot)", cfgPath, err)
		} else {
			log.Printf("generated VAPID keys and wrote to %s", cfgPath)
		}
	}

	// /push/public-key: the PWA fetches this during registration to
	// pass as applicationServerKey to pushManager.subscribe().
	mux.HandleFunc("GET /push/public-key", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"publicKey": cfg.Push.VAPIDPublic})
	})

	// Push dispatcher: fan out qualifying events to registered devices.
	rateLimit := time.Duration(cfg.Push.RateLimitS) * time.Second
	pushDispatcher := push.NewDispatcher(push.Config{
		VAPIDPrivateKey: cfg.Push.VAPIDPrivate,
		VAPIDPublicKey:  cfg.Push.VAPIDPublic,
		Subject:         cfg.Push.Subject,
		Threshold:       cfg.Push.Threshold,
		RateLimit:       rateLimit,
	}, deviceReg, pub, s)
	go pushDispatcher.Run(serverCtx)

	// Static frontend: if a built web/dist exists, serve it under "/"
	// so the whole app (backend + PWA shell) is reachable on a single
	// port. This is what lets `tailscale serve --https=443 -> :7777`
	// front everything with one proxy rule and one cert. Must be
	// registered LAST so more-specific API handlers above win.
	if webDist := server.FindWebDist(); webDist != "" {
		mux.Handle("/", server.NewSPAHandler(webDist))
		server.LogServingStatic(webDist)
	} else {
		server.LogServingStatic("")
	}

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
		live = append(live, ls)
	}
	return live, nil
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
