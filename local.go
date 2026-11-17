package lobby

import (
	"context"
	"sync"
	"time"
)

// Event is a message delivered to a LocalConn.
//
// Data is the exact Go value the room or game passed to Broadcast/Send; it
// is not copied or encoded. Treat it as read-only. Games that want to
// support local clients should send values (or fresh copies), not pointers
// to state they keep mutating, the same rule as sending on any Go channel.
//
// Lobby events carry typed data: MsgWelcome → WelcomeData, MsgState →
// StateData, MsgError → map[string]string.
type Event struct {
	Type string
	Data any
}

// LocalConn is an in-process connection to a room over Go channels: no
// HTTP, websocket or JSON. Use it for bots, tests, terminal clients, or apps
// where the players share a process with the server. It behaves like a
// websocket client in every other way: it holds a seat via its token,
// receives the same messages, and is subject to the same slow-consumer and
// reconnect rules.
type LocalConn struct {
	room   *Room
	client Client
	events chan Event

	closed bool // room goroutine only

	mu     sync.Mutex
	status StatusCode
	reason string
}

// LocalBuffer is the number of undelivered events a LocalConn may hold
// before it is dropped as too slow.
const LocalBuffer = 256

// Connect attaches an in-process client to a room, using the token from
// Join. A connection that already holds the seat (websocket or local) is
// replaced, as with a second browser tab.
//
// The welcome and state events are already waiting on Events when Connect
// returns.
func (m *Manager) Connect(ctx context.Context, code, token string) (*LocalConn, error) {
	room, ok := m.Room(code)
	if !ok {
		return nil, ErrRoomNotFound
	}
	lc := &LocalConn{room: room, events: make(chan Event, LocalBuffer)}
	var attachErr error
	if err := room.call(ctx, func(rctx context.Context, r *Room) { lc.client, attachErr = r.attach(rctx, token, lc) }); err != nil {
		return nil, err
	}
	if attachErr != nil {
		return nil, attachErr
	}
	return lc, nil
}

// Client returns the client this connection is seated as.
func (lc *LocalConn) Client() Client { return lc.client }

// Events delivers everything the room sends this client. It is closed when
// the connection ends; CloseStatus then says why.
func (lc *LocalConn) Events() <-chan Event { return lc.events }

// Send sends a message to the room, exactly as a websocket client would.
// The game receives it in OnMessage and reads it with Message.Decode, which
// hands over data directly when the types match. ctx bounds only the
// enqueue; Send doesn't wait for the game to handle the message.
func (lc *LocalConn) Send(ctx context.Context, typ string, data any) error {
	return lc.room.Do(ctx, func(rctx context.Context, r *Room) {
		r.handle(rctx, lc.client.ID, lc, Message{Type: typ, value: data})
	})
}

// Leave removes the client from the room.
func (lc *LocalConn) Leave(ctx context.Context) error { return lc.Send(ctx, MsgLeave, nil) }

// Close disconnects without leaving: the seat is kept for ReconnectGrace,
// as when a websocket drops. Events is closed once the room processes it.
// Like io.Closer it takes no context; it gives up after 5s if the room is
// wedged, and returns ErrRoomClosed if the room is already gone.
func (lc *LocalConn) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return lc.room.Do(ctx, func(rctx context.Context, r *Room) {
		r.detach(rctx, lc.client.ID, lc)
		lc.close(StatusNormalClosure, "closed")
	})
}

// CloseStatus reports why the connection ended. Valid after Events is
// closed; before that it returns 0, "".
func (lc *LocalConn) CloseStatus() (StatusCode, string) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.status, lc.reason
}

func (lc *LocalConn) deliver(typ string, data any, _ func() ([]byte, error)) bool {
	if lc.closed {
		return true
	}
	select {
	case lc.events <- Event{Type: typ, Data: data}:
		return true
	default:
		return false
	}
}

func (lc *LocalConn) close(code StatusCode, reason string) {
	if lc.closed {
		return
	}
	lc.closed = true
	lc.mu.Lock()
	lc.status, lc.reason = code, reason
	lc.mu.Unlock()
	close(lc.events)
}
