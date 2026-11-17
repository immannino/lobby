package connect4_test

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/examples/games/botkit"
	"github.com/immannino/lobby/examples/games/connect4"
	"github.com/immannino/lobby/examples/games/duel"
	gt "github.com/immannino/lobby/lobbytest"
)

// play drops pieces alternately (first, second, ...) into the given columns.
func play(t *testing.T, cols ...int) *connect4.Board {
	t.Helper()
	b := &connect4.Board{}
	b.Reset()
	side := duel.First
	for _, c := range cols {
		if !b.Play(side, msg(c)) {
			t.Fatalf("drop in column %d rejected", c)
		}
		if side == duel.First {
			side = duel.Second
		} else {
			side = duel.First
		}
	}
	return b
}

// msg builds a move message the way an in-process client would send it.
func msg(col int) lobby.Message {
	b, _ := json.Marshal(col)
	return lobby.Message{Type: "move", Data: b}
}

func TestGravityAndFullColumn(t *testing.T) {
	b := play(t, 3, 3)
	if b.Cells[5*connect4.Cols+3] != "R" || b.Cells[4*connect4.Cols+3] != "Y" {
		t.Fatalf("pieces didn't stack from the bottom: %v", b.Cells)
	}
	b = play(t, 0, 0, 0, 0, 0, 0)
	if b.Play(duel.First, msg(0)) {
		t.Error("dropped into a full column")
	}
	if b.Play(duel.First, msg(7)) || b.Play(duel.First, msg(-1)) {
		t.Error("dropped outside the board")
	}
}

func TestOutcome(t *testing.T) {
	tests := []struct {
		name string
		cols []int
		want duel.Side
	}{
		{"horizontal", []int{0, 0, 1, 1, 2, 2, 3}, duel.First},
		{"vertical", []int{0, 1, 0, 1, 0, 1, 0}, duel.First},
		{"diagonal up-right", []int{0, 1, 1, 2, 2, 3, 2, 3, 3, 6, 3}, duel.First},
		{"diagonal up-left", []int{6, 5, 5, 4, 4, 3, 4, 3, 3, 0, 3}, duel.First},
		{"second wins", []int{0, 1, 0, 1, 0, 1, 6, 1}, duel.Second},
		{"not yet", []int{0, 1, 2}, ""},
	}
	for _, tt := range tests {
		got, line := play(t, tt.cols...).Outcome()
		if got != tt.want {
			t.Errorf("%s: outcome = %q, want %q", tt.name, got, tt.want)
		}
		if tt.want != "" && len(line) != 4 {
			t.Errorf("%s: winning line = %v", tt.name, line)
		}
	}
	// A full board with no four in a row is a draw. Alternating these two
	// rows never lines up four in any direction.
	full := connect4.Board{Last: -1}
	for row := range connect4.Rows {
		pattern := "RRYYRRY"
		if row%2 == 1 {
			pattern = "YYRRYYR"
		}
		for col, ch := range pattern {
			full.Cells[row*connect4.Cols+col] = string(ch)
		}
	}
	if got, _ := full.Outcome(); got != duel.Draw {
		t.Errorf("full board outcome = %q, want draw", got)
	}
}

func TestBrainWinsAndBlocks(t *testing.T) {
	b := play(t, 0, 6, 1, 6, 2) // first has 0,1,2 on the bottom row; second to move
	v := duel.View{Seats: duel.Seats{First: "them", Second: "me"}, ToMove: "me", Board: *b}
	for range 20 {
		if _, col, _ := connect4.Brain("me", lobby.Event{Data: v}); col != 3 {
			t.Fatalf("Brain didn't block: played %v", col)
		}
	}
	b = play(t, 6, 0, 6, 1, 6, 2, 5) // second has 0,1,2; second to move and can win at 3
	v = duel.View{Seats: duel.Seats{First: "them", Second: "me"}, ToMove: "me", Board: *b}
	for range 20 {
		if _, col, _ := connect4.Brain("me", lobby.Event{Data: v}); col != 3 {
			t.Fatalf("Brain didn't take the win: played %v", col)
		}
	}
}

func TestBotPlaysAFullGame(t *testing.T) {
	var m *lobby.Manager
	m = gt.Manager(t, func() lobby.Game {
		return connect4.New(func(code string) {
			go botkit.Run(t.Context(), m, code, connect4.Brain, botkit.Options{Hello: "bot.hello", Logger: slog.New(slog.DiscardHandler)})
		})
	})
	ada, _ := gt.Create(t, m, "Ada")
	me := ada.Client().ID
	gt.Send(t, ada, "bot.add", nil)
	moves := 0
	for {
		v := gt.Wait(t, ada, func(v duel.View) bool { return v.Winner != "" || v.ToMove == me })
		if v.Winner != "" {
			t.Logf("result after %d of Ada's moves: %s", moves, v.Winner)
			return
		}
		b := v.Board.(connect4.Board)
		for col := range connect4.Cols { // leftmost open column
			if b.Cells[col] == "" {
				gt.Send(t, ada, "move", col)
				moves++
				break
			}
		}
	}
}
