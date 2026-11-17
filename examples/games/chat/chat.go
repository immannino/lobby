// Package chat is the simplest lobby game: a shared counter and a chat log.
// It exists to exercise broadcasts, per-client snapshots and persistence.
package chat

import (
	"context"
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/immannino/lobby"
)

// Name is the game kind clients pass when creating a room.
const Name = "chat"

// ClientJS is the browser module for this game.
//
//go:embed chat.js
var ClientJS []byte

// New returns a chat game.
func New() lobby.Game { return &game{} }

// game holds the room's state. All methods run on the room goroutine, so
// there's no locking.
type game struct {
	lobby.BaseGame
	Count int    `json:"count"`
	Chat  []Line `json:"chat"`
}

// Line is one chat message.
type Line struct {
	From   string    `json:"from,omitempty"` // empty for system lines
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
	System bool      `json:"system,omitempty"`
}

// Snapshot is sent to each client when it connects.
type Snapshot struct {
	Count int    `json:"count"`
	Chat  []Line `json:"chat"`
}

const maxChat = 50

func (g *game) OnConnect(ctx context.Context, r *lobby.Room, c lobby.Client) {
	// A copy, never the live slice: in-process clients receive the value itself.
	r.Send(c.ID, "snapshot", Snapshot{Count: g.Count, Chat: append([]Line{}, g.Chat...)})
}

func (g *game) OnJoin(ctx context.Context, r *lobby.Room, c lobby.Client) {
	if c.Role == lobby.RolePlayer {
		g.say(ctx, r, Line{Text: c.Name + " joined", System: true})
	}
}

func (g *game) OnLeave(ctx context.Context, r *lobby.Room, c lobby.Client) {
	if c.Role == lobby.RolePlayer {
		g.say(ctx, r, Line{Text: c.Name + " left", System: true})
	}
}

func (g *game) OnMessage(ctx context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	if c.Role != lobby.RolePlayer {
		return // displays watch; they don't play
	}
	switch m.Type {
	case "chat":
		var d struct{ Text string }
		if m.Decode(&d) != nil {
			return
		}
		text := strings.TrimSpace(d.Text)
		if text == "" || utf8.RuneCountInString(text) > 200 {
			return
		}
		g.say(ctx, r, Line{From: c.Name, Text: text})
	case "add":
		var delta int
		if m.Decode(&delta) != nil || (delta != 1 && delta != -1) {
			return
		}
		g.Count += delta
		r.Broadcast("count", g.Count)
		r.Persist(ctx)
	}
}

func (g *game) say(ctx context.Context, r *lobby.Room, line Line) {
	line.At = time.Now()
	g.Chat = append(g.Chat, line)
	if len(g.Chat) > maxChat {
		g.Chat = slices.Clone(g.Chat[len(g.Chat)-maxChat:])
	}
	r.Broadcast("chat", line)
	r.Persist(ctx)
}

func (g *game) Snapshot() ([]byte, error) { return json.Marshal(g) }
func (g *game) Restore(b []byte) error    { return json.Unmarshal(b, g) }
