package main

import (
	"testing"

	"github.com/immannino/lobby"
	gt "github.com/immannino/lobby/lobbytest"
)

func TestFirstBuzzWinsAndVIPResets(t *testing.T) {
	m := gt.Manager(t, func() lobby.Game { return &buzzer{} })
	ada, code := gt.Create(t, m, "Ada") // first player: VIP
	bob := gt.Join(t, m, code, "Bob")

	gt.Send(t, bob, "buzz", nil)
	gt.Send(t, ada, "buzz", nil) // too late
	if got := gt.Wait(t, ada, func(s string) bool { return s != "" }); got != "Bob" {
		t.Fatalf("winner = %q, want Bob", got)
	}
	gt.Send(t, bob, "reset", nil) // not the VIP: ignored
	gt.Send(t, ada, "reset", nil)
	gt.Wait(t, bob, func(s string) bool { return s == "" })
}
