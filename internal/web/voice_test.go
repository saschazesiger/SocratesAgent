package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/piper"
)

// The deadline a browser puts on one render decides which answers can be heard
// at all: a request the page abandons is an answer the server rendered and
// nobody got. It used to be flat, which cut off everything past roughly 4,700
// characters, so it is a function now - and a function is something a test can
// hold to its floor, its slope and its ceiling.
//
// The browser timer itself is not what is checked here; only the arithmetic
// that decides how long it runs for.
func TestTheSpeechDeadlineGrowsWithTheText(t *testing.T) {
	cases := []struct {
		name   string
		length int
		want   int
	}{
		// Short answers keep the protection the flat deadline was written for:
		// a request that gets nowhere is given up on rather than waited out.
		{"empty", 0, 20000},
		{"a sentence", 100, 20000},
		{"just under the floor", 1200, 20000},
		// In between, the deadline follows the text at 16 ms per character,
		// four times what a render measures at.
		{"a long paragraph", 2000, 32000},
		{"a long answer", 4000, 64000},
		// The longest text the server accepts is what Piper will read, and
		// the slope still applies there: the ceiling must not clip the answers
		// this app really produces, or a slow board renders them and the page
		// hangs up anyway.
		{"the longest answer accepted", piper.MaxTextRunes, piper.MaxTextRunes * 16},
		// The ceiling only catches a request that has gone nowhere at all. It
		// sits above the five minutes the server allows one render, so the
		// server's own timeout is what fires - and that one says what
		// happened rather than blaming the connection.
		{"longer than anything accepted", 100000, 330000},
	}
	for _, c := range cases {
		if got := speechDeadlineMS(t, c.length); got != c.want {
			t.Errorf("speechDeadline(%d) = %d ms for %s, want %d", c.length, got, c.name, c.want)
		}
	}
}

// speechDeadlineMS asks the real module, in a real JavaScript engine, rather
// than reimplementing the formula in Go where it could drift away from the
// file that is actually served.
func speechDeadlineMS(t *testing.T, length int) int {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so the browser module cannot be run")
	}
	source, err := filepath.Abs(filepath.Join("static", "js", "voice.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal(err)
	}
	// A Windows path is C:\... and no URL at all until it is turned into one,
	// and the path is quoted rather than pasted so nothing in it can end the
	// JavaScript string it sits in.
	path := filepath.ToSlash(source)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	script := "import(" + strconv.Quote("file://"+path) + ").then((m) => " +
		"console.log(JSON.stringify(m.speechDeadline(" + strconv.Itoa(length) + "))))"
	// Only stdout is parsed: node writes warnings of its own to stderr - a
	// stray package.json is enough for one - and a warning mixed into the
	// output would fail this test with a parse error about the wrong thing.
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	var complaints strings.Builder
	cmd.Stderr = &complaints
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, complaints.String())
	}
	var ms int
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &ms); err != nil {
		t.Fatalf("node printed %q: %v\n%s", out, err, complaints.String())
	}
	return ms
}
