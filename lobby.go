// Package lobby manages short-lived, code-joined rooms ("Jackbox-style"
// lobbies) with realtime websocket connections.
//
// A Manager owns rooms. Each Room runs on its own goroutine, and every Game
// callback for that room runs on that goroutine, one at a time, so game code
// never needs locks. People join over HTTP (receiving a reconnect token, also
// set as a cookie) and then attach a websocket. If the socket drops, the client
// keeps its seat for Config.ReconnectGrace.
//
// The package knows nothing about the game being played: it handles
// membership, roles (players vs. a shared display), the VIP, reconnects,
// expiry, persistence and message fan-out, and hands everything else to the
// Game interface.
package lobby

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Game is the per-room logic plugged into the lobby. All methods are called
// on the room's goroutine; it is safe to call any *Room method from them.
// Embed BaseGame to implement only what you need.
//
// ctx is the room's context: it is cancelled when the room closes or the
// server shuts down. Pass it to anything slow the game starts (API calls,
// goroutines) so that work stops with the room.
type Game interface {
	// OnJoin is called when a client is added to the room.
	OnJoin(ctx context.Context, r *Room, c Client)
	// OnLeave is called when a client is removed: it left, was kicked, or
	// stayed disconnected past ReconnectGrace.
	OnLeave(ctx context.Context, r *Room, c Client)
	// OnConnect is called each time a client's connection attaches, including
	// reconnects. Send the client whatever state it needs to render here.
	OnConnect(ctx context.Context, r *Room, c Client)
	// OnDisconnect is called when a client's connection drops. The client is
	// still in the room until it reconnects or the grace period ends.
	OnDisconnect(ctx context.Context, r *Room, c Client)
	// OnMessage receives every non-"lobby." message a client sends.
	OnMessage(ctx context.Context, r *Room, c Client, m Message)
}

// Snapshotter is optionally implemented by a Game whose state should be
// persisted alongside the room (requires a Store).
type Snapshotter interface {
	Snapshot() ([]byte, error)
	Restore(data []byte) error
}

// BaseGame is a no-op Game to embed.
type BaseGame struct{}

func (BaseGame) OnJoin(context.Context, *Room, Client)             {}
func (BaseGame) OnLeave(context.Context, *Room, Client)            {}
func (BaseGame) OnConnect(context.Context, *Room, Client)          {}
func (BaseGame) OnDisconnect(context.Context, *Room, Client)       {}
func (BaseGame) OnMessage(context.Context, *Room, Client, Message) {}

// Errors returned by Manager methods (and mapped to HTTP statuses by Handler).
var (
	ErrRoomNotFound = errors.New("lobby: room not found")
	ErrRoomFull     = errors.New("lobby: room is full")
	ErrNameTaken    = errors.New("lobby: name already taken")
	ErrInvalidName  = errors.New("lobby: invalid name")
	ErrRoomClosed   = errors.New("lobby: room closed")
	ErrUnauthorized = errors.New("lobby: invalid or missing token")
	ErrNoCodes      = errors.New("lobby: could not allocate a room code")
	ErrRoomLocked   = errors.New("lobby: game in progress, room is locked")
	ErrRateLimited  = errors.New("lobby: too many requests, slow down")
	ErrUnknownGame  = errors.New("lobby: unknown game")
)

// Config configures a Manager. Only NewGame is required.
type Config struct {
	// NewGame returns the Game for a new (or restored) room. kind is the game
	// name requested at creation ("" if none was given); return an error
	// wrapping ErrUnknownGame to reject it.
	NewGame func(code, kind string) (Game, error)
	// Store persists rooms across restarts. Nil keeps everything in memory.
	Store Store
	// MaxPlayers caps players per room (displays don't count). Default 8.
	MaxPlayers int
	// MaxNameLength caps player names, in characters. Default 20.
	MaxNameLength int
	// CodeLength is the room code length. Default 4.
	CodeLength int
	// ReconnectGrace is how long a disconnected client keeps its seat.
	// Default 2 minutes.
	ReconnectGrace time.Duration
	// IdleTimeout closes a room once nobody has been connected for this long.
	// Default 10 minutes.
	IdleTimeout time.Duration
	// SweepInterval is how often rooms check for expired clients and
	// idleness. Default 5 seconds.
	SweepInterval time.Duration
	// OriginPatterns are extra allowed websocket Origin hosts (see
	// github.com/coder/websocket AcceptOptions). Same-origin is always allowed.
	OriginPatterns []string
	// SecureCookies sets the Secure flag on token cookies. Enable behind HTTPS.
	SecureCookies bool
	// RateLimit caps create+join requests per client IP per minute.
	// Default 30; negative disables.
	RateLimit int
	// MessageRate caps messages per second from one websocket connection,
	// with bursts up to MessageBurst. A connection that exceeds it is closed
	// with 1008 (policy violation); clients may reconnect. Defaults 20 and
	// 40; negative MessageRate disables. LocalConns are not limited.
	MessageRate  float64
	MessageBurst int
	// MaxMessageBytes caps the size of one incoming websocket message.
	// Default 32 KiB.
	MaxMessageBytes int64
	// PingInterval is how often idle websockets are pinged, which keeps
	// proxies from timing them out and detects dead peers. Default 30s.
	PingInterval time.Duration
	// ClientIP extracts the client address used for rate limiting. Defaults
	// to the host part of Request.RemoteAddr. Behind a reverse proxy, supply
	// one that reads a header your proxy sets (and that clients can't spoof).
	ClientIP func(*http.Request) string
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

func (c *Config) setDefaults() {
	if c.MaxPlayers <= 0 {
		c.MaxPlayers = 8
	}
	if c.MaxNameLength <= 0 {
		c.MaxNameLength = 20
	}
	if c.CodeLength <= 0 {
		c.CodeLength = 4
	}
	if c.ReconnectGrace <= 0 {
		c.ReconnectGrace = 2 * time.Minute
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 10 * time.Minute
	}
	if c.RateLimit == 0 {
		c.RateLimit = 30
	}
	if c.MessageRate == 0 {
		c.MessageRate = 20
	}
	if c.MessageBurst <= 0 {
		c.MessageBurst = max(1, int(2*c.MessageRate))
	}
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = 32 << 10
	}
	if c.PingInterval <= 0 {
		c.PingInterval = 30 * time.Second
	}
	if c.ClientIP == nil {
		c.ClientIP = remoteIP
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = 5 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Manager owns all rooms on this server.
type Manager struct {
	cfg Config
	log *slog.Logger

	mu    sync.RWMutex
	rooms map[string]*Room

	limiter *limiter // nil when rate limiting is disabled

	closing sync.WaitGroup // in-flight websocket close handshakes
	stats   counters
}

// New creates a Manager. It panics if cfg.NewGame is nil.
func New(cfg Config) *Manager {
	if cfg.NewGame == nil {
		panic("lobby: Config.NewGame is required")
	}
	cfg.setDefaults()
	m := &Manager{cfg: cfg, log: cfg.Logger, rooms: make(map[string]*Room)}
	if cfg.RateLimit > 0 {
		m.limiter = newLimiter(cfg.RateLimit, time.Minute)
	}
	return m
}

// Create opens a new, empty room running the given kind of game. Rooms with
// nobody connected are closed after IdleTimeout, so callers normally Join
// right away.
func (m *Manager) Create(ctx context.Context, kind string) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var code string
	for range 20 {
		c := newCode(m.cfg.CodeLength)
		if _, taken := m.rooms[c]; !taken {
			code = c
			break
		}
	}
	if code == "" {
		return nil, ErrNoCodes
	}
	game, err := m.cfg.NewGame(code, kind)
	if err != nil {
		return nil, err
	}
	if game == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownGame, kind)
	}
	r := newRoom(m, code, kind, time.Now(), game)
	m.rooms[code] = r
	m.stats.roomsCreated.Add(1)

	go r.run()
	m.log.Info("room created", "code", code, "game", kind)
	return r, nil
}

// Room looks up an open room by code (case-insensitive).
func (m *Manager) Room(code string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rooms[NormalizeCode(code)]
	return r, ok
}

// Rooms returns the number of open rooms.
func (m *Manager) Rooms() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

// JoinRequest describes who is joining.
type JoinRequest struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
}

// JoinResult is returned from Join. Token is the client's reconnect secret;
// it is only ever returned here.
type JoinResult struct {
	Code   string `json:"code"`
	Client Client `json:"client"`
	Token  string `json:"token"`
}

// Join adds a client to the room with the given code.
func (m *Manager) Join(ctx context.Context, code string, req JoinRequest) (JoinResult, error) {
	r, ok := m.Room(code)
	if !ok {
		return JoinResult{}, ErrRoomNotFound
	}
	if req.Role == RolePlayer {
		name, err := m.cleanName(req.Name)
		if err != nil {
			return JoinResult{}, err
		}
		req.Name = name
	} else {
		req.Name = "Display"
	}

	var (
		res     JoinResult
		joinErr error
	)
	err := r.call(ctx, func(rctx context.Context, r *Room) { res, joinErr = r.join(rctx, req) })
	if err != nil {
		return JoinResult{}, err
	}
	return res, joinErr
}

// CreateAndJoin creates a room for the given kind of game and joins it in
// one step.
func (m *Manager) CreateAndJoin(ctx context.Context, kind string, req JoinRequest) (JoinResult, error) {
	if req.Role == RolePlayer {
		if _, err := m.cleanName(req.Name); err != nil {
			return JoinResult{}, err
		}
	}
	r, err := m.Create(ctx, kind)
	if err != nil {
		return JoinResult{}, err
	}
	res, err := m.Join(ctx, r.code, req)
	if err != nil {
		_ = r.Do(ctx, func(rctx context.Context, r *Room) { r.Close(rctx) })
	}
	return res, err
}

func (m *Manager) cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > m.cfg.MaxNameLength {
		return "", fmt.Errorf("%w: must be 1-%d characters", ErrInvalidName, m.cfg.MaxNameLength)
	}
	return name, nil
}

// Restore reloads persisted rooms from the Store. Call it once at startup,
// before serving traffic. All restored clients start out disconnected.
func (m *Manager) Restore(ctx context.Context) error {
	if m.cfg.Store == nil {
		return nil
	}
	recs, err := m.cfg.Store.LoadRooms(ctx)
	if err != nil {
		return fmt.Errorf("lobby: loading rooms: %w", err)
	}
	now := time.Now()
	for _, rec := range recs {
		game, err := m.cfg.NewGame(rec.Code, rec.Game)
		if err != nil || game == nil {
			m.log.Error("dropping persisted room with unknown game", "code", rec.Code, "game", rec.Game, "err", err)
			_ = m.cfg.Store.DeleteRoom(ctx, rec.Code)
			continue
		}
		if s, ok := game.(Snapshotter); ok && len(rec.GameState) > 0 {
			if err := s.Restore(rec.GameState); err != nil {
				m.log.Error("restoring game state; starting fresh", "code", rec.Code, "err", err)
			}
		}
		r := newRoom(m, rec.Code, rec.Game, rec.CreatedAt, game)
		for _, cr := range rec.Clients {
			c := cr.Client
			c.Connected = false
			r.members[c.ID] = &member{Client: c, tokenHash: cr.TokenHash, disconnectedAt: now}
		}
		r.emptySince = now
		r.locked = rec.Locked
		r.recomputeVIP()

		m.mu.Lock()
		m.rooms[rec.Code] = r
		m.mu.Unlock()
		go r.run()
	}
	m.log.Info("rooms restored", "count", len(recs))
	return nil
}

// Shutdown persists every room and disconnects all clients with "going away"
// so they reconnect to the next server process. Rooms are not deleted from
// the Store.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.RLock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.RUnlock()

	var errs []error
	for _, r := range rooms {
		if err := r.call(ctx, func(_ context.Context, r *Room) { r.shutdown(ctx) }); err != nil && !errors.Is(err, ErrRoomClosed) {
			errs = append(errs, err)
		}
	}

	// Let "going away" close frames reach clients before the process exits.
	flushed := make(chan struct{})
	go func() { m.closing.Wait(); close(flushed) }()
	select {
	case <-flushed:
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}
	return errors.Join(errs...)
}

func (m *Manager) remove(code string) {
	m.mu.Lock()
	delete(m.rooms, code)
	m.mu.Unlock()
}
