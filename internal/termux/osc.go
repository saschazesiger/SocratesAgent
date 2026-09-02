package termux

import (
	"fmt"
	"io"
)

// The colours the generated tmux configuration paints a window in. The
// responder answers with the same pair, so that every viewer is told the same
// thing the panes were told.
var (
	// White, as sRGB in the 16 bits per channel form tmux uses.
	DefaultBackground = [3]uint16{0xffff, 0xffff, 0xffff}
	// The near black the terminal writes in.
	DefaultForeground = [3]uint16{0x1717, 0x1818, 0x1b1b}
)

// maxQuery is the longest query the responder recognises, and therefore the
// most it ever has to carry from one read to the next.
const maxQuery = 16

// The cell a pixel report is measured in. Nothing on this side of the socket
// knows what the browser's font is, and the two programs that ask - image
// protocols, mostly - want a plausible ratio rather than the truth.
const (
	cellWidth  = 8
	cellHeight = 18
)

// Responder answers the questions tmux asks a client when it attaches, and
// takes them out of the stream so that the browser never sees them.
//
// Measured on tmux 3.6, an attach asks a client six things: the theme mode
// (CSI ? 996 n), the two colours (OSC 10 and OSC 11), what terminal it is
// (DA1, DA2, XTVERSION) and how big the text area is in cells and in pixels
// (CSI 18 t, CSI 14 t). Every one of them is answered here, for one reason
// that matters more than the round trip it saves.
//
// A reply is only a reply while the terminal is still waiting for it. tmux
// stops waiting a fraction of a second after the attach; a reply that arrives
// later - from a browser on a phone behind a tunnel, on a link that dropped,
// after a wake from sleep - is ordinary client input, and tmux types it into
// the pane. The bytes are printable, so what the person sees is their next
// command silently prefixed with 1;2c0;276;0c and a shell that runs the wrong
// thing. Answering here, from a process on the same machine as tmux, is what
// makes that impossible rather than unlikely.
//
// The pane's own palette does not depend on this: an explicit window-style
// makes tmux answer a pane with no client attached at all, and its answer
// beats a client's. The responder exists so that the answer is the same for
// every viewer and costs no round trip over a mobile link.
type Responder struct {
	Foreground [3]uint16
	Background [3]uint16
	// W is the PTY master the answers are written to.
	W io.Writer
	// Size is the viewer's terminal size, for the two window reports. Nil
	// answers with the size a session is created at.
	Size func() (cols, rows int)

	pending []byte
}

type queryKind int

const (
	queryNone queryKind = iota
	queryPartial
	queryForeground   // OSC 10 ; ?
	queryBackground   // OSC 11 ; ?
	queryThemeMode    // CSI ? 996 n
	queryDA1          // CSI c
	queryDA2          // CSI > c
	queryVersion      // CSI > q
	queryTextCells    // CSI 18 t
	queryTextPixels   // CSI 14 t
	queryCursorReport // CSI 6 n
)

// Filter answers every recognised query in p and returns p without them.
//
// It is stateful across calls: a query may straddle a read boundary, so up to
// maxQuery bytes are carried over rather than passed on half finished. Each
// query is answered exactly once, whether or not it arrived in one piece.
func (r *Responder) Filter(p []byte) []byte {
	data := p
	if len(r.pending) > 0 {
		data = append(append(make([]byte, 0, len(r.pending)+len(p)), r.pending...), p...)
		r.pending = nil
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if data[i] != 0x1b {
			out = append(out, data[i])
			i++
			continue
		}
		n, kind := matchQuery(data[i:])
		switch kind {
		case queryPartial:
			if len(data)-i > maxQuery {
				// Too long to be one of ours however it continues.
				out = append(out, data[i])
				i++
				continue
			}
			// Hold the tail back until the rest of it arrives.
			r.pending = append(r.pending, data[i:]...)
			return out
		case queryNone:
			out = append(out, data[i])
			i++
		default:
			r.answer(kind)
			i += n
		}
	}
	return out
}

func (r *Responder) answer(kind queryKind) {
	if r.W == nil {
		return
	}
	switch kind {
	case queryForeground:
		fmt.Fprintf(r.W, "\x1b]10;%s\x1b\\", rgbString(r.colour(r.Foreground, DefaultForeground)))
	case queryBackground:
		fmt.Fprintf(r.W, "\x1b]11;%s\x1b\\", rgbString(r.colour(r.Background, DefaultBackground)))
	case queryThemeMode:
		// 997 is the theme report, and 2 is light.
		_, _ = io.WriteString(r.W, "\x1b[?997;2n")
	case queryDA1:
		// What xterm.js answers, byte for byte, so that tmux sees the terminal
		// it would have seen: a VT100 with an advanced video option.
		_, _ = io.WriteString(r.W, "\x1b[?1;2c")
	case queryDA2:
		_, _ = io.WriteString(r.W, "\x1b[>0;276;0c")
	case queryTextCells:
		cols, rows := r.size()
		fmt.Fprintf(r.W, "\x1b[8;%d;%dt", rows, cols)
	case queryTextPixels:
		cols, rows := r.size()
		fmt.Fprintf(r.W, "\x1b[4;%d;%dt", rows*cellHeight, cols*cellWidth)
	case queryVersion, queryCursorReport:
		// Taken out of the stream and left unanswered, deliberately. Nothing
		// answers XTVERSION today - xterm.js does not implement it - and only
		// the browser knows where its cursor is, so any answer here would be
		// an invention. tmux stops waiting for both on its own, and neither
		// changes anything it draws; what matters is that the browser is never
		// asked, because its answer would arrive as a keystroke.
	}
}

func (r *Responder) size() (cols, rows int) {
	if r.Size != nil {
		if cols, rows = r.Size(); cols > 0 && rows > 0 {
			return cols, rows
		}
	}
	return 120, 40
}

func (r *Responder) colour(set, fallback [3]uint16) [3]uint16 {
	if set == [3]uint16{} {
		return fallback
	}
	return set
}

func rgbString(c [3]uint16) string {
	return fmt.Sprintf("rgb:%04x/%04x/%04x", c[0], c[1], c[2])
}

// matchQuery looks at the escape sequence starting at s[0] and reports how
// long it is and what it asks. A sequence that could still become one of ours
// if more bytes arrived is reported as partial.
func matchQuery(s []byte) (int, queryKind) {
	if n, kind := matchOSC(s, "\x1b]10;?", queryForeground); kind != queryNone {
		return n, kind
	}
	if n, kind := matchOSC(s, "\x1b]11;?", queryBackground); kind != queryNone {
		return n, kind
	}
	for _, q := range []struct {
		seq  string
		kind queryKind
	}{
		{"\x1b[?996n", queryThemeMode},
		{"\x1b[c", queryDA1},
		{"\x1b[0c", queryDA1},
		{"\x1b[>c", queryDA2},
		{"\x1b[>0c", queryDA2},
		{"\x1b[>q", queryVersion},
		{"\x1b[>0q", queryVersion},
		{"\x1b[18t", queryTextCells},
		{"\x1b[14t", queryTextPixels},
		{"\x1b[6n", queryCursorReport},
	} {
		if n, kind := matchExact(s, q.seq, q.kind); kind != queryNone {
			return n, kind
		}
	}
	return 0, queryNone
}

// matchOSC matches an OSC query with either terminator: ESC \ or BEL.
func matchOSC(s []byte, head string, kind queryKind) (int, queryKind) {
	n, k := matchExact(s, head, kind)
	if k != kind {
		return n, k
	}
	rest := s[len(head):]
	if len(rest) == 0 {
		return 0, queryPartial
	}
	if rest[0] == 0x07 {
		return len(head) + 1, kind
	}
	if rest[0] != 0x1b {
		return 0, queryNone
	}
	if len(rest) == 1 {
		return 0, queryPartial
	}
	if rest[1] != '\\' {
		return 0, queryNone
	}
	return len(head) + 2, kind
}

// matchExact reports a full match of want, or that s is a prefix of it and may
// yet become one.
func matchExact(s []byte, want string, kind queryKind) (int, queryKind) {
	for i := 0; i < len(s) && i < len(want); i++ {
		if s[i] != want[i] {
			return 0, queryNone
		}
	}
	if len(s) < len(want) {
		return 0, queryPartial
	}
	return len(want), kind
}

// ---------------------------------------------------------------- the way back

// StripReplies removes terminal reports from what a browser sends, and is the
// second half of the same defence as the Responder.
//
// A report is an answer, and an answer is only ever wanted while something is
// waiting for it. Socrates answers every question tmux asks on its own side
// now, so nothing on this side ever asks the browser anything - which makes
// any report arriving here stale by construction: a reply to a question from
// before an outage, from a tab that was asleep, or from a page loaded against
// an older Socrates. Written to the pane it is not input, it is corruption:
// the shell reads 1;2c0;276;0c as the beginning of the next command line.
//
// It is deliberately exact. Only well formed reports are dropped - a device
// attributes reply, a window report with its three numbers, a cursor position
// report, an XTVERSION reply - and everything else, every key and every
// pasted byte, is passed through untouched. The one keystroke this cannot
// tell apart from a report is shift with F3 under xterm's modified function
// key encoding, which is the same CSI 1 ; 2 R a cursor at the top left would
// send, and a corrupted command line is the worse of the two to keep.
//
// Bracketed paste is respected: between ESC [ 200 ~ and ESC [ 201 ~ the bytes
// are somebody's text rather than a terminal talking, and text is delivered as
// it was pasted. paste says whether the frame before this one ended inside a
// paste, and the second result says whether this one does.
func StripReplies(p []byte, paste bool) ([]byte, bool) {
	if len(p) == 0 {
		return p, paste
	}
	const (
		pasteOn  = "\x1b[200~"
		pasteOff = "\x1b[201~"
	)
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); {
		if p[i] != 0x1b {
			out = append(out, p[i])
			i++
			continue
		}
		if hasPrefix(p[i:], pasteOff) {
			paste = false
			out = append(out, pasteOff...)
			i += len(pasteOff)
			continue
		}
		if !paste && hasPrefix(p[i:], pasteOn) {
			paste = true
			out = append(out, pasteOn...)
			i += len(pasteOn)
			continue
		}
		if n := matchReply(p[i:]); n > 0 && !paste {
			i += n
			continue
		}
		out = append(out, p[i])
		i++
	}
	return out, paste
}

func hasPrefix(s []byte, want string) bool {
	return len(s) >= len(want) && string(s[:len(want)]) == want
}

// matchReport returns the length of the report starting at s[0], or zero.
func matchReply(s []byte) int {
	if n := matchCSIReply(s); n > 0 {
		return n
	}
	return matchVersionReply(s)
}

// matchCSIReply covers the three CSI shaped reports:
//
//	ESC [ ? <params> c    the primary device attributes
//	ESC [ > <params> c    the secondary device attributes
//	ESC [ <n;n;n> t       a window report
//	ESC [ <n;n> R         a cursor position report
func matchCSIReply(s []byte) int {
	if len(s) < 3 || s[1] != '[' {
		return 0
	}
	i := 2
	private := byte(0)
	if s[i] == '?' || s[i] == '>' {
		private = s[i]
		i++
	}
	params := 1
	for ; i < len(s); i++ {
		switch {
		case s[i] >= '0' && s[i] <= '9':
			continue
		case s[i] == ';':
			params++
			continue
		}
		break
	}
	if i >= len(s) || i == 2 {
		return 0 // Nothing but the introducer, or no parameters at all.
	}
	switch s[i] {
	case 'c':
		if private == 0 {
			return 0 // A bare CSI c is a question, and no browser asks one.
		}
		return i + 1
	case 't':
		// A window report carries its kind and two numbers; the commands that
		// share the letter carry fewer, and no keyboard sends any of them.
		if private != 0 || params != 3 {
			return 0
		}
		return i + 1
	case 'R':
		if private != 0 || params != 2 {
			return 0
		}
		return i + 1
	}
	return 0
}

// matchVersionReply covers ESC P > | <text> ESC \, the XTVERSION answer.
func matchVersionReply(s []byte) int {
	const head = "\x1bP>|"
	if len(s) < len(head) || string(s[:len(head)]) != head {
		return 0
	}
	for i := len(head); i+1 < len(s); i++ {
		if s[i] == 0x1b && s[i+1] == '\\' {
			return i + 2
		}
		if s[i] == 0x07 {
			return i + 1
		}
	}
	return 0
}
