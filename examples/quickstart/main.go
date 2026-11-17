// Command quickstart is the smallest complete lobby game: a buzzer. Open
// http://localhost:8080 on a laptop and a few phones, join with the code,
// and race to buzz first. The VIP resets.
//
//	go run github.com/immannino/lobby/examples/quickstart@latest
package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"

	"github.com/immannino/lobby"
)

// buzzer is the whole game. Its methods run one at a time on the room's
// goroutine, so plain fields are safe without locks.
type buzzer struct {
	lobby.BaseGame
	winner string // name of the first player to buzz, "" while open
}

func (b *buzzer) OnConnect(ctx context.Context, r *lobby.Room, c lobby.Client) {
	r.Send(c.ID, "buzz", b.winner) // catch up anyone who (re)connects
}

func (b *buzzer) OnMessage(ctx context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	switch {
	case m.Type == "buzz" && b.winner == "" && c.Role == lobby.RolePlayer:
		b.winner = c.Name
	case m.Type == "reset" && c.ID == r.VIP():
		b.winner = ""
	default:
		return
	}
	r.Broadcast("buzz", b.winner)
}

//go:embed index.html
var page []byte

func main() {
	m := lobby.New(lobby.Config{
		NewGame: func(code, kind string) (lobby.Game, error) { return &buzzer{}, nil },
	})
	http.Handle("/api/", http.StripPrefix("/api", m.Handler()))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write(page) })
	log.Println("open http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
