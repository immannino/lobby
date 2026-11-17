# Storage

The lobby keeps live state in memory no matter what. A `Store` only adds
durability, so rooms and seats survive a server restart. Pick the lightest
option that does the job:

| Mode | Setup | Survives restart | Extra deps |
|---|---|---|---|
| **In-memory** | `Config{}` (no `Store`) | No | None |
| **SQLite** (demo default) | `sqlitestore.Open(ctx, "lobby.db")` | Yes | `modernc.org/sqlite` (pure Go) |
| **Postgres / MySQL / other** | Implement `lobby.Store` (below) | Yes | Your driver |

## In-memory (no store)

Leave `Config.Store` nil. Rooms live in the `Manager`, each room's state is
owned by its own goroutine, and events flow over Go channels. Nothing is
serialized or written anywhere. The only encoding is JSON for websocket
clients, and in-process clients (`Manager.Connect` → `LocalConn`) don't pay
even that: they receive the Go values games send.

This is the right choice for tests, bots, local/LAN parties, and anything
where losing rooms on restart is fine.

## SQLite (default for the demo)

```go
store, err := sqlitestore.Open(ctx, "lobby.db") // WAL mode, single writer
cfg.Store = store
store.Prune(ctx, time.Now().Add(-time.Hour))     // drop rooms abandoned during downtime
m := lobby.New(cfg)
m.Restore(ctx)                                   // reload rooms before serving
```

Use `sqlitestore.New(ctx, db)` to share an existing `*sql.DB` (opened with
the `"sqlite"` driver) with the rest of your app.

## What a Store sees

```go
type Store interface {
	SaveRoom(ctx context.Context, rec RoomRecord) error   // upsert
	DeleteRoom(ctx context.Context, code string) error
	LoadRooms(ctx context.Context) ([]RoomRecord, error)  // once, at startup
}
```

- `SaveRoom` runs on every membership change, lock change, and whenever a
  game calls `r.Persist()`. It's an upsert of the whole room.
- `DeleteRoom` runs when a room closes (idle timeout or `r.Close()`), not on
  shutdown, so rooms survive restarts.
- Calls happen **on the room's goroutine** with a 5s timeout. A slow store
  slows that one room, not others. If your database is remote and latency
  matters, wrap your store in a write-behind queue (see below).
- `RoomRecord` is JSON-friendly. Storing it as one JSON blob per room is the
  simplest correct schema: rooms are small, short-lived, and always
  read and written whole.

## Postgres

```sql
CREATE TABLE IF NOT EXISTS lobby_rooms (
    code       TEXT PRIMARY KEY,
    data       JSONB NOT NULL,          -- JSONB lets you query rooms ad hoc
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS lobby_rooms_updated_at ON lobby_rooms (updated_at);
```

```go
package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/immannino/lobby"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx"
)

type Store struct{ db *sql.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil { // schema = the SQL above
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) SaveRoom(ctx context.Context, rec lobby.RoomRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO lobby_rooms (code, data, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (code) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		rec.Code, data)
	return err
}

func (s *Store) DeleteRoom(ctx context.Context, code string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lobby_rooms WHERE code = $1`, code)
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
		var rec lobby.RoomRecord
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) Prune(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lobby_rooms WHERE updated_at < $1`, before)
	return err
}
```

`RoomRecord.GameState` is `[]byte`, so inside the JSON it's base64. If you
want game state queryable in JSONB, have your game's `Snapshot` return JSON
and store it in a separate `JSONB` column.

## MySQL / MariaDB

Same shape. The differences:

```sql
CREATE TABLE IF NOT EXISTS lobby_rooms (
    code       VARCHAR(16) PRIMARY KEY,  -- TEXT can't be a primary key without a prefix length
    data       JSON NOT NULL,            -- or LONGBLOB on MariaDB < 10.2
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX (updated_at)
);
```

```go
import _ "github.com/go-sql-driver/mysql" // DSN needs ?parseTime=true

// Upsert: MySQL 8.0.19+ alias syntax (VALUES() is deprecated).
`INSERT INTO lobby_rooms (code, data, updated_at) VALUES (?, ?, NOW(3)) AS new
 ON DUPLICATE KEY UPDATE data = new.data, updated_at = NOW(3)`
// MariaDB / older MySQL:
`INSERT INTO lobby_rooms (code, data, updated_at) VALUES (?, ?, NOW(3))
 ON DUPLICATE KEY UPDATE data = VALUES(data), updated_at = NOW(3)`
```

Placeholders are `?` (like SQLite), and `DeleteRoom`/`LoadRooms` are
unchanged.

## Other backends

Anything with get/put/delete/scan works: Redis (`HSET lobby:rooms <code>
<json>` + `HGETALL`), DynamoDB, a directory of JSON files. Implement the
three methods; `go test ./lobby` has a persist-and-restore test
(`TestPersistAndRestore`) you can copy and point at your store.

## Write-behind for remote databases

If round-trip latency to your database hurts (each save runs on the room's
goroutine), queue saves instead:

```go
type asyncStore struct {
	lobby.Store
	saves chan lobby.RoomRecord // drained by one goroutine calling Store.SaveRoom
}

func (a *asyncStore) SaveRoom(_ context.Context, rec lobby.RoomRecord) error {
	select {
	case a.saves <- rec:
	default: // queue full: drop; the next save carries the full room anyway
	}
	return nil
}
```

Saves are whole-room snapshots, so dropping or coalescing intermediate ones
is safe. Make sure the drainer finishes before process exit (after
`Manager.Shutdown`), and that a `DeleteRoom` isn't overtaken by a queued
save for the same code.

## What a store does *not* give you

Horizontal scaling. Live connections and room goroutines exist in one
process, and the Store is read only at startup. Running several servers
needs routing by room code (each room owned by one node, with requests for
that code sent to it), which is a load-balancer and ownership problem, not
a storage one. A shared Postgres doesn't solve it on its own.

## Possible next step

The SQLite, Postgres and MySQL stores differ only in placeholders, upsert
syntax and column types. If more than one ships, fold them into a single
`lobby/sqlstore` with a small `Dialect` (and no driver imports, so users
bring their own) rather than maintaining near-duplicate packages.
