package hangman_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/examples/games/hangman"
	gt "github.com/immannino/lobby/lobbytest"
)

func newGame(opts hangman.Options) func() lobby.Game {
	if len(opts.Words) == 0 {
		opts.Words = []string{"gopher"}
	}
	if opts.NextDelay == 0 {
		opts.NextDelay = 30 * time.Millisecond
	}
	return func() lobby.Game { return hangman.New(opts) }
}

func masked(v hangman.View) string {
	var b strings.Builder
	for _, ch := range v.Masked {
		if ch == "" {
			ch = "_"
		}
		b.WriteString(ch)
	}
	return b.String()
}

func TestSoloComputerPicks(t *testing.T) {
	m := gt.Manager(t, newGame(hangman.Options{}))
	ada, _ := gt.Create(t, m, "Ada")
	me := ada.Client().ID
	gt.Wait(t, ada, func(v hangman.View) bool { return v.Phase == hangman.Waiting })
	gt.Send(t, ada, "start", nil)

	v := gt.Wait(t, ada, func(v hangman.View) bool { return v.Phase == hangman.Guessing })
	if v.Setter != "" || v.Turn != me || masked(v) != "______" || v.Secret != "" {
		t.Fatalf("solo start: setter=%q turn=%q masked=%s secret=%q", v.Setter, v.Turn, masked(v), v.Secret)
	}

	gt.Send(t, ada, "guess", "o")
	v = gt.Wait(t, ada, func(v hangman.View) bool { return len(v.Guessed) == 1 })
	if masked(v) != "_O____" || v.Scores[me] != 1 || v.Misses != 0 {
		t.Fatalf("after O: masked=%s scores=%v misses=%d", masked(v), v.Scores, v.Misses)
	}

	gt.Send(t, ada, "guess", "o") // repeat: rejected, no state change
	gt.Send(t, ada, "guess", "z")
	v = gt.Wait(t, ada, func(v hangman.View) bool { return len(v.Guessed) == 2 })
	if v.Misses != 1 {
		t.Fatalf("after Z: misses=%d guessed=%v", v.Misses, v.Guessed)
	}

	gt.Send(t, ada, "guess", "Gopher")
	v = gt.Wait(t, ada, func(v hangman.View) bool { return v.Phase == hangman.Over })
	if v.Result != hangman.Solved || v.Solver != me || v.Secret != "GOPHER" || v.Scores[me] != 1+hangman.PointsSolve {
		t.Fatalf("solved: result=%s solver=%s secret=%s scores=%v", v.Result, v.Solver, v.Secret, v.Scores)
	}
	v = gt.Wait(t, ada, func(v hangman.View) bool { return v.Round == 2 })
	if v.Phase != hangman.Guessing || len(v.Guessed) != 0 {
		t.Fatalf("round 2: phase=%s guessed=%v", v.Phase, v.Guessed)
	}
}

func TestSetterSeesSecretOthersDont(t *testing.T) {
	m := gt.Manager(t, newGame(hangman.Options{}))
	ada, code := gt.Create(t, m, "Ada")
	bob := gt.Join(t, m, code, "Bob")
	tv := gt.Join(t, m, code, "") // display
	adaID, bobID := ada.Client().ID, bob.Client().ID

	gt.Send(t, bob, "start", nil) // only the VIP (Ada) can start
	gt.Send(t, ada, "start", nil)

	// Two players: the first round's setter is the first player.
	v := gt.Wait(t, bob, func(v hangman.View) bool { return v.Phase == hangman.Choosing })
	if v.Setter != adaID {
		t.Fatalf("setter = %s, want Ada", v.Setter)
	}
	gt.Send(t, bob, "word", "sneaky") // not the setter
	gt.Send(t, ada, "word", "no spaces")
	gt.Send(t, ada, "word", "ab") // too short
	gt.Send(t, ada, "word", "Lobby")

	adaView := gt.Wait(t, ada, func(v hangman.View) bool { return v.Phase == hangman.Guessing })
	bobView := gt.Wait(t, bob, func(v hangman.View) bool { return v.Phase == hangman.Guessing })
	tvView := gt.Wait(t, tv, func(v hangman.View) bool { return v.Phase == hangman.Guessing })
	if adaView.Secret != "LOBBY" {
		t.Errorf("setter secret = %q", adaView.Secret)
	}
	if bobView.Secret != "" || tvView.Secret != "" {
		t.Errorf("secret leaked: bob=%q display=%q", bobView.Secret, tvView.Secret)
	}
	if bobView.Turn != bobID {
		t.Fatalf("turn = %s, want Bob", bobView.Turn)
	}

	gt.Send(t, ada, "guess", "b") // setter can't guess
	gt.Send(t, bob, "guess", "b")
	v = gt.Wait(t, bob, func(v hangman.View) bool { return len(v.Guessed) == 1 })
	if masked(v) != "__BB_" || v.Scores[bobID] != 2 {
		t.Fatalf("after B: masked=%s scores=%v", masked(v), v.Scores)
	}

	// Next round rotates the setter to Bob.
	gt.Send(t, bob, "guess", "lobby")
	gt.Wait(t, bob, func(v hangman.View) bool { return v.Phase == hangman.Over })
	v = gt.Wait(t, bob, func(v hangman.View) bool { return v.Round == 2 && v.Phase == hangman.Choosing })
	if v.Setter != bobID {
		t.Fatalf("round 2 setter = %s, want Bob", v.Setter)
	}
}

func TestHangedPaysSetter(t *testing.T) {
	m := gt.Manager(t, newGame(hangman.Options{}))
	ada, code := gt.Create(t, m, "Ada")
	bob := gt.Join(t, m, code, "Bob")
	gt.Send(t, ada, "start", nil)
	gt.Wait(t, ada, func(v hangman.View) bool { return v.Phase == hangman.Choosing })
	gt.Send(t, ada, "word", "zzz")
	for i, letter := range []string{"a", "b", "c", "d", "e", "f"} {
		gt.Wait(t, bob, func(v hangman.View) bool { return len(v.Guessed) == i && v.Turn == bob.Client().ID })
		gt.Send(t, bob, "guess", letter)
	}
	v := gt.Wait(t, ada, func(v hangman.View) bool { return v.Phase == hangman.Over })
	if v.Result != hangman.Hanged || v.Misses != hangman.MaxMisses || v.Scores[ada.Client().ID] != hangman.PointsHanged {
		t.Fatalf("hanged: result=%s misses=%d scores=%v", v.Result, v.Misses, v.Scores)
	}
}

func TestTimersSkipAndPick(t *testing.T) {
	m := gt.Manager(t, newGame(hangman.Options{
		ChooseTime: 30 * time.Millisecond,
		TurnTime:   30 * time.Millisecond,
	}))
	ada, code := gt.Create(t, m, "Ada")
	bob := gt.Join(t, m, code, "Bob")
	cy := gt.Join(t, m, code, "Cy")
	gt.Send(t, ada, "start", nil)

	// Ada (setter) never picks: a random word is chosen for her.
	v := gt.Wait(t, cy, func(v hangman.View) bool { return v.Phase == hangman.Guessing })
	if len(v.Masked) != len("GOPHER") {
		t.Fatalf("auto-picked word length = %d", len(v.Masked))
	}
	// Nobody guesses: turns rotate between the guessers, never the setter.
	seen := map[string]bool{}
	for len(seen) < 2 {
		v = gt.Wait(t, cy, func(v hangman.View) bool { return v.Turn != "" && !seen[v.Turn] })
		if v.Turn == ada.Client().ID {
			t.Fatal("setter got a guessing turn")
		}
		seen[v.Turn] = true
	}
	if !seen[bob.Client().ID] || !seen[cy.Client().ID] {
		t.Errorf("turns went to %v", seen)
	}
}

func TestSetterLeavingAbandonsRound(t *testing.T) {
	m := gt.Manager(t, newGame(hangman.Options{}))
	ada, code := gt.Create(t, m, "Ada")
	bob := gt.Join(t, m, code, "Bob")
	gt.Send(t, ada, "start", nil)
	gt.Wait(t, bob, func(v hangman.View) bool { return v.Phase == hangman.Choosing })
	if err := ada.Leave(t.Context()); err != nil {
		t.Fatal(err)
	}
	v := gt.Wait(t, bob, func(v hangman.View) bool { return v.Phase == hangman.Over })
	if v.Result != hangman.Abandoned {
		t.Fatalf("result = %s", v.Result)
	}
	// Bob alone: next round is solo against the computer.
	v = gt.Wait(t, bob, func(v hangman.View) bool { return v.Round == 2 })
	if v.Setter != "" || v.Phase != hangman.Guessing || !slices.Contains([]string{bob.Client().ID}, v.Turn) {
		t.Fatalf("solo round: setter=%q phase=%s turn=%s", v.Setter, v.Phase, v.Turn)
	}
}
