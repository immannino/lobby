# lobby

Jackbox-style rooms for Go. Someone opens a room and gets a short code;
everyone else joins with that code and plays in real time. Players connect
over websockets, or over Go channels if they're in the same process.

```sh
go get github.com/immannino/lobby
```

- **Rooms and codes.** Short codes like `BCDF`, made only of consonants so
  they can't spell words.
- **Players and a shared display.** A "big screen" role alongside phones,
  with a VIP who can kick and lock.
- **Reconnects that keep your seat.** Seats are tied to tokens, not names, and
  survive refreshes, sleeping phones and server restarts.
- **One goroutine per room.** Game code needs no locks. Timers come built in
  (`r.After`).
- **Optional persistence.** In-memory by default; SQLite included (pure Go);
  Postgres or MySQL via a small interface.
- **In-process clients.** `LocalConn` gives you bots, tests and CLIs with no
  network and no JSON.

## Repository layout

| Path | What |
|---|---|
| `/` (package `lobby`) | The library. Knows nothing about any game. |
| `sqlitestore/` | Optional SQLite `Store` (modernc.org/sqlite, no cgo). |
| `client.js` | Browser client, served by `Manager.Handler()` at `/client.js`. |
| `lobbytest/` | Test helpers: play your game with in-process clients and assert on your own Go types. |
| `examples/games/` | Example games, each a package with its own browser module: `chat`, `tictactoe`, `connect4`, `hangman`, plus the shared `duel` (two-player, winner stays on) and `botkit` (computer players over `LocalConn`). |
| `examples/quickstart/` | A complete buzzer game in ~35 lines of Go. Start here. |
| `examples/site/` | The website: landing page, docs (`/docs`) and a live demo (`/play`) of all the example games. |
| `docs/storage.md` | In-memory vs. SQLite vs. Postgres/MySQL, with schemas. |
| `docs/roadmap.md` | Production-readiness checklist, benchmarks, site, quickstarts. |
| `AGENTS.md` | A guide for AI coding agents building on the library. |

## Run it

```sh
go run ./examples/quickstart   # the buzzer → http://localhost:8080
make run     # the site and demo → http://localhost:1234
make test    # go test -race ./...
make bench   # go test -run '^$' -bench . -benchmem .
```

Pick a game and click **Host on this screen**, then join from other tabs or a
phone using the code. Rooms persist to `lobby.db`, so restarting the server
keeps rooms, seats and game state. Use `-db=` to keep everything in memory
only.

The pages are styled with [yearn.css](https://yearn.ope.cool/), which is
classless. Append `?theme=dusk` (or mint, ember, tide, mono, candy, arc,
sharebuilder) to any page.

## A game in 20 lines

```go
type echo struct{ lobby.BaseGame } // BaseGame: no-op defaults for every hook

func (echo) OnMessage(ctx context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	var text string
	if m.Decode(&text) == nil {
		r.Broadcast("echo", c.Name+": "+text)
	}
}

m := lobby.New(lobby.Config{
	NewGame: func(code, kind string) (lobby.Game, error) { return echo{}, nil },
	// Store: sqlitestore.Open(ctx, "lobby.db") for restart survival; nil = in-memory
})
mux.Handle("/api/", http.StripPrefix("/api", m.Handler()))
```

From a browser:

```js
import { joinRoom, connect } from "/api/client.js";
const { code } = await joinRoom("/api", "BCDF", { name: "Ada" }); // sets the seat cookie
const room = connect("/api", code, { onMessage: ({ type, data }) => console.log(type, data) });
room.send("say", "hi");
```

From Go, in the same process (no JSON; `ev.Data` is the value the game sent):

```go
res, _ := m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "Bot"})
conn, _ := m.Connect(ctx, res.Code, res.Token)
defer conn.Close()
conn.Send(ctx, "say", "hi")
for ev := range conn.Events() {
	if ev.Type == "echo" {
		fmt.Println(ev.Data.(string))
	}
}
```

## API conventions

- **Context first.** Every call that changes state takes `context.Context` as
  its first argument: `Create`, `Join`, `Connect`, `Room.Do`, `SetLocked`,
  `Kick`, `Persist`, `Close`, `LocalConn.Send`/`Leave`, every `Game` hook, and
  every `Store` method. Inside a hook, the context is the room's own, which is
  cancelled when the room closes, so pass it to anything you start.
- **Sends don't block.** `Broadcast`, `SendRole` and `Send` queue messages and
  return immediately, so they take no context. A client that can't keep up is
  disconnected, and it reconnects to a fresh snapshot.
- **One deliberate exception.** `LocalConn.Close()` takes no context, so it
  can be used as an `io.Closer` with `defer`.

## How it works

- **One goroutine per room.** Hooks run one at a time on the room's
  goroutine. Use `r.After(d, fn)` for timers. Use `room.Do(ctx, fn)` to reach
  into a room from outside.
- **Game kinds.** `NewGame(code, kind)` receives the `game` field from the
  create request, so one server can host several games. The kind is
  persisted and sent to clients in `lobby.welcome`.
- **Identity is a token, not a name.** Joining returns a random token, also
  set as an HttpOnly cookie `lobby_<CODE>`. Only its SHA-256 is stored.
  Reconnecting with the token reclaims the seat. A second connection with the
  same token replaces the first.
- **Roles.** `RolePlayer` counts toward `MaxPlayers`, and the earliest-joined
  player is VIP. `RoleDisplay` is the big screen.
- **Locking.** The VIP (`lobby.lock`) or the game (`r.SetLocked`) can stop
  new players joining mid-round. Members can still reconnect, and displays
  can still join.
- **Rate limiting.** Create and join are limited per IP (`RateLimit`, default
  30/min), because codes are guessable by design. Set `ClientIP` when running
  behind a proxy.
- **Expiry.** A disconnected client keeps its seat for `ReconnectGrace` (2m).
  A room with nobody connected closes after `IdleTimeout` (10m). A room whose
  last member leaves closes immediately.
- **Persistence.** See [docs/storage.md](docs/storage.md). Games that
  implement `Snapshotter` get their state saved too. `Manager.Shutdown`
  persists every room and tells clients "going away" so they reconnect to the
  next process.

## Wire protocol

Every websocket frame is `{"type": "...", "data": ...}`. Types starting with
`lobby.` are reserved:

| Direction | Type | Data |
|---|---|---|
| server → client | `lobby.welcome` | `{code, game, you}`, once per connection |
| server → client | `lobby.state` | `{code, vip, locked, clients[]}`, on any membership change |
| server → client | `lobby.error` | `{error}` |
| client → server | `lobby.leave` | — |
| client → server | `lobby.kick` | `{id}` (VIP only) |
| client → server | `lobby.lock` | `{locked}` (VIP only) |

These close codes are terminal: `4401` unauthorized, `4403` kicked, `4404`
room not found, `4409` replaced, `4410` room closed. `client.js` reconnects
with backoff on anything else.

HTTP routes, relative to wherever you mount `Handler()`:
- `POST /rooms` — `{name, role?, game?}`
- `GET /rooms/{code}`
- `POST /rooms/{code}/join`
- `GET /rooms/{code}/ws`
- `GET /client.js`

## Performance

From `make bench` on an Apple M-series laptop, over loopback. The websocket
numbers include the clients' own read cost in the same process.

| Benchmark | Result |
|---|---|
| One broadcast → 8 in-process clients | ~1 µs |
| One broadcast → 64 in-process clients | ~4 µs |
| One broadcast → 8 websocket clients | ~52 µs |
| One broadcast → 64 websocket clients | ~244 µs |
| Websocket round trip (send → game → reply) | ~25 µs |
| Room lifecycle (create, join ×2, connect, leave, close) | ~12 µs |

A broadcast is JSON-encoded once, no matter how many clients receive it. See
[docs/roadmap.md](docs/roadmap.md#benchmarking) for what to measure next.

## Limits

Live connections are held in memory, so one process owns all rooms. A store
gives you restart survival, not horizontal scaling (see
[docs/storage.md](docs/storage.md#what-a-store-does-not-give-you)).

## License

MIT. See [LICENSE](LICENSE).
