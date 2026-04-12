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
