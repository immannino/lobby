// Package sqlitestore is a lobby.Store backed by SQLite (pure Go, no cgo).
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/immannino/lobby"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS lobby_rooms (
	code       TEXT PRIMARY KEY,
	data       BLOB NOT NULL,
	updated_at INTEGER NOT NULL
);`

// Store implements lobby.Store.
type Store struct {
	db *sql.DB
}

var _ lobby.Store = (*Store)(nil)

// Open opens (or creates) a SQLite database file at path and prepares the
// schema. Use ":memory:" for a throwaway database.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection avoids
	// SQLITE_BUSY and keeps ":memory:" databases consistent.
	db.SetMaxOpenConns(1)
	s, err := New(ctx, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// New wraps an existing *sql.DB (opened with the "sqlite" driver) and creates
// the lobby_rooms table if needed. Use this to share a database with the rest
// of your app.
func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, fmt.Errorf("sqlitestore: creating schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SaveRoom(ctx context.Context, rec lobby.RoomRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO lobby_rooms (code, data, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(code) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		rec.Code, data, time.Now().Unix())
	return err
}

func (s *Store) DeleteRoom(ctx context.Context, code string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lobby_rooms WHERE code = ?`, code)
	return err
}

func (s *Store) LoadRooms(ctx context.Context) ([]lobby.RoomRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM lobby_rooms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []lobby.RoomRecord
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var rec lobby.RoomRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("sqlitestore: decoding room: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Prune deletes rooms not updated since before. Rooms close themselves while
// the server runs; this cleans up after crashes or long downtime. Call it
// before Manager.Restore.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM lobby_rooms WHERE updated_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
