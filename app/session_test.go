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

	name := "Test"

	// Create a session
	res, err := c.Post(url+"/create?name="+name, "application/json", nil)
	assert.NoError(t, err)

	var s *GameSession
	err = json.NewDecoder(res.Body).Decode(&s)
	assert.NoError(t, err)

	var sessions map[string]*GameSession
	res, err = c.Get(url + "/sessions")
	assert.NoError(t, err)
	err = json.NewDecoder(res.Body).Decode(&sessions)
	assert.NoError(t, err)
	PrettyJson(sessions)
	assert.Contains(t, sessions, s.Code)
	assert.Contains(t, sessions[s.Code].Players, name)

	res, err = c.Get(url + "/" + s.Code + "/join?name=" + name)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusNotAcceptable, res.StatusCode)

	name = "test2"
	res, err = c.Get(url + "/" + s.Code + "/join?name=" + name)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)

	res, err = c.Get(url + "/" + "f" + "/join?name=" + name)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, res.StatusCode)

	res, err = c.Get(url + "/" + "1111" + "/join?name=" + name)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
}
