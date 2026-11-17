package lobby

import (
	"cmp"
	"context"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

// Room is one lobby. Its state is owned by a single goroutine.
//
// Methods documented as "room goroutine only" may only be called from Game
// callbacks or from functions passed to Do. Everything else is safe to call
// from anywhere.
//
// Context: every room has its own context, cancelled when the room closes or
// the server shuts down. Game callbacks and Do functions receive it; pass it
// to anything slow you start from a callback (API calls, goroutines) so it
// stops with the room. Methods that write to the Store or change membership
// take a ctx; pure in-memory sends (Broadcast, Send, SendRole) never block
// and don't.
type Room struct {
	m         *Manager
	code      string
	kind      string
	createdAt time.Time
	game      Game

	ctx    context.Context // lives as long as the room
	cancel context.CancelFunc

	// Owned by the room goroutine.
	members    map[string]*member
	vip        string
	emptySince time.Time // when the last connection dropped; zero while anyone is connected
	locked     bool
	closed     bool

	inbox chan func(context.Context)
	done  chan struct{}
}

func newRoom(m *Manager, code, kind string, createdAt time.Time, game Game) *Room {
	ctx, cancel := context.WithCancel(context.Background())
	return &Room{
		m:          m,
		code:       code,
		kind:       kind,
		createdAt:  createdAt,
		game:       game,
		ctx:        ctx,
		cancel:     cancel,
		members:    make(map[string]*member),
		emptySince: time.Now(),
		inbox:      make(chan func(context.Context), 256),
		done:       make(chan struct{}),
	}
}

// Code returns the room's join code. Safe from any goroutine.
func (r *Room) Code() string { return r.code }

// Kind returns the game kind the room was created with. Safe from any goroutine.
func (r *Room) Kind() string { return r.kind }

// Done is closed when the room closes. Safe from any goroutine.
func (r *Room) Done() <-chan struct{} { return r.done }

// Do queues fn to run on the room goroutine and returns without waiting for
// it. fn receives the room's context. ctx bounds only the wait to enqueue
// (the queue is buffered, so this is normally instant). Do returns
// ErrRoomClosed if the room has closed.
//
// Safe from any goroutine. From inside a callback, call Room methods
// directly instead.
func (r *Room) Do(ctx context.Context, fn func(context.Context, *Room)) error {
	select {
	case <-r.done:
		return ErrRoomClosed
	default:
	}
	select {
	case r.inbox <- func(rctx context.Context) { fn(rctx, r) }:
		return nil
	case <-r.done:
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// call is Do, but waits for fn to finish.
func (r *Room) call(ctx context.Context, fn func(context.Context, *Room)) error {
	finished := make(chan struct{})
	err := r.Do(ctx, func(rctx context.Context, r *Room) {
		defer close(finished)
		fn(rctx, r)
	})
	if err != nil {
		return err
	}
	select {
	case <-finished:
		return nil
	case <-r.done:
		select {
		case <-finished: // fn ran and closed the room itself
			return nil
		default:
			return ErrRoomClosed
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Room) run() {
	t := time.NewTicker(r.m.cfg.SweepInterval)
	defer t.Stop()
	for !r.closed {
		select {
		case fn := <-r.inbox:
			r.guard("event", func() { fn(r.ctx) })
		case <-t.C:
			r.guard("sweep", func() { r.sweep(r.ctx) })
		}
	}
}

// guard recovers panics so a buggy game can't take down the server.
func (r *Room) guard(what string, fn func()) {
	defer func() {
		if p := recover(); p != nil {
			r.m.stats.panics.Add(1)
			r.m.log.Error("panic in room", "code", r.code, "in", what, "panic", p, "stack", string(debug.Stack()))
		}
	}()
	fn()
}

// ---- Membership (room goroutine) ----

func (r *Room) join(ctx context.Context, req JoinRequest) (JoinResult, error) {
	if req.Role == RolePlayer {
		players := 0
		for _, mb := range r.members {
			if mb.Role != RolePlayer {
				continue
			}
			players++
			if strings.EqualFold(mb.Name, req.Name) {
				return JoinResult{}, ErrNameTaken
			}
		}
		if r.locked {
			return JoinResult{}, ErrRoomLocked
		}
		if players >= r.m.cfg.MaxPlayers {
			return JoinResult{}, ErrRoomFull
		}
	}

	now := time.Now()
	token := newToken()
	mb := &member{
		Client:         Client{ID: newID(), Name: req.Name, Role: req.Role, JoinedAt: now},
		tokenHash:      hashToken(token),
		disconnectedAt: now, // must attach a connection within ReconnectGrace
	}
	r.members[mb.ID] = mb
	r.recomputeVIP()

	r.guard("OnJoin", func() { r.game.OnJoin(ctx, r, mb.Client) })
	r.broadcastState()
	r.Persist(ctx)

	r.m.log.Info("client joined", "code", r.code, "id", mb.ID, "role", mb.Role, "name", mb.Name)
	return JoinResult{Code: r.code, Client: mb.Client, Token: token}, nil
}

func (r *Room) attach(ctx context.Context, token string, c conn) (Client, error) {
	var mb *member
	for _, m := range r.members {
		if tokenMatches(m.tokenHash, token) {
			mb = m
			break
		}
	}
	if mb == nil {
		return Client{}, ErrUnauthorized
	}

	if mb.conn != nil {
		mb.conn.close(StatusReplaced, "connected from another tab or device")
	}
	mb.conn = c
	mb.Connected = true
	mb.disconnectedAt = time.Time{}
	r.emptySince = time.Time{}

	r.send(mb, MsgWelcome, WelcomeData{Protocol: ProtocolVersion, Code: r.code, Game: r.kind, You: mb.Client})
	r.broadcastState()
	r.guard("OnConnect", func() { r.game.OnConnect(ctx, r, mb.Client) })
	return mb.Client, nil
}

func (r *Room) detach(ctx context.Context, id string, c conn) {
	mb := r.members[id]
	if mb == nil || mb.conn != c {
		return // already removed, or replaced by a newer connection
	}
	now := time.Now()
	mb.conn = nil
	mb.Connected = false
	mb.disconnectedAt = now
	if r.connectedCount() == 0 {
		r.emptySince = now
	}
	r.guard("OnDisconnect", func() { r.game.OnDisconnect(ctx, r, mb.Client) })
	r.broadcastState()
}

func (r *Room) remove(ctx context.Context, id string, code StatusCode, reason string) {
	mb := r.members[id]
	if mb == nil {
		return
	}
	delete(r.members, id)
	if mb.conn != nil {
		mb.conn.close(code, reason)
	}
	if r.connectedCount() == 0 && r.emptySince.IsZero() {
		r.emptySince = time.Now()
	}
	r.recomputeVIP()

	r.guard("OnLeave", func() { r.game.OnLeave(ctx, r, mb.Client) })
	r.m.log.Info("client removed", "code", r.code, "id", id, "reason", reason)
	if len(r.members) == 0 {
		// Nobody holds a seat, so nobody can reconnect: close now rather
		// than waiting out IdleTimeout.
		r.Close(ctx)
		return
	}
	r.broadcastState()
	r.Persist(ctx)
}

func (r *Room) handle(ctx context.Context, id string, c conn, msg Message) {
	mb := r.members[id]
	if mb == nil || mb.conn != c {
		return
	}
	if !msg.reserved() {
		r.guard("OnMessage", func() { r.game.OnMessage(ctx, r, mb.Client, msg) })
		return
	}

	switch msg.Type {
	case MsgLeave:
		r.remove(ctx, id, StatusNormalClosure, "left")
	case MsgKick:
		var d struct {
			ID string `json:"id"`
		}
		switch {
		case id != r.vip:
			r.sendError(mb, "only the VIP can kick")
		case msg.Decode(&d) != nil || r.members[d.ID] == nil:
			r.sendError(mb, "no such client")
		case d.ID == id:
			r.sendError(mb, "you can't kick yourself")
		default:
			r.remove(ctx, d.ID, StatusKicked, "kicked by VIP")
		}
	case MsgLock:
		var d struct {
			Locked bool `json:"locked"`
		}
		if id != r.vip {
			r.sendError(mb, "only the VIP can lock the room")
		} else if msg.Decode(&d) == nil {
			r.SetLocked(ctx, d.Locked)
		}
	default:
		r.sendError(mb, "unknown message type "+msg.Type)
	}
}

func (r *Room) sweep(ctx context.Context) {
	now := time.Now()
	grace := r.m.cfg.ReconnectGrace
	for id, mb := range r.members {
		if mb.conn == nil && now.Sub(mb.disconnectedAt) > grace {
			r.remove(ctx, id, StatusNormalClosure, "reconnect grace period expired")
		}
	}
	if !r.emptySince.IsZero() && now.Sub(r.emptySince) > r.m.cfg.IdleTimeout {
		r.m.log.Info("closing idle room", "code", r.code)
		r.Close(ctx)
	}
}

// recomputeVIP makes the earliest-joined player VIP. The VIP keeps the role
// while disconnected; it only moves when they leave the room.
func (r *Room) recomputeVIP() {
	if mb, ok := r.members[r.vip]; ok && mb.Role == RolePlayer {
		return
	}
	r.vip = ""
	if players := r.Players(); len(players) > 0 {
		r.vip = players[0].ID
	}
}

func (r *Room) connectedCount() int {
	n := 0
	for _, mb := range r.members {
		if mb.conn != nil {
			n++
		}
	}
	return n
}

// ---- Public API for games (room goroutine only) ----

// Clients returns everyone in the room, ordered by join time.
func (r *Room) Clients() []Client {
	out := make([]Client, 0, len(r.members))
	for _, mb := range r.members {
		out = append(out, mb.Client)
	}
	slices.SortFunc(out, func(a, b Client) int {
		return cmp.Or(a.JoinedAt.Compare(b.JoinedAt), strings.Compare(a.ID, b.ID))
	})
	return out
}

// Players returns clients with RolePlayer, ordered by join time.
func (r *Room) Players() []Client {
	return slices.DeleteFunc(r.Clients(), func(c Client) bool { return c.Role != RolePlayer })
}

// Client looks up a client by ID.
func (r *Room) Client(id string) (Client, bool) {
	mb, ok := r.members[id]
	if !ok {
		return Client{}, false
	}
	return mb.Client, true
}

// VIP returns the VIP's client ID, or "" if there are no players.
func (r *Room) VIP() string { return r.vip }

// SetLocked stops (or resumes) new players joining, e.g. while a round is in
// progress. Existing members can still reconnect and displays can still join.
func (r *Room) SetLocked(ctx context.Context, locked bool) {
	if r.locked == locked {
		return
	}
	r.locked = locked
	r.broadcastState()
	r.Persist(ctx)
}

// Locked reports whether new players are blocked from joining.
func (r *Room) Locked() bool { return r.locked }

// Broadcast sends a message to every connected client. It never blocks.
func (r *Room) Broadcast(typ string, data any) {
	r.sendWhere(typ, data, func(*member) bool { return true })
}

// SendRole sends a message to every connected client with the given role,
// e.g. big-screen-only updates to RoleDisplay. It never blocks.
func (r *Room) SendRole(role Role, typ string, data any) {
	r.sendWhere(typ, data, func(mb *member) bool { return mb.Role == role })
}

// Send sends a message to one client. It's a no-op if they aren't connected,
// and it never blocks.
func (r *Room) Send(id string, typ string, data any) {
	if mb, ok := r.members[id]; ok {
		r.send(mb, typ, data)
	}
}

// Kick removes a client from the room.
func (r *Room) Kick(ctx context.Context, id, reason string) {
	r.remove(ctx, id, StatusKicked, reason)
}

// After runs fn on the room goroutine after d, with the room's context. Use
// it for round timers. The returned function cancels the timer if it hasn't
// fired yet. Timers that fire after the room closes are dropped.
func (r *Room) After(d time.Duration, fn func(context.Context, *Room)) (stop func() bool) {
	t := time.AfterFunc(d, func() { _ = r.Do(r.ctx, fn) })
	return t.Stop
}

// Persist saves the room (and game snapshot, if any) to the Store, bounded
// by ctx and a 5s timeout. Membership changes persist automatically; games
// call this after meaningful state changes.
func (r *Room) Persist(ctx context.Context) {
	store := r.m.cfg.Store
	if store == nil || r.closed {
		return
	}
	rec := RoomRecord{Code: r.code, Game: r.kind, CreatedAt: r.createdAt, Locked: r.locked}
	for _, c := range r.Clients() {
		rec.Clients = append(rec.Clients, ClientRecord{Client: c, TokenHash: r.members[c.ID].tokenHash})
	}
	if s, ok := r.game.(Snapshotter); ok {
		state, err := s.Snapshot()
		if err != nil {
			r.m.log.Error("snapshotting game", "code", r.code, "err", err)
			return
		}
		rec.GameState = state
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.SaveRoom(ctx, rec); err != nil {
		r.m.log.Error("persisting room", "code", r.code, "err", err)
	}
}

// Close disconnects everyone, deletes the room from the Store, frees the
// code and cancels the room's context.
func (r *Room) Close(ctx context.Context) {
	if r.closed {
		return
	}
	r.closeConns(StatusRoomClosed, "room closed")
	if store := r.m.cfg.Store; store != nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := store.DeleteRoom(ctx, r.code); err != nil {
			r.m.log.Error("deleting room", "code", r.code, "err", err)
		}
	}
	r.finish()
	r.m.log.Info("room closed", "code", r.code)
}

// shutdown persists and stops the room without deleting it from the Store.
func (r *Room) shutdown(ctx context.Context) {
	if r.closed {
		return
	}
	r.Persist(ctx)
	r.closeConns(StatusGoingAway, "server restarting")
	r.finish()
}

func (r *Room) finish() {
	r.closed = true
	r.m.remove(r.code)
	close(r.done)
	r.cancel()
}

func (r *Room) closeConns(code StatusCode, reason string) {
	for _, mb := range r.members {
		if mb.conn != nil {
			mb.conn.close(code, reason)
			mb.conn = nil
		}
	}
}

// ---- Sending (room goroutine) ----

func (r *Room) broadcastState() {
	r.Broadcast(MsgState, StateData{Code: r.code, VIP: r.vip, Locked: r.locked, Clients: r.Clients()})
}

func (r *Room) sendError(mb *member, msg string) {
	r.send(mb, MsgError, map[string]string{"error": msg})
}

func (r *Room) send(mb *member, typ string, data any) {
	r.sendWhere(typ, data, func(m *member) bool { return m == mb })
}

func (r *Room) sendWhere(typ string, data any, match func(*member) bool) {
	// Encode at most once, and only if a websocket client needs bytes;
	// local (channel) clients get the Go value as-is.
	var (
		payload []byte
		encErr  error
		encoded bool
	)
	enc := func() ([]byte, error) {
		if !encoded {
			encoded = true
			payload, encErr = encode(typ, data)
			if encErr != nil {
				r.m.log.Error("encoding message", "code", r.code, "type", typ, "err", encErr)
			}
		}
		return payload, encErr
	}

	var slow []*member
	for _, mb := range r.members {
		if mb.conn == nil || !match(mb) {
			continue
		}
		if !mb.conn.deliver(typ, data, enc) {
			slow = append(slow, mb)
		} else {
			r.m.stats.messagesOut.Add(1)
		}
	}
	// Clients that can't keep up are dropped; they can reconnect and get a
	// fresh snapshot. Done after the loop because detach broadcasts.
	for _, mb := range slow {
		r.m.stats.slowDrops.Add(1)
		c := mb.conn
		c.close(StatusPolicyViolation, "too slow")
		r.detach(r.ctx, mb.ID, c)
	}
}

func (r *Room) String() string { return fmt.Sprintf("Room(%s)", r.code) }
