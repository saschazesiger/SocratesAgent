package term

import (
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"colour", "\x1b[1;32mgreen\x1b[0m", "green"},
		{"cursor move", "a\x1b[3;5Hb", "ab"},
		{"clear screen", "\x1b[2J\x1b[Hclean", "clean"},
		{"window title", "\x1b]0;a title\x07text", "text"},
		{"osc string terminator", "\x1b]8;;https://example.com\x1b\\link", "link"},
		{"private mode", "\x1b[?1049htext", "text"},
		{"charset", "\x1b(Btext", "text"},
		{"bel and nul", "a\x07b\x00c", "abc"},
		{"plain", "nothing to strip", "nothing to strip"},
	}
	for _, c := range cases {
		if got := StripANSI(c.in); got != c.want {
			t.Errorf("%s: StripANSI(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestStripperHandlesSplitSequences(t *testing.T) {
	// An escape sequence cut in half by the read buffer must still be removed.
	var s stripper
	got := s.filter("before\x1b[1") + s.filter(";32mafter")
	if want := "beforeafter"; got != want {
		t.Errorf("split sequence: got %q, want %q", got, want)
	}
}

func TestJournalAppliesCarriageReturns(t *testing.T) {
	j := NewJournal(0)
	j.Write([]byte("\rworking 10%"))
	j.Write([]byte("\rworking 90%"))
	j.Write([]byte("\rdone       \r\n"))
	got := j.String()
	if strings.Contains(got, "10%") || strings.Contains(got, "90%") {
		t.Errorf("intermediate progress kept: %q", got)
	}
	if got != "done" {
		t.Errorf("journal = %q, want %q", got, "done")
	}
}

func TestJournalTreatsCRLFAsALineEnding(t *testing.T) {
	j := NewJournal(0)
	// A terminal turns \n into \r\n, so \r\r\n reaches the reader.
	j.Write([]byte("first\r\r\nsecond\r\r\n"))
	if got, want := j.String(), "first\nsecond"; got != want {
		t.Errorf("journal = %q, want %q", got, want)
	}
}

func TestJournalAppliesBackspace(t *testing.T) {
	j := NewJournal(0)
	j.Write([]byte("helllo\b\bo\n"))
	if got, want := j.String(), "hello"; got != want {
		t.Errorf("journal = %q, want %q", got, want)
	}
}

func TestJournalDropsOldestLinesWhenFull(t *testing.T) {
	j := NewJournal(64)
	for i := 0; i < 50; i++ {
		j.Write([]byte("this is line number " + string(rune('a'+i%26)) + "\n"))
	}
	got := j.String()
	if len(got) > 128 {
		t.Errorf("journal grew past its cap: %d bytes", len(got))
	}
	if strings.Count(got, "\n") == 0 {
		t.Error("journal kept no complete lines")
	}
}

func TestJournalTail(t *testing.T) {
	j := NewJournal(0)
	for _, line := range []string{"one", "two", "three", "four"} {
		j.Write([]byte(line + "\n"))
	}
	if got := j.Tail(0); got != "one\ntwo\nthree\nfour" {
		t.Errorf("Tail(0) = %q", got)
	}
	tail := j.Tail(10)
	if !strings.Contains(tail, "earlier output dropped") {
		t.Errorf("a truncated tail should say so: %q", tail)
	}
	if !strings.Contains(tail, "four") {
		t.Errorf("a truncated tail must keep the newest lines: %q", tail)
	}
}
