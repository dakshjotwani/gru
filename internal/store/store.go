package store

import (
	"database/sql"
	"embed"
	"fmt"
	"strconv"
	"strings"

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

// Re-export sqlc param + row types so callers don't need to import the generated db package.
type UpsertProjectParams  = db.UpsertProjectParams
type CreateSessionParams  = db.CreateSessionParams
type CreateEventParams    = db.CreateEventParams
type ListSessionsParams   = db.ListSessionsParams
type UpdateSessionStatusParams       = db.UpdateSessionStatusParams
type UpdateSessionDerivedParams      = db.UpdateSessionDerivedParams
type UpdateSessionAttentionScoreParams = db.UpdateSessionAttentionScoreParams
type ListEventsAfterSeqParams        = db.ListEventsAfterSeqParams
type RenameProjectParams = db.RenameProjectParams
type CreateArtifactParams = db.CreateArtifactParams
type CreateSessionLinkParams = db.CreateSessionLinkParams

// Row types
type Event   = db.Event
type Session = db.Session
type Project = db.Project

// Open opens (or creates) the SQLite database at path, enables WAL mode,
// and runs migrations.
func Open(path string) (*Store, error) {
	// Pass PRAGMAs via the DSN so every connection in the pool gets
	// them — PRAGMA executed via conn.Exec only applies to whichever
	// connection sql.DB picks for that call. busy_timeout in particular
	// MUST be per-connection or writers will spuriously see SQLITE_BUSY
	// (one tailer goroutine per session = a lot of parallel writers).
	dsn := path
	if path != ":memory:" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		dsn = path + sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// In-memory DBs are per-connection; pin the pool to a single conn so
	// every goroutine sees the same schema/data. (Harmless for on-disk
	// paths too — SQLite serializes writes internally — but we only
	// enable it for :memory: to avoid impacting prod concurrency.)
	if path == ":memory:" {
		conn.SetMaxOpenConns(1)
		// In-memory DBs can't get the per-connection PRAGMA via DSN
		// (one connection only), so set them explicitly on the conn.
		if _, err := conn.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			conn.Close()
			return nil, fmt.Errorf("store: set WAL mode: %w", err)
		}
		if _, err := conn.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			conn.Close()
			return nil, fmt.Errorf("store: enable foreign keys: %w", err)
		}
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
	// The first migration bootstraps the schema_migrations table itself and is
	// idempotent on re-run (all CREATE TABLE statements use IF NOT EXISTS), so
	// it's always safe to exec. Subsequent migrations are versioned: parse the
	// numeric prefix from each filename, skip any version already recorded, and
	// record the version after a successful apply.
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}

	applied, err := s.appliedVersions()
	if err != nil {
		return fmt.Errorf("read applied versions: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		version, ok := parseMigrationVersion(e.Name())
		if !ok {
			return fmt.Errorf("migration %s: filename must begin with a numeric prefix", e.Name())
		}
		if version > 1 && applied[version] {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		if _, err := s.conn.Exec(string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", e.Name(), err)
		}
		if _, err := s.conn.Exec(`INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			return fmt.Errorf("record migration %s: %w", e.Name(), err)
		}
		applied[version] = true
	}
	return nil
}

func (s *Store) appliedVersions() (map[int]bool, error) {
	out := map[int]bool{}
	// schema_migrations may not exist yet on a cold database; that's fine — return empty.
	rows, err := s.conn.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

func parseMigrationVersion(name string) (int, bool) {
	// Filenames look like "002_session_role.sql". Take everything before the first underscore.
	underscore := strings.IndexByte(name, '_')
	if underscore <= 0 {
		return 0, false
	}
	v, err := strconv.Atoi(name[:underscore])
	if err != nil {
		return 0, false
	}
	return v, true
}
