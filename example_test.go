package lobby_test

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/immannino/lobby"
)

type echo struct{ lobby.BaseGame }

func (echo) OnMessage(_ context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	var text string
	if m.Decode(&text) == nil {
		r.Broadcast("echo", c.Name+": "+text)
	}
}

// An in-process client talks to a room over Go channels: no HTTP, websocket
// or JSON, and no Store needed.
func ExampleManager_Connect() {
	ctx := context.Background()
	m := lobby.New(lobby.Config{
		NewGame: func(code, kind string) (lobby.Game, error) { return echo{}, nil },
		Logger:  slog.New(slog.DiscardHandler),
	})

	res, _ := m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "Bot"})
	conn, _ := m.Connect(ctx, res.Code, res.Token)
	_ = conn.Send(ctx, "say", "hi")

	for ev := range conn.Events() {
		switch data := ev.Data.(type) {
		case lobby.WelcomeData:
			fmt.Println("welcome,", data.You.Name)
		case string:
			fmt.Println(ev.Type, data)
			conn.Close()
		}
	}
	// Output:
	// welcome, Bot
	// echo Bot: hi
}
