package lobby

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// StatusCode is a websocket close code. Clients can use these to decide
// whether to reconnect.
type StatusCode = websocket.StatusCode

// Close codes sent by the server. 4xxx codes are terminal: a client should
// not reconnect automatically after receiving one.
const (
	StatusNormalClosure   = websocket.StatusNormalClosure   // 1000: left the room
	StatusGoingAway       = websocket.StatusGoingAway       // 1001: server restarting; reconnect
	StatusPolicyViolation = websocket.StatusPolicyViolation // 1008: too slow or too chatty; reconnect

	StatusUnauthorized StatusCode = 4401 // bad or missing token
	StatusKicked       StatusCode = 4403
	StatusRoomNotFound StatusCode = 4404
	StatusReplaced     StatusCode = 4409 // same client connected elsewhere
	StatusRoomClosed   StatusCode = 4410
)

const (
	sendBuffer   = 64
	writeTimeout = 10 * time.Second
)

//go:embed client.js
var clientJS []byte

// Handler returns the lobby's HTTP API. Mount it under a prefix, e.g.
//
//	mux.Handle("/api/", http.StripPrefix("/api", m.Handler()))
//
// Routes:
//
//	POST /rooms               {"name","role"} → create + join, returns JoinResult
//	GET  /rooms/{code}        public room info
//	POST /rooms/{code}/join   {"name","role"} → JoinResult
//	GET  /rooms/{code}/ws     websocket; token from cookie, ?token= or Bearer header
//	GET  /client.js           browser client (ES module)
//
// Join and create also set an HttpOnly cookie holding the token, so browser
// clients don't need to handle it.
//
// Don't set http.Server.WriteTimeout on a server hosting this handler: it
// kills long-lived websocket connections. Use ReadHeaderTimeout instead.
func (m *Manager) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rooms", m.handleCreate)
	mux.HandleFunc("GET /rooms/{code}", m.handleInfo)
	mux.HandleFunc("POST /rooms/{code}/join", m.handleJoin)
	mux.HandleFunc("GET /rooms/{code}/ws", m.handleWS)
	mux.HandleFunc("GET /client.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(clientJS)
	})
	return mux
}

func (m *Manager) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !m.allow(w, r) {
		return
	}
	req, err := decodeJoin(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := m.CreateAndJoin(r.Context(), req.Game, req.JoinRequest)
	m.writeJoin(w, res, err)
}

func (m *Manager) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !m.allow(w, r) {
		return
	}
	req, err := decodeJoin(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := m.Join(r.Context(), r.PathValue("code"), req.JoinRequest)
	m.writeJoin(w, res, err)
}

func (m *Manager) handleInfo(w http.ResponseWriter, r *http.Request) {
	room, ok := m.Room(r.PathValue("code"))
	if !ok {
		writeError(w, http.StatusNotFound, "room not found")
		return
	}
	type info struct {
		Code       string `json:"code"`
		Game       string `json:"game"`
		Locked     bool   `json:"locked"`
		Players    int    `json:"players"`
		MaxPlayers int    `json:"maxPlayers"`
	}
	var out info
	err := room.call(r.Context(), func(_ context.Context, rm *Room) {
		out = info{Code: rm.code, Game: rm.kind, Locked: rm.locked, Players: len(rm.Players()), MaxPlayers: m.cfg.MaxPlayers}
	})
	if err != nil {
		writeError(w, statusFor(err), message(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Manager) writeJoin(w http.ResponseWriter, res JoinResult, err error) {
	if err != nil {
		writeError(w, statusFor(err), message(err))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(res.Code),
		Value:    res.Token,
		Path:     "/",
		MaxAge:   int((24 * time.Hour).Seconds()),
		HttpOnly: true,
		Secure:   m.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, res)
}

func (m *Manager) handleWS(w http.ResponseWriter, r *http.Request) {
	code := NormalizeCode(r.PathValue("code"))
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: m.cfg.OriginPatterns})
	if err != nil {
		return // Accept already wrote an HTTP error
	}
	ws.SetReadLimit(m.cfg.MaxMessageBytes)

	// Accept first, then reject with a close code: browsers can't see the
	// HTTP status of a failed upgrade, but they can see close codes.
	room, ok := m.Room(code)
	if !ok {
		ws.Close(StatusRoomNotFound, "room not found")
		return
	}
	token := tokenFrom(r, code)
	if token == "" {
		ws.Close(StatusUnauthorized, "missing token")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &wsConn{ws: ws, send: make(chan []byte, sendBuffer), closing: &m.closing, ping: m.cfg.PingInterval}

	var (
		client    Client
		attachErr error
	)
	if err := room.call(ctx, func(rctx context.Context, rm *Room) { client, attachErr = rm.attach(rctx, token, c) }); err != nil {
		ws.Close(StatusRoomClosed, "room closed")
		return
	}
	if attachErr != nil {
		ws.Close(StatusUnauthorized, "not a member of this room")
		return
	}

	m.stats.webSockets.Add(1)
	defer m.stats.webSockets.Add(-1)
	go c.writeLoop(ctx)
	defer func() {
		// The request context is already done here; detaching must still happen.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = room.Do(dctx, func(rctx context.Context, rm *Room) { rm.detach(rctx, client.ID, c) })
	}()

	var (
		bucket  *tokenBucket
		limited bool
	)
	if m.cfg.MessageRate > 0 {
		bucket = newTokenBucket(m.cfg.MessageRate, m.cfg.MessageBurst, time.Now())
	}
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if limited {
			continue // closing; drain until the close handshake ends the read
		}
		if bucket != nil && !bucket.take(time.Now()) {
			limited = true
			m.stats.rateLimited.Add(1)
			m.log.Warn("closing chatty connection", "code", code, "id", client.ID)
			c.close(StatusPolicyViolation, "too many messages")
			continue
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil || msg.Type == "" {
			continue // ignore junk rather than punishing the client
		}
		m.stats.messagesIn.Add(1)
		if err := room.Do(ctx, func(rctx context.Context, rm *Room) { rm.handle(rctx, client.ID, c, msg) }); err != nil {
			return
		}
	}
}

// conn is how a room reaches a connected member. deliver and close are only
// called on the room goroutine. Implementations: wsConn and LocalConn.
type conn interface {
	// deliver queues a message without blocking; false means the client is
	// too slow and will be dropped. encoded lazily returns the JSON form.
	deliver(typ string, data any, encoded func() ([]byte, error)) bool
	close(code StatusCode, reason string)
}

// wsConn is one websocket attached to a member.
type wsConn struct {
	ws        *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
	closing   *sync.WaitGroup // lets Manager.Shutdown wait for close handshakes
	ping      time.Duration
}

func (c *wsConn) deliver(_ string, _ any, encoded func() ([]byte, error)) bool {
	b, err := encoded()
	if err != nil {
		return true // already logged; nothing to send
	}
	select {
	case c.send <- b:
		return true
	default:
		return false
	}
}

// close sends a close frame asynchronously (the close handshake can take
// seconds and must not block the room).
func (c *wsConn) close(code StatusCode, reason string) {
	c.closeOnce.Do(func() { c.closing.Go(func() { c.ws.Close(code, reason) }) })
}

func (c *wsConn) writeLoop(ctx context.Context) {
	ping := time.NewTicker(c.ping)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.close(StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func cookieName(code string) string { return "lobby_" + code }

func tokenFrom(r *http.Request, code string) string {
	if ck, err := r.Cookie(cookieName(code)); err == nil && ck.Value != "" {
		return ck.Value
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if h, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return h
	}
	return ""
}

// joinBody is the create/join request body. Game is only used on create.
type joinBody struct {
	JoinRequest
	Game string `json:"game"`
}

// decodeJoin accepts JSON or form-encoded bodies (so plain forms and htmx work).
func decodeJoin(r *http.Request) (joinBody, error) {
	var body joinBody
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body)
		if err != nil && !errors.Is(err, io.EOF) { // empty body is fine for displays
			return body, errors.New("invalid JSON body")
		}
		return body, nil
	}
	if err := r.ParseForm(); err != nil {
		return body, errors.New("invalid form body")
	}
	body.Name = r.Form.Get("name")
	body.Game = r.Form.Get("game")
	if err := body.Role.UnmarshalText([]byte(r.Form.Get("role"))); err != nil {
		return body, err
	}
	return body, nil
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrRoomNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrRoomFull), errors.Is(err, ErrNameTaken), errors.Is(err, ErrRoomLocked):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidName), errors.Is(err, ErrUnknownGame):
		return http.StatusBadRequest
	case errors.Is(err, ErrRoomClosed):
		return http.StatusGone
	case errors.Is(err, ErrRateLimited):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrNoCodes):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func message(err error) string {
	if statusFor(err) == http.StatusInternalServerError {
		return "internal error"
	}
	return strings.TrimPrefix(err.Error(), "lobby: ")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
