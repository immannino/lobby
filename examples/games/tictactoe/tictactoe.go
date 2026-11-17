// Package tictactoe is winner-stays-on tic-tac-toe for the lobby, built on
// the duel package. It contributes only the rules, a bot brain and the
// browser module.
package tictactoe

import (
	_ "embed"
	"math/rand/v2"
	"slices"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/examples/games/duel"
)

// Name is the game kind clients pass when creating a room.
const Name = "tictactoe"

// ClientJS is the browser module for this game (imports ../duel.js).
//
//go:embed tictactoe.js
var ClientJS []byte

// New returns a tic-tac-toe game. addBot may be nil to disable computer
// opponents.
func New(addBot func(code string)) lobby.Game {
	return duel.New(&Board{}, duel.Options{ViewType: "ttt", AddBot: addBot})
}

// Board is a 3×3 grid in row-major order: "", "X" (First) or "O" (Second).
type Board [9]string

var lines = [8][3]int{
	{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, // rows
	{0, 3, 6}, {1, 4, 7}, {2, 5, 8}, // columns
	{0, 4, 8}, {2, 4, 6}, // diagonals
}

func mark(s duel.Side) string {
	if s == duel.First {
		return "X"
	}
	return "O"
}

func (b *Board) Reset() { *b = Board{} }

// Play expects the cell index (0-8) as the message data.
func (b *Board) Play(side duel.Side, m lobby.Message) bool {
	var cell int
	if m.Decode(&cell) != nil || cell < 0 || cell >= len(b) || b[cell] != "" {
		return false
	}
	b[cell] = mark(side)
	return true
}

func (b *Board) Outcome() (duel.Side, []int) {
	for _, l := range lines {
		if m := b[l[0]]; m != "" && m == b[l[1]] && m == b[l[2]] {
			if m == "X" {
				return duel.First, l[:]
			}
			return duel.Second, l[:]
		}
	}
	if !slices.Contains(b[:], "") {
		return duel.Draw, nil
	}
	return "", nil
}

func (b *Board) View() any { return *b } // arrays copy by value

// Brain plays decently but beatably: take a win, usually block a loss,
// like the center, otherwise play anywhere. Use with botkit.Run.
func Brain(me string, ev lobby.Event) (string, any, bool) {
	v, ok := ev.Data.(duel.View)
	if !ok || v.ToMove != me {
		return "", nil, false
	}
	b := v.Board.(Board)
	mine, theirs := "O", "X"
	if v.First == me {
		mine, theirs = "X", "O"
	}
	if c, ok := completing(b, mine); ok {
		return "move", c, true
	}
	if c, ok := completing(b, theirs); ok && rand.IntN(4) > 0 { // misses 1 in 4 blocks
		return "move", c, true
	}
	if b[4] == "" && rand.IntN(2) == 0 {
		return "move", 4, true
	}
	var free []int
	for i, cell := range b {
		if cell == "" {
			free = append(free, i)
		}
	}
	return "move", free[rand.IntN(len(free))], true
}

// completing finds a cell that gives mark three in a row.
func completing(b Board, mark string) (int, bool) {
	for _, l := range lines {
		count, empty := 0, -1
		for _, i := range l {
			switch b[i] {
			case mark:
				count++
			case "":
				empty = i
			}
		}
		if count == 2 && empty >= 0 {
			return empty, true
		}
	}
	return 0, false
}
