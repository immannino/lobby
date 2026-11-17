// Package connect4 is winner-stays-on Connect Four for the lobby, built on
// the duel package. It contributes only the rules, a bot brain and the
// browser module.
package connect4

import (
	_ "embed"
	"math/rand/v2"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/examples/games/duel"
)

// Name is the game kind clients pass when creating a room.
const Name = "connect4"

// ClientJS is the browser module for this game (imports ../duel.js).
//
//go:embed connect4.js
var ClientJS []byte

const (
	Cols = 7
	Rows = 6
)

// New returns a Connect Four game. addBot may be nil to disable computer
// opponents.
func New(addBot func(code string)) lobby.Game {
	b := &Board{}
	b.Reset()
	return duel.New(b, duel.Options{ViewType: "c4", AddBot: addBot})
}

// Board holds the grid in row-major order with row 0 at the top. Cells are
// "", "R" (First) or "Y" (Second).
type Board struct {
	Cells [Rows * Cols]string `json:"cells"`
	Last  int                 `json:"last"` // cell of the most recent drop, or -1
}

func mark(s duel.Side) string {
	if s == duel.First {
		return "R"
	}
	return "Y"
}

func (b *Board) Reset() { *b = Board{Last: -1} }

// Play expects the column (0-6) as the message data; the piece falls to the
// lowest empty row.
func (b *Board) Play(side duel.Side, m lobby.Message) bool {
	var col int
	if m.Decode(&col) != nil {
		return false
	}
	cell, ok := b.drop(col)
	if !ok {
		return false
	}
	b.Cells[cell] = mark(side)
	b.Last = cell
	return true
}

// drop returns the cell a piece dropped in col would land in.
func (b *Board) drop(col int) (int, bool) {
	if col < 0 || col >= Cols {
		return 0, false
	}
	for row := Rows - 1; row >= 0; row-- {
		if i := row*Cols + col; b.Cells[i] == "" {
			return i, true
		}
	}
	return 0, false // column full
}

// directions to scan for four in a row: right, down, down-right, down-left.
var directions = [4][2]int{{0, 1}, {1, 0}, {1, 1}, {1, -1}}

func (b *Board) Outcome() (duel.Side, []int) {
	if line := b.fourInARow(); line != nil {
		if b.Cells[line[0]] == "R" {
			return duel.First, line
		}
		return duel.Second, line
	}
	for _, c := range b.Cells[:Cols] { // the top row is full only when the board is
		if c == "" {
			return "", nil
		}
	}
	return duel.Draw, nil
}

func (b *Board) fourInARow() []int {
	for row := range Rows {
		for col := range Cols {
			m := b.Cells[row*Cols+col]
			if m == "" {
				continue
			}
			for _, d := range directions {
				line := []int{row*Cols + col}
				for step := 1; step < 4; step++ {
					r, c := row+d[0]*step, col+d[1]*step
					if r < 0 || r >= Rows || c < 0 || c >= Cols || b.Cells[r*Cols+c] != m {
						break
					}
					line = append(line, r*Cols+c)
				}
				if len(line) == 4 {
					return line
				}
			}
		}
	}
	return nil
}

func (b *Board) View() any { return *b } // arrays copy by value

// Brain takes a win, blocks a loss, avoids handing the opponent a win, and
// otherwise prefers central columns. Use with botkit.Run.
func Brain(me string, ev lobby.Event) (string, any, bool) {
	v, ok := ev.Data.(duel.View)
	if !ok || v.ToMove != me {
		return "", nil, false
	}
	b := v.Board.(Board)
	mine, theirs := "Y", "R"
	if v.First == me {
		mine, theirs = "R", "Y"
	}
	return "move", bestColumn(b, mine, theirs), true
}

func bestColumn(b Board, mine, theirs string) int {
	wins := func(b Board, col int, m string) bool {
		cell, ok := b.drop(col)
		if !ok {
			return false
		}
		b.Cells[cell] = m // b is a copy
		return b.fourInARow() != nil
	}
	var legal []int
	for col := range Cols {
		if _, ok := b.drop(col); ok {
			legal = append(legal, col)
		}
	}
	for _, col := range legal {
		if wins(b, col, mine) {
			return col
		}
	}
	for _, col := range legal {
		if wins(b, col, theirs) {
			return col
		}
	}
	// Prefer moves that don't let the opponent win by playing on top.
	var safe []int
	for _, col := range legal {
		after := b
		cell, _ := after.drop(col)
		after.Cells[cell] = mine
		if !wins(after, col, theirs) {
			safe = append(safe, col)
		}
	}
	if len(safe) == 0 {
		safe = legal
	}
	// Weight towards the center, where more lines pass through.
	weights := [Cols]int{1, 2, 3, 4, 3, 2, 1}
	total := 0
	for _, col := range safe {
		total += weights[col]
	}
	pick := rand.IntN(total)
	for _, col := range safe {
		if pick -= weights[col]; pick < 0 {
			return col
		}
	}
	return safe[0]
}
