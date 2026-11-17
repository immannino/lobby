package poll_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/examples/games/poll"
	"github.com/immannino/lobby/lobbytest"
)

func newRoom(t *testing.T) (*lobby.Manager, *lobby.LocalConn, string) {
	m := lobbytest.Manager(t, poll.New)
	host, code := lobbytest.Create(t, m, "Host") // first player: VIP
	return m, host, code
}

var question = poll.Settings{Question: " Pizza or tacos? ", Options: []string{"Pizza", " Tacos ", ""}}

func TestHiddenUntilReveal(t *testing.T) {
	m, host, code := newRoom(t)
	ada := lobbytest.Join(t, m, code, "Ada")
	bob := lobbytest.Join(t, m, code, "Bob")
	tv := lobbytest.Join(t, m, code, "") // display

	lobbytest.Send(t, ada, "poll.open", question) // not the host: rejected
	lobbytest.Send(t, host, "poll.open", question)

	v := lobbytest.Wait(t, ada, func(v poll.View) bool { return v.Phase == poll.Open })
	if v.Question != "Pizza or tacos?" || len(v.Options) != 2 || v.Options[1] != "Tacos" {
		t.Fatalf("poll not cleaned up: %q %q", v.Question, v.Options)
	}
	if v.Eligible != 3 || v.MyVote != -1 {
		t.Fatalf("eligible=%d myVote=%d", v.Eligible, v.MyVote)
	}

	lobbytest.Send(t, ada, "vote", 1)
	lobbytest.Send(t, bob, "vote", 0)
	lobbytest.Send(t, bob, "vote", 1) // changed their mind

	// Players see who voted, but not the counts.
	v = lobbytest.Wait(t, bob, func(v poll.View) bool { return v.MyVote == 1 })
	if len(v.Voters) != 2 || v.CountsVisible || len(v.Counts) != 0 || v.MyVote != 1 {
		t.Fatalf("player view leaks counts: %+v", v)
	}
	tvView := lobbytest.Wait(t, tv, func(v poll.View) bool { return len(v.Voters) == 2 })
	if tvView.CountsVisible || len(tvView.Counts) != 0 {
		t.Fatalf("display view leaks counts: %+v", tvView)
	}
	// The host always sees the distribution.
	hv := lobbytest.Wait(t, host, func(v poll.View) bool { return len(v.Counts) == 2 && v.Counts[1] == 2 })
	if !hv.CountsVisible || hv.Counts[0] != 0 {
		t.Fatalf("host counts = %v (visible %v)", hv.Counts, hv.CountsVisible)
	}

	lobbytest.Send(t, ada, "poll.reveal", nil) // not the host
	lobbytest.Send(t, host, "poll.reveal", nil)
	v = lobbytest.Wait(t, tv, func(v poll.View) bool { return v.Phase == poll.Revealed })
	if !v.CountsVisible || v.Counts[1] != 2 || len(v.Past) != 1 || v.Past[0].Total != 2 {
		t.Fatalf("revealed: %+v", v)
	}
	lobbytest.Send(t, ada, "vote", 0) // too late
}

func TestLivePoll(t *testing.T) {
	m, host, code := newRoom(t)
	ada := lobbytest.Join(t, m, code, "Ada")
	s := question
	s.Live = true
	lobbytest.Send(t, host, "poll.open", s)
	lobbytest.Wait(t, ada, func(v poll.View) bool { return v.Phase == poll.Open })
	lobbytest.Send(t, ada, "vote", 0)
	v := lobbytest.Wait(t, ada, func(v poll.View) bool { return len(v.Voters) == 1 })
	if !v.CountsVisible || v.Counts[0] != 1 {
		t.Fatalf("live poll should show counts to players: %+v", v)
	}
}

func TestAutoRevealWhenEveryoneVoted(t *testing.T) {
	m, host, code := newRoom(t)
	ada := lobbytest.Join(t, m, code, "Ada")
	s := question
	s.AutoReveal = true
	lobbytest.Send(t, host, "poll.open", s)
	lobbytest.Wait(t, ada, func(v poll.View) bool { return v.Phase == poll.Open })

	lobbytest.Send(t, ada, "vote", 0)
	v := lobbytest.Wait(t, ada, func(v poll.View) bool { return len(v.Voters) == 1 })
	if v.Phase != poll.Open {
		t.Fatal("revealed before the host voted")
	}
	lobbytest.Send(t, host, "vote", 1)
	v = lobbytest.Wait(t, ada, func(v poll.View) bool { return v.Phase == poll.Revealed })
	if v.Counts[0] != 1 || v.Counts[1] != 1 {
		t.Fatalf("counts = %v", v.Counts)
	}
}

func TestPastPollsAndNextPoll(t *testing.T) {
	m, host, code := newRoom(t)
	ada := lobbytest.Join(t, m, code, "Ada")
	for i, q := range []string{"First?", "Second?"} {
		lobbytest.Send(t, host, "poll.open", poll.Settings{Question: q, Options: []string{"Yes", "No"}})
		lobbytest.Wait(t, ada, func(v poll.View) bool { return v.ID == i+1 && v.Phase == poll.Open })
		lobbytest.Send(t, ada, "vote", i)
		lobbytest.Wait(t, ada, func(v poll.View) bool { return v.ID == i+1 && len(v.Voters) == 1 })
		lobbytest.Send(t, host, "poll.reveal", nil)
		lobbytest.Wait(t, ada, func(v poll.View) bool { return v.ID == i+1 && v.Phase == poll.Revealed })
	}
	lobbytest.Send(t, host, "poll.open", poll.Settings{Question: "Third?", Options: []string{"A", "B"}})
	v := lobbytest.Wait(t, ada, func(v poll.View) bool { return v.ID == 3 })
	if v.MyVote != -1 || len(v.Voters) != 0 {
		t.Fatalf("new poll kept old votes: %+v", v)
	}
	if len(v.Past) != 2 || v.Past[0].Question != "Second?" || v.Past[1].Counts[0] != 1 {
		t.Fatalf("past = %+v", v.Past)
	}
}

func TestRejectsBadPolls(t *testing.T) {
	_, host, _ := newRoom(t)
	for _, s := range []poll.Settings{
		{Question: "", Options: []string{"a", "b"}},
		{Question: "One option?", Options: []string{"only"}},
		{Question: strings.Repeat("x", 141), Options: []string{"a", "b"}},
		{Question: "Too many?", Options: strings.Split("a b c d e f g h i", " ")},
	} {
		lobbytest.Send(t, host, "poll.open", s)
	}
	lobbytest.Send(t, host, "poll.open", question)
	v := lobbytest.Wait(t, host, func(v poll.View) bool { return v.Phase == poll.Open })
	if v.ID != 1 {
		t.Fatalf("a bad poll was accepted: id=%d", v.ID)
	}
}

func TestViewJSONHasNoNulls(t *testing.T) {
	_, host, _ := newRoom(t)
	v := lobbytest.Wait(t, host, lobbytest.Any[poll.View])
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "null") {
		t.Errorf("view JSON contains null: %s", b)
	}
}

func TestDisconnectedVotersStillCount(t *testing.T) {
	m, host, code := newRoom(t)
	ada := lobbytest.Join(t, m, code, "Ada")
	lobbytest.Send(t, host, "poll.open", question)
	lobbytest.Wait(t, ada, func(v poll.View) bool { return v.Phase == poll.Open })
	lobbytest.Send(t, host, "vote", 1)
	lobbytest.Send(t, ada, "vote", 0)
	lobbytest.Wait(t, host, func(v poll.View) bool { return len(v.Voters) == 2 })

	_ = ada.Close() // phone sleeps; seat (and vote) kept
	// Every view from now on must count Ada as eligible: never "2 of 1 voted".
	deadline := time.After(300 * time.Millisecond)
	seen := 0
	for {
		select {
		case ev := <-host.Events():
			if v, ok := ev.Data.(poll.View); ok {
				seen++
				if len(v.Voters) > v.Eligible {
					t.Fatalf("%d voted of %d eligible", len(v.Voters), v.Eligible)
				}
			}
		case <-deadline:
			if seen == 0 {
				t.Fatal("no view after the disconnect")
			}
			return
		}
	}
}
