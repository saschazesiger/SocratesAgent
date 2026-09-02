//go:build !windows

package termux

import (
	"bytes"
	"strings"
	"testing"
	"time"
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
	// Answers, drawing, and the title commands that share the letter t with a
	// window report: none of them is a question.
	stream := "\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[22;0;0t\x1b[23;0;0t\x1b[1;30r\x1b[?1049h"
	if got := r.Filter([]byte(stream)); string(got) != stream {
		t.Fatalf("filtered = %q, want it untouched", got)
	}
	if answers.Len() != 0 {
		t.Fatalf("answered %q to something that was not a question", answers.String())
	}
}

// TestResponderAnswersEveryAttachQuery is the fix for the one defect a person
// could see: a reply that arrives after tmux has stopped waiting is typed into
// the pane, and the command they meant to run is prefixed with 1;2c0;276;0c.
//
// The stream here is what tmux 3.6 actually sends a client on attach, taken
// off a pseudo terminal. Every question in it is answered on this side, and
// none of it reaches the browser.
func TestResponderAnswersEveryAttachQuery(t *testing.T) {
	r, answers := newResponder()
	r.Size = func() (int, int) { return 100, 30 }
	attach := "\x1b[?2031h\x1b[?996n\x1b[1;1H\x1b[1;30r" +
		"\x1b[c\x1b[>c\x1b[>q\x1b]10;?\x1b\\\x1b]11;?\x1b\\" +
		"\x1b[1;3H\x1b[18t\x1b[14t\x1b[6n#"
	got := string(r.Filter([]byte(attach)))
	for _, query := range []string{"\x1b[c", "\x1b[>c", "\x1b[>q", "\x1b[18t", "\x1b[14t",
		"\x1b[6n", "\x1b[?996n", "\x1b]10;?", "\x1b]11;?"} {
		if strings.Contains(got, query) {
			t.Fatalf("%q reached the browser: %q", query, got)
		}
	}
	if want := "\x1b[?2031h\x1b[1;1H\x1b[1;30r\x1b[1;3H#"; got != want {
		t.Fatalf("the stream came out as %q, want %q", got, want)
	}
	want := "\x1b[?997;2n" + // the theme
		"\x1b[?1;2c" + // DA1
		"\x1b[>0;276;0c" + // DA2
		"\x1b]10;rgb:1717/1818/1b1b\x1b\\" +
		"\x1b]11;rgb:ffff/ffff/ffff\x1b\\" +
		"\x1b[8;30;100t" + // the text area in cells
		"\x1b[4;540;800t" // and in pixels
	if answers.String() != want {
		t.Fatalf("answers = %q, want %q", answers.String(), want)
	}
}

// TestStripRepliesDropsReportsAndNothingElse is the other half: what the
// browser sends. A report is stale by construction - nothing here asks it
// anything any more - and a keystroke is not.
func TestStripRepliesDropsReportsAndNothingElse(t *testing.T) {
	dropped := []string{
		"\x1b[?1;2c",                   // DA1, xterm.js
		"\x1b[?62;1;2;4;6c",            // DA1, a fuller terminal
		"\x1b[>0;276;0c",               // DA2
		"\x1b[>c",                      // DA2, no parameters at all
		"\x1b[8;33;41t",                // the text area in cells
		"\x1b[4;594;344t",              // and in pixels
		"\x1b[24;80R",                  // where the cursor is
		"\x1bP>|xterm.js(5.5.0)\x1b\\", // XTVERSION
	}
	for _, reply := range dropped {
		if got, _ := StripReplies([]byte("a"+reply+"b"), false); string(got) != "ab" {
			t.Fatalf("%q survived as %q", reply, got)
		}
	}

	kept := []string{
		"ls -l\r", "\x03", "\x1b", "\x1b\x1b",
		"\x1b[A", "\x1b[B", "\x1b[1;5C", // arrows, plain and modified
		"\x1bOP", "\x1b[15~", "\x1b[3~", // function keys, home and delete
		"\x1b[200~pasted \x1b[?1;2c not a reply\x1b[201~", // a paste keeps its shape
		"\x1b[2t", "\x1b[c", "\x1b[6n", // commands and questions, not answers
		"\x1b[H", "\x1b[1;1H", "\x1bP", "\x1bP>|unfinished",
	}
	for _, keys := range kept {
		if got, _ := StripReplies([]byte(keys), false); string(got) != keys {
			t.Fatalf("%q was mangled into %q", keys, got)
		}
	}

	// A paste is text, including a paste of something report shaped, and it
	// stays text across the frames it arrives in.
	split := [][]byte{[]byte("\x1b[200~one \x1b[?1;2c"), []byte(" two\x1b[201~\x1b[?1;2c")}
	var joined []byte
	paste := false
	for _, frame := range split {
		var keys []byte
		keys, paste = StripReplies(frame, paste)
		joined = append(joined, keys...)
	}
	if want := "\x1b[200~one \x1b[?1;2c two\x1b[201~"; string(joined) != want {
		t.Fatalf("the paste came out as %q, want %q", joined, want)
	}
	if paste {
		t.Fatal("the paste never ended")
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

// TestViewerAnswersTheAttachQueriesItself is the same fix seen from the
// outside: a real tmux attach on a real pseudo terminal, and none of the
// questions tmux asks its client reaches the ring the browser reads.
func TestViewerAnswersTheAttachQueriesItself(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	v := l.attach(row.ID, 100, 30)
	// The prompt means the attach is done and tmux has asked whatever it asks.
	waitForRing(t, v, 10*time.Second, "#")
	time.Sleep(300 * time.Millisecond)
	seen, _ := v.Ring().Since(0)

	for _, query := range []string{
		"\x1b[c", "\x1b[>c", "\x1b[>q", "\x1b[18t", "\x1b[14t", "\x1b[6n",
		"\x1b[?996n", "\x1b]10;?", "\x1b]11;?",
	} {
		if bytes.Contains(seen, []byte(query)) {
			t.Fatalf("the browser was asked %q; it would answer into the pane", query)
		}
	}
}
