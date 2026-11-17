package lobby_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/sqlitestore"

	"github.com/coder/websocket"
)

// echoGame broadcasts "say" messages and keeps a counter it can snapshot.
type echoGame struct {
	lobby.BaseGame
	mu      sync.Mutex
	Count   int `json:"count"`
	events  []string
	roomCtx context.Context // the ctx passed to callbacks, for lifetime tests
}

func (g *echoGame) log(s string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, s)
}

func (g *echoGame) Events() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

func (g *echoGame) OnJoin(ctx context.Context, r *lobby.Room, c lobby.Client) {
	g.mu.Lock()
	g.roomCtx = ctx
	g.mu.Unlock()
	g.log("join:" + c.Name)
}
func (g *echoGame) OnLeave(_ context.Context, r *lobby.Room, c lobby.Client) {
	g.log("leave:" + c.Name)
}
func (g *echoGame) OnConnect(_ context.Context, r *lobby.Room, c lobby.Client) {
	r.Send(c.ID, "count", g.Count)
}
func (g *echoGame) OnMessage(ctx context.Context, r *lobby.Room, c lobby.Client, m lobby.Message) {
	switch m.Type {
	case "say":
		var d struct{ Text string }
		_ = m.Decode(&d)
		r.Broadcast("said", map[string]string{"from": c.Name, "text": d.Text})
	case "inc":
		g.Count++
		r.Broadcast("count", g.Count)
		r.Persist(ctx)
	case "boom":
		panic("game bug")
	case "lock":
		var locked bool
		_ = m.Decode(&locked)
		r.SetLocked(ctx, locked)
	case "timer":
		start := time.Now()
		r.After(30*time.Millisecond, func(_ context.Context, r *lobby.Room) {
			r.Broadcast("ding", time.Since(start).Milliseconds())
		})
		stop := r.After(10*time.Millisecond, func(_ context.Context, r *lobby.Room) { r.Broadcast("cancelled-fired", nil) })
		stop()
	}
}
func (g *echoGame) Snapshot() ([]byte, error) { return json.Marshal(g.Count) }
func (g *echoGame) Restore(b []byte) error    { return json.Unmarshal(b, &g.Count) }

type harness struct {
	t     *testing.T
	m     *lobby.Manager
	srv   *httptest.Server
	mu    sync.Mutex
	games map[string]*echoGame
}

func newHarness(t *testing.T, cfg lobby.Config) *harness {
	t.Helper()
	h := &harness{t: t, games: map[string]*echoGame{}}
	cfg.NewGame = func(code, kind string) (lobby.Game, error) {
		if kind != "" && kind != "echo" {
			return nil, lobby.ErrUnknownGame
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		g := &echoGame{}
		h.games[code] = g
		return g, nil
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	h.m = lobby.New(cfg)
	h.srv = httptest.NewServer(h.m.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) game(code string) *echoGame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.games[code]
}

func (h *harness) post(path string, body any) (int, lobby.JoinResult, string) {
	h.t.Helper()
	b, _ := json.Marshal(body)
	res, err := http.Post(h.srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(res.Body).Decode(&raw)
	var jr lobby.JoinResult
	_ = json.Unmarshal(raw, &jr)
	var e struct{ Error string }
	_ = json.Unmarshal(raw, &e)
	return res.StatusCode, jr, e.Error
}

func (h *harness) create(name string) lobby.JoinResult {
	h.t.Helper()
	status, res, msg := h.post("/rooms", lobby.JoinRequest{Name: name})
	if status != http.StatusOK {
		h.t.Fatalf("create: %d %s", status, msg)
	}
	return res
}

func (h *harness) join(code string, req lobby.JoinRequest) lobby.JoinResult {
	h.t.Helper()
	status, res, msg := h.post("/rooms/"+code+"/join", req)
	if status != http.StatusOK {
		h.t.Fatalf("join: %d %s", status, msg)
	}
	return res
}

type client struct {
	t  *testing.T
	ws *websocket.Conn
}

func (h *harness) dial(code, token string) *client {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/rooms/" + code + "/ws?token=" + token
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { ws.CloseNow() })
	return &client{t: h.t, ws: ws}
}

func (c *client) send(typ string, data any) {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"type": typ, "data": data})
	if err := c.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) read() (lobby.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, b, err := c.ws.Read(ctx)
	if err != nil {
		return lobby.Message{}, err
	}
	var m lobby.Message
	return m, json.Unmarshal(b, &m)
}

// expect reads until a message of type typ arrives, skipping others.
func (c *client) expect(typ string) lobby.Message {
	c.t.Helper()
	for {
		m, err := c.read()
		if err != nil {
			c.t.Fatalf("waiting for %q: %v", typ, err)
		}
		if m.Type == typ {
			return m
		}
	}
}

// expectState reads state messages until one satisfies ok.
func (c *client) expectState(ok func(lobby.StateData) bool) lobby.StateData {
	c.t.Helper()
	for {
		var s lobby.StateData
		if err := c.expect(lobby.MsgState).Decode(&s); err != nil {
			c.t.Fatal(err)
		}
		if ok(s) {
			return s
		}
	}
}

// expectClose reads until the connection closes and returns the close code.
func (c *client) expectClose() websocket.StatusCode {
	c.t.Helper()
	for {
		_, err := c.read()
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func TestCreateJoinAndBroadcast(t *testing.T) {
	h := newHarness(t, lobby.Config{})

	ada := h.create("Ada")
	if len(ada.Code) != 4 || ada.Token == "" || ada.Client.Role != lobby.RolePlayer {
		t.Fatalf("unexpected create result: %+v", ada)
	}

	a := h.dial(ada.Code, ada.Token)
	var w lobby.WelcomeData
	if err := a.expect(lobby.MsgWelcome).Decode(&w); err != nil || w.You.ID != ada.Client.ID {
		t.Fatalf("welcome = %+v, %v", w, err)
	}

	bob := h.join(strings.ToLower(ada.Code), lobby.JoinRequest{Name: "  Bob  "})
	if bob.Client.Name != "Bob" {
		t.Errorf("name not trimmed: %q", bob.Client.Name)
	}
	s := a.expectState(func(s lobby.StateData) bool { return len(s.Clients) == 2 })
	if s.VIP != ada.Client.ID {
		t.Errorf("VIP = %s, want Ada", s.VIP)
	}

	b := h.dial(ada.Code, bob.Token)
	b.expect(lobby.MsgWelcome)
	b.send("say", map[string]string{"text": "hello"})

	var said map[string]string
	_ = a.expect("said").Decode(&said)
	if said["from"] != "Bob" || said["text"] != "hello" {
		t.Errorf("said = %v", said)
	}
}

func TestJoinErrors(t *testing.T) {
	h := newHarness(t, lobby.Config{MaxPlayers: 2})
	room := h.create("Ada")

	tests := []struct {
		name string
		path string
		req  lobby.JoinRequest
		want int
	}{
		{"unknown room", "/rooms/ZZZZ/join", lobby.JoinRequest{Name: "X"}, http.StatusNotFound},
		{"empty name", "/rooms/" + room.Code + "/join", lobby.JoinRequest{Name: "   "}, http.StatusBadRequest},
		{"long name", "/rooms/" + room.Code + "/join", lobby.JoinRequest{Name: strings.Repeat("x", 21)}, http.StatusBadRequest},
		{"name taken, case-insensitive", "/rooms/" + room.Code + "/join", lobby.JoinRequest{Name: "ADA"}, http.StatusConflict},
		{"second player ok", "/rooms/" + room.Code + "/join", lobby.JoinRequest{Name: "Bob"}, http.StatusOK},
		{"room full", "/rooms/" + room.Code + "/join", lobby.JoinRequest{Name: "Cy"}, http.StatusConflict},
		{"display doesn't count", "/rooms/" + room.Code + "/join", lobby.JoinRequest{Role: lobby.RoleDisplay}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _, msg := h.post(tt.path, tt.req); got != tt.want {
				t.Errorf("status = %d (%s), want %d", got, msg, tt.want)
			}
		})
	}
}

func TestWebsocketRejections(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	room := h.create("Ada")

	if got := h.dial("ZZZZ", room.Token).expectClose(); got != lobby.StatusRoomNotFound {
		t.Errorf("unknown room close = %d", got)
	}
	if got := h.dial(room.Code, "nope").expectClose(); got != lobby.StatusUnauthorized {
		t.Errorf("bad token close = %d", got)
	}
}

func TestSecondConnectionReplacesFirst(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	room := h.create("Ada")

	first := h.dial(room.Code, room.Token)
	first.expect(lobby.MsgWelcome)
	second := h.dial(room.Code, room.Token)
	second.expect(lobby.MsgWelcome)

	if got := first.expectClose(); got != lobby.StatusReplaced {
		t.Errorf("first close = %d, want %d", got, lobby.StatusReplaced)
	}
	// The replaced socket closing must not mark Ada disconnected or stop
	// the new socket from being served.
	second.send("say", map[string]string{"text": "still here"})
	second.expect("said")
	rm, _ := h.m.Room(room.Code)
	connected := make(chan bool, 1)
	_ = rm.Do(context.Background(), func(_ context.Context, r *lobby.Room) { c, _ := r.Client(r.VIP()); connected <- c.Connected })
	if !<-connected {
		t.Error("Ada marked disconnected after replaced socket closed")
	}
}

func TestKickAndVIPTransfer(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ada := h.create("Ada")
	bob := h.join(ada.Code, lobby.JoinRequest{Name: "Bob"})
	cy := h.join(ada.Code, lobby.JoinRequest{Name: "Cy"})

	a, b, c := h.dial(ada.Code, ada.Token), h.dial(ada.Code, bob.Token), h.dial(ada.Code, cy.Token)
	for _, cl := range []*client{a, b, c} {
		cl.expect(lobby.MsgWelcome)
	}

	b.send(lobby.MsgKick, map[string]string{"id": cy.Client.ID})
	b.expect(lobby.MsgError) // Bob isn't VIP

	a.send(lobby.MsgKick, map[string]string{"id": cy.Client.ID})
	if got := c.expectClose(); got != lobby.StatusKicked {
		t.Errorf("kicked close = %d", got)
	}

	a.send(lobby.MsgLeave, nil)
	if got := a.expectClose(); got != websocket.StatusNormalClosure {
		t.Errorf("leave close = %d", got)
	}
	s := b.expectState(func(s lobby.StateData) bool { return len(s.Clients) == 1 })
	if s.VIP != bob.Client.ID {
		t.Errorf("VIP after Ada left = %q, want Bob", s.VIP)
	}

	g := h.game(ada.Code)
	want := []string{"join:Ada", "join:Bob", "join:Cy", "leave:Cy", "leave:Ada"}
	if got := g.Events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestGracePeriodAndIdleClose(t *testing.T) {
	h := newHarness(t, lobby.Config{
		ReconnectGrace: 50 * time.Millisecond,
		IdleTimeout:    150 * time.Millisecond,
		SweepInterval:  10 * time.Millisecond,
	})
	ada := h.create("Ada")
	room, _ := h.m.Room(ada.Code)

	a := h.dial(ada.Code, ada.Token)
	a.expect(lobby.MsgWelcome)
	a.ws.CloseNow() // drop without leaving

	select {
	case <-room.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("room never closed")
	}
	if _, ok := h.m.Room(ada.Code); ok {
		t.Error("closed room still registered")
	}
	g := h.game(ada.Code)
	if got := g.Events(); len(got) != 2 || got[1] != "leave:Ada" {
		t.Errorf("events = %v", got)
	}
	g.mu.Lock()
	roomCtx := g.roomCtx
	g.mu.Unlock()
	select {
	case <-roomCtx.Done():
	default:
		t.Error("room context passed to callbacks was not cancelled when the room closed")
	}
}

func TestGamePanicDoesNotKillRoom(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ada := h.create("Ada")
	a := h.dial(ada.Code, ada.Token)
	a.expect(lobby.MsgWelcome)

	a.send("boom", nil)
	a.send("say", map[string]string{"text": "after"})
	a.expect("said")
}

func TestPersistAndRestore(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, t.TempDir()+"/lobby.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	h1 := newHarness(t, lobby.Config{Store: store})
	ada := h1.create("Ada")
	a := h1.dial(ada.Code, ada.Token)
	a.expect(lobby.MsgWelcome)
	for range 3 {
		a.send("inc", nil)
	}
	for string(a.expect("count").Data) != "3" {
	}

	closed := make(chan websocket.StatusCode, 1)
	go func() { closed <- a.expectClose() }() // a real client answers the close handshake promptly
	if err := h1.m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-closed; got != websocket.StatusGoingAway {
		t.Errorf("shutdown close = %d", got)
	}

	// "Restart": a new manager over the same database.
	h2 := newHarness(t, lobby.Config{Store: store})
	if err := h2.m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	a2 := h2.dial(ada.Code, ada.Token)
	var w lobby.WelcomeData
	_ = a2.expect(lobby.MsgWelcome).Decode(&w)
	if w.You.ID != ada.Client.ID {
		t.Errorf("restored identity = %+v", w.You)
	}
	if got := string(a2.expect("count").Data); got != "3" {
		t.Errorf("restored count = %s, want 3", got)
	}

	// Closing a room deletes it from the store.
	room, _ := h2.m.Room(ada.Code)
	_ = room.Do(ctx, func(rctx context.Context, r *lobby.Room) { r.Close(rctx) })
	<-room.Done()
	recs, err := store.LoadRooms(ctx)
	if err != nil || len(recs) != 0 {
		t.Errorf("rooms after close = %d, %v", len(recs), err)
	}
}

func TestManyRoomsConcurrently(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Go(func() {
			r, err := h.m.CreateAndJoin(context.Background(), "", lobby.JoinRequest{Name: "p" + strconv.Itoa(i)})
			if err != nil {
				errs <- err
				return
			}
			if _, err := h.m.Join(context.Background(), r.Code, lobby.JoinRequest{Name: "other"}); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := h.m.Rooms(); got != 50 {
		t.Errorf("rooms = %d, want 50", got)
	}
}

func TestLockedRoom(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ada := h.create("Ada")
	bob := h.join(ada.Code, lobby.JoinRequest{Name: "Bob"})
	a := h.dial(ada.Code, ada.Token)
	a.expect(lobby.MsgWelcome)

	a.send("lock", true)
	if s := a.expectState(func(s lobby.StateData) bool { return s.Locked }); !s.Locked {
		t.Fatal("state never showed locked")
	}

	if status, _, msg := h.post("/rooms/"+ada.Code+"/join", lobby.JoinRequest{Name: "Cy"}); status != http.StatusConflict {
		t.Errorf("join while locked = %d %s, want 409", status, msg)
	}
	if status, _, _ := h.post("/rooms/"+ada.Code+"/join", lobby.JoinRequest{Role: lobby.RoleDisplay}); status != http.StatusOK {
		t.Errorf("display join while locked = %d, want 200", status)
	}
	// Existing members can still (re)connect.
	h.dial(ada.Code, bob.Token).expect(lobby.MsgWelcome)

	// The built-in lobby.lock is VIP-only.
	bc := h.dial(ada.Code, bob.Token)
	bc.expect(lobby.MsgWelcome)
	bc.send(lobby.MsgLock, map[string]bool{"locked": false})
	bc.expect(lobby.MsgError)

	a.send(lobby.MsgLock, map[string]bool{"locked": false})
	a.expectState(func(s lobby.StateData) bool { return !s.Locked })
	h.join(ada.Code, lobby.JoinRequest{Name: "Cy"})
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t, lobby.Config{RateLimit: 3})
	ada := h.create("Ada") // 1
	for i := range 2 {     // 2, 3
		h.join(ada.Code, lobby.JoinRequest{Name: "p" + strconv.Itoa(i)})
	}
	status, _, _ := h.post("/rooms/ZZZZ/join", lobby.JoinRequest{Name: "guess"})
	if status != http.StatusTooManyRequests {
		t.Errorf("4th request = %d, want 429", status)
	}
	// The manager API itself isn't rate limited; only HTTP is.
	if _, err := h.m.Join(context.Background(), ada.Code, lobby.JoinRequest{Name: "direct"}); err != nil {
		t.Errorf("direct join: %v", err)
	}
}

func TestAfterTimer(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ada := h.create("Ada")
	a := h.dial(ada.Code, ada.Token)
	a.expect(lobby.MsgWelcome)

	a.send("timer", nil)
	for {
		m, err := a.read()
		if err != nil {
			t.Fatal(err)
		}
		switch m.Type {
		case "cancelled-fired":
			t.Fatal("stopped timer fired")
		case "ding":
			var ms int
			_ = m.Decode(&ms)
			if ms < 30 {
				t.Errorf("timer fired after %dms, want >= 30", ms)
			}
			return
		}
	}
}

func TestGameKinds(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	status, res, _ := h.post("/rooms", map[string]string{"name": "Ada", "game": "echo"})
	if status != http.StatusOK {
		t.Fatalf("create echo = %d", status)
	}
	if room, _ := h.m.Room(res.Code); room.Kind() != "echo" {
		t.Errorf("kind = %q", room.Kind())
	}
	var w lobby.WelcomeData
	_ = h.dial(res.Code, res.Token).expect(lobby.MsgWelcome).Decode(&w)
	if w.Game != "echo" {
		t.Errorf("welcome game = %q", w.Game)
	}
	if status, _, _ := h.post("/rooms", map[string]string{"name": "Ada", "game": "chess"}); status != http.StatusBadRequest {
		t.Errorf("unknown game = %d, want 400", status)
	}
}

// nextEvent waits for an event of the given type on a LocalConn.
func nextEvent(t *testing.T, lc *lobby.LocalConn, typ string) lobby.Event {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-lc.Events():
			if !ok {
				code, reason := lc.CloseStatus()
				t.Fatalf("waiting for %q: closed (%d %s)", typ, code, reason)
			}
			if ev.Type == typ {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %q", typ)
		}
	}
}

func TestLocalConn(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ctx := context.Background()

	ada, err := h.m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	local, err := h.m.Connect(ctx, ada.Code, ada.Token)
	if err != nil {
		t.Fatal(err)
	}

	// Lobby events arrive as typed Go values, no JSON involved.
	welcome, ok := nextEvent(t, local, lobby.MsgWelcome).Data.(lobby.WelcomeData)
	if !ok || welcome.You.ID != ada.Client.ID {
		t.Fatalf("welcome = %#v", welcome)
	}
	if count := nextEvent(t, local, "count").Data; count != 0 {
		t.Errorf("count data = %#v, want int 0", count)
	}

	// A websocket client in the same room sees the local client's messages,
	// and vice versa.
	bob := h.join(ada.Code, lobby.JoinRequest{Name: "Bob"})
	b := h.dial(ada.Code, bob.Token)
	b.expect(lobby.MsgWelcome)

	if err := local.Send(ctx, "say", struct{ Text string }{"from a channel"}); err != nil {
		t.Fatal(err)
	}
	var said map[string]string
	_ = b.expect("said").Decode(&said)
	if said["from"] != "Ada" || said["text"] != "from a channel" {
		t.Errorf("websocket saw %v", said)
	}

	b.send("say", map[string]string{"text": "from a socket"})
	for {
		// The local client also receives its own earlier broadcast; skip it.
		if got := nextEvent(t, local, "said").Data.(map[string]string); got["from"] == "Bob" {
			if got["text"] != "from a socket" {
				t.Errorf("local saw %v", got)
			}
			break
		}
	}

	// Close keeps the seat (like a dropped socket); reconnecting works.
	local.Close()
	for range local.Events() {
	}
	if code, _ := local.CloseStatus(); code != lobby.StatusNormalClosure {
		t.Errorf("close status = %d", code)
	}
	again, err := h.m.Connect(ctx, ada.Code, ada.Token)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	nextEvent(t, again, lobby.MsgWelcome)

	// Kicked: the channel closes with the kick status.
	bob2, _ := h.m.Join(ctx, ada.Code, lobby.JoinRequest{Name: "Cy"})
	cy, _ := h.m.Connect(ctx, ada.Code, bob2.Token)
	if err := again.Send(ctx, lobby.MsgKick, map[string]string{"id": cy.Client().ID}); err != nil {
		t.Fatal(err)
	}
	for range cy.Events() {
	}
	if code, _ := cy.CloseStatus(); code != lobby.StatusKicked {
		t.Errorf("kicked status = %d", code)
	}

	if _, err := h.m.Connect(ctx, ada.Code, "bogus"); !errors.Is(err, lobby.ErrUnauthorized) {
		t.Errorf("bad token err = %v", err)
	}
}

func TestLocalSlowConsumerDropped(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ctx := context.Background()
	ada, _ := h.m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "Ada"})
	bob, _ := h.m.Join(ctx, ada.Code, lobby.JoinRequest{Name: "Bob"})
	fast, _ := h.m.Connect(ctx, ada.Code, ada.Token)
	slow, _ := h.m.Connect(ctx, ada.Code, bob.Token) // never read

	go func() {
		for range lobby.LocalBuffer + 10 {
			_ = fast.Send(ctx, "say", map[string]string{"text": "spam"})
		}
	}()
	timeout := time.After(5 * time.Second)
	bobWasConnected := false
	for {
		select {
		case ev, ok := <-fast.Events():
			if !ok {
				t.Fatal("fast client closed")
			}
			if ev.Type != lobby.MsgState {
				continue
			}
			for _, c := range ev.Data.(lobby.StateData).Clients {
				if c.Name == "Bob" && c.Connected {
					bobWasConnected = true
				}
				if c.Name == "Bob" && !c.Connected && bobWasConnected {
					if code, _ := slow.CloseStatus(); code != lobby.StatusPolicyViolation {
						t.Errorf("slow status = %d", code)
					}
					return
				}
			}
		case <-timeout:
			t.Fatal("slow client never dropped")
		}
	}
}

func TestRoomClosesWhenLastMemberLeaves(t *testing.T) {
	h := newHarness(t, lobby.Config{})
	ctx := t.Context()
	res, err := h.m.CreateAndJoin(ctx, "", lobby.JoinRequest{Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	lc, err := h.m.Connect(ctx, res.Code, res.Token)
	if err != nil {
		t.Fatal(err)
	}
	room, _ := h.m.Room(res.Code)
	if err := lc.Leave(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-room.Done():
	case <-time.After(time.Second):
		t.Fatal("empty room still open; it should close without waiting for IdleTimeout")
	}
	if _, ok := h.m.Room(res.Code); ok {
		t.Error("closed room still registered")
	}
}
