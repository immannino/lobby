package lobby

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Message is the wire envelope for everything sent over a room's websocket,
// in both directions: {"type": "...", "data": {...}}.
//
// Types beginning with "lobby." are reserved for this package; everything
// else is passed through to the Game untouched.
type Message struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`

	// value is set instead of Data for messages from a LocalConn, so
	// in-process clients skip JSON entirely when types line up.
	value any
}

// Decode stores the message payload in v, which must be a pointer. Always
// use Decode rather than reading Data directly: messages from in-process
// clients (LocalConn) carry a Go value and have no Data.
func (m Message) Decode(v any) error {
	if m.value != nil {
		dst := reflect.ValueOf(v)
		if dst.Kind() == reflect.Pointer && !dst.IsNil() {
			if src := reflect.ValueOf(m.value); src.Type().AssignableTo(dst.Elem().Type()) {
				dst.Elem().Set(src)
				return nil
			}
		}
		// Different types (e.g. a map sent, a struct expected): convert via JSON.
		b, err := json.Marshal(m.value)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, v)
	}
	if len(m.Data) == 0 {
		return nil
	}
	return json.Unmarshal(m.Data, v)
}

func (m Message) reserved() bool { return strings.HasPrefix(m.Type, "lobby.") }

// Messages the server sends.
const (
	// MsgWelcome is sent once per connection: {"you": Client, "code": "BCDF"}.
	MsgWelcome = "lobby.welcome"
	// MsgState is broadcast whenever membership or connectivity changes.
	MsgState = "lobby.state"
	// MsgError reports a rejected client request: {"error": "..."}.
	MsgError = "lobby.error"
)

// Messages clients may send.
const (
	// MsgLeave removes the sender from the room immediately.
	MsgLeave = "lobby.leave"
	// MsgKick lets the VIP remove a player: {"id": "..."}.
	MsgKick = "lobby.kick"
	// MsgLock lets the VIP lock or unlock the room: {"locked": true}.
	MsgLock = "lobby.lock"
)

// StateData is the payload of MsgState.
type StateData struct {
	Code    string   `json:"code"`
	VIP     string   `json:"vip"`
	Locked  bool     `json:"locked"`
	Clients []Client `json:"clients"`
}

// ProtocolVersion is sent in every welcome so clients can detect a server
// speaking a wire protocol they don't understand. It changes only on
// incompatible changes to the lobby.* messages or close codes.
const ProtocolVersion = 1

// WelcomeData is the payload of MsgWelcome.
type WelcomeData struct {
	Protocol int    `json:"protocol"`
	Code     string `json:"code"`
	Game     string `json:"game"`
	You      Client `json:"you"`
}

func encode(typ string, data any) ([]byte, error) {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(Message{Type: typ, Data: raw})
}
