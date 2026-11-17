# AGENTS.md: building on `github.com/immannino/lobby`

This guide is for AI coding agents (and the humans steering them). It covers
how to use the library correctly, the rules that keep games safe, and how to
work in this repository. If you remember only one thing: **game code runs on
one goroutine per room, so never lock, never block, and never hand out
pointers to live state.**

## What the library is

A Go package for Jackbox-style multiplayer rooms. A host creates a room and
gets a 4-letter code. Players join from phones, and an optional "display"
(a TV or laptop) shows the shared view. The library handles rooms, codes,
identity, reconnects, VIP, kicking, locking, expiry, rate limiting,
persistence and message fan-out. **You write a `Game`**, which reacts to
joins, leaves and messages and sends views back.

## Minimal integration (copy this)

```go
package main

import (
	"context"
	"net/http"

	"github.com/immannino/lobby"
)

type game struct {
	lobby.BaseGame          // no-op defaults; override only what you need
	Count int `json:"count"` // plain fields; no mutex needed
}

func (g *game) OnConnect(ctx context.Context, r *lobby.Room, c lobby.Client) {
	r.Send(c.ID, "count", g.Count) // give every (re)connecting client a snapshot
}

func (g *game) OnMessage(ctx context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	switch m.Type {
	case "add":
		var n int
		if m.Decode(&n) != nil || (n != 1 && n != -1) {
			r.Send(c.ID, "error", map[string]string{"error": "bad move"})
			return
		}
		g.Count += n
		r.Broadcast("count", g.Count)
		r.Persist(ctx) // only does anything if a Store is configured
	}
}

func main() {
	m := lobby.New(lobby.Config{
		NewGame: func(code, kind string) (lobby.Game, error) { return &game{}, nil },
	})
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", m.Handler()))
	http.ListenAndServe(":8080", mux)
}
```

Browser side (the library serves `client.js`):

```js
import { createRoom, joinRoom, connect } from "/api/client.js";
const { code } = await createRoom("/api", { name: "Ada" }); // or joinRoom("/api", code, {name})
const room = connect("/api", code, {
  onMessage: ({ type, data }) => { /* lobby.welcome, lobby.state, then your game's types */ },
  onStatus: (status, detail) => { /* "open" | "reconnecting" | "closed" */ },
});
room.send("add", 1);
```

## The `Game` interface

```go
type Game interface {
	OnJoin(ctx context.Context, r *Room, c Client)       // took a seat (once per client)
	OnLeave(ctx context.Context, r *Room, c Client)      // seat gone: left, kicked, or grace expired
	OnConnect(ctx context.Context, r *Room, c Client)    // a connection attached (every reconnect too)
	OnDisconnect(ctx context.Context, r *Room, c Client) // a connection dropped; the seat is kept
	OnMessage(ctx context.Context, r *Room, c Client, m Message)
}
// Optional: implement lobby.Snapshotter (Snapshot() ([]byte, error); Restore([]byte) error)
// to have game state saved by the Store and restored after a restart.
```

| Hook | Use it for |
|---|---|
| `OnJoin` | Add the player to turn order, queues or scores. Broadcast the new view. |
| `OnConnect` | **Send this client its current view** (`r.Send(c.ID, ...)`). Also called after every reconnect and after a server restart. |
| `OnDisconnect` | Usually nothing. The seat is kept for `ReconnectGrace`. |
| `OnLeave` | Remove the player from game state. Forfeit or advance the turn if it was theirs. |
| `OnMessage` | Validate, apply and broadcast. Reject bad input with an `"error"` message. |

`ctx` is the room's context, which is cancelled when the room closes. Pass
it to anything you call.

### Room methods you can call inside hooks

| Method | Notes |
|---|---|
| `r.Broadcast(typ, data)` / `r.SendRole(role, typ, data)` / `r.Send(id, typ, data)` | Non-blocking. Data is JSON-encoded once, lazily. In-process clients get the Go value itself. |
| `r.Clients()`, `r.Players()`, `r.Client(id)`, `r.VIP()`, `r.Locked()`, `r.Code()`, `r.Kind()` | Read membership. Players are sorted by join order. |
| `r.After(d, func(ctx, r))` | Timer whose callback runs on the room goroutine. Returns `stop()`. |
| `r.SetLocked(ctx, bool)` | Stop new players joining mid-round. |
| `r.Kick(ctx, id, reason)`, `r.Close(ctx)` | |
| `r.Persist(ctx)` | Save room and game state now, if a Store is set. Call it after meaningful changes. |

From **outside** a hook (an HTTP handler, a goroutine), never touch the game
or room directly. Use `room.Do(ctx, func(ctx context.Context, r *lobby.Room) { ... })`.

## Rules (read before writing a game)

1. **No locks, no blocking in hooks.** Hooks are serialized per room. Don't
   add a mutex, and don't sleep, do network I/O, or wait on channels inside a
   hook. Slow work belongs in a goroutine that reports back via
   `room.Do(ctx, ...)`. Use `r.After` for delays.
2. **Never send live state.** In-process clients (`LocalConn`) receive the
   exact value you pass, so send copies: `append([]T{}, s...)`,
   `maps.Clone(m)`, arrays by value. Build a `View` struct per send.
3. **No nil slices or maps in views.** They encode as JSON `null` and break
   browser code. Initialize them to `[]T{}` and `map[K]V{}`. Add a test like
   `TestViewJSONHasNoNulls` (see `examples/games/duel/duel_test.go`).
4. **Secrets need per-client views.** If clients must see different things
   (a hidden word, a hand of cards), loop `for _, c := range r.Clients() {
   r.Send(c.ID, typ, viewFor(c.ID)) }` instead of using `Broadcast`. See
   `examples/games/hangman`.
5. **Server is authoritative.** Validate every message: who sent it (`c.ID`,
   `c.Role`), whose turn it is, and whether it's well-formed
   (`m.Decode(&v) != nil`). Displays (`c.Role == lobby.RoleDisplay`) usually
   shouldn't be able to act.
6. **Timers must survive being superseded.** A timer can fire just before
   you stop it. Keep a generation counter and ignore stale callbacks (see
   `setTimer` in hangman). After a restart no timers exist, so recreate them
   in `OnConnect` if the phase needs one.
7. **Don't use `lobby.` message types.** That prefix is reserved for the
   library.
8. **Context first.** Any function you write that changes state takes
   `ctx context.Context` as its first parameter.

## Recipes

- **Several games on one server:** switch on `kind` in `NewGame`. Clients
  pass `{"game": "connect4"}` when creating a room, and `lobby.welcome.game`
  tells the browser which UI to load. See `examples/site/main.go`.
- **Bots and computer players:** `examples/games/botkit` joins a room over a
  `LocalConn` and runs a `Brain func(me string, ev lobby.Event) (typ string,
  data any, ok bool)`. Start it in a goroutine from your game.
- **Two-player turn-based games:** implement `duel.Board` (`Reset`, `Play`,
  `Outcome`, `View`) and call `duel.New(board, opts)`. Seating, turns,
  forfeits, scores, "winner stays on" and bots come for free. See
  `tictactoe` (about 80 lines) and `connect4`.
- **Tests without a server:** use `github.com/immannino/lobby/lobbytest`:
  ```go
  m := lobbytest.Manager(t, func() lobby.Game { return &game{} })
  ada, code := lobbytest.Create(t, m, "Ada")     // first player, VIP
  bob := lobbytest.Join(t, m, code, "Bob")       // name "" joins a display
  lobbytest.Send(t, bob, "move", 4)
  v := lobbytest.Wait(t, ada, func(v View) bool { return v.Turn == ada.Client().ID })
  ```
  `Wait[T]` matches on your own Go view types, because in-process clients
  receive the values you sent. Always run with `-race`.
- **Persistence:** `sqlitestore.Open(ctx, "lobby.db")` as `Config.Store`,
  plus `Snapshotter` on the game, plus `m.Restore(ctx)` at startup and
  `m.Shutdown(ctx)` on exit. For Postgres or MySQL, see `docs/storage.md`.
- **Behind a proxy:** set `Config.ClientIP` (for rate limiting),
  `OriginPatterns` (for websocket origin checks) and `SecureCookies: true`.

## Common mistakes

| Symptom | Cause |
|---|---|
| Browser shows a blank view after a refresh | No `r.Send` in `OnConnect`. |
| `TypeError: Cannot read properties of null` in the browser | A nil slice or map in a view. |
| Data race in `go test -race` | State touched outside a hook. Use `room.Do`. |
| Bot sees state change under it | Sent a pointer or slice backed by live state. |
| Old timer fires after a phase change | No generation check in the timer callback. |
| Room never closes in tests | Clients disconnected but never left, so it waits for `IdleTimeout`. Call `Leave` or `Close`. |

## Working in this repo

- Go 1.26. `make test` (`go test -race ./...`), `make bench`, `make run`
  (demo on :1234), `make dev` (live reload with air).
- The library is at the root (package `lobby`) and must stay game-agnostic.
  Example games live in `examples/games/<name>`, each with a `<name>.js`
  browser module embedded as `ClientJS`.
- The browser demo is plain ES modules with no build step, styled with
  yearn.css (classless). Style with `data-*` attributes in
  `examples/site/web/demo.css`, not classes.
- `lobbytest/` is public test tooling for game authors; `examples/quickstart`
  is the code shown on the landing page, so keep it short and tested.
- Before finishing: `gofmt -l .`, `go vet ./...`, `go test -race ./...`.
- `_legacy/` is the old prototype. Don't edit it.
