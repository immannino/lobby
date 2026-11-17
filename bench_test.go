package lobby_test

// Benchmarks for the costs that matter in a party game server:
//
//   - fan-out: one message in, N clients out (the hot path every move takes)
//   - round trip: client → room goroutine → client over a real websocket
//   - room churn: create + join + connect + leave
//
// Run: go test -run '^$' -bench . -benchmem
// Compare across changes with benchstat (golang.org/x/perf/cmd/benchstat).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/immannino/lobby"
)

// relay broadcasts every "say" to the room and answers "ping" to the sender.
type relay struct{ lobby.BaseGame }

type said struct {
	From string `json:"from"`
	Text string `json:"text"`
	Seq  int    `json:"seq"`
}

func (relay) OnMessage(_ context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	switch m.Type {
	case "say":
		var d said
		_ = m.Decode(&d)
		d.From = c.Name
		r.Broadcast("said", d)
	case "ping":
		r.Send(c.ID, "pong", m.Data)
	}
}

func benchManager(b *testing.B, maxPlayers int) *lobby.Manager {
	b.Helper()
	m := lobby.New(lobby.Config{
		NewGame:    func(string, string) (lobby.Game, error) { return relay{}, nil },
		Logger:     slog.New(slog.DiscardHandler),
		RateLimit:  -1,
		MaxPlayers: maxPlayers,
	})
	b.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m
}

// drainUntil reads events until one of type typ arrives.
func drainUntil(b *testing.B, lc *lobby.LocalConn, typ string) {
	for ev := range lc.Events() {
		if ev.Type == typ {
			return
		}
	}
	b.Fatal("connection closed")
}

// BenchmarkFanoutLocal measures one broadcast reaching N in-process clients:
// the room's own overhead, with no network or JSON.
func BenchmarkFanoutLocal(b *testing.B) {
	for _, n := range []int{2, 8, 64} {
		b.Run(fmt.Sprintf("clients=%d", n), func(b *testing.B) {
			ctx := context.Background()
			m := benchManager(b, n)
			res, err := m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "p0"})
			if err != nil {
				b.Fatal(err)
			}
			conns := make([]*lobby.LocalConn, n)
			for i := range n {
				r := res
				if i > 0 {
					if r, err = m.Join(ctx, res.Code, lobby.JoinRequest{Name: fmt.Sprint("p", i)}); err != nil {
						b.Fatal(err)
					}
				}
				if conns[i], err = m.Connect(ctx, res.Code, r.Token); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				if err := conns[0].Send(ctx, "say", said{Text: "hi", Seq: i}); err != nil {
					b.Fatal(err)
				}
				for _, c := range conns {
					drainUntil(b, c, "said")
				}
			}
			b.ReportMetric(float64(n), "deliveries/op")
		})
	}
}

// BenchmarkFanoutWebsocket is the same broadcast to N real websocket clients
// over loopback: what one move costs end to end, including JSON (encoded
// once per broadcast, not per client).
func BenchmarkFanoutWebsocket(b *testing.B) {
	for _, n := range []int{2, 8, 64} {
		b.Run(fmt.Sprintf("clients=%d", n), func(b *testing.B) {
			ctx := context.Background()
			m := benchManager(b, n)
			srv := httptest.NewServer(m.Handler())
			b.Cleanup(srv.Close)
			res, err := m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "p0"})
			if err != nil {
				b.Fatal(err)
			}
			conns := make([]*websocket.Conn, n)
			for i := range n {
				r := res
				if i > 0 {
					if r, err = m.Join(ctx, res.Code, lobby.JoinRequest{Name: fmt.Sprint("p", i)}); err != nil {
						b.Fatal(err)
					}
				}
				url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/rooms/" + res.Code + "/ws?token=" + r.Token
				if conns[i], _, err = websocket.Dial(ctx, url, nil); err != nil {
					b.Fatal(err)
				}
				conns[i].SetReadLimit(1 << 20)
			}
			msg, _ := json.Marshal(lobby.Message{Type: "say", Data: json.RawMessage(`{"text":"hi"}`)})
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := conns[0].Write(ctx, websocket.MessageText, msg); err != nil {
					b.Fatal(err)
				}
				for _, c := range conns {
					wsReadUntil(b, c, `"said"`)
				}
			}
			b.ReportMetric(float64(n), "deliveries/op")
		})
	}
}

func wsReadUntil(b *testing.B, c *websocket.Conn, marker string) {
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if strings.Contains(string(data), marker) {
			return
		}
	}
}

// BenchmarkRoundTripWebsocket is the latency floor for one client: send a
// message, have the game reply to the sender, read the reply.
func BenchmarkRoundTripWebsocket(b *testing.B) {
	ctx := context.Background()
	m := benchManager(b, 8)
	srv := httptest.NewServer(m.Handler())
	b.Cleanup(srv.Close)
	res, err := m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "p0"})
	if err != nil {
		b.Fatal(err)
	}
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/rooms/" + res.Code + "/ws?token=" + res.Token
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		b.Fatal(err)
	}
	msg := []byte(`{"type":"ping","data":1}`)
	b.ReportAllocs()
	for b.Loop() {
		if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
			b.Fatal(err)
		}
		wsReadUntil(b, c, `"pong"`)
	}
}

// BenchmarkRoomLifecycle measures room churn: create, a second player joins,
// both connect in-process, both leave (which closes the room).
func BenchmarkRoomLifecycle(b *testing.B) {
	ctx := context.Background()
	m := benchManager(b, 8)
	b.ReportAllocs()
	for b.Loop() {
		host, err := m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "Ada"})
		if err != nil {
			b.Fatal(err)
		}
		guest, err := m.Join(ctx, host.Code, lobby.JoinRequest{Name: "Bob"})
		if err != nil {
			b.Fatal(err)
		}
		for _, tok := range []string{host.Token, guest.Token} {
			lc, err := m.Connect(ctx, host.Code, tok)
			if err != nil {
				b.Fatal(err)
			}
			if err := lc.Leave(ctx); err != nil {
				b.Fatal(err)
			}
		}
		if room, ok := m.Room(host.Code); ok {
			<-room.Done()
		}
	}
}
