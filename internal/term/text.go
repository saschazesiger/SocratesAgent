package term

import "strings"

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
