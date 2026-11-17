// Package duel runs two-player, turn-based board games in a lobby room with
// "winner stays on" rotation: two players sit, everyone else queues, the
// challenger moves first, the winner defends, and leaving mid-match
// forfeits. Games supply only their rules as a Board; see the tictactoe and
// connect4 packages.
package duel

import (
	"context"
	_ "embed"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/immannino/lobby"
)

// ClientJS is the shared browser module for duel games: it renders seats,
// turns, results and countdowns, and games plug in their board. Serve it
// next to the game modules as duel.js.
//
//go:embed duel.js
var ClientJS []byte

// Side identifies a seat. Draw is used only as a match result.
type Side string

const (
	First  Side = "first" // the challenger; moves first
	Second Side = "second"
	Draw   Side = "draw"
)

// Board is the rules of a two-player game. Implementations should be pointer
// types so that Game can restore them from a snapshot.
type Board interface {
	// Reset clears the board for a new match.
	Reset()
	// Play applies side's move, carried in m. It reports false if the move
	// is malformed or illegal.
	Play(side Side, m lobby.Message) bool
	// Outcome returns First, Second or Draw once the match is over (with the
	// winning cells, if any), or "" while it's still going.
	Outcome() (Side, []int)
	// View returns a copy of the board for clients. It must not share
	// memory with the board: in-process clients receive the value itself.
	View() any
}

// Options configures a Game.
type Options struct {
	// ViewType is the message type for state updates sent to clients.
	ViewType string
	// MoveType is the message type clients send moves in. Default "move".
	MoveType string
	// NextDelay is the pause between matches. Default 4s.
	NextDelay time.Duration
	// AddBot, if set, lets players request a computer opponent ("bot.add").
	// It is called on the room goroutine and must not block: start the bot
	// in its own goroutine (see the botkit package).
	AddBot func(code string)
}

// Seats tracks who is playing, who is waiting, and the score.
type Seats struct {
	First  string         `json:"first"`  // client ID of the challenger
	Second string         `json:"second"` // client ID of the defender
	Queue  []string       `json:"queue"`  // waiting client IDs, in order
	Scores map[string]int `json:"scores"` // wins by client ID
	Bots   []string       `json:"bots"`   // client IDs of computer players
	Round  int            `json:"round"`
}

// View is what clients receive. Every slice and map is a fresh copy, and
// none are nil, so it is safe for in-process clients and encodes to JSON
// arrays and objects rather than null.
type View struct {
	Seats
	Board   any    `json:"board"`
	Turn    Side   `json:"turn"`
	Winner  Side   `json:"winner"` // "", First, Second or Draw
	Line    []int  `json:"line"`
	Forfeit bool   `json:"forfeit"`
	ToMove  string `json:"toMove"` // client ID whose move it is, or ""
	NextIn  int    `json:"nextIn"` // seconds until the next match, if one is scheduled
}

// Game is a lobby.Game for a duel. All methods run on the room goroutine.
type Game struct {
	lobby.BaseGame
	Seats
	Board   Board `json:"board"`
	Turn    Side  `json:"turn"`
	Winner  Side  `json:"winner"`
	Line    []int `json:"line"`
	Forfeit bool  `json:"forfeit"`

	opts      Options
	nextTimer func() bool // pending "start next match" timer, if any
	nextAt    time.Time   // when it fires
}

// New returns a Game played on board.
func New(board Board, opts Options) *Game {
	if opts.MoveType == "" {
		opts.MoveType = "move"
	}
	if opts.NextDelay <= 0 {
		opts.NextDelay = 4 * time.Second
	}
	return &Game{Board: board, Seats: Seats{Scores: map[string]int{}}, opts: opts}
}

// View returns the current state for clients.
func (g *Game) View() View {
	v := View{
		Seats: Seats{
			First: g.First, Second: g.Second, Round: g.Round,
			Queue:  append([]string{}, g.Queue...),
			Bots:   append([]string{}, g.Bots...),
			Scores: maps.Clone(g.Scores),
		},
		Board: g.Board.View(), Turn: g.Turn, Winner: g.Winner,
		Line: append([]int{}, g.Line...), Forfeit: g.Forfeit,
	}
	if v.Scores == nil {
		v.Scores = map[string]int{}
	}
	if g.Winner == "" && g.First != "" && g.Second != "" {
		v.ToMove = g.seat(g.Turn)
	}
	if g.nextTimer != nil {
		// Time actually remaining (a client may connect mid-countdown),
		// rounded up so sub-second delays don't read as "no next match".
		v.NextIn = max(1, int(math.Ceil(time.Until(g.nextAt).Seconds())))
	}
	return v
}

func (g *Game) seat(s Side) string {
	if s == First {
		return g.First
	}
	return g.Second
}

func (g *Game) publish(ctx context.Context, r *lobby.Room) {
	r.Broadcast(g.opts.ViewType, g.View())
	r.Persist(ctx)
}

func (g *Game) OnConnect(ctx context.Context, r *lobby.Room, c lobby.Client) {
	// After a restart a finished match has no pending timer; restart it.
	if g.Winner != "" && g.nextTimer == nil {
		g.scheduleNext(r)
	}
	r.Send(c.ID, g.opts.ViewType, g.View())
}

func (g *Game) OnJoin(ctx context.Context, r *lobby.Room, c lobby.Client) {
	if c.Role != lobby.RolePlayer {
		return
	}
	g.Queue = append(g.Queue, c.ID)
	g.fill()
	g.publish(ctx, r)
}

func (g *Game) OnLeave(ctx context.Context, r *lobby.Room, c lobby.Client) {
	g.Queue = slices.DeleteFunc(g.Queue, func(id string) bool { return id == c.ID })
	g.Bots = slices.DeleteFunc(g.Bots, func(id string) bool { return id == c.ID })
	delete(g.Scores, c.ID)

	seated := c.ID == g.First || c.ID == g.Second
	if seated && g.First != "" && g.Second != "" && g.Winner == "" {
		// Leaving mid-match forfeits to the opponent.
		g.Winner, g.Forfeit, g.Line = First, true, nil
		if c.ID == g.First {
			g.Winner = Second
		}
		g.Scores[g.seat(g.Winner)]++
		g.scheduleNext(r)
	}
	if c.ID == g.First {
		g.First = ""
	}
	if c.ID == g.Second {
		g.Second = ""
	}
	if g.Winner == "" {
		g.fill() // e.g. the lone waiting player left and someone queued can sit
	}
	g.publish(ctx, r)
}

func (g *Game) OnMessage(ctx context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	switch m.Type {
	case g.opts.MoveType:
		g.move(ctx, r, c.ID, m)
	case "bot.hello":
		// Bots announce themselves so the UI can label them.
		if !slices.Contains(g.Bots, c.ID) {
			g.Bots = append(g.Bots, c.ID)
			g.publish(ctx, r)
		}
	case "bot.add":
		if c.Role == lobby.RolePlayer && len(g.Bots) == 0 && g.opts.AddBot != nil {
			g.opts.AddBot(r.Code())
		}
	}
}

func (g *Game) move(ctx context.Context, r *lobby.Room, id string, m lobby.Message) {
	if g.View().ToMove != id {
		r.Send(id, "error", map[string]string{"error": "it's not your move"})
		return
	}
	if !g.Board.Play(g.Turn, m) {
		r.Send(id, "error", map[string]string{"error": "you can't play there"})
		return
	}
	if winner, line := g.Board.Outcome(); winner != "" {
		g.Winner, g.Line = winner, line
		if winner != Draw {
			g.Scores[id]++
		}
		g.scheduleNext(r)
	} else if g.Turn == First {
		g.Turn = Second
	} else {
		g.Turn = First
	}
	g.publish(ctx, r)
}

func (g *Game) scheduleNext(r *lobby.Room) {
	if g.nextTimer != nil {
		return
	}
	g.nextAt = time.Now().Add(g.opts.NextDelay)
	g.nextTimer = r.After(g.opts.NextDelay, func(ctx context.Context, r *lobby.Room) {
		g.nextTimer = nil
		g.rotate()
		g.fill()
		g.publish(ctx, r)
	})
}

// rotate applies "winner stays on": the loser (or both players, after a
// draw) go to the back of the queue, and the winner defends as Second.
func (g *Game) rotate() {
	var stay string
	var requeue []string
	switch g.Winner {
	case First:
		stay, requeue = g.First, []string{g.Second}
	case Second:
		stay, requeue = g.Second, []string{g.First}
	default:
		requeue = []string{g.First, g.Second}
	}
	for _, id := range requeue {
		if id != "" {
			g.Queue = append(g.Queue, id)
		}
	}
	g.First, g.Second = "", stay
	g.Winner, g.Line, g.Forfeit = "", nil, false
	g.Board.Reset()
}

// fill seats players from the queue and starts a match once both seats are
// taken. It does nothing while a match is in progress or awaiting rotation.
func (g *Game) fill() {
	if g.Winner != "" || (g.First != "" && g.Second != "") {
		return
	}
	if g.Second == "" && g.First != "" {
		g.Second, g.First = g.First, "" // a lone seated player defends
	}
	for g.First == "" || g.Second == "" {
		if len(g.Queue) == 0 {
			return
		}
		next := g.Queue[0]
		g.Queue = g.Queue[1:]
		if g.Second == "" {
			g.Second = next
		} else {
			g.First = next
		}
	}
	g.Board.Reset()
	g.Turn, g.Winner, g.Line, g.Forfeit = First, "", nil, false
	g.Round++
}

// Snapshot and Restore implement lobby.Snapshotter.
func (g *Game) Snapshot() ([]byte, error) { return json.Marshal(g) }

func (g *Game) Restore(b []byte) error {
	// Board is an interface holding a pointer, so json fills it in place.
	if err := json.Unmarshal(b, g); err != nil {
		return err
	}
	if g.Scores == nil {
		g.Scores = map[string]int{}
	}
	return nil
}
