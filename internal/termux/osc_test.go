//go:build !windows

package termux

import (
	"bytes"
	"strings"
	"testing"
)

func newResponder() (*Responder, *bytes.Buffer) {
	var out bytes.Buffer
	return &Responder{Foreground: DefaultForeground, Background: DefaultBackground, W: &out}, &out
}

func TestResponderAnswersAndRemoves(t *testing.T) {
	r, answers := newResponder()
	got := r.Filter([]byte("a\x1b]11;?\x1b\\b\x1b]10;?\x07c\x1b[?996nd"))
	if string(got) != "abcd" {
		t.Fatalf("the queries should be gone from the stream, got %q", got)
	}
	want := "\x1b]11;rgb:ffff/ffff/ffff\x1b\\" +
		"\x1b]10;rgb:1717/1818/1b1b\x1b\\" +
		"\x1b[?997;2n"
	if answers.String() != want {
		t.Fatalf("answers = %q, want %q", answers.String(), want)
	}
}

// TestResponderStraddle is the case a byte stream makes inevitable: a query
// arrives in two reads. It must still be answered, and answered once.
func TestResponderStraddle(t *testing.T) {
	full := []byte("x\x1b]11;?\x1b\\y")
	for split := 1; split < len(full); split++ {
		r, answers := newResponder()
		out := append([]byte(nil), r.Filter(full[:split])...)
		out = append(out, r.Filter(full[split:])...)
		if string(out) != "xy" {
			t.Fatalf("split at %d: stream = %q, want \"xy\"", split, out)
		}
		if n := strings.Count(answers.String(), "\x1b]11;"); n != 1 {
			t.Fatalf("split at %d: answered %d times, want once", split, n)
		}
	}
}

func TestResponderPassesEverythingElseThrough(t *testing.T) {
	r, answers := newResponder()
	// DA1, DA2, XTVERSION and the window size reports are somebody else's to
	// answer: tmux answers DA1 itself and xterm.js answers the rest.
	stream := "\x1b[c\x1b[>c\x1b[>q\x1b[18t\x1b[14t\x1b]11;rgb:0000/0000/0000\x1b\\"
	if got := r.Filter([]byte(stream)); string(got) != stream {
		t.Fatalf("filtered = %q, want it untouched", got)
	}
	if answers.Len() != 0 {
		t.Fatalf("answered %q to something that was not a question", answers.String())
	}
}

func TestResponderHoldsBackNoMoreThanAQuery(t *testing.T) {
	r, _ := newResponder()
	long := "\x1b" + strings.Repeat("z", 64)
	if got := r.Filter([]byte(long)); string(got) != long {
		t.Fatalf("a long escape sequence must not be held back: %q", got)
	}
	if len(r.pending) != 0 {
		t.Fatalf("pending = %q, want nothing held", r.pending)
	}
}
