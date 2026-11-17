package lobby

import (
	"math/rand/v2"
	"strings"
)

// codeAlphabet is consonants only. With no vowels a random code can't spell
// anything rude, and there's no I/1 or O/0 confusion when read off a TV.
const codeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

// Codes are join codes, not secrets (anyone in the room can see them, and
// reconnect tokens are what authenticate a client), so math/rand is fine.
func newCode(length int) string {
	var b strings.Builder
	b.Grow(length)
	for range length {
		b.WriteByte(codeAlphabet[rand.IntN(len(codeAlphabet))])
	}
	return b.String()
}

// NormalizeCode uppercases and trims user input so "bcdf " matches "BCDF".
func NormalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
