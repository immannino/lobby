package lobby

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"time"
)

// Role distinguishes the shared "big screen" from people playing on their
// own devices.
type Role int

const (
	// RolePlayer is a named participant. Players count toward MaxPlayers and
	// the earliest-joined player is the room's VIP.
	RolePlayer Role = iota
	// RoleDisplay is the shared screen (TV, laptop). Displays have no name,
	// don't count toward MaxPlayers and can never be VIP.
	RoleDisplay
)

func (r Role) String() string {
	switch r {
	case RolePlayer:
		return "player"
	case RoleDisplay:
		return "display"
	}
	return fmt.Sprintf("Role(%d)", int(r))
}

func (r Role) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

func (r *Role) UnmarshalText(b []byte) error {
	switch string(b) {
	case "", "player":
		*r = RolePlayer
	case "display":
		*r = RoleDisplay
	default:
		return fmt.Errorf("lobby: unknown role %q", b)
	}
	return nil
}

// Client is the public view of someone in a room. It is a value type: games
// receive copies and should key any per-client state by ID.
type Client struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	Connected bool      `json:"connected"`
	JoinedAt  time.Time `json:"joinedAt"`
}

// member is the room-internal record for a client. Only the room goroutine
// touches it.
type member struct {
	Client
	tokenHash      []byte
	conn           conn      // nil while disconnected
	disconnectedAt time.Time // zero while connected
}

func randomString(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error on supported platforms
	return base64.RawURLEncoding.EncodeToString(b)
}

func newID() string    { return randomString(9) }
func newToken() string { return randomString(32) }

// hashToken lets us store and compare reconnect tokens without keeping the
// secret itself around (or in the database).
func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func tokenMatches(hash []byte, token string) bool {
	return subtle.ConstantTimeCompare(hash, hashToken(token)) == 1
}
