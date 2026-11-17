package duel_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/examples/games/duel"
	gt "github.com/immannino/lobby/lobbytest"
)

// race is a trivial board: the first move wins, and playing 0 is a draw.
type race struct{ Moves []int }

func (b *race) Reset() { b.Moves = nil }
func (b *race) Play(_ duel.Side, m lobby.Message) bool {
	var n int
	if m.Decode(&n) != nil || n < 0 {
		return false
	}
	b.Moves = append(b.Moves, n)
	return true
}
func (b *race) Outcome() (duel.Side, []int) {
	switch {
	case len(b.Moves) == 0:
		return "", nil
	case b.Moves[0] == 0:
		return duel.Draw, nil
	case len(b.Moves) == 1:
		return duel.First, []int{b.Moves[0]}
	}
	return duel.Second, nil
}
func (b *race) View() any { return append([]int{}, b.Moves...) }

func newRace() lobby.Game {
	g := duel.New(&race{}, duel.Options{ViewType: "race", NextDelay: 20 * time.Millisecond})
	return g
}

func TestSeatingRotationAndForfeit(t *testing.T) {
	m := gt.Manager(t, newRace)
	ada, code := gt.Create(t, m, "Ada")
	bob := gt.Join(t, m, code, "Bob")
	cy := gt.Join(t, m, code, "Cy")
	adaID, bobID, cyID := ada.Client().ID, bob.Client().ID, cy.Client().ID

	// The first player defends; the next is the challenger and moves first.
	v := gt.Wait(t, cy, func(v duel.View) bool { return len(v.Queue) == 1 })
	if v.Second != adaID || v.First != bobID || v.ToMove != bobID || v.Queue[0] != cyID {
		t.Fatalf("seating: first=%s second=%s toMove=%s queue=%v", v.First, v.Second, v.ToMove, v.Queue)
	}

	// Out of turn, and malformed moves, are rejected without changing state.
	gt.Send(t, ada, "move", 1)
	gt.Send(t, bob, "move", "nope")

	gt.Send(t, bob, "move", 7) // first move wins
	v = gt.Wait(t, cy, func(v duel.View) bool { return v.Winner != "" })
	if v.Winner != duel.First || v.Scores[bobID] != 1 || v.NextIn == 0 {
		t.Fatalf("win: winner=%s scores=%v nextIn=%d", v.Winner, v.Scores, v.NextIn)
	}

	// Winner stays on as Second; the queue's head challenges; the loser queues.
	v = gt.Wait(t, cy, func(v duel.View) bool { return v.Round == 2 })
	if v.Second != bobID || v.First != cyID || len(v.Queue) != 1 || v.Queue[0] != adaID {
		t.Fatalf("round 2: first=%s second=%s queue=%v", v.First, v.Second, v.Queue)
	}

	// Leaving mid-match forfeits to the opponent.
	if err := cy.Leave(t.Context()); err != nil {
		t.Fatal(err)
	}
	v = gt.Wait(t, ada, func(v duel.View) bool { return v.Forfeit })
	if v.Winner != duel.Second || v.Scores[bobID] != 2 {
		t.Fatalf("forfeit: winner=%s scores=%v", v.Winner, v.Scores)
	}
	v = gt.Wait(t, ada, func(v duel.View) bool { return v.Round == 3 })
	if v.Second != bobID || v.First != adaID {
		t.Fatalf("round 3: first=%s second=%s", v.First, v.Second)
	}
}

func TestDrawRequeuesBoth(t *testing.T) {
	m := gt.Manager(t, newRace)
	ada, code := gt.Create(t, m, "Ada")
	bob := gt.Join(t, m, code, "Bob")
	gt.Wait(t, bob, func(v duel.View) bool { return v.ToMove == bob.Client().ID })
	gt.Send(t, bob, "move", 0) // draw
	v := gt.Wait(t, ada, func(v duel.View) bool { return v.Round == 2 })
	// Both requeue (challenger first), so they swap seats.
	if v.Second != bob.Client().ID || v.First != ada.Client().ID {
		t.Fatalf("after draw: first=%s second=%s", v.First, v.Second)
	}
	if len(v.Scores) != 0 && (v.Scores[ada.Client().ID] != 0 || v.Scores[bob.Client().ID] != 0) {
		t.Errorf("draw scored: %v", v.Scores)
	}
}

func TestSnapshotRestoresBoard(t *testing.T) {
	g := duel.New(&race{}, duel.Options{ViewType: "race"})
	g.First, g.Second, g.Round = "a", "b", 3
	g.Board.(*race).Moves = []int{4, 2}
	b, err := g.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored := duel.New(&race{}, duel.Options{ViewType: "race"})
	if err := restored.Restore(b); err != nil {
		t.Fatal(err)
	}
	if got := restored.Board.(*race).Moves; len(got) != 2 || got[0] != 4 || restored.Round != 3 || restored.First != "a" {
		t.Errorf("restored = %+v board=%v", restored.Seats, got)
	}
}

func TestViewJSONHasNoNulls(t *testing.T) {
	b, err := json.Marshal(duel.New(&race{}, duel.Options{}).View())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("view JSON contains null (browser code expects arrays/objects): %s", b)
	}
}
