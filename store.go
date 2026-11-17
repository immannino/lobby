package lobby

import (
	"context"
	"time"
)

// Store persists rooms so they survive a server restart. Live websocket
// connections can't be persisted; after a restart every client starts out
// disconnected and reconnects with its token.
//
// A nil Store means rooms live in memory only.
//
// Store methods are called from a room's goroutine, so slow stores slow that
// room down (but not other rooms).
type Store interface {
	SaveRoom(ctx context.Context, rec RoomRecord) error
	DeleteRoom(ctx context.Context, code string) error
	LoadRooms(ctx context.Context) ([]RoomRecord, error)
}

// RoomRecord is the persisted form of a room.
type RoomRecord struct {
	Code      string         `json:"code"`
	Game      string         `json:"game,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	Clients   []ClientRecord `json:"clients"`
	Locked    bool           `json:"locked,omitempty"`
	// GameState is whatever the room's Game returned from Snapshot, if it
	// implements Snapshotter.
	GameState []byte `json:"gameState,omitempty"`
}

// ClientRecord is a Client plus the hash of its reconnect token.
type ClientRecord struct {
	Client
	TokenHash []byte `json:"tokenHash"`
}
