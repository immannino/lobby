# Roadmap: from working to finished

Four tracks, in the order I'd do them. Production hardening comes first,
because the site and quickstarts will send people to the library and it
should hold up when they arrive.

## 1. Productionize the library

What's already solid: per-room serialization, token identity, reconnect
grace, slow-client eviction, panic recovery in hooks, graceful shutdown with
persistence, create/join rate limiting, and race-tested tests.

Gaps, roughly by priority:

- [x] **Per-connection message rate limit.** Token bucket per websocket
  (`MessageRate` 20/s, `MessageBurst` 40); excess closes with 1008.
- [x] **Configurable read limit and keepalive** (`MaxMessageBytes`,
  `PingInterval`).
- [x] **Stats.** `Manager.Stats()`: rooms, websockets, messages in/out, slow
  drops, rate limits, panics. Served at `/healthz` on the site.
  - [ ] Hook latency histogram and an OpenTelemetry adapter subpackage.
- [x] **Fuzz tests** for HTTP create/join bodies and `Message.Decode`.
- [x] **Proxy-aware client IPs** via `lobby.HeaderClientIP`.
- [x] **Rooms close as soon as the last member leaves.**
- [x] **Client wakes up**: reconnects immediately on page visibility or
  network return instead of waiting out the backoff.
- [x] **License:** MIT.
- [ ] **API review before v0.1.0.** Decide what's frozen: `Game`, `Room`
  methods, `Store`, wire protocol, close codes.
  - [x] `protocol` version in `lobby.welcome`; `client.js` warns on mismatch.
- [x] **CI** (`.github/workflows/ci.yml`): gofmt, vet, staticcheck, race
  tests, short fuzzing, govulncheck, and benchstat against the base branch on
  PRs.
- [ ] **Release hygiene.** Semver tags, CHANGELOG, pkg.go.dev examples for
  every major type (`Example_*` functions double as tests).
- [ ] **Horizontal scaling story** (later). Route by room code (consistent
  hashing at the proxy, or a `/rooms/{code}` redirect to the owning node).
  The one-goroutine-per-room design makes this a routing problem, not a
  consensus problem. Document it before building it.
- [ ] **Features people will ask for:** an audience role (watch and vote in
  locked rooms), a QR code for the join URL, host migration when the display
  leaves, and kick with ban for the room's lifetime.

## 2. Site: marketing + get started + live demo

**Done:** `examples/site` serves `/` (landing), `/docs`, `/play` and
`/healthz`, and deploys to Railway from the root `Dockerfile`. The original
recommendation:

**one Go binary that is the site and the demo**, deployed
to a single Fly.io (or Railway) machine with a volume for `lobby.db`.
Dogfooding is the pitch: "this page is running on the library."

- `/`: a hero with a live room code you can join from your phone right now
  (the page itself is a display), a three-step "how it works", and a code
  sample.
- `/docs`: Getting started, Concepts (rooms, roles, reconnects, the
  one-goroutine model), Recipes, Wire protocol, Storage, and the AGENTS.md
  content rendered as "Using with AI agents".
- `/play`: the current demo (tic-tac-toe, connect 4, hangman, chat).
- Styled with yearn.css, so the site doubles as a showcase for it.
- Domain idea: `lobby.ope.cool`, alongside yearn.

Open question for you: keep the site in this repo (`examples/site`) or put
it in a separate repo? Same repo is simpler, and the docs can't drift from
the code.

## 3. Quickstarts (the Socket.IO / PartyKit comparison)

Developers will compare lobby with **Socket.IO** (rooms plus events; Node;
you build identity, reconnect-to-seat and game state yourself) and
**PartyKit / Cloudflare Durable Objects** (one stateful actor per room;
`onConnect` / `onMessage`; hosted). PartyKit's model is the closest
equivalent. The pitch: *PartyKit's room-actor model, plus join codes,
seats, VIP and reconnects, as one Go package you self-host.*

Quickstarts to ship, from simplest:

- [x] **`examples/quickstart`**: a single `main.go` (~40 lines) and a single
  `index.html` (~40 lines). It should go from `go run` to a phone-joinable
  room in under a minute. This is the first code on the site.
- [x] **`lobbytest` package** for game authors' tests.
- [ ] **Template repo / `go run .../cmd/lobby-new@latest mygame`**: scaffolds
  a game package, its JS module, a test using `lobbytest`, and a Dockerfile.
- [ ] **Framework clients:** publish `client.js` to npm as `@lobby/client`
  (same file, with types), plus tiny React (`useLobby`) and Svelte
  examples, so the frontend doesn't have to be vanilla.
- [ ] **"Coming from…" guides:** the same chat game written in Socket.IO,
  PartyKit and lobby, side by side. This is also the DX benchmark below.

## 4. Benchmarking: is this any good?

"Good" means three different things. Measure each one.

### a. Performance (is it fast and cheap?)

Already in `bench_test.go` (`make bench`). Apple M-series, loopback:

| | Result |
|---|---|
| Broadcast → 8 / 64 in-process clients | ~1 µs / ~4 µs |
| Broadcast → 8 / 64 websocket clients | ~52 µs / ~244 µs |
| Websocket round trip | ~25 µs |
| Room create → join → leave → close | ~12 µs |

Next steps:

- [ ] **Load test** (`cmd/loadgen`). N rooms × M websocket clients playing
  scripted games across real sockets. Report p50/p99/p999 broadcast latency,
  messages per second, **memory per idle connection**, goroutines per room,
  and CPU at 1k, 10k and 50k connections. Party games are mostly idle
  connections, so memory per connection is the number that sets hosting
  cost.
- [ ] **Chaos run.** Kill and restart the server mid-game; force reconnect
  storms (all clients drop at once); check that rooms, seats and game state
  survive and p99 recovers.
- [ ] **Head-to-head.** Run the same chat/broadcast load against a Socket.IO
  server and a PartyKit (or plain Durable Objects) deployment. Go plus one
  goroutine per room should do very well on memory per connection; publish
  whatever the numbers turn out to be.
- [ ] **Regression gate.** `benchstat` in CI, failing a PR on more than a
  10% regression in fan-out or allocations.

### b. Developer experience (is it easy?)

- [ ] **Lines of code and concepts** for the same games (chat, tic-tac-toe)
  across lobby, Socket.IO and PartyKit, counting everything needed for
  reconnect-to-seat and a host display. Today tic-tac-toe is ~80 lines of
  rules on top of `duel`.
- [ ] **Time to first game:** fresh machine to a phone joined, timed with a
  few people who haven't seen it.

### c. Agent-friendliness (can an AI build on it?)

This is where AGENTS.md pays off, and it is measurable:

- [ ] **Agent eval.** Give a coding agent a fresh repo, the library and
  AGENTS.md, plus prompts like "build rock-paper-scissors for 2–8 players",
  "build a trivia game with a 15s timer" and "add a computer player to
  connect 4". Score automatically: does it compile, does `go test -race`
  pass, does a scripted `LocalConn` playthrough finish, and are there no
  JSON nulls in views. Run each prompt several times, and compare success
  rate with and without AGENTS.md, and against Socket.IO with its docs.
- [ ] Feed failures back into AGENTS.md ("Common mistakes") and into the API.
  If agents keep making the same mistake, the API is probably the problem.
