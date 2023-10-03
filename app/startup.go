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
)

type JoinRequest struct {
	Code string `json:"Code"`
	Name string `json:"Name"`
}

var (
	handlers = map[string]func(http.ResponseWriter, *http.Request){
		"home": func(w http.ResponseWriter, r *http.Request) { html.HTML(w, 200, "index", nil) },
		"create": func(w http.ResponseWriter, r *http.Request) {
			s := CreateSession()
			sessions[s.Code] = s
			json.NewEncoder(w).Encode(s)
		},
		"join": func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)

			code := vars["Code"]
			name := r.URL.Query().Get("Name")

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

			if _, ok := sessions[code]; !ok {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("session not found"))
				return
			}

			s := sessions[code]

			if _, ok := s.Players[name]; ok {
				w.WriteHeader(http.StatusNotAcceptable)
				w.Write([]byte("username already in use"))
				return
			}

			p := NewPlayer(name)
			s.AddPlayer(p)

			http.Redirect(w, r, "/"+code+"/lobby", http.StatusPermanentRedirect)
		},
		"sessions": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(sessions)
		},
	}
)

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
	router.HandleFunc("/{Code}/join", handlers["join"]).Methods("GET").Queries("Name", "{name}")
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
