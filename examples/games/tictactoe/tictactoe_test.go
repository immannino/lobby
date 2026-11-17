package tictactoe_test

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/examples/games/botkit"
	"github.com/immannino/lobby/examples/games/duel"
	"github.com/immannino/lobby/examples/games/tictactoe"
	gt "github.com/immannino/lobby/lobbytest"
)

func board(s string) tictactoe.Board {
	var b tictactoe.Board
	for i, ch := range s {
		if ch != '.' {
			b[i] = string(ch)
		}
	}
	return b
}

func TestOutcome(t *testing.T) {
	tests := []struct {
		board string
		want  duel.Side
	}{
		{"XXX......", duel.First},
		{"O..O..O..", duel.Second},
		{"X...X...X", duel.First},
		{"..O.O.O..", duel.Second},
		{"XOXXOOOXX", duel.Draw},
		{"XO.......", ""},
	}
	for _, tt := range tests {
		b := board(tt.board)
		if got, _ := b.Outcome(); got != tt.want {
			t.Errorf("%s: outcome = %q, want %q", tt.board, got, tt.want)
		}
	}
}

func TestBrainTakesWin(t *testing.T) {
	v := duel.View{Seats: duel.Seats{First: "x", Second: "me"}, ToMove: "me", Board: board("OO.XX....")}
	for range 20 { // winning beats blocking, every time
		if _, cell, _ := tictactoe.Brain("me", lobby.Event{Type: "ttt", Data: v}); cell != 2 {
			t.Fatalf("Brain played %v, want 2", cell)
		}
	}
}

func TestFullGameAgainstBot(t *testing.T) {
	var m *lobby.Manager
	m = gt.Manager(t, func() lobby.Game {
		return tictactoe.New(func(code string) {
			go botkit.Run(t.Context(), m, code, tictactoe.Brain, botkit.Options{Hello: "bot.hello", Logger: slog.New(slog.DiscardHandler)})
		})
	})
	ada, _ := gt.Create(t, m, "Ada")
	me := ada.Client().ID
	gt.Send(t, ada, "bot.add", nil)

	// The bot joins over its own LocalConn and challenges, so it's First.
	v := gt.Wait(t, ada, func(v duel.View) bool { return len(v.Bots) == 1 && v.First != "" })
	if v.First != v.Bots[0] || v.Second != me {
		t.Fatalf("seating: first=%s second=%s bots=%v", v.First, v.Second, v.Bots)
	}
	// Ada plays the first free cell until the match ends.
	for {
		v = gt.Wait(t, ada, func(v duel.View) bool { return v.Winner != "" || v.ToMove == me })
		if v.Winner != "" {
			break
		}
		b := v.Board.(tictactoe.Board)
		gt.Send(t, ada, "move", slices.Index(b[:], ""))
	}
	t.Logf("result: %s (bot was first)", v.Winner)
}
