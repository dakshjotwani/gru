# Gru Phase 1a — Backend Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish the Go project skeleton, protobuf API contract, SQLite schema, event ingestion HTTP endpoint, gRPC service, and auth middleware — everything Phase 1b/1c/1d depend on.

**Architecture:** Single Go binary using connect-go to serve both gRPC (native + grpc-web) and plain HTTP (event ingestion from hooks) on one port. SQLite in WAL mode with sqlc-generated type-safe queries. Pub/sub broadcaster pushes events to active `SubscribeEvents` streams.

**Tech Stack:** Go 1.26+, `connectrpc.com/connect`, `google.golang.org/protobuf`, `modernc.org/sqlite`, `sqlc`, `buf`, `golang.org/x/net/http2/h2c`

**Note:** Module path uses `github.com/dakshjotwani/gru`. buf and sqlc are pinned as module tools (`go tool`) — no global install needed.

**Prerequisites:** Go 1.26+ (uses `go tool` directive introduced in 1.24).

---

## File Map

```
go.mod
go.sum
Makefile
buf.yaml
buf.gen.yaml
proto/gru/v1/gru.proto                   # API contract (source of truth)
proto/gru/v1/*.pb.go                     # generated — do not edit
proto/gru/v1/gruv1connect/*.go           # generated — do not edit
internal/config/config.go                # server config from ~/.gru/server.yaml
internal/config/config_test.go
internal/store/migrations/001_init.sql   # SQLite schema
internal/store/queries/sessions.sql      # sqlc queries
internal/store/queries/events.sql
internal/store/queries/projects.sql
internal/store/sqlc.yaml
internal/store/db/                       # sqlc generated — do not edit
internal/store/store.go                  # SQLite connection, WAL, migrations, pub/sub
internal/store/store_test.go
internal/adapter/normalizer.go           # EventNormalizer interface + GruEvent types
internal/ingestion/handler.go            # POST /events HTTP handler
internal/ingestion/handler_test.go
internal/server/auth.go                  # API key middleware for connect-go
internal/server/auth_test.go
internal/server/service.go               # GruService gRPC implementation
internal/server/service_test.go
cmd/gru/main.go                          # entry point
cmd/gru/server.go                        # `gru server` subcommand wiring
```

---

### Task 1: Project Scaffold

**Files:**
- Create: `go.mod`, `Makefile`, `buf.yaml`, `buf.gen.yaml`
- Create: `cmd/gru/main.go` (stub)

- [ ] **Step 1: Initialize the Go module and pin tools**

```bash
cd /path/to/gru
go mod init github.com/dakshjotwani/gru
go get -tool github.com/bufbuild/buf/cmd/buf@latest
go get -tool github.com/sqlc-dev/sqlc/cmd/sqlc@latest
```

Expected: `go.mod` created with `module github.com/dakshjotwani/gru`, `go 1.26`, and `tool` directives for buf and sqlc. Run `go tool buf version` and `go tool sqlc version` to verify.

- [ ] **Step 2: Create directory structure**

```bash
mkdir -p proto/gru/v1 \
         internal/config \
         internal/store/migrations \
         internal/store/queries \
         internal/store/db \
         internal/adapter \
         internal/ingestion \
         internal/server \
         cmd/gru
```

- [ ] **Step 3: Create `buf.yaml`**

```yaml
version: v2
deps:
  - buf.build/googleapis/googleapis
```

- [ ] **Step 4: Create `buf.gen.yaml`**

```yaml
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: github.com/dakshjotwani/gru
plugins:
  - remote: buf.build/protocolbuffers/go
    out: .
    opt: paths=source_relative
  - remote: buf.build/connectrpc/go
    out: .
    opt: paths=source_relative
```

- [ ] **Step 5: Create `Makefile`**

```makefile
.PHONY: proto sqlc build test generate lint

proto:
	go tool buf generate

sqlc:
	go tool sqlc generate -f internal/store/sqlc.yaml

build:
	go build ./cmd/gru/...

test:
	go test ./...

generate: proto sqlc

lint:
	go tool buf lint
	go vet ./...
```

- [ ] **Step 6: Create stub `cmd/gru/main.go`**

```go
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fmt.Println("gru — not yet implemented")
	return nil
}
```

- [ ] **Step 7: Verify it builds**

```bash
go build ./cmd/gru/...
```

Expected: no errors, binary produced.

- [ ] **Step 8: Commit**

```bash
git add go.mod Makefile buf.yaml buf.gen.yaml cmd/ proto/ internal/ 2>/dev/null; git add -A
git commit -m "feat: initialize go module and project structure"
```

---

### Task 2: Protobuf Definitions

**Files:**
- Create: `proto/gru/v1/gru.proto`

- [ ] **Step 1: Write `proto/gru/v1/gru.proto`**

```protobuf
syntax = "proto3";

package gru.v1;

option go_package = "github.com/dakshjotwani/gru/proto/gru/v1;gruv1";

import "google/protobuf/timestamp.proto";

service GruService {
  // Sessions
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc GetSession(GetSessionRequest)     returns (Session);
  rpc LaunchSession(LaunchSessionRequest) returns (LaunchSessionResponse);
  rpc KillSession(KillSessionRequest)   returns (KillSessionResponse);

  // Projects
  rpc ListProjects(ListProjectsRequest) returns (ListProjectsResponse);

  // Real-time event stream
  rpc SubscribeEvents(SubscribeEventsRequest) returns (stream SessionEvent);
}

// ── enums ──────────────────────────────────────────────────────────────────

enum SessionStatus {
  SESSION_STATUS_UNSPECIFIED      = 0;
  SESSION_STATUS_STARTING         = 1;
  SESSION_STATUS_RUNNING          = 2;
  SESSION_STATUS_IDLE             = 3;
  SESSION_STATUS_NEEDS_ATTENTION  = 4;
  SESSION_STATUS_COMPLETED        = 5;
  SESSION_STATUS_ERRORED          = 6;
  SESSION_STATUS_KILLED           = 7;
}

// ── core types ─────────────────────────────────────────────────────────────

message Session {
  string         id              = 1;
  string         project_id      = 2;
  string         runtime         = 3;
  SessionStatus  status          = 4;
  string         profile         = 5;
  double         attention_score = 6;
  google.protobuf.Timestamp started_at    = 7;
  google.protobuf.Timestamp ended_at      = 8; // zero if still running
  google.protobuf.Timestamp last_event_at = 9; // zero if no events yet
  int32          pid             = 10;
  string tmux_session = 11; // tmux session name, e.g. "gru-av-sim"
  string tmux_window  = 12; // tmux window name, e.g. "feat-dev·a1b2c3d4"
}

message Project {
  string id      = 1;
  string name    = 2;
  string path    = 3;
  string runtime = 4;
  google.protobuf.Timestamp created_at = 5;
}

// SessionEvent is used both for the event store and for SubscribeEvents streaming.
// On stream open, the server sends a snapshot of current sessions as synthetic
// events (type = "snapshot.session"), then streams real events as they arrive.
message SessionEvent {
  string id         = 1;
  string session_id = 2;
  string project_id = 3;
  string runtime    = 4;
  string type       = 5;
  google.protobuf.Timestamp timestamp = 6;
  bytes  payload    = 7; // raw JSON from hook
}

// ── request / response ─────────────────────────────────────────────────────

message ListSessionsRequest {
  string        project_id = 1; // optional; empty = all projects
  SessionStatus status     = 2; // optional; UNSPECIFIED = all statuses
}

message ListSessionsResponse {
  repeated Session sessions = 1;
}

message GetSessionRequest {
  string id = 1;
}

message LaunchSessionRequest {
  string project_dir = 1;
  string prompt      = 2;
  string profile     = 3; // agent profile name; empty = default
}

message LaunchSessionResponse {
  Session session = 1;
}

message KillSessionRequest {
  string id = 1;
}

message KillSessionResponse {
  bool success = 1;
}

message ListProjectsRequest {}

message ListProjectsResponse {
  repeated Project projects = 1;
}

message SubscribeEventsRequest {
  repeated string project_ids       = 1; // empty = all projects
  double          min_attention     = 2; // 0 = all
}
```

- [ ] **Step 2: Update buf deps and generate**

```bash
buf dep update
buf generate
```

Expected: `proto/gru/v1/*.pb.go` and `proto/gru/v1/gruv1connect/*.go` created.

- [ ] **Step 3: Add required Go dependencies**

```bash
go get connectrpc.com/connect
go get google.golang.org/protobuf
go get golang.org/x/net
```

- [ ] **Step 4: Verify generated code compiles**

```bash
go build ./proto/...
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add proto/ buf.yaml buf.gen.yaml go.mod go.sum
git commit -m "feat: add protobuf API definitions and generate Go code"
```

---

### Task 3: SQLite Schema

**Files:**
- Create: `internal/store/migrations/001_init.sql`

- [ ] **Step 1: Write `internal/store/migrations/001_init.sql`**

```sql
-- Projects: known codebases managed by gru.
CREATE TABLE IF NOT EXISTS projects (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL UNIQUE,
    runtime    TEXT NOT NULL DEFAULT 'claude-code',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Sessions: one row per agent process lifecycle.
CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id),
    runtime         TEXT NOT NULL DEFAULT 'claude-code',
    -- status values: starting | running | idle | needs_attention | completed | errored | killed
    status          TEXT NOT NULL DEFAULT 'starting',
    profile         TEXT,
    pid             INTEGER,
    pgid            INTEGER,
    attention_score REAL NOT NULL DEFAULT 1.0,
    started_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    ended_at        TEXT,         -- NULL while running
    last_event_at   TEXT,         -- NULL until first event
    tmux_session    TEXT,    -- NULL for externally detected sessions
    tmux_window     TEXT     -- tmux window name within the project session
);

CREATE INDEX IF NOT EXISTS idx_sessions_project_id ON sessions(project_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status     ON sessions(status);

-- Events: append-only log of all hook events from all runtimes.
CREATE TABLE IF NOT EXISTS events (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    runtime    TEXT NOT NULL,
    type       TEXT NOT NULL,
    timestamp  TEXT NOT NULL,
    payload    TEXT NOT NULL,      -- raw JSON
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_events_session_id ON events(session_id);
CREATE INDEX IF NOT EXISTS idx_events_timestamp  ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_type       ON events(type);

-- Schema version tracking (for future migrations).
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
INSERT OR IGNORE INTO schema_migrations (version) VALUES (1);
```

- [ ] **Step 2: Commit**

```bash
git add internal/store/migrations/
git commit -m "feat: add SQLite schema migration"
```

---

### Task 4: sqlc Configuration and Queries

**Files:**
- Create: `internal/store/sqlc.yaml`
- Create: `internal/store/queries/projects.sql`
- Create: `internal/store/queries/sessions.sql`
- Create: `internal/store/queries/events.sql`

- [ ] **Step 1: Write `internal/store/sqlc.yaml`**

```yaml
version: "2"
sql:
  - engine: "sqlite"
    queries: "./queries"
    schema: "./migrations"
    gen:
      go:
        package: "db"
        out: "./db"
        emit_interface: true
        emit_empty_slices: true
        emit_pointers_for_null_types: true
```

- [ ] **Step 2: Write `internal/store/queries/projects.sql`**

```sql
-- name: UpsertProject :one
INSERT INTO projects (id, name, path, runtime)
VALUES (?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
    name    = excluded.name,
    runtime = excluded.runtime
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ? LIMIT 1;

-- name: GetProjectByPath :one
SELECT * FROM projects WHERE path = ? LIMIT 1;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name ASC;
```

- [ ] **Step 3: Write `internal/store/queries/sessions.sql`**

```sql
-- name: CreateSession :one
INSERT INTO sessions (id, project_id, runtime, status, profile, pid, pgid, tmux_session, tmux_window)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ? LIMIT 1;

-- name: ListSessions :many
SELECT * FROM sessions
WHERE (? = '' OR project_id = ?)
  AND (? = '' OR status = ?)
ORDER BY started_at DESC;

-- name: UpdateSessionStatus :one
UPDATE sessions
SET status = ?,
    ended_at = CASE WHEN ? IN ('completed','errored','killed') THEN strftime('%Y-%m-%dT%H:%M:%SZ','now') ELSE ended_at END
WHERE id = ?
RETURNING *;

-- name: UpdateSessionLastEvent :exec
UPDATE sessions
SET status = ?,
    last_event_at = ?,
    attention_score = ?
WHERE id = ?;

-- name: UpdateSessionPID :exec
UPDATE sessions SET pid = ?, pgid = ? WHERE id = ?;
```

- [ ] **Step 4: Write `internal/store/queries/events.sql`**

```sql
-- name: CreateEvent :one
INSERT INTO events (id, session_id, project_id, runtime, type, timestamp, payload)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListEventsBySession :many
SELECT * FROM events
WHERE session_id = ?
ORDER BY timestamp ASC;

-- name: GetLatestEventForSession :one
SELECT * FROM events
WHERE session_id = ?
ORDER BY timestamp DESC
LIMIT 1;
```

- [ ] **Step 5: Generate sqlc code**

```bash
go get modernc.org/sqlite
go tool sqlc generate -f internal/store/sqlc.yaml
```

Expected: `internal/store/db/` populated with `db.go`, `models.go`, `querier.go`, `projects.sql.go`, `sessions.sql.go`, `events.sql.go`.

- [ ] **Step 6: Verify generated code compiles**

```bash
go build ./internal/store/...
```

- [ ] **Step 7: Commit**

```bash
git add internal/store/sqlc.yaml internal/store/queries/ internal/store/db/ go.mod go.sum
git commit -m "feat: add sqlc config, queries, and generated store code"
```

---

### Task 5: Config

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test `internal/config/config_test.go`**

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dakshjotwani/gru/internal/config"
)

func TestLoad_defaults(t *testing.T) {
	// No config file — should return defaults.
	cfg, err := config.Load("/nonexistent/path/server.yaml")
	if err != nil {
		t.Fatalf("Load returned error for missing file: %v", err)
	}
	if cfg.Addr != ":7777" {
		t.Errorf("default addr = %q, want %q", cfg.Addr, ":7777")
	}
	if cfg.APIKey == "" {
		t.Error("default APIKey should not be empty (auto-generated)")
	}
}

func TestLoad_fromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	content := "addr: \":9090\"\napi_key: \"test-key-123\"\ndb_path: \"/tmp/gru.db\"\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9090" {
		t.Errorf("addr = %q, want %q", cfg.Addr, ":9090")
	}
	if cfg.APIKey != "test-key-123" {
		t.Errorf("api_key = %q, want %q", cfg.APIKey, "test-key-123")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/config/...
```

Expected: compile error — package `config` does not exist.

- [ ] **Step 3: Write `internal/config/config.go`**

```go
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Addr   string `yaml:"addr"`
	APIKey string `yaml:"api_key"`
	DBPath string `yaml:"db_path"`
}

// Load reads server config from path. Missing file returns defaults;
// parse errors are returned as errors.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Addr:   ":7777",
		DBPath: filepath.Join(os.Getenv("HOME"), ".gru", "gru.db"),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.APIKey = generateKey()
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	if cfg.APIKey == "" {
		cfg.APIKey = generateKey()
	}

	return cfg, nil
}

func generateKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("config: failed to generate API key: " + err.Error())
	}
	return hex.EncodeToString(b)
}
```

- [ ] **Step 4: Add yaml dependency**

```bash
go get gopkg.in/yaml.v3
```

- [ ] **Step 5: Run tests and verify they pass**

```bash
go test ./internal/config/...
```

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git add internal/config/ go.mod go.sum
git commit -m "feat: add server config loader with defaults"
```

---

### Task 6: Store (SQLite Connection + WAL + Migrations)

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing test `internal/store/store_test.go`**

```go
package store_test

import (
	"context"
	"testing"

	"github.com/dakshjotwani/gru/internal/store"
)

func TestOpen_createsSchema(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Verify tables exist by running a simple query.
	ctx := context.Background()
	projects, err := s.Queries().ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects after Open: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(projects))
	}
}

func TestStore_upsertAndGetProject(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	q := s.Queries()

	p, err := q.UpsertProject(ctx, store.UpsertProjectParams{
		ID:      "proj-1",
		Name:    "my-project",
		Path:    "/home/daksh/my-project",
		Runtime: "claude-code",
	})
	if err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if p.ID != "proj-1" {
		t.Errorf("id = %q, want %q", p.ID, "proj-1")
	}

	got, err := q.GetProject(ctx, "proj-1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Path != "/home/daksh/my-project" {
		t.Errorf("path = %q, want %q", got.Path, "/home/daksh/my-project")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/store/...
```

Expected: compile error — package `store` does not exist.

- [ ] **Step 3: Write `internal/store/store.go`**

```go
package store

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/dakshjotwani/gru/internal/store/db"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store wraps the SQLite connection and provides access to typed queries.
type Store struct {
	conn    *sql.DB
	queries *db.Queries
}

// UpsertProjectParams re-exports the sqlc type so callers don't need to
// import the generated db package directly.
type UpsertProjectParams = db.UpsertProjectParams
type CreateSessionParams = db.CreateSessionParams
type CreateEventParams   = db.CreateEventParams

// Open opens (or creates) the SQLite database at path, enables WAL mode,
// and runs migrations.
func Open(path string) (*Store, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// WAL mode for concurrent read/write.
	if _, err := conn.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: set WAL mode: %w", err)
	}
	if _, err := conn.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: enable foreign keys: %w", err)
	}

	s := &Store{conn: conn, queries: db.New(conn)}
	if err := s.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Queries() *db.Queries { return s.queries }
func (s *Store) DB() *sql.DB          { return s.conn }

func (s *Store) Close() error { return s.conn.Close() }

func (s *Store) migrate() error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		sql, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		if _, err := s.conn.Exec(string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", e.Name(), err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests and verify they pass**

```bash
go test ./internal/store/...
```

Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add SQLite store with WAL mode and embedded migrations"
```

---

### Task 7: EventNormalizer Interface and GruEvent Types

**Files:**
- Create: `internal/adapter/normalizer.go`

This file defines the contracts that Phase 1b (Claude Code adapter) implements.
No test needed — it's a pure interface definition.

- [ ] **Step 1: Write `internal/adapter/normalizer.go`**

```go
package adapter

import (
	"context"
	"encoding/json"
	"time"
)

// EventType is the normalized event type string.
type EventType string

const (
	// Required — every runtime adapter must emit these.
	EventSessionStart EventType = "session.start"
	EventSessionEnd   EventType = "session.end"
	EventSessionCrash EventType = "session.crash"
	EventToolPre      EventType = "tool.pre"
	EventToolPost     EventType = "tool.post"
	EventToolError    EventType = "tool.error"
	EventNotification EventType = "notification"

	// Optional — pass through if the runtime emits them.
	EventSubagentStart EventType = "subagent.start"
	EventSubagentEnd   EventType = "subagent.end"
)

// GruEvent is the normalized event schema written to the store
// and broadcast to SubscribeEvents streams.
type GruEvent struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	ProjectID string          `json:"project_id"`
	Runtime   string          `json:"runtime"`
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"` // original runtime-specific JSON
}

// EventNormalizer translates runtime-specific hook payloads into GruEvent.
// Implementations are stateless and registered at startup.
type EventNormalizer interface {
	// RuntimeID returns the runtime identifier this normalizer handles.
	// Example: "claude-code"
	RuntimeID() string

	// Normalize converts the raw hook payload into a GruEvent.
	// The returned event's ID, SessionID, ProjectID, and Timestamp must be set.
	Normalize(ctx context.Context, raw json.RawMessage) (*GruEvent, error)
}

// Registry holds registered normalizers, keyed by runtime ID.
type Registry struct {
	normalizers map[string]EventNormalizer
}

func NewRegistry() *Registry {
	return &Registry{normalizers: make(map[string]EventNormalizer)}
}

// Register adds a normalizer. Panics on duplicate runtime IDs (programming error).
func (r *Registry) Register(n EventNormalizer) {
	id := n.RuntimeID()
	if _, exists := r.normalizers[id]; exists {
		panic("adapter: duplicate normalizer for runtime: " + id)
	}
	r.normalizers[id] = n
}

// Get returns the normalizer for the given runtime ID, or nil if not found.
func (r *Registry) Get(runtimeID string) EventNormalizer {
	return r.normalizers[runtimeID]
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./internal/adapter/...
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/adapter/
git commit -m "feat: add EventNormalizer interface and GruEvent types"
```

---

### Task 8: Auth Middleware

**Files:**
- Create: `internal/server/auth.go`
- Create: `internal/server/auth_test.go`

- [ ] **Step 1: Write the failing test `internal/server/auth_test.go`**

```go
package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dakshjotwani/gru/internal/server"
)

func TestBearerAuth_missingHeader(t *testing.T) {
	handler := server.BearerAuth("secret-key", okHandler())
	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuth_wrongKey(t *testing.T) {
	handler := server.BearerAuth("secret-key", okHandler())
	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuth_correctKey(t *testing.T) {
	handler := server.BearerAuth("secret-key", okHandler())
	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/server/...
```

Expected: compile error — package `server` does not exist.

- [ ] **Step 3: Write `internal/server/auth.go`**

```go
package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuth wraps handler, requiring a valid Bearer token.
// Uses constant-time comparison to prevent timing attacks.
func BearerAuth(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		return parts[1]
	}
	return ""
}
```

- [ ] **Step 4: Run tests and verify they pass**

```bash
go test ./internal/server/...
```

Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/server/auth.go internal/server/auth_test.go
git commit -m "feat: add bearer token auth middleware"
```

---

### Task 9: Event Ingestion Handler

**Files:**
- Create: `internal/ingestion/handler.go`
- Create: `internal/ingestion/handler_test.go`

The handler receives raw hook JSON, requires a pre-existing session (looked up by `X-Gru-Session-ID` header), normalizes the event, stores it, and publishes to subscribers. Gru only tracks sessions it launched — non-Gru sessions are rejected with 404.

- [ ] **Step 1: Write the failing test `internal/ingestion/handler_test.go`**

```go
package ingestion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
)

// stubNormalizer is a minimal EventNormalizer for testing.
type stubNormalizer struct{}

func (s *stubNormalizer) RuntimeID() string { return "test-runtime" }
func (s *stubNormalizer) Normalize(_ context.Context, raw json.RawMessage) (*adapter.GruEvent, error) {
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &adapter.GruEvent{
		ID:        "evt-1",
		SessionID: m["session_id"],
		ProjectID: m["project_id"],
		Runtime:   "test-runtime",
		Type:      adapter.EventSessionStart,
		Payload:   raw,
	}, nil
}

func setup(t *testing.T) (*store.Store, *adapter.Registry) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	reg := adapter.NewRegistry()
	reg.Register(&stubNormalizer{})
	return s, reg
}

func TestHandler_rejectsMissingSessionID(t *testing.T) {
	s, reg := setup(t)
	pub := ingestion.NewPublisher()
	h := ingestion.NewHandler(s, reg, pub)

	body := `{"hook_event_name":"PreToolUse"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	// No X-Gru-Session-ID header
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandler_rejectsUnknownSession(t *testing.T) {
	s, reg := setup(t)
	pub := ingestion.NewPublisher()
	h := ingestion.NewHandler(s, reg, pub)

	body := `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	req.Header.Set("X-Gru-Session-ID", "nonexistent-session")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandler_storesEvent(t *testing.T) {
	s, reg := setup(t)
	pub := ingestion.NewPublisher()
	h := ingestion.NewHandler(s, reg, pub)

	// Session pre-exists (created by launcher before tmux window starts).
	ctx := context.Background()
	_, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "test", Path: "/tmp/test", Runtime: "test-runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-1", ProjectID: "proj-1", Runtime: "test-runtime", Status: "starting",
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"session_id":"sess-1","project_id":"proj-1"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	req.Header.Set("X-Gru-Session-ID", "sess-1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusAccepted, rr.Body.String())
	}

	events, err := s.Queries().ListEventsBySession(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("events in DB = %d, want 1", len(events))
	}
}

func TestHandler_publishesToSubscribers(t *testing.T) {
	s, reg := setup(t)
	pub := ingestion.NewPublisher()

	sub := make(chan *gruv1.SessionEvent, 1)
	pub.Subscribe("test-sub", sub)
	defer pub.Unsubscribe("test-sub")

	h := ingestion.NewHandler(s, reg, pub)

	ctx := context.Background()
	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "test", Path: "/tmp/test", Runtime: "test-runtime",
	})
	_, _ = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-1", ProjectID: "proj-1", Runtime: "test-runtime", Status: "starting",
	})

	body := `{"session_id":"sess-1","project_id":"proj-1"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	req.Header.Set("X-Gru-Session-ID", "sess-1")
	httptest.NewRecorder().Result() // discard
	h.ServeHTTP(httptest.NewRecorder(), req)

	select {
	case evt := <-sub:
		if evt.SessionId != "sess-1" {
			t.Errorf("published event session_id = %q, want %q", evt.SessionId, "sess-1")
		}
	default:
		t.Error("no event published to subscriber")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/ingestion/...
```

Expected: compile error — package `ingestion` does not exist.

- [ ] **Step 3: Write `internal/ingestion/handler.go`**

```go
package ingestion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Publisher broadcasts ingested events to active SubscribeEvents streams.
type Publisher struct {
	mu   sync.Mutex
	subs map[string]chan *gruv1.SessionEvent
}

func NewPublisher() *Publisher {
	return &Publisher{subs: make(map[string]chan *gruv1.SessionEvent)}
}

func (p *Publisher) Subscribe(id string, ch chan *gruv1.SessionEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs[id] = ch
}

func (p *Publisher) Unsubscribe(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.subs, id)
}

func (p *Publisher) Publish(evt *gruv1.SessionEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ch := range p.subs {
		select {
		case ch <- evt:
		default:
			// Slow subscriber: drop rather than block ingestion.
		}
	}
}

// Handler handles POST /events from hook scripts.
type Handler struct {
	store *store.Store
	reg   *adapter.Registry
	pub   *Publisher
}

func NewHandler(s *store.Store, reg *adapter.Registry, pub *Publisher) http.Handler {
	return &Handler{store: s, reg: reg, pub: pub}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Require X-Gru-Session-ID header (400 if missing).
	sessionID := r.Header.Get("X-Gru-Session-ID")
	if sessionID == "" {
		http.Error(w, "missing X-Gru-Session-ID header", http.StatusBadRequest)
		return
	}

	// 2. Require X-Gru-Runtime header (400 if missing).
	runtime := r.Header.Get("X-Gru-Runtime")
	if runtime == "" {
		http.Error(w, "missing X-Gru-Runtime header", http.StatusBadRequest)
		return
	}

	normalizer := h.reg.Get(runtime)
	if normalizer == nil {
		http.Error(w, fmt.Sprintf("unknown runtime: %s", runtime), http.StatusBadRequest)
		return
	}

	// 3. Look up session by X-Gru-Session-ID header — return 404 if not found. No auto-creation.
	q := h.store.Queries()
	sess, err := q.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("look up session: %v", err), http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		http.Error(w, "failed to read body", http.StatusInternalServerError)
		return
	}

	evt, err := normalizer.Normalize(r.Context(), json.RawMessage(body))
	if err != nil {
		http.Error(w, fmt.Sprintf("normalize: %v", err), http.StatusUnprocessableEntity)
		return
	}

	// 4. Normalize event, store it, publish it.
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
	evt.SessionID = sess.ID
	evt.ProjectID = sess.ProjectID

	if _, err := q.CreateEvent(r.Context(), store.CreateEventParams{
		ID:        evt.ID,
		SessionID: evt.SessionID,
		ProjectID: evt.ProjectID,
		Runtime:   evt.Runtime,
		Type:      string(evt.Type),
		Timestamp: evt.Timestamp.UTC().Format(time.RFC3339),
		Payload:   string(evt.Payload),
	}); err != nil {
		http.Error(w, fmt.Sprintf("store event: %v", err), http.StatusInternalServerError)
		return
	}

	protoEvt := &gruv1.SessionEvent{
		Id:        evt.ID,
		SessionId: evt.SessionID,
		ProjectId: evt.ProjectID,
		Runtime:   evt.Runtime,
		Type:      string(evt.Type),
		Timestamp: timestamppb.New(evt.Timestamp),
		Payload:   evt.Payload,
	}
	h.pub.Publish(protoEvt)

	w.WriteHeader(http.StatusAccepted)
}
```

- [ ] **Step 4: Run tests and verify they pass**

```bash
go test ./internal/ingestion/...
```

Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/ingestion/
git commit -m "feat: add event ingestion HTTP handler with pub/sub"
```

---

### Task 10: gRPC Service Implementation

**Files:**
- Create: `internal/server/service.go`
- Create: `internal/server/service_test.go`

Implements `GruService`: `ListSessions`, `GetSession`, `ListProjects` (real),
`LaunchSession` and `KillSession` (stubs returning unimplemented — Phase 1c fills these in),
`SubscribeEvents` (real — snapshot + stream).

- [ ] **Step 1: Write the failing test `internal/server/service_test.go`**

```go
package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
)

func newTestServer(t *testing.T) (gruv1connect.GruServiceClient, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	pub := ingestion.NewPublisher()
	svc := server.NewService(s, pub)
	mux := http.NewServeMux()
	mux.Handle(gruv1connect.NewGruServiceHandler(svc))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	client := gruv1connect.NewGruServiceClient(ts.Client(), ts.URL)
	return client, s
}

func TestListSessions_empty(t *testing.T) {
	client, _ := newTestServer(t)
	resp, err := client.ListSessions(context.Background(),
		connect.NewRequest(&gruv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Msg.Sessions) != 0 {
		t.Errorf("sessions = %d, want 0", len(resp.Msg.Sessions))
	}
}

func TestListSessions_afterInsert(t *testing.T) {
	client, s := newTestServer(t)
	ctx := context.Background()

	_, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p1", Name: "proj", Path: "/tmp/proj", Runtime: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "s1", ProjectID: "p1", Runtime: "claude-code", Status: "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.ListSessions(ctx, connect.NewRequest(&gruv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Msg.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(resp.Msg.Sessions))
	}
	if resp.Msg.Sessions[0].Id != "s1" {
		t.Errorf("session id = %q, want %q", resp.Msg.Sessions[0].Id, "s1")
	}
}

func TestGetSession_notFound(t *testing.T) {
	client, _ := newTestServer(t)
	_, err := client.GetSession(context.Background(),
		connect.NewRequest(&gruv1.GetSessionRequest{Id: "nonexistent"}))
	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
	// connect-go wraps not-found as codes.NotFound
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("error code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestListProjects(t *testing.T) {
	client, s := newTestServer(t)
	ctx := context.Background()

	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p1", Name: "alpha", Path: "/a", Runtime: "claude-code",
	})
	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p2", Name: "beta", Path: "/b", Runtime: "claude-code",
	})

	resp, err := client.ListProjects(ctx, connect.NewRequest(&gruv1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(resp.Msg.Projects) != 2 {
		t.Errorf("projects = %d, want 2", len(resp.Msg.Projects))
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/server/...
```

Expected: compile error — `server.NewService` does not exist.

- [ ] **Step 3: Write `internal/server/service.go`**

```go
package server

import (
	"context"
	"database/sql"
	"errors"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements gruv1connect.GruServiceHandler.
type Service struct {
	store *store.Store
	pub   *ingestion.Publisher
}

var _ gruv1connect.GruServiceHandler = (*Service)(nil)

func NewService(s *store.Store, pub *ingestion.Publisher) *Service {
	return &Service{store: s, pub: pub}
}

func (s *Service) ListSessions(
	ctx context.Context,
	req *connect.Request[gruv1.ListSessionsRequest],
) (*connect.Response[gruv1.ListSessionsResponse], error) {
	rows, err := s.store.Queries().ListSessions(ctx, store.ListSessionsParams{
		ProjectID:  req.Msg.ProjectId,
		ProjectID2: req.Msg.ProjectId,
		Status:     statusToString(req.Msg.Status),
		Status2:    statusToString(req.Msg.Status),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sessions := make([]*gruv1.Session, 0, len(rows))
	for _, r := range rows {
		sessions = append(sessions, rowToSession(r))
	}
	return connect.NewResponse(&gruv1.ListSessionsResponse{Sessions: sessions}), nil
}

func (s *Service) GetSession(
	ctx context.Context,
	req *connect.Request[gruv1.GetSessionRequest],
) (*connect.Response[gruv1.Session], error) {
	row, err := s.store.Queries().GetSession(ctx, req.Msg.Id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(rowToSession(row)), nil
}

func (s *Service) LaunchSession(
	ctx context.Context,
	req *connect.Request[gruv1.LaunchSessionRequest],
) (*connect.Response[gruv1.LaunchSessionResponse], error) {
	// Implemented in Phase 1c.
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("launch not yet implemented"))
}

func (s *Service) KillSession(
	ctx context.Context,
	req *connect.Request[gruv1.KillSessionRequest],
) (*connect.Response[gruv1.KillSessionResponse], error) {
	// Implemented in Phase 1c.
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("kill not yet implemented"))
}

func (s *Service) ListProjects(
	ctx context.Context,
	req *connect.Request[gruv1.ListProjectsRequest],
) (*connect.Response[gruv1.ListProjectsResponse], error) {
	rows, err := s.store.Queries().ListProjects(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	projects := make([]*gruv1.Project, 0, len(rows))
	for _, r := range rows {
		projects = append(projects, &gruv1.Project{
			Id:        r.ID,
			Name:      r.Name,
			Path:      r.Path,
			Runtime:   r.Runtime,
			CreatedAt: parseTimestamp(r.CreatedAt),
		})
	}
	return connect.NewResponse(&gruv1.ListProjectsResponse{Projects: projects}), nil
}

// SubscribeEvents sends a snapshot of current sessions, then streams new events.
func (s *Service) SubscribeEvents(
	ctx context.Context,
	req *connect.Request[gruv1.SubscribeEventsRequest],
	stream *connect.ServerStream[gruv1.SessionEvent],
) error {
	// 1. Send snapshot: one synthetic "snapshot.session" event per current session.
	rows, err := s.store.Queries().ListSessions(ctx, store.ListSessionsParams{})
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for _, row := range rows {
		sess := rowToSession(row)
		payload, _ := sessionToJSON(sess)
		if err := stream.Send(&gruv1.SessionEvent{
			Type:      "snapshot.session",
			SessionId: row.ID,
			ProjectId: row.ProjectID,
			Runtime:   row.Runtime,
			Payload:   payload,
		}); err != nil {
			return err
		}
	}

	// 2. Subscribe to live events.
	subID := req.Header().Get("Grpc-Metadata-Sub-Id")
	if subID == "" {
		subID = req.Peer().Addr
	}
	ch := make(chan *gruv1.SessionEvent, 64)
	s.pub.Subscribe(subID, ch)
	defer s.pub.Unsubscribe(subID)

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt := <-ch:
			if err := stream.Send(evt); err != nil {
				return err
			}
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func statusToString(s gruv1.SessionStatus) string {
	switch s {
	case gruv1.SessionStatus_SESSION_STATUS_RUNNING:
		return "running"
	case gruv1.SessionStatus_SESSION_STATUS_IDLE:
		return "idle"
	case gruv1.SessionStatus_SESSION_STATUS_NEEDS_ATTENTION:
		return "needs_attention"
	case gruv1.SessionStatus_SESSION_STATUS_COMPLETED:
		return "completed"
	case gruv1.SessionStatus_SESSION_STATUS_ERRORED:
		return "errored"
	case gruv1.SessionStatus_SESSION_STATUS_KILLED:
		return "killed"
	default:
		return "" // UNSPECIFIED → all statuses
	}
}

func rowToSession(r interface{ /* db.Session */ }) *gruv1.Session {
	// Using type switch because sqlc generates a concrete struct.
	// See store/db/models.go for the Session struct fields.
	//
	// This is a type assertion helper — update field names to match sqlc output.
	type dbSession interface {
		GetID() string
		GetProjectID() string
		GetRuntime() string
		GetStatus() string
		GetProfile() interface{ String() string }
		GetAttentionScore() float64
		GetStartedAt() string
		GetEndedAt() *string
		GetLastEventAt() *string
		GetPid() *int64
	}
	// Fall through to concrete type since sqlc generates a plain struct.
	// The real implementation uses the concrete db.Session type directly.
	return &gruv1.Session{} // placeholder — see service_impl note below
}
```

> **Implementation note for `rowToSession`:** sqlc generates a concrete `db.Session` struct (not an interface). Replace the placeholder with:
>
> ```go
> func rowToSession(r db.Session) *gruv1.Session {
>     sess := &gruv1.Session{
>         Id:             r.ID,
>         ProjectId:      r.ProjectID,
>         Runtime:        r.Runtime,
>         Status:         stringToStatus(r.Status),
>         AttentionScore: r.AttentionScore,
>         StartedAt:      parseTimestamp(r.StartedAt),
>     }
>     if r.Profile != nil {
>         sess.Profile = *r.Profile
>     }
>     if r.EndedAt != nil {
>         sess.EndedAt = parseTimestamp(*r.EndedAt)
>     }
>     if r.LastEventAt != nil {
>         sess.LastEventAt = parseTimestamp(*r.LastEventAt)
>     }
>     if r.Pid != nil {
>         sess.Pid = int32(*r.Pid)
>     }
>     return sess
> }
> ```
> Add `stringToStatus`, `parseTimestamp`, and `sessionToJSON` helpers in the same file.

- [ ] **Step 4: Add connect dependency**

```bash
go get connectrpc.com/connect
go mod tidy
```

- [ ] **Step 5: Run tests and verify they pass**

```bash
go test ./internal/server/...
```

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git add internal/server/service.go internal/server/service_test.go go.mod go.sum
git commit -m "feat: add gRPC service with ListSessions, GetSession, ListProjects, SubscribeEvents"
```

---

### Task 11: Main Server Binary

**Files:**
- Modify: `cmd/gru/main.go`
- Create: `cmd/gru/server.go`

- [ ] **Step 1: Write `cmd/gru/server.go`**

```go
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
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
	reg := adapter.NewRegistry()
	// Phase 1b registers the Claude Code normalizer here:
	// reg.Register(claude.NewNormalizer())

	svc := server.NewService(s, pub)
	ingestionHandler := ingestion.NewHandler(s, reg, pub)

	mux := http.NewServeMux()

	// gRPC + grpc-web via connect-go (single port, no Envoy).
	grpcPath, grpcHandler := gruv1connect.NewGruServiceHandler(svc,
		connect.WithCompressMinBytes(1024),
	)
	mux.Handle(grpcPath, server.BearerAuth(cfg.APIKey, grpcHandler))

	// Hook event ingestion (plain HTTP POST).
	mux.Handle("POST /events", server.BearerAuth(cfg.APIKey, ingestionHandler))

	// h2c enables HTTP/2 cleartext (required for gRPC without TLS).
	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}

	log.Printf("gru server listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
	log.Printf("API key: %s", cfg.APIKey)
	return httpServer.ListenAndServe()
}
```

- [ ] **Step 2: Update `cmd/gru/main.go`**

```go
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: gru <command>")
		fmt.Println("commands: server")
		return nil
	}
	switch args[0] {
	case "server":
		return runServer()
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}
```

- [ ] **Step 3: Verify it builds**

```bash
go build ./cmd/gru/...
```

Expected: binary produced with no errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/gru/
git commit -m "feat: wire up main server binary with connect-go and h2c"
```

---

### Task 12: Integration Test

**Files:**
- Create: `internal/integration/integration_test.go`

End-to-end: start real server → POST event via HTTP → verify via gRPC `ListSessions`.

- [ ] **Step 1: Write `internal/integration/integration_test.go`**

```go
//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// stubNormalizer mirrors the one in ingestion tests — used here until
// Phase 1b provides the real Claude Code normalizer.
type stubNormalizer struct{}
// ... (same implementation as in ingestion/handler_test.go)

func startTestServer(t *testing.T) (string, string) {
	t.Helper()
	const apiKey = "integration-test-key"

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	pub := ingestion.NewPublisher()
	reg := adapter.NewRegistry()
	reg.Register(&stubNormalizer{})

	svc := server.NewService(s, pub)
	ingestionHandler := ingestion.NewHandler(s, reg, pub)

	mux := http.NewServeMux()
	mux.Handle(gruv1connect.NewGruServiceHandler(svc))
	mux.Handle("POST /events", server.BearerAuth(apiKey, ingestionHandler))

	ts := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	ts.Start()
	t.Cleanup(ts.Close)

	return ts.URL, apiKey
}

func TestIntegration_PostEventAndListSession(t *testing.T) {
	url, apiKey := startTestServer(t)
	ctx := context.Background()

	// Pre-seed project + session — sessions must pre-exist before any hook fires.
	// In production, the launcher creates the session before starting the tmux window.
	// ... (seed project and session as in handler_test.go)

	// POST event.
	body := `{"session_id":"sess-int","project_id":"proj-int"}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/events",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	req.Header.Set("X-Gru-Session-ID", "sess-int")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /events: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /events status = %d, want 202", resp.StatusCode)
	}

	// Verify via gRPC.
	client := gruv1connect.NewGruServiceClient(http.DefaultClient, url)
	listResp, err := client.ListSessions(ctx,
		connect.NewRequest(&gruv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(listResp.Msg.Sessions) != 1 {
		t.Errorf("sessions = %d, want 1", len(listResp.Msg.Sessions))
	}
}
```

- [ ] **Step 2: Run unit tests (integration tests use build tag)**

```bash
go test ./...
```

Expected: all unit tests pass. Integration tests skipped (no `integration` build tag).

- [ ] **Step 3: Run integration test explicitly**

```bash
go test -tags integration ./internal/integration/...
```

Expected: `PASS`

- [ ] **Step 4: Final build verification**

```bash
go build ./...
go vet ./...
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/integration/
git commit -m "feat: add integration test for event ingestion + gRPC list"
```

---

## Self-Review Checklist

**Spec coverage (Phase 1 backend scope):**
- [x] Event ingestion HTTP endpoint — Task 9
- [x] gRPC service (list, get, subscribe) — Task 10
- [x] connect-go single-port setup — Task 11
- [x] SQLite store in WAL mode — Task 6
- [x] sqlc type-safe queries — Task 4
- [x] Protobuf as API contract — Task 2
- [x] API key auth — Task 8
- [x] EventNormalizer interface (for Phase 1b) — Task 7
- [x] Server streaming for real-time updates — Task 10 (SubscribeEvents)
- [x] LaunchSession/KillSession stubs for Phase 1c — Task 10
- [ ] `gru init` hook installation — Phase 1b plan
- [ ] Process liveness polling — Phase 1c plan
- [ ] React dashboard — Phase 1d plan

**Open questions addressed in this plan:**
- Single port via connect-go: confirmed ✓
- SQLite WAL mode: Task 6 ✓
- Auth mechanism: Bearer token, constant-time compare ✓
- Pub/sub for streaming: in-memory channel broadcaster ✓
