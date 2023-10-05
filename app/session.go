package app

import (
	"math/rand"
	"time"

	"github.com/google/uuid"
)

var (
	Sessions = map[string]*GameSession{}
)

type GameSession struct {
	Code    string            `json:"Code"`
	Players map[string]Player `json:"Players"`
	Created time.Time         `json:"Created"`
	Updated time.Time         `json:"Updated"`
}

func (g *GameSession) AddPlayer(p Player) {
	g.Players[p.Name] = p
	g.Updated = time.Now()
}

func (g *GameSession) RemovePlayer(id string) {
	delete(g.Players, id)
	g.Updated = time.Now()
}

type Player struct {
	SessionID string `json:"SessionID"`
	Name      string `json:"Name"`
}

func NewPlayer(name string) Player {
	return Player{
		Name:      name,
		SessionID: uuid.NewString(),
	}
}
func CreateSession() *GameSession {
	s := &GameSession{
		Code:    newCode(),
		Players: make(map[string]Player),
		Created: time.Now(),
		Updated: time.Now(),
	}

	Sessions[s.Code] = s

	return s
}

func CloseSession(code string) {
	delete(Sessions, code)
}

func newCode() (code string) {
	chars := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

	for i := 0; i < 4; i++ {
		code = code + string(chars[r(len(chars))])
	}

	return
}

func r(max int) int {
	return rand.Intn(max)
}
