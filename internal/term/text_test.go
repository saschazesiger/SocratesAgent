package term

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
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

// renderStyled runs bytes through the same path a session uses - the dim
// filter, then the emulator - and returns both views of the screen.
func renderStyled(t *testing.T, cols, rows int, chunks ...string) (string, [][]StyledRun, *ScreenCursor) {
	t.Helper()
	vt := vt10x.New(vt10x.WithSize(cols, rows))
	var d dimmer
	for _, chunk := range chunks {
		vt.Write(d.filter([]byte(chunk)))
	}
	styled, cursor := styledScreen(vt)
	return trimScreen(vt.String()), styled, cursor
}

// firstRuns is the styled first line, which is where the table tests below
// paint everything they check.
func firstRuns(t *testing.T, styled [][]StyledRun) []StyledRun {
	t.Helper()
	if len(styled) == 0 {
		t.Fatal("the screen came back unstyled")
	}
	return styled[0]
}

func TestStyledScreenReadsSGR(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		plain string
		want  []StyledRun
	}{
		{
			name: "basic colour", in: "\x1b[31mred\x1b[0m plain", plain: "red plain",
			want: []StyledRun{{Text: "red", FG: "a1"}, {Text: " plain"}},
		},
		{
			name: "background", in: "\x1b[44mblue\x1b[49m", plain: "blue",
			want: []StyledRun{{Text: "blue", BG: "a4"}},
		},
		{
			name: "bright colour", in: "\x1b[92mbright\x1b[39m", plain: "bright",
			want: []StyledRun{{Text: "bright", FG: "a10"}},
		},
		{
			// A bold ANSI colour is the bright one, the way a terminal shows it.
			name: "bold blue", in: "\x1b[1;34mbold\x1b[0m", plain: "bold",
			want: []StyledRun{{Text: "bold", FG: "a12", Bold: true}},
		},
		{
			name: "256 colour", in: "\x1b[38;5;208mamber\x1b[0m", plain: "amber",
			want: []StyledRun{{Text: "amber", FG: "#ff8700"}},
		},
		{
			name: "256 grey background", in: "\x1b[48;5;236mbar\x1b[0m", plain: "bar",
			want: []StyledRun{{Text: "bar", BG: "#303030"}},
		},
		{
			// The trailing 2 and 5 are colour values, not "faint" and "blink".
			name: "256 colour with awkward values", in: "\x1b[38;5;2mgreen\x1b[0m", plain: "green",
			want: []StyledRun{{Text: "green", FG: "a2"}},
		},
		{
			name: "truecolor", in: "\x1b[38;2;255;128;0mrgb\x1b[0m", plain: "rgb",
			want: []StyledRun{{Text: "rgb", FG: "#ff8000"}},
		},
		{
			name: "truecolor background", in: "\x1b[48;2;10;20;30mrgb\x1b[0m", plain: "rgb",
			want: []StyledRun{{Text: "rgb", BG: "#0a141e"}},
		},
		{
			// A truecolor argument list also carries a bare 2 and 5.
			name: "truecolor with awkward values", in: "\x1b[38;2;2;5;22mrgb\x1b[0m", plain: "rgb",
			want: []StyledRun{{Text: "rgb", FG: "#020516"}},
		},
		{
			name: "bold and underline", in: "\x1b[1;4mboth\x1b[0m", plain: "both",
			want: []StyledRun{{Text: "both", Bold: true, Underline: true}},
		},
		{
			name: "italic", in: "\x1b[3mit\x1b[23mnot", plain: "itnot",
			want: []StyledRun{{Text: "it", Italic: true}, {Text: "not"}},
		},
		{
			name: "faint", in: "\x1b[2mdim\x1b[22mnot", plain: "dimnot",
			want: []StyledRun{{Text: "dim", Dim: true}, {Text: "not"}},
		},
		{
			name: "underline off", in: "\x1b[4mon\x1b[24moff", plain: "onoff",
			want: []StyledRun{{Text: "on", Underline: true}, {Text: "off"}},
		},
		{
			// Reverse video swaps the colours, and with none set it means the
			// terminal's own two.
			name: "inverse", in: "\x1b[7minv\x1b[27mnot", plain: "invnot",
			want: []StyledRun{{Text: "inv", FG: "bg", BG: "fg"}, {Text: "not"}},
		},
		{
			name: "inverse with a colour", in: "\x1b[31;7minv\x1b[0m", plain: "inv",
			want: []StyledRun{{Text: "inv", FG: "bg", BG: "a1"}},
		},
		{
			name: "reset by empty parameters", in: "\x1b[31mred\x1b[mplain", plain: "redplain",
			want: []StyledRun{{Text: "red", FG: "a1"}, {Text: "plain"}},
		},
		{
			name: "default colour only", in: "\x1b[31;42mboth\x1b[39mfg\x1b[49mnone", plain: "bothfgnone",
			want: []StyledRun{
				{Text: "both", FG: "a1", BG: "a2"},
				{Text: "fg", BG: "a2"},
				{Text: "none"},
			},
		},
		{
			name: "runs are merged", in: "\x1b[31mre\x1b[31md\x1b[0m", plain: "red",
			want: []StyledRun{{Text: "red", FG: "a1"}},
		},
		{
			// A background painted onto spaces is a bar, and it survives the
			// trailing space trimming that the plain screen does.
			name: "coloured spaces", in: "\x1b[41m  \x1b[0m", plain: "",
			want: []StyledRun{{Text: "  ", BG: "a1"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plain, styled, _ := renderStyled(t, 40, 4, c.in)
			if plain != c.plain {
				t.Errorf("plain screen = %q, want %q", plain, c.plain)
			}
			got := firstRuns(t, styled)
			if len(got) != len(c.want) {
				t.Fatalf("runs = %#v, want %#v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("run %d = %#v, want %#v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// Blinking is not something a web page should ever do, so it is dropped -
// which leaves a screen that only blinks with no styling at all.
func TestStyledScreenDropsBlink(t *testing.T) {
	plain, styled, _ := renderStyled(t, 20, 2, "\x1b[5mblink\x1b[25mnot")
	if plain != "blinknot" {
		t.Errorf("plain screen = %q, want %q", plain, "blinknot")
	}
	if styled != nil {
		t.Errorf("blinking text should carry no styling: %#v", styled)
	}
}

func TestStyledScreenIsNilWhenNothingIsStyled(t *testing.T) {
	plain, styled, cursor := renderStyled(t, 20, 4, "just text\r\nand more")
	if want := "just text\nand more"; plain != want {
		t.Errorf("plain screen = %q, want %q", plain, want)
	}
	if styled != nil {
		t.Errorf("an unstyled screen should carry no styled payload: %#v", styled)
	}
	if cursor == nil || !cursor.Visible || cursor.Row != 1 {
		t.Errorf("cursor = %#v, want a visible one on the second line", cursor)
	}
}

func TestStyledScreenKeepsAttributesAcrossWrapAndScroll(t *testing.T) {
	// The colour is set once and has to survive the line wrapping and then the
	// screen scrolling, exactly as the character does.
	line := strings.Repeat("x", 12)
	_, styled, _ := renderStyled(t, 10, 3, "\x1b[32m"+line+"\r\na\r\nb\r\nc")
	if len(styled) == 0 {
		t.Fatal("the screen came back unstyled")
	}
	for y, runs := range styled {
		for _, run := range runs {
			if run.FG != "a2" {
				t.Errorf("line %d lost its colour after scrolling: %#v", y, runs)
			}
		}
	}
}

func TestStyledScreenSurvivesSplitSequences(t *testing.T) {
	// The escape sequence is cut in half by the read buffer, which is what a
	// pty does whenever a program writes more than a chunk at a time.
	_, styled, _ := renderStyled(t, 20, 2, "\x1b[38;5", ";208msplit\x1b[0m")
	got := firstRuns(t, styled)
	if len(got) != 1 || got[0].Text != "split" || got[0].FG != "#ff8700" {
		t.Errorf("runs = %#v, want one amber run", got)
	}
}

func TestStyledScreenLeavesTheAlternateScreenAlone(t *testing.T) {
	// Switching to the alternate screen and back must not leak the colour of
	// the screen that was left behind.
	plain, styled, _ := renderStyled(t, 20, 3,
		"main\r\n\x1b[?1049h\x1b[31malt\x1b[0m\x1b[?1049l")
	if plain != "main" {
		t.Errorf("plain screen = %q, want %q", plain, "main")
	}
	for _, runs := range styled {
		for _, run := range runs {
			if run.FG != "" {
				t.Errorf("colour leaked out of the alternate screen: %#v", runs)
			}
		}
	}
}

func TestDimmerPassesEverythingElseThrough(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello", "hello"},
		{"cursor move", "\x1b[3;5H", "\x1b[3;5H"},
		{"private mode", "\x1b[?1049h", "\x1b[?1049h"},
		{"colon separated sgr", "\x1b[38:2::1:2:3m", "\x1b[38:2::1:2:3m"},
		{"osc", "\x1b]0;title\x07", "\x1b]0;title\x07"},
		{"unchanged sgr", "\x1b[1;31m", "\x1b[1;31m"},
		{"reset", "\x1b[0m", "\x1b[0m"},
		{"faint", "\x1b[2m", "\x1b[5m"},
		{"faint among others", "\x1b[1;2;31m", "\x1b[1;5;31m"},
		{"bold off also ends faint", "\x1b[22m", "\x1b[22;25m"},
		{"blink becomes nothing", "\x1b[5m", "\x1b[25m"},
		{"256 colour is untouched", "\x1b[38;5;2m", "\x1b[38;5;2m"},
		{"truecolor is untouched", "\x1b[38;2;2;5;22m", "\x1b[38;2;2;5;22m"},
		{"faint after a colour", "\x1b[38;5;2;2m", "\x1b[38;5;2;5m"},
		{"clear screen", "\x1b[2J", "\x1b[2J"},
		{"clear line", "\x1b[2K", "\x1b[2K"},
		{"scroll region", "\x1b[2;22r", "\x1b[2;22r"},
		{"osc with a string terminator", "\x1b]8;;https://example.com\x1b\\", "\x1b]8;;https://example.com\x1b\\"},
		{"osc that looks like an sgr", "\x1b]0;2m\x07", "\x1b]0;2m\x07"},
		{"malformed 256 colour", "\x1b[38;5m", "\x1b[38;5m"},
		{"malformed truecolor", "\x1b[38;2;1;2m", "\x1b[38;2;1;2m"},
		{"colour with nothing after it", "\x1b[48m", "\x1b[48m"},
		{"faint after a malformed colour is left alone", "\x1b[38;5;2m", "\x1b[38;5;2m"},
		{"lone escape", "\x1b", ""},
	}
	for _, c := range cases {
		var d dimmer
		if got := string(d.filter([]byte(c.in))); got != c.want {
			t.Errorf("%s: filter(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestDimmerHandlesSplitSequences(t *testing.T) {
	var d dimmer
	got := string(d.filter([]byte("a\x1b[1;2"))) + string(d.filter([]byte(";31mb")))
	if want := "a\x1b[1;5;31mb"; got != want {
		t.Errorf("split filter = %q, want %q", got, want)
	}
}
