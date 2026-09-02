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

// Responder answers the terminal colour questions tmux asks a client when it
// attaches, and takes them out of the stream so that the browser never sees
// them.
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

	pending []byte
}

type queryKind int

const (
	queryNone queryKind = iota
	queryPartial
	queryForeground // OSC 10 ; ?
	queryBackground // OSC 11 ; ?
	queryThemeMode  // CSI ? 996 n
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
	}
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
	return matchExact(s, "\x1b[?996n", queryThemeMode)
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
