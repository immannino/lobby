// Package hangman is a party hangman game for the lobby. Once everyone is in,
// the VIP starts the game. Players take turns as the setter, who picks a
// secret word on their phone; everyone else takes timed turns guessing
// letters (or the whole word). With one player the computer picks the word.
//
// It shows per-client views: the secret goes only to the setter until the
// round ends, so each client is sent its own copy rather than a broadcast.
package hangman

import (
	"context"
	_ "embed"
	"encoding/json"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/immannino/lobby"
)

// Name is the game kind clients pass when creating a room.
const Name = "hangman"

// ClientJS is the browser module for this game.
//
//go:embed hangman.js
var ClientJS []byte

// MaxMisses is how many wrong guesses complete the gallows.
const MaxMisses = 6

// Phase is where a round is.
type Phase string

const (
	Waiting  Phase = "waiting"  // in the lobby until the VIP starts
	Choosing Phase = "choosing" // the setter is picking a word
	Guessing Phase = "guessing"
	Over     Phase = "over" // word revealed; next round soon
)

// Result is how a round ended.
type Result string

const (
	Solved    Result = "solved"
	Hanged    Result = "hanged"
	Abandoned Result = "abandoned" // the setter left
)

// Points awarded.
const (
	PointsPerLetter  = 1 // per occurrence of a correct letter
	PointsLastLetter = 3 // bonus for revealing the final letter
	PointsSolve      = 5 // guessing the whole word
	PointsHanged     = 3 // to the setter when nobody gets it
)

// Options configures a game. Zero values get defaults.
type Options struct {
	TurnTime   time.Duration // time per guess before the turn is skipped; default 25s
	ChooseTime time.Duration // time for the setter to pick before a random word; default 30s
	NextDelay  time.Duration // pause between rounds; default 6s
	Words      []string      // words for random picks; default built-in list
}

// View is what each client receives. Secret is set only for the setter,
// and for everyone once the round is over.
type View struct {
	Phase     Phase          `json:"phase"`
	Round     int            `json:"round"`
	Setter    string         `json:"setter"` // client ID, or "" if the computer picked
	Turn      string         `json:"turn"`   // client ID of the guesser to move
	Masked    []string       `json:"masked"` // one entry per letter, "" while hidden
	Guessed   []string       `json:"guessed"`
	Misses    int            `json:"misses"`
	MaxMisses int            `json:"maxMisses"`
	Scores    map[string]int `json:"scores"`
	Result    Result         `json:"result"`
	Solver    string         `json:"solver"`
	Secret    string         `json:"secret,omitempty"`
	Deadline  int            `json:"deadline"` // seconds left in the current phase or turn, 0 if none
}

type game struct {
	lobby.BaseGame
	Phase      Phase          `json:"phase"`
	Round      int            `json:"round"`
	Setter     string         `json:"setter"`
	LastSetter string         `json:"lastSetter"`
	Word       string         `json:"word"`
	Guessed    []string       `json:"guessed"`
	Misses     int            `json:"misses"`
	Turn       string         `json:"turn"`
	Scores     map[string]int `json:"scores"`
	Result     Result         `json:"result"`
	Solver     string         `json:"solver"`

	opts     Options
	stop     func() bool // pending phase/turn timer
	timerGen int         // bumps on every setTimer, so superseded timers no-op
	deadline time.Time
}

// New returns a hangman game.
func New(opts Options) lobby.Game {
	if opts.TurnTime <= 0 {
		opts.TurnTime = 25 * time.Second
	}
	if opts.ChooseTime <= 0 {
		opts.ChooseTime = 30 * time.Second
	}
	if opts.NextDelay <= 0 {
		opts.NextDelay = 6 * time.Second
	}
	if len(opts.Words) == 0 {
		opts.Words = defaultWords
	}
	return &game{Phase: Waiting, Scores: map[string]int{}, opts: opts}
}

// ---- lobby.Game ----

func (g *game) OnJoin(ctx context.Context, r *lobby.Room, c lobby.Client) {
	g.publish(ctx, r) // new face in the guessing order / scores
}

func (g *game) OnLeave(ctx context.Context, r *lobby.Room, c lobby.Client) {
	delete(g.Scores, c.ID)
	switch {
	case len(r.Players()) == 0:
		g.setTimer(r, 0, nil)
		g.Phase, g.Setter, g.Turn = Waiting, "", ""
	case c.ID == g.Setter && (g.Phase == Choosing || g.Phase == Guessing):
		g.end(r, Abandoned)
	case c.ID == g.Turn && g.Phase == Guessing:
		g.advanceTurn(r)
	}
	g.publish(ctx, r)
}

func (g *game) OnConnect(ctx context.Context, r *lobby.Room, c lobby.Client) {
	// After a restart there's no timer running; give the phase a fresh one.
	if g.stop == nil {
		switch g.Phase {
		case Choosing:
			g.setTimer(r, g.opts.ChooseTime, g.chooseTimeout)
		case Guessing:
			g.setTimer(r, g.opts.TurnTime, g.turnTimeout)
		case Over:
			g.setTimer(r, g.opts.NextDelay, g.startRound)
		}
	}
	if g.Phase == Guessing && g.Turn == "" {
		g.advanceTurn(r) // the first guesser has just arrived
		g.publish(ctx, r)
		return
	}
	r.Send(c.ID, "hm", g.view(c.ID))
}

func (g *game) OnMessage(ctx context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	fail := func(msg string) { r.Send(c.ID, "error", map[string]string{"error": msg}) }
	switch m.Type {
	case "start":
		if c.ID != r.VIP() {
			fail("only the VIP can start the game")
			return
		}
		if g.Phase != Waiting {
			return
		}
		g.startRound(ctx, r)
		return
	case "word":
		if g.Phase != Choosing || c.ID != g.Setter {
			fail("you're not choosing the word")
			return
		}
		var word string
		if m.Decode(&word) != nil {
			fail("send the word as a string")
			return
		}
		word, ok := normalize(word)
		if !ok || len(word) < 3 || len(word) > 14 {
			fail("pick a word of 3 to 14 letters, A to Z only")
			return
		}
		g.beginGuessing(r, word)
	case "random":
		if g.Phase != Choosing || c.ID != g.Setter {
			fail("you're not choosing the word")
			return
		}
		g.beginGuessing(r, g.randomWord())
	case "guess":
		if g.Phase != Guessing || c.ID != g.Turn {
			fail("it's not your turn")
			return
		}
		var guess string
		if m.Decode(&guess) != nil {
			fail("send your guess as a string")
			return
		}
		guess, ok := normalize(guess)
		if !ok || guess == "" {
			fail("guess a letter or the whole word")
			return
		}
		if len(guess) == 1 && slices.Contains(g.Guessed, guess) {
			fail(guess + " was already guessed")
			return
		}
		g.guess(r, c.ID, guess)
	default:
		return
	}
	g.publish(ctx, r)
}

func (g *game) Snapshot() ([]byte, error) { return json.Marshal(g) }

func (g *game) Restore(b []byte) error {
	if err := json.Unmarshal(b, g); err != nil {
		return err
	}
	if g.Scores == nil {
		g.Scores = map[string]int{}
	}
	return nil
}

// ---- rounds ----

func (g *game) startRound(ctx context.Context, r *lobby.Room) {
	players := r.Players()
	g.Guessed, g.Misses, g.Result, g.Solver, g.Word, g.Turn = nil, 0, "", "", "", ""
	if len(players) == 0 {
		g.setTimer(r, 0, nil)
		g.Phase, g.Setter = Waiting, ""
		g.publish(ctx, r)
		return
	}
	g.Round++
	if len(players) == 1 {
		// Solo: the computer sets, the player guesses.
		g.Setter = ""
		g.beginGuessing(r, g.randomWord())
	} else {
		g.Setter = nextAfter(ids(players), g.LastSetter)
		g.LastSetter = g.Setter
		g.Phase = Choosing
		g.setTimer(r, g.opts.ChooseTime, g.chooseTimeout)
	}
	g.publish(ctx, r)
}

func (g *game) chooseTimeout(ctx context.Context, r *lobby.Room) {
	g.beginGuessing(r, g.randomWord())
	g.publish(ctx, r)
}

func (g *game) beginGuessing(r *lobby.Room, word string) {
	g.Word, g.Phase = word, Guessing
	g.Turn = ""
	g.advanceTurn(r)
}

func (g *game) turnTimeout(ctx context.Context, r *lobby.Room) {
	g.advanceTurn(r) // too slow: skip
	g.publish(ctx, r)
}

// advanceTurn passes the turn to the next guesser (anyone but the setter),
// preferring connected ones, and restarts the turn timer.
func (g *game) advanceTurn(r *lobby.Room) {
	var all, connected []string
	for _, p := range r.Players() {
		if p.ID == g.Setter {
			continue
		}
		all = append(all, p.ID)
		if p.Connected {
			connected = append(connected, p.ID)
		}
	}
	switch {
	case len(connected) > 0:
		g.Turn = nextAfter(connected, g.Turn)
	case len(all) > 0:
		g.Turn = nextAfter(all, g.Turn) // nobody connected yet; the timer moves things along
	default:
		g.Turn = ""
	}
	g.setTimer(r, g.opts.TurnTime, g.turnTimeout)
}

func (g *game) guess(r *lobby.Room, id, guess string) {
	if len(guess) == 1 {
		g.Guessed = append(g.Guessed, guess)
		if n := strings.Count(g.Word, guess); n > 0 {
			g.Scores[id] += n * PointsPerLetter
			if g.revealed() {
				g.Scores[id] += PointsLastLetter
				g.Solver = id
				g.end(r, Solved)
				return
			}
		} else {
			g.Misses++
		}
	} else if guess == g.Word {
		g.Scores[id] += PointsSolve
		g.Solver = id
		g.end(r, Solved)
		return
	} else {
		g.Misses++ // a wrong word costs a miss too
	}
	if g.Misses >= MaxMisses {
		g.end(r, Hanged)
		return
	}
	g.advanceTurn(r)
}

func (g *game) end(r *lobby.Room, result Result) {
	g.Phase, g.Result, g.Turn = Over, result, ""
	if result == Hanged && g.Setter != "" {
		g.Scores[g.Setter] += PointsHanged
	}
	g.setTimer(r, g.opts.NextDelay, g.startRound)
}

func (g *game) revealed() bool {
	for _, ch := range g.Word {
		if !slices.Contains(g.Guessed, string(ch)) {
			return false
		}
	}
	return true
}

// ---- views and plumbing ----

// publish sends each client its own view: only the setter sees the secret.
func (g *game) publish(ctx context.Context, r *lobby.Room) {
	for _, c := range r.Clients() {
		r.Send(c.ID, "hm", g.view(c.ID))
	}
	r.Persist(ctx)
}

func (g *game) view(viewer string) View {
	v := View{
		Phase: g.Phase, Round: g.Round, Setter: g.Setter, Turn: g.Turn,
		Guessed: append([]string{}, g.Guessed...), Misses: g.Misses, MaxMisses: MaxMisses,
		Scores: maps.Clone(g.Scores), Result: g.Result, Solver: g.Solver,
		Masked: make([]string, len(g.Word)),
	}
	for i, ch := range g.Word {
		if g.Phase == Over || slices.Contains(g.Guessed, string(ch)) {
			v.Masked[i] = string(ch)
		}
	}
	if g.Phase == Over || (viewer == g.Setter && g.Setter != "") {
		v.Secret = g.Word
	}
	if g.stop != nil {
		v.Deadline = max(0, int(time.Until(g.deadline).Round(time.Second).Seconds()))
	}
	return v
}

// setTimer replaces the pending timer. d == 0 or fn == nil just cancels it.
func (g *game) setTimer(r *lobby.Room, d time.Duration, fn func(context.Context, *lobby.Room)) {
	g.timerGen++
	if g.stop != nil {
		g.stop()
		g.stop = nil
	}
	if d <= 0 || fn == nil {
		return
	}
	gen := g.timerGen
	g.deadline = time.Now().Add(d)
	g.stop = r.After(d, func(ctx context.Context, r *lobby.Room) {
		// A timer can fire (and queue this) just before being stopped, so
		// check it's still the current one.
		if gen != g.timerGen {
			return
		}
		g.stop = nil
		fn(ctx, r)
	})
}

func (g *game) randomWord() string {
	w, _ := normalize(g.opts.Words[rand.IntN(len(g.opts.Words))])
	return w
}

// normalize uppercases and trims s, reporting whether it is all A-Z.
func normalize(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	for _, ch := range s {
		if ch < 'A' || ch > 'Z' {
			return s, false
		}
	}
	return s, true
}

// nextAfter returns the element after cur in order (wrapping), or the first
// element if cur isn't present.
func nextAfter(order []string, cur string) string {
	i := slices.Index(order, cur)
	return order[(i+1)%len(order)]
}

func ids(cs []lobby.Client) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
