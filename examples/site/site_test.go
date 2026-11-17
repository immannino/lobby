package main

import (
	"os"
	"strings"
	"testing"
)

// The landing page and docs show the quickstart's source. Keep them in sync:
// if this fails, copy the new code into web/home.html and web/docs.html.
func TestPagesShowCurrentQuickstart(t *testing.T) {
	src, err := os.ReadFile("../quickstart/main.go")
	if err != nil {
		t.Fatal(err)
	}
	game := string(src)
	game = strings.TrimRight(game[strings.Index(game, "// buzzer is the whole game"):], "\n")
	for _, page := range []string{"web/home.html", "web/docs.html"} {
		b, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), escapeNoQuotes(game)) {
			t.Errorf("%s doesn't contain the current examples/quickstart/main.go", page)
		}
	}
}

// escapeNoQuotes escapes text for an HTML element body; quotes don't need it.
func escapeNoQuotes(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
