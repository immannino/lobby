// Package botkit runs computer players inside lobby rooms over in-process
// connections (lobby.LocalConn): no HTTP, no websocket, no JSON. A bot is
// just a Brain that looks at each event and maybe answers.
package botkit

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/immannino/lobby"
)

// Brain decides how a bot reacts to an event: return the message to send,
// or ok=false to do nothing. me is the bot's client ID. ev.Data is the Go
// value the game sent, so brains can type-assert it directly.
type Brain func(me string, ev lobby.Event) (typ string, data any, ok bool)

// Options configures a bot.
type Options struct {
	// Names are tried in order until one isn't taken.
	// Default: "Computer", "Computer 2", ... "Computer 5".
	Names []string
	// Hello is a message type sent once after connecting, e.g. "bot.hello"
	// so the game can label the bot. Empty sends nothing.
	Hello string
	// Think is a pause before each reply, so the bot feels less instant.
	Think time.Duration
	// AloneTimeout makes the bot leave once no other client has been
	// connected for this long, so it never keeps an abandoned room alive.
	// Default 30s.
	AloneTimeout time.Duration
	Logger       *slog.Logger
}

// Run joins the room as a player, connects in-process and plays until the
// connection ends (kicked, room closed, or alone too long) or ctx is
// cancelled. It blocks; start it with go.
func Run(ctx context.Context, m *lobby.Manager, code string, brain Brain, opts Options) error {
	if len(opts.Names) == 0 {
		opts.Names = []string{"Computer"}
		for i := 2; i <= 5; i++ {
			opts.Names = append(opts.Names, "Computer "+strconv.Itoa(i))
		}
	}
	if opts.AloneTimeout <= 0 {
		opts.AloneTimeout = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	var (
		res lobby.JoinResult
		err error
	)
	for _, name := range opts.Names {
		if res, err = m.Join(ctx, code, lobby.JoinRequest{Name: name}); !errors.Is(err, lobby.ErrNameTaken) {
			break
		}
	}
	if err != nil {
		return err
	}
	conn, err := m.Connect(ctx, code, res.Token)
	if err != nil {
		return err
	}
	defer conn.Close()
	me := conn.Client().ID
	if opts.Hello != "" {
		if err := conn.Send(ctx, opts.Hello, nil); err != nil {
			return err
		}
	}

	var alone <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-alone:
			alone = nil
			if err := conn.Leave(ctx); err != nil {
				return err
			}

		case ev, ok := <-conn.Events():
			if !ok {
				status, reason := conn.CloseStatus()
				opts.Logger.Info("bot finished", "code", code, "status", status, "reason", reason)
				return nil
			}
			if s, isState := ev.Data.(lobby.StateData); isState {
				switch {
				case othersConnected(s, me):
					alone = nil
				case alone == nil:
					alone = time.After(opts.AloneTimeout) // maybe they're just refreshing
				}
			}
			typ, data, reply := brain(me, ev)
			if !reply {
				continue
			}
			if opts.Think > 0 {
				select {
				case <-time.After(opts.Think):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if err := conn.Send(ctx, typ, data); err != nil {
				return err
			}
		}
	}
}

func othersConnected(s lobby.StateData, me string) bool {
	for _, c := range s.Clients {
		if c.ID != me && c.Connected {
			return true
		}
	}
	return false
}
