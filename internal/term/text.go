package term

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/hinshun/vt10x"
)

// escape sequence parser states.
const (
	stText = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
	stCharset
)

// stripper removes terminal escape sequences from a byte stream that arrives
// in arbitrary chunks, which is why it has to remember where it stopped.
type stripper struct {
	state int
}

func (s *stripper) filter(in string) string {
	var out strings.Builder
	out.Grow(len(in))
	for _, r := range in {
		switch s.state {
		case stText:
			switch r {
			case 0x1b:
				s.state = stEsc
			case 0x00, 0x07:
				// NUL and BEL carry no text.
			default:
				out.WriteRune(r)
			}
		case stEsc:
			switch r {
			case '[':
				s.state = stCSI
			case ']':
				s.state = stOSC
			case 'P', '^', '_': // DCS, PM, APC all end like an OSC string
				s.state = stOSC
			case '(', ')', '*', '+': // character set selection, one more byte
				s.state = stCharset
			default:
				s.state = stText
			}
		case stCSI:
			// Parameter and intermediate bytes, then a final byte.
			if r >= 0x40 && r <= 0x7e {
				s.state = stText
			}
		case stOSC:
			switch r {
			case 0x07:
				s.state = stText
			case 0x1b:
				s.state = stOSCEsc
			}
		case stOSCEsc:
			// ESC \ terminates the string; anything else was part of it.
			s.state = stText
			if r != '\\' {
				s.state = stOSC
			}
		case stCharset:
			s.state = stText
		}
	}
	return out.String()
}

// StripANSI removes escape sequences from a complete string.
func StripANSI(s string) string {
	var st stripper
	return st.filter(s)
}

// Journal is the plain text transcript of a session: escape sequences removed,
// carriage returns and backspaces applied so that progress bars collapse to
// their final state instead of filling the log with every intermediate step.
// It keeps at most max bytes, dropping the oldest lines first.
type Journal struct {
	strip stripper
	done  []byte // completed lines, each ending in \n
	cur   []rune // the line being written
	// pendingCR remembers a carriage return whose meaning is not decided yet:
	// followed by a newline it is an ordinary line ending, on its own it means
	// the program is about to overwrite the line it just wrote.
	pendingCR bool
	max       int
}

// NewJournal returns a journal that keeps roughly max bytes of text.
func NewJournal(max int) *Journal {
	if max <= 0 {
		max = maxOutputBytes
	}
	return &Journal{max: max}
}

// Write adds raw terminal output.
func (j *Journal) Write(chunk []byte) {
	for _, r := range j.strip.filter(string(chunk)) {
		if j.pendingCR {
			if r == '\r' {
				// The terminal turns every \n into \r\n, so runs of carriage
				// returns are common and mean nothing on their own.
				continue
			}
			j.pendingCR = false
			if r != '\n' {
				// A bare carriage return: the line is about to be redrawn.
				j.cur = j.cur[:0]
			}
		}
		switch r {
		case '\n':
			j.flush()
		case '\r':
			j.pendingCR = true
		case '\b':
			if len(j.cur) > 0 {
				j.cur = j.cur[:len(j.cur)-1]
			}
		default:
			j.cur = append(j.cur, r)
		}
	}
}

func (j *Journal) flush() {
	line := strings.TrimRight(string(j.cur), " \t")
	j.cur = j.cur[:0]
	j.done = append(j.done, line...)
	j.done = append(j.done, '\n')
	if len(j.done) > j.max {
		cut := len(j.done) - j.max
		if i := indexByteFrom(j.done, '\n', cut); i >= 0 {
			cut = i + 1
		}
		j.done = append([]byte(nil), j.done[cut:]...)
	}
}

// String returns the transcript, including the line currently being written.
func (j *Journal) String() string {
	out := string(j.done)
	if len(j.cur) > 0 {
		out += strings.TrimRight(string(j.cur), " \t")
	}
	return strings.TrimRight(out, "\n")
}

// Tail returns at most the last maxBytes of the transcript, cut at a line.
func (j *Journal) Tail(maxBytes int) string {
	out := j.String()
	if maxBytes <= 0 || len(out) <= maxBytes {
		return out
	}
	out = out[len(out)-maxBytes:]
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	return "… (earlier output dropped)\n" + out
}

func indexByteFrom(b []byte, c byte, from int) int {
	for i := from; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// ------------------------------------------------------------------ colour
//
// The tools Socrates drives paint in colour, so the browser is given the
// appearance of every cell as well as its text. The plain screen above is
// what the agent reads and stays exactly as it was: colour is for the eye.

// StyledRun is a stretch of characters on one line that look the same. The web
// terminal paints one span per run.
//
// Colours are tokens rather than finished CSS: "a0".."a15" name one of the
// sixteen ANSI colours, which the page resolves to its own palette, while the
// 256 colour cube and 24 bit colours arrive as "#rrggbb". "fg" and "bg" mean
// the terminal's own foreground and background, which is what reverse video
// leaves behind.
type StyledRun struct {
	Text      string `json:"t"`
	FG        string `json:"fg,omitempty"`
	BG        string `json:"bg,omitempty"`
	Bold      bool   `json:"b,omitempty"`
	Dim       bool   `json:"d,omitempty"`
	Italic    bool   `json:"i,omitempty"`
	Underline bool   `json:"u,omitempty"`
}

// styled reports whether a run needs a span at all.
func (r StyledRun) styled() bool {
	r.Text = ""
	return r != StyledRun{}
}

// ScreenCursor is where the program thinks the caret is, so the browser can
// show it. Row and column are zero based.
type ScreenCursor struct {
	Row     int  `json:"row"`
	Col     int  `json:"col"`
	Visible bool `json:"visible"`
}

// vt10x keeps the appearance of a cell in a bitmask whose constants it does
// not export, so they are repeated here. The SGR tests fail loudly if a future
// version renumbers them.
const (
	attrReverse   = 1 << 0
	attrUnderline = 1 << 1
	attrBold      = 1 << 2
	attrGfx       = 1 << 3
	attrItalic    = 1 << 4
	attrBlink     = 1 << 5
)

// screenView is the part of the emulator the styled screen is read from.
type screenView interface {
	Size() (cols, rows int)
	Cell(x, y int) vt10x.Glyph
	Cursor() vt10x.Cursor
	CursorVisible() bool
}

// styledScreen renders the screen as runs of styled text, lined up with the
// plain screen trimScreen produces. It returns nil when nothing on the screen
// carries any colour or emphasis at all, which keeps the payload for an
// ordinary shell session as small as it was before.
func styledScreen(v screenView) ([][]StyledRun, *ScreenCursor) {
	cols, rows := v.Size()
	pos := v.Cursor()
	cursor := &ScreenCursor{Row: pos.Y, Col: pos.X, Visible: v.CursorVisible()}
	if cols <= 0 || rows <= 0 {
		return nil, cursor
	}

	lines := make([][]StyledRun, rows)
	anyStyle := false
	for y := 0; y < rows; y++ {
		// Trailing blanks are dropped the way the plain screen drops them, but
		// a space with a background colour is not blank: it is a bar, a badge
		// or a selection, and cutting it away would take the drawing with it.
		end := 0
		for x := 0; x < cols; x++ {
			ch, style := cellStyle(v.Cell(x, y))
			if ch != ' ' || style.styled() {
				end = x + 1
			}
		}
		var runs []StyledRun
		var text strings.Builder
		var open StyledRun
		for x := 0; x < end; x++ {
			ch, style := cellStyle(v.Cell(x, y))
			if x > 0 && style != open {
				open.Text = text.String()
				runs = append(runs, open)
				text.Reset()
			}
			open = style
			text.WriteRune(ch)
			anyStyle = anyStyle || style.styled()
		}
		if text.Len() > 0 {
			open.Text = text.String()
			runs = append(runs, open)
		}
		lines[y] = runs
	}

	// Trailing empty lines go, except that the caret is never cut off: on a
	// fresh prompt it sits on the first line the screen no longer has text on.
	last := len(lines)
	for last > 0 && len(lines[last-1]) == 0 {
		last--
	}
	if cursor.Visible && cursor.Row+1 > last && cursor.Row < len(lines) {
		last = cursor.Row + 1
	}
	lines = lines[:last]
	if !anyStyle {
		return nil, cursor
	}
	return lines, cursor
}

// cellStyle turns one emulator cell into its character and its appearance.
func cellStyle(g vt10x.Glyph) (rune, StyledRun) {
	ch := g.Char
	if ch == 0 {
		ch = ' '
	}
	// Reverse video has already been applied to the colours by the emulator,
	// which is why the bit itself is ignored here.
	style := StyledRun{
		FG:        colorToken(g.FG, true),
		BG:        colorToken(g.BG, false),
		Bold:      g.Mode&attrBold != 0,
		Italic:    g.Mode&attrItalic != 0,
		Underline: g.Mode&attrUnderline != 0,
		// Faint text is carried in the blink bit; see dimmer below.
		Dim: g.Mode&attrBlink != 0,
	}
	return ch, style
}

// colorToken names a colour for the browser. An empty token means "whatever
// the terminal normally uses", which is the common case by far.
func colorToken(c vt10x.Color, foreground bool) string {
	switch c {
	case vt10x.DefaultFG:
		if foreground {
			return ""
		}
		return "fg"
	case vt10x.DefaultBG, vt10x.DefaultCursor:
		if foreground {
			return "bg"
		}
		return ""
	}
	switch {
	case c < 16:
		return "a" + strconv.Itoa(int(c))
	case c < 256:
		return xterm256[c]
	case c < 1<<24:
		// 24 bit colour, packed by the emulator as r<<16 | g<<8 | b.
		return fmt.Sprintf("#%06x", uint32(c))
	}
	return ""
}

// xterm256 is the fixed part of the 256 colour palette: a 6x6x6 cube followed
// by 24 greys. The first sixteen entries are the ANSI colours, which the page
// paints from its own palette instead.
var xterm256 = func() [256]string {
	var out [256]string
	levels := [6]int{0, 95, 135, 175, 215, 255}
	for i := 16; i < 232; i++ {
		n := i - 16
		out[i] = fmt.Sprintf("#%02x%02x%02x", levels[n/36%6], levels[n/6%6], levels[n%6])
	}
	for i := 232; i < 256; i++ {
		g := 8 + (i-232)*10
		out[i] = fmt.Sprintf("#%02x%02x%02x", g, g, g)
	}
	return out
}()

// dimmer keeps faint text alive on the way into the emulator.
//
// vt10x tracks bold, italic, underline, reverse and blink, but not SGR 2, and
// faint text is everywhere in the agent CLIs this app drives - it is how they
// write their hints. Blinking, on the other hand, is not something a web page
// should ever do. So the one is carried in the other's bit: SGR 2 becomes
// blink on the way in, blink becomes nothing, and everything else - including
// the colour arguments of 38 and 48, which contain the digits 2 and 5 - is
// passed through byte for byte.
type dimmer struct {
	state int
	buf   []byte
}

const (
	dimText = iota
	dimEsc
	dimCSI
	dimSkip // a CSI too long to be an SGR sequence, copied as it comes
)

// maxCSI is longer than any SGR sequence a program has business sending.
const maxCSI = 128

func (d *dimmer) filter(in []byte) []byte {
	// The overwhelming majority of chunks contain no SGR sequence at all, and
	// those are handed straight back without a copy.
	if d.state == dimText && !bytes.Contains(in, []byte{0x1b}) {
		return in
	}
	out := make([]byte, 0, len(in)+16)
	for _, b := range in {
		switch d.state {
		case dimText:
			if b == 0x1b {
				d.state = dimEsc
				continue
			}
			out = append(out, b)
		case dimEsc:
			if b == '[' {
				d.state = dimCSI
				d.buf = d.buf[:0]
				continue
			}
			out = append(out, 0x1b)
			if b == 0x1b {
				continue
			}
			out = append(out, b)
			d.state = dimText
		case dimCSI:
			d.buf = append(d.buf, b)
			if b >= 0x40 && b <= 0x7e {
				out = append(out, rewriteCSI(d.buf)...)
				d.state = dimText
				continue
			}
			if len(d.buf) > maxCSI {
				out = append(out, 0x1b, '[')
				out = append(out, d.buf...)
				d.buf = d.buf[:0]
				d.state = dimSkip
			}
		case dimSkip:
			out = append(out, b)
			if b >= 0x40 && b <= 0x7e {
				d.state = dimText
			}
		}
	}
	return out
}

// rewriteCSI takes the body of a CSI sequence, without the leading ESC [, and
// returns it ready to be written to the emulator.
func rewriteCSI(body []byte) []byte {
	verbatim := append([]byte{0x1b, '['}, body...)
	if len(body) == 0 || body[len(body)-1] != 'm' {
		return verbatim
	}
	params := string(body[:len(body)-1])
	if strings.TrimLeft(params, "0123456789;") != "" {
		// A private or colon separated form; not ours to touch.
		return verbatim
	}
	fields := strings.Split(params, ";")
	if params == "" {
		fields = nil
	}
	out := make([]string, 0, len(fields)+1)
	changed := false
	for i := 0; i < len(fields); i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			out = append(out, fields[i])
			continue
		}
		switch n {
		case 38, 48:
			// The colour arguments are values, not attributes.
			out = append(out, fields[i])
			if i+2 < len(fields) && fields[i+1] == "5" {
				out = append(out, fields[i+1], fields[i+2])
				i += 2
			} else if i+4 < len(fields) && fields[i+1] == "2" {
				out = append(out, fields[i+1:i+5]...)
				i += 4
			} else {
				// A colour that names no colour. What the emulator makes of the
				// rest is its business; guessing here would only turn a broken
				// sequence into a different broken sequence.
				return verbatim
			}
		case 2: // faint
			out = append(out, "5")
			changed = true
		case 5, 6: // blink, which the page will not do
			out = append(out, "25")
			changed = true
		case 22: // neither bold nor faint any more
			out = append(out, "22", "25")
			changed = true
		default:
			out = append(out, fields[i])
		}
	}
	if !changed {
		return verbatim
	}
	return []byte("\x1b[" + strings.Join(out, ";") + "m")
}
