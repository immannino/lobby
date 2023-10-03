package app

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_Integration(t *testing.T) {
	url := "http://localhost:1234"
	c := http.Client{}

	// Create a session
	res, err := c.Post(url+"/create", "application/json", nil)
	assert.NoError(t, err)

	var s *GameSession
	err = json.NewDecoder(res.Body).Decode(&s)
	assert.NoError(t, err)

	var sessions map[string]*GameSession
	res, err = c.Get(url + "/sessions")
	assert.NoError(t, err)

	err = json.NewDecoder(res.Body).Decode(&sessions)
	assert.NoError(t, err)
	assert.Contains(t, sessions, s.Code)

	name := "Test"

	res, err = c.Get(url + "/" + s.Code + "/join?Name=" + name)
	assert.NoError(t, err)

	res, err = c.Get(url + "/sessions")
	assert.NoError(t, err)
	err = json.NewDecoder(res.Body).Decode(&sessions)
	assert.NoError(t, err)
	PrettyJson(sessions)
	assert.Contains(t, sessions[s.Code].Players, name)

	res, err = c.Get(url + "/" + s.Code + "/join?Name=" + name)
	assert.ErrorContains(t, err, "foo")
}
