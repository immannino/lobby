package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	html "chunky.ai/html"

	"github.com/gorilla/mux"
	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
)

var store = sessions.NewCookieStore([]byte("chunky.ai"))
var hashKey = []byte("very-secret")
var cookieKey = "chunky.ai"

// Block keys should be 16 bytes (AES-128) or 32 bytes (AES-256) long.
// Shorter keys may weaken the encryption used.
var blockKey = []byte("a-lot-secret")
var s = securecookie.New(hashKey, blockKey)

type JoinRequest struct {
	Code string `json:"Code"`
	Name string `json:"Name"`
}

var (
	handlers = map[string]func(http.ResponseWriter, *http.Request){
		"home": func(w http.ResponseWriter, r *http.Request) { html.HTML(w, 200, "index", nil) },
		"create": func(w http.ResponseWriter, r *http.Request) {
			name := r.URL.Query().Get("name")
			if len(name) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("no name provided"))
				return
			}

			s := CreateSession()
			p := NewPlayer(name)
			s.AddPlayer(p)

			Sessions[s.Code] = s

			cookieEncoder(s.Code, p.Name, w)

			json.NewEncoder(w).Encode(s)
		},
		"join": func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)

			code := vars["Code"]
			name := r.URL.Query().Get("name")

			fmt.Println("Code", code, "Name", name)

			if len(code) != 4 {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("not a valid code"))
				return
			}

			if len(name) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("No name provided"))
				return
			}

			if _, ok := Sessions[code]; !ok {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("session not found"))
				return
			}

			s := Sessions[code]

			if _, ok := s.Players[name]; ok {
				fmt.Println("ok", ok, s.Players[name])
				w.WriteHeader(http.StatusNotAcceptable)
				w.Write([]byte("username already in use"))
				return
			}

			p := NewPlayer(name)
			s.AddPlayer(p)

			cookieEncoder(s.Code, p.Name, w)

			http.Redirect(w, r, "/"+code+"/lobby", http.StatusPermanentRedirect)
		},
		"cookie": func(w http.ResponseWriter, r *http.Request) {
			cookieParser(r)
			w.WriteHeader(http.StatusTeapot)
			w.Write([]byte(""))
		},
		"sessions": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(Sessions)
		},
		"lobby": func(w http.ResponseWriter, r *http.Request) {

		},
	}
)

func cookieEncoder(code, name string, w http.ResponseWriter) {
	value := map[string]string{
		"code": code,
		"name": name,
	}

	if encoded, err := s.Encode(cookieKey, value); err == nil {
		cookie := &http.Cookie{
			Name:     cookieKey,
			Value:    encoded,
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
		}
		http.SetCookie(w, cookie)
	}
}

func cookieParser(r *http.Request) (string, string, error) {
	if cookie, err := r.Cookie(cookieKey); err == nil {
		value := make(map[string]string)
		if err = s.Decode(cookieKey, cookie.Value, &value); err == nil {
			PrettyJson(cookieKey)
			return value["code"], value["name"], nil
		}
	}

	return "", "", nil
}

func Start() {
	router := mux.NewRouter()

	fs := http.FileServer(http.Dir(filepath.Join("./static")))

	router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", fs))
	router.HandleFunc("/close", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			html.HTML(w, 200, "close", nil)
		},
	)).Methods("GET")

	router.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ping"))
	})
	router.HandleFunc("/", handlers["home"]).Methods("GET")
	router.HandleFunc("/create", handlers["create"]).Methods("POST")
	router.HandleFunc("/cookie", handlers["cookie"]).Methods("GET")
	router.HandleFunc("/{Code}/join", handlers["join"]).Methods("GET").Queries("name", "{name}")
	router.HandleFunc("/{Code}/leave", handlers["home"]).Methods("GET")
	router.HandleFunc("/{Code}/lobby", handlers["home"]).Methods("GET")
	router.HandleFunc("/{Code}/game", handlers["home"]).Methods("GET")
	router.HandleFunc("/{Code}/end", handlers["home"]).Methods("GET")
	router.HandleFunc("/sessions", handlers["sessions"]).Methods("GET")
	router.HandleFunc("/{Path}", handlers["home"]).Methods("GET")

	srv := &http.Server{
		Addr: "0.0.0.0:1234",

		// Slowloris is no friend of mine
		WriteTimeout: time.Second * 15,
		ReadTimeout:  time.Second * 15,
		IdleTimeout:  time.Second * 60,

		Handler: router,
	}

	fmt.Println("Server started on 0.0.0.0:1234")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %s\n", err)
	}
}

// PrettyJson pretty prints interfaces
func PrettyJson(v interface{}) {
	b, err := json.MarshalIndent(v, "", "\t")
	if err != nil {
		fmt.Println("error:", err)
	}
	fmt.Println(string(b))
}
