package lobby_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/immannino/lobby"
)

func TestMessageRateLimitClosesChattyConnection(t *testing.T) {
	h := newHarness(t, lobby.Config{MessageRate: 5, MessageBurst: 5})
	ada := h.create("Ada")
	c := h.dial(ada.Code, ada.Token)
	c.expect(lobby.MsgWelcome)

	for range 50 { // far past the burst, faster than the refill
		b, _ := json.Marshal(map[string]any{"type": "say", "data": map[string]string{"text": "spam"}})
		if err := c.ws.Write(t.Context(), websocket.MessageText, b); err != nil {
			break // already closed
		}
	}
	if code := c.expectClose(); code != lobby.StatusPolicyViolation {
		t.Fatalf("close code = %d, want %d", code, lobby.StatusPolicyViolation)
	}
	if s := h.m.Stats(); s.RateLimited == 0 {
		t.Errorf("stats.RateLimited = 0 after closing a chatty connection")
	}
	// The seat survives: a well-behaved reconnect works.
	again := h.dial(ada.Code, ada.Token)
	again.expect(lobby.MsgWelcome)
}

func TestMessageRateLimitAllowsNormalPlay(t *testing.T) {
	h := newHarness(t, lobby.Config{MessageRate: 5, MessageBurst: 10})
	ada := h.create("Ada")
	c := h.dial(ada.Code, ada.Token)
	c.expect(lobby.MsgWelcome)
	for range 10 { // within the burst
		c.send("say", map[string]string{"text": "hi"})
		c.expect("said")
	}
}

func TestWelcomeHasProtocolAndStatsCount(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ada := h.create("Ada")
	c := h.dial(ada.Code, ada.Token)
	var w lobby.WelcomeData
	if err := c.expect(lobby.MsgWelcome).Decode(&w); err != nil {
		t.Fatal(err)
	}
	if w.Protocol != lobby.ProtocolVersion {
		t.Errorf("protocol = %d, want %d", w.Protocol, lobby.ProtocolVersion)
	}
	c.send("say", map[string]string{"text": "hi"})
	c.expect("said")

	s := h.m.Stats()
	if s.Rooms != 1 || s.RoomsCreated != 1 || s.WebSockets != 1 || s.MessagesIn != 1 || s.MessagesOut < 3 {
		t.Errorf("stats = %+v", s)
	}
}

func TestPanicsCounted(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ada := h.create("Ada")
	c := h.dial(ada.Code, ada.Token)
	c.expect(lobby.MsgWelcome)
	c.send("boom", nil)
	c.send("say", map[string]string{"text": "still here"})
	c.expect("said")
	if s := h.m.Stats(); s.Panics != 1 {
		t.Errorf("stats.Panics = %d, want 1", s.Panics)
	}
}

func TestHeaderClientIP(t *testing.T) {
	ip := lobby.HeaderClientIP("X-Forwarded-For")
	tests := []struct{ header, want string }{
		{"", "10.0.0.1"}, // falls back to RemoteAddr
		{"203.0.113.9", "203.0.113.9"},
		{"6.6.6.6, 203.0.113.9", "203.0.113.9"}, // the proxy's entry, not the client's claim
		{" 6.6.6.6 ,  203.0.113.9 ", "203.0.113.9"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("POST", "/rooms", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		if tt.header != "" {
			r.Header.Set("X-Forwarded-For", tt.header)
		}
		if got := ip(r); got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.header, got, tt.want)
		}
	}
}

// FuzzHTTPJoin throws arbitrary bodies and content types at create and join.
// The handler must never panic or answer 500.
func FuzzHTTPJoin(f *testing.F) {
	f.Add("application/json", `{"name":"Ada"}`)
	f.Add("application/json", `{"role":"display","game":"echo"}`)
	f.Add("application/json", `{"name":"`+strings.Repeat("é", 30)+`"}`)
	f.Add("application/x-www-form-urlencoded", "name=Ada&role=player")
	f.Add("application/x-www-form-urlencoded", "role=wizard")
	f.Add("text/plain", "\x00\xff")
	m := lobby.New(lobby.Config{
		NewGame:   func(string, string) (lobby.Game, error) { return &echoGame{}, nil },
		RateLimit: -1,
		Logger:    discard(),
	})
	h := m.Handler()
	f.Fuzz(func(t *testing.T, contentType, body string) {
		for _, path := range []string{"/rooms", "/rooms/BCDF/join"} {
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Content-Type", contentType)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code >= 500 && w.Code != http.StatusServiceUnavailable {
				t.Fatalf("POST %s %q: %d %s", path, body, w.Code, w.Body)
			}
		}
	})
}

// FuzzMessageDecode: decoding any frame into any shape must not panic.
func FuzzMessageDecode(f *testing.F) {
	f.Add(`{"type":"say","data":{"text":"hi"}}`)
	f.Add(`{"type":"move","data":4}`)
	f.Add(`{"type":"lobby.kick","data":{"id":null}}`)
	f.Add(`{"type":"x","data":[1,"a",{}]}`)
	f.Fuzz(func(t *testing.T, frame string) {
		var m lobby.Message
		if json.Unmarshal([]byte(frame), &m) != nil {
			return
		}
		var (
			s   string
			n   int
			mp  map[string]any
			arr []any
			st  struct {
				ID   string `json:"id"`
				Text string
			}
		)
		for _, v := range []any{&s, &n, &mp, &arr, &st} {
			_ = m.Decode(v)
		}
	})
}

func TestTokenBucketRefills(t *testing.T) {
	// Behavioural check through the public API: after waiting, a client that
	// used its burst can send again.
	h := newHarness(t, lobby.Config{MessageRate: 20, MessageBurst: 3})
	ada := h.create("Ada")
	c := h.dial(ada.Code, ada.Token)
	c.expect(lobby.MsgWelcome)
	for round := range 3 {
		for range 3 {
			c.send("say", map[string]string{"text": "hi"})
			c.expect("said")
		}
		_ = round
		time.Sleep(200 * time.Millisecond) // refills 4 tokens
	}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
