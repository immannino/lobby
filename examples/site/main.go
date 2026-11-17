// Command site is the lobby website: a landing page, docs, and a live demo
// hosting the example games (create a room on a "big screen", join from
// phones with the code, and play). It runs on the library it documents.
//
//	go run ./examples/site -addr :1234 -db lobby.db
//
// Every flag can also be set from the environment:
// PORT, LOBBY_DB, LOBBY_PROXY_HEADER (e.g. X-Real-IP) and LOBBY_SECURE_COOKIES.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/immannino/lobby"
	"github.com/immannino/lobby/examples/games/botkit"
	"github.com/immannino/lobby/examples/games/chat"
	"github.com/immannino/lobby/examples/games/connect4"
	"github.com/immannino/lobby/examples/games/duel"
	"github.com/immannino/lobby/examples/games/hangman"
	"github.com/immannino/lobby/examples/games/poll"
	"github.com/immannino/lobby/examples/games/tictactoe"
	"github.com/immannino/lobby/sqlitestore"
)

//go:embed web
var webFS embed.FS

// options are the server's settings, from flags or the environment.
type options struct {
	addr          string
	dbPath        string
	proxyHeader   string // header holding the client IP, set by a trusted proxy
	secureCookies bool
}

func main() {
	defaultAddr := ":1234"
	if port := os.Getenv("PORT"); port != "" { // set by most hosting platforms
		defaultAddr = ":" + port
	}
	var o options
	flag.StringVar(&o.addr, "addr", defaultAddr, "listen address (default from $PORT)")
	flag.StringVar(&o.dbPath, "db", envOr("LOBBY_DB", "lobby.db"), `SQLite file for persisting rooms; "" keeps everything in memory ($LOBBY_DB)`)
	flag.StringVar(&o.proxyHeader, "proxy-header", os.Getenv("LOBBY_PROXY_HEADER"), "trusted header with the client IP for rate limiting, e.g. X-Real-IP ($LOBBY_PROXY_HEADER); only set this behind a proxy that overwrites it")
	flag.BoolVar(&o.secureCookies, "secure-cookies", os.Getenv("LOBBY_SECURE_COOKIES") == "true", "mark seat cookies Secure; enable when served over HTTPS ($LOBBY_SECURE_COOKIES)")
	flag.Parse()

	var log *slog.Logger
	if os.Getenv("LOBBY_LOG_JSON") == "true" {
		log = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	} else {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if err := run(o, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// gameDef registers one game with the demo.
type gameDef struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Blurb string `json:"blurb"`
	new   func(code string) lobby.Game
	js    []byte
}

func games(ctx context.Context, m func() *lobby.Manager, log *slog.Logger) []gameDef {
	// bot returns an AddBot hook that runs brain as a computer player for
	// as long as the server (ctx) lives.
	bot := func(brain botkit.Brain) func(code string) {
		return func(code string) {
			go func() {
				opts := botkit.Options{Hello: "bot.hello", Think: 700 * time.Millisecond, Logger: log}
				if err := botkit.Run(ctx, m(), code, brain, opts); err != nil && !errors.Is(err, context.Canceled) {
					log.Warn("bot stopped", "code", code, "err", err)
				}
			}()
		}
	}
	return []gameDef{
		{Name: tictactoe.Name, Title: "Tic-tac-toe", Blurb: "Winner stays on. Play the computer if you're alone.",
			new: func(string) lobby.Game { return tictactoe.New(bot(tictactoe.Brain)) }, js: tictactoe.ClientJS},
		{Name: connect4.Name, Title: "Connect 4", Blurb: "Drop discs, get four in a row. Winner stays on.",
			new: func(string) lobby.Game { return connect4.New(bot(connect4.Brain)) }, js: connect4.ClientJS},
		{Name: hangman.Name, Title: "Hangman", Blurb: "Take turns setting a secret word for everyone else.",
			new: func(string) lobby.Game { return hangman.New(hangman.Options{}) }, js: hangman.ClientJS},
		{Name: poll.Name, Title: "Quick poll", Blurb: "Ask the room a question. Reveal the results, or watch them live.",
			new: func(string) lobby.Game { return poll.New() }, js: poll.ClientJS},
		{Name: chat.Name, Title: "Chat & counter", Blurb: "The simplest possible game.",
			new: func(string) lobby.Game { return chat.New() }, js: chat.ClientJS},
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func run(o options, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var m *lobby.Manager // games start bots, which need the manager
	defs := games(ctx, func() *lobby.Manager { return m }, log)
	byName := map[string]gameDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}

	cfg := lobby.Config{
		Logger:        log,
		SecureCookies: o.secureCookies,
		NewGame: func(code, kind string) (lobby.Game, error) {
			if kind == "" {
				kind = defs[0].Name
			}
			d, ok := byName[kind]
			if !ok {
				return nil, fmt.Errorf("%w: %q", lobby.ErrUnknownGame, kind)
			}
			return d.new(code), nil
		},
	}
	if o.proxyHeader != "" {
		cfg.ClientIP = lobby.HeaderClientIP(o.proxyHeader)
	}
	if o.dbPath != "" {
		if err := os.MkdirAll(filepath.Dir(o.dbPath), 0o755); err != nil {
			return err
		}
		store, err := sqlitestore.Open(ctx, o.dbPath)
		if err != nil {
			return err
		}
		defer store.Close()
		if _, err := store.Prune(ctx, time.Now().Add(-time.Hour)); err != nil {
			return err
		}
		cfg.Store = store
	}

	m = lobby.New(cfg)
	if err := m.Restore(ctx); err != nil {
		return err
	}

	web, _ := fs.Sub(webFS, "web")
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, web, name)
		}
	}
	js := func(b []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(b)
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", m.Handler()))
	mux.HandleFunc("GET /api/games", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(defs)
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /static/games/duel.js", js(duel.ClientJS))
	mux.HandleFunc("GET /static/games/{file}", func(w http.ResponseWriter, r *http.Request) {
		name, ok := cutSuffix(r.PathValue("file"), ".js")
		d, known := byName[name]
		if !ok || !known {
			http.NotFound(w, r)
			return
		}
		js(d.js)(w, r)
	})
	mux.HandleFunc("GET /{$}", page("home.html"))
	mux.HandleFunc("GET /play", page("play.html"))
	mux.HandleFunc("GET /docs", page("docs.html"))
	mux.HandleFunc("GET /r/{code}", page("room.html"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(m.Stats())
	})

	srv := &http.Server{
		Addr:              o.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: it would kill websocket connections.
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", o.addr, "db", o.dbPath, "proxyHeader", o.proxyHeader)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Lobby first: it persists rooms and tells clients to reconnect, and its
	// websockets are hijacked connections http.Server.Shutdown doesn't track.
	err := m.Shutdown(shutdownCtx)
	if serr := srv.Shutdown(shutdownCtx); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		err = errors.Join(err, serr)
	}
	return err
}

func cutSuffix(s, suffix string) (string, bool) {
	if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)], true
	}
	return s, false
}
