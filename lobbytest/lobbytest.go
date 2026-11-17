// Package lobbytest drives lobby games in tests with in-process players
// (lobby.LocalConn): no server, no browser, no JSON.
//
//	m := lobbytest.Manager(t, func() lobby.Game { return &myGame{} })
//	ada, code := lobbytest.Create(t, m, "Ada")
//	bob := lobbytest.Join(t, m, code, "Bob")
//	lobbytest.Send(t, bob, "move", 4)
//	v := lobbytest.Wait(t, ada, func(v MyView) bool { return v.Turn == ada.Client().ID })
//
// Everything a game sends arrives as the Go value it passed, so Wait matches
// on your own view types directly.
package lobbytest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/immannino/lobby"
)

// Manager returns a lobby.Manager whose rooms all run the game newGame
// returns. Rate limiting is off and logs are discarded.
func Manager(t *testing.T, newGame func() lobby.Game) *lobby.Manager {
	t.Helper()
	return lobby.New(lobby.Config{
		Logger:    slog.New(slog.DiscardHandler),
		RateLimit: -1,
		NewGame:   func(code, kind string) (lobby.Game, error) { return newGame(), nil },
	})
}

// Create makes a room and seats name in it as its first player.
func Create(t *testing.T, m *lobby.Manager, name string) (*lobby.LocalConn, string) {
	t.Helper()
	ctx := t.Context()
	res, err := m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return connect(t, m, res), res.Code
}

// Join seats another player (or a display, if name is "") in code.
func Join(t *testing.T, m *lobby.Manager, code, name string) *lobby.LocalConn {
	t.Helper()
	req := lobby.JoinRequest{Name: name}
	if name == "" {
		req.Role = lobby.RoleDisplay
	}
	res, err := m.Join(t.Context(), code, req)
	if err != nil {
		t.Fatal(err)
	}
	return connect(t, m, res)
}

func connect(t *testing.T, m *lobby.Manager, res lobby.JoinResult) *lobby.LocalConn {
	t.Helper()
	lc, err := m.Connect(t.Context(), res.Code, res.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lc.Close() })
	return lc
}

// Send sends a message from a player, failing the test on error.
func Send(t *testing.T, lc *lobby.LocalConn, typ string, data any) {
	t.Helper()
	if err := lc.Send(context.Background(), typ, data); err != nil {
		t.Fatal(err)
	}
}

// Wait reads events until one carries a T for which ok returns true.
func Wait[T any](t *testing.T, lc *lobby.LocalConn, ok func(T) bool) T {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, open := <-lc.Events():
			if !open {
				code, reason := lc.CloseStatus()
				t.Fatalf("connection closed while waiting: %d %s", code, reason)
			}
			if v, isT := ev.Data.(T); isT && ok(v) {
				return v
			}
		case <-timeout:
			var zero T
			t.Fatalf("timed out waiting for %T", zero)
		}
	}
}

// Any matches every value; use with Wait to take the next T.
func Any[T any](T) bool { return true }
