// Package poll runs quick polls in a lobby room. The VIP writes a question,
// sends it out, and reveals the results when enough people have voted, or
// runs it "live" so everyone watches the distribution move. Past polls stay
// in the room, so a run of them works as a quick survey.
//
// Votes are private until shown: each client gets its own view, and counts
// appear only for the host, for live polls, or after the reveal.
package poll

import (
	"context"
	_ "embed"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/immannino/lobby"
)

// Name is the game kind clients pass when creating a room.
const Name = "poll"

// ClientJS is the browser module for this game.
//
//go:embed poll.js
var ClientJS []byte

// Limits on what a poll can contain.
const (
	MaxQuestion   = 140 // characters
	MaxOption     = 60  // characters
	MinOptions    = 2
	MaxOptions    = 8
	MaxPastPolls  = 20
	ViewType      = "poll"
	noVote        = -1
	errNotHost    = "only the host (VIP) can do that"
	errNotRunning = "there's no poll open right now"
)

// Phase is where the current poll is.
type Phase string

const (
	Drafting Phase = "drafting" // waiting for the host to send a poll
	Open     Phase = "open"     // accepting votes
	Revealed Phase = "revealed" // results shown to everyone
)

// Settings is what the host sends to open a poll ("poll.open").
type Settings struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	// Live shows the distribution to everyone while voting is open.
	Live bool `json:"live"`
	// AutoReveal reveals the results once every connected player has voted.
	AutoReveal bool `json:"autoReveal"`
}

// Result is a finished poll.
type Result struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Counts   []int    `json:"counts"`
	Total    int      `json:"total"`
}

// View is what each client receives. Counts is empty unless CountsVisible.
type View struct {
	Phase         Phase    `json:"phase"`
	ID            int      `json:"id"` // increments per poll, so clients can reset their UI
	Question      string   `json:"question"`
	Options       []string `json:"options"`
	Live          bool     `json:"live"`
	AutoReveal    bool     `json:"autoReveal"`
	CountsVisible bool     `json:"countsVisible"`
	Counts        []int    `json:"counts"`
	Voters        []string `json:"voters"`   // client IDs that have voted (not what they chose)
	Eligible      int      `json:"eligible"` // connected players, plus anyone who voted and dropped
	MyVote        int      `json:"myVote"`   // option index, or -1
	Past          []Result `json:"past"`     // newest first
}

type game struct {
	lobby.BaseGame
	Phase    Phase          `json:"phase"`
	ID       int            `json:"id"`
	Settings Settings       `json:"settings"`
	Votes    map[string]int `json:"votes"` // client ID → option index
	Past     []Result       `json:"past"`
}

// New returns a poll game.
func New() lobby.Game {
	return &game{Phase: Drafting, Votes: map[string]int{}}
}

func (g *game) OnConnect(ctx context.Context, r *lobby.Room, c lobby.Client) {
	g.publish(ctx, r) // eligible count changed; also catches this client up
}

func (g *game) OnDisconnect(ctx context.Context, r *lobby.Room, c lobby.Client) {
	g.publish(ctx, r)
}

func (g *game) OnJoin(ctx context.Context, r *lobby.Room, c lobby.Client) {
	g.publish(ctx, r)
}

func (g *game) OnLeave(ctx context.Context, r *lobby.Room, c lobby.Client) {
	delete(g.Votes, c.ID) // their seat is gone, so is their vote
	g.maybeAutoReveal(r)
	g.publish(ctx, r)
}

func (g *game) OnMessage(ctx context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	fail := func(msg string) { r.Send(c.ID, "error", map[string]string{"error": msg}) }
	isHost := c.ID == r.VIP()

	switch m.Type {
	case "poll.open":
		if !isHost {
			fail(errNotHost)
			return
		}
		var s Settings
		if m.Decode(&s) != nil {
			fail("send the poll as {question, options}")
			return
		}
		s, err := clean(s)
		if err != "" {
			fail(err)
			return
		}
		if g.Phase == Open {
			g.archive() // replacing an open poll still keeps its results
		}
		g.ID++
		g.Phase, g.Settings, g.Votes = Open, s, map[string]int{}

	case "vote":
		if g.Phase != Open {
			fail(errNotRunning)
			return
		}
		if c.Role != lobby.RolePlayer {
			return
		}
		var choice int
		if m.Decode(&choice) != nil || choice < 0 || choice >= len(g.Settings.Options) {
			fail("pick one of the options")
			return
		}
		g.Votes[c.ID] = choice // changing your mind is fine until the reveal
		g.maybeAutoReveal(r)

	case "poll.reveal":
		if !isHost {
			fail(errNotHost)
			return
		}
		if g.Phase != Open {
			fail(errNotRunning)
			return
		}
		g.reveal()

	default:
		return
	}
	g.publish(ctx, r)
}

func (g *game) maybeAutoReveal(r *lobby.Room) {
	if g.Phase != Open || !g.Settings.AutoReveal {
		return
	}
	eligible := connectedPlayers(r)
	if len(eligible) == 0 {
		return
	}
	for _, id := range eligible {
		if _, voted := g.Votes[id]; !voted {
			return
		}
	}
	g.reveal()
}

func (g *game) reveal() {
	g.Phase = Revealed
	g.archive()
}

// archive records the current poll's result in Past.
func (g *game) archive() {
	counts := g.counts()
	total := 0
	for _, n := range counts {
		total += n
	}
	res := Result{Question: g.Settings.Question, Options: slices.Clone(g.Settings.Options), Counts: counts, Total: total}
	g.Past = append([]Result{res}, g.Past...)
	if len(g.Past) > MaxPastPolls {
		g.Past = g.Past[:MaxPastPolls]
	}
}

func (g *game) counts() []int {
	counts := make([]int, len(g.Settings.Options))
	for _, choice := range g.Votes {
		if choice >= 0 && choice < len(counts) {
			counts[choice]++
		}
	}
	return counts
}

// publish sends every client its own view: vote counts may be hidden from
// some viewers, and each sees their own vote.
func (g *game) publish(ctx context.Context, r *lobby.Room) {
	vip := r.VIP()
	voters := slices.Sorted(maps.Keys(g.Votes))
	eligible := len(connectedPlayers(r))
	for _, id := range voters {
		if c, ok := r.Client(id); ok && !c.Connected {
			eligible++ // voted, then their phone slept: still counts
		}
	}
	for _, c := range r.Clients() {
		r.Send(c.ID, ViewType, g.view(c.ID, c.ID == vip, eligible, voters))
	}
	r.Persist(ctx)
}

func (g *game) view(viewer string, isHost bool, eligible int, voters []string) View {
	v := View{
		Phase: g.Phase, ID: g.ID,
		Question: g.Settings.Question, Options: append([]string{}, g.Settings.Options...),
		Live: g.Settings.Live, AutoReveal: g.Settings.AutoReveal,
		Counts: []int{}, Voters: append([]string{}, voters...),
		Eligible: eligible, MyVote: noVote,
		Past: make([]Result, len(g.Past)),
	}
	for i, p := range g.Past {
		v.Past[i] = Result{Question: p.Question, Options: slices.Clone(p.Options), Counts: slices.Clone(p.Counts), Total: p.Total}
	}
	if choice, ok := g.Votes[viewer]; ok && g.Phase != Drafting {
		v.MyVote = choice
	}
	v.CountsVisible = g.Phase == Revealed || (g.Phase == Open && (g.Settings.Live || isHost))
	if v.CountsVisible {
		v.Counts = g.counts()
	}
	return v
}

func connectedPlayers(r *lobby.Room) []string {
	var ids []string
	for _, p := range r.Players() {
		if p.Connected {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// clean trims and validates a poll, returning a message for the host if
// it's not usable.
func clean(s Settings) (Settings, string) {
	s.Question = strings.TrimSpace(s.Question)
	if s.Question == "" || utf8.RuneCountInString(s.Question) > MaxQuestion {
		return s, "write a question (up to 140 characters)"
	}
	var opts []string
	for _, o := range s.Options {
		if o = strings.TrimSpace(o); o != "" {
			if utf8.RuneCountInString(o) > MaxOption {
				return s, "keep each option under 60 characters"
			}
			opts = append(opts, o)
		}
	}
	if len(opts) < MinOptions || len(opts) > MaxOptions {
		return s, "give 2 to 8 options"
	}
	s.Options = opts
	return s, ""
}

func (g *game) Snapshot() ([]byte, error) { return json.Marshal(g) }

func (g *game) Restore(b []byte) error {
	if err := json.Unmarshal(b, g); err != nil {
		return err
	}
	if g.Votes == nil {
		g.Votes = map[string]int{}
	}
	return nil
}
