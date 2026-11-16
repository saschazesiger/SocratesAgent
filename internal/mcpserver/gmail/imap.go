package gmail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// A small IMAP4rev1 client (RFC 3501), as much of it as Gmail's tools need:
// commands with synchronizing literals, and responses read into strings,
// lists and NIL. Gmail's extensions (X-GM-RAW, X-GM-MSGID, X-GM-THRID,
// X-GM-LABELS) are ordinary search keys and fetch items to it.

// maxLiteral is the largest literal a response may carry: a message with its
// attachments, as large as Gmail takes one (35 MB), and some room.
const maxLiteral = 48 << 20

// cmdTimeout is how long one command may take when the caller set no
// deadline.
const cmdTimeout = 90 * time.Second

// literal is a command argument sent as a synchronizing literal.
type literal []byte

// response is one response of the server: tagged (Tag is the command's),
// untagged ("*") or a continuation ("+").
type response struct {
	Tag string
	// Num is the number before an untagged EXISTS, EXPUNGE or FETCH.
	Num uint32
	// Kind is what it is, in capitals: OK, NO, BAD, BYE, PREAUTH, LIST,
	// SEARCH, FETCH, CAPABILITY, STATUS …
	Kind string
	// Text is the text of a status response (OK, NO …) or a continuation.
	Text string
	// Values is the rest of any other response: strings (atoms, quoted
	// strings and literals), nil for NIL, and []any for lists.
	Values []any
}

// imapError is a command the server refused (NO or BAD).
type imapError struct {
	Kind, Text string
}

func (e *imapError) Error() string { return "the mail server answered " + e.Kind + ": " + e.Text }

// authFailed is whether err is the server refusing the login.
func authFailed(err error) bool {
	var ie *imapError
	if !errors.As(err, &ie) {
		return false
	}
	t := strings.ToUpper(ie.Text)
	return strings.Contains(t, "AUTHENTICATIONFAILED") || strings.Contains(t, "INVALID CREDENTIALS") ||
		strings.Contains(t, "AUTHORIZATIONFAILED") || strings.Contains(t, "WEBLOGINREQUIRED")
}

type imapConn struct {
	nc   net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	tag  int
	caps map[string]bool
	// utf8 is whether UTF8=ACCEPT is on (RFC 6855): quoted strings and
	// mailbox names are UTF-8.
	utf8 bool
	// broken is whether the connection failed (not a command the server
	// refused): it is not used again.
	broken bool
}

// newConn reads the greeting of a connection just opened.
func newConn(ctx context.Context, nc net.Conn) (*imapConn, error) {
	c := &imapConn{nc: nc, r: bufio.NewReaderSize(nc, 64<<10), w: bufio.NewWriter(nc), caps: map[string]bool{}}
	stop := c.watch(ctx)
	greet, err := c.read()
	stop()
	if err != nil {
		c.close()
		return nil, fmt.Errorf("the mail server sent no greeting: %w", err)
	}
	if greet.Tag != "*" || (greet.Kind != "OK" && greet.Kind != "PREAUTH") {
		c.close()
		return nil, fmt.Errorf("the mail server greeted with %s %s", greet.Kind, greet.Text)
	}
	return c, nil
}

// watch sets the connection's deadline from ctx (cmdTimeout without one)
// and ends what is waiting when ctx is done; stop it when the command is
// over. The connection outlives ctx: it may serve the next call.
func (c *imapConn) watch(ctx context.Context) (stop func() bool) {
	if d, ok := ctx.Deadline(); ok {
		_ = c.nc.SetDeadline(d)
	} else {
		_ = c.nc.SetDeadline(time.Now().Add(cmdTimeout))
	}
	return context.AfterFunc(ctx, func() { _ = c.nc.SetDeadline(time.Unix(1, 0)) })
}

// close says LOGOUT, without waiting for the answer, and closes the
// connection.
func (c *imapConn) close() {
	if c == nil || c.nc == nil {
		return
	}
	if c.tag > 0 && !c.broken {
		_ = c.nc.SetDeadline(time.Now().Add(2 * time.Second))
		c.tag++
		c.w.WriteString("S" + strconv.Itoa(c.tag) + " LOGOUT\r\n")
		_ = c.w.Flush()
	}
	c.broken = true
	c.nc.Close()
}

// str is s as a command argument: a quoted string when it may be one, a
// literal otherwise.
func (c *imapConn) str(s string) any {
	if strings.ContainsAny(s, "\r\n\x00") {
		return literal(s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 && !c.utf8 {
			return literal(s)
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// run sends a command and reads its responses up to the tagged one, which
// is the last of those it answers (its text has the response code). Parts
// are raw text (string) or literals; a space goes between them. A command
// the server refuses is an *imapError.
func (c *imapConn) run(ctx context.Context, parts ...any) ([]*response, error) {
	if c.broken {
		return nil, errors.New("the connection to the mail server was lost")
	}
	stop := c.watch(ctx)
	rs, err := c.exec(parts...)
	stop()
	var refused *imapError
	if err != nil && !errors.As(err, &refused) {
		c.broken = true
		if ctx.Err() != nil {
			err = fmt.Errorf("the mail server did not answer in time: %w", ctx.Err())
		}
	}
	return rs, err
}

func (c *imapConn) exec(parts ...any) ([]*response, error) {
	c.tag++
	tag := "S" + strconv.Itoa(c.tag)
	var untagged []*response
	c.w.WriteString(tag)
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			c.w.WriteByte(' ')
			c.w.WriteString(v)
		case literal:
			fmt.Fprintf(c.w, " {%d}\r\n", len(v))
			if err := c.w.Flush(); err != nil {
				return nil, err
			}
			for {
				r, err := c.read()
				if err != nil {
					return nil, err
				}
				if r.Tag == "+" {
					break
				}
				if r.Tag == tag {
					return nil, &imapError{Kind: r.Kind, Text: r.Text}
				}
				untagged = append(untagged, r)
			}
			c.w.Write(v)
		default:
			return nil, fmt.Errorf("imap: an argument of type %T", p)
		}
	}
	c.w.WriteString("\r\n")
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	for {
		r, err := c.read()
		if err != nil {
			return nil, err
		}
		switch {
		case r.Tag == tag:
			if r.Kind != "OK" {
				return untagged, &imapError{Kind: r.Kind, Text: r.Text}
			}
			return append(untagged, r), nil
		case r.Tag == "*" && r.Kind == "BYE":
			// The server says goodbye; its tagged answer may still come
			// (LOGOUT), or the connection ends.
			untagged = append(untagged, r)
		default:
			untagged = append(untagged, r)
		}
	}
}

// read reads one response, with its literals.
func (c *imapConn) read() (*response, error) {
	var text []byte
	var lits [][]byte
	for {
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		n, start := literalTail(line)
		if n < 0 {
			text = append(text, line...)
			break
		}
		if n > maxLiteral {
			return nil, fmt.Errorf("the mail server sent %d bytes at once, more than Socrates takes", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		text = append(append(text, line[:start]...), 0)
		lits = append(lits, buf)
	}
	return parseResponse(string(text), lits)
}

// readLine is a line without its CRLF.
func (c *imapConn) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		line = append(line, chunk...)
		if err == nil {
			break
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
		if len(line) > 1<<20 {
			return nil, errors.New("the mail server sent a line too long")
		}
	}
	return bytes.TrimRight(line, "\r\n"), nil
}

// literalTail is the size of the literal a line announces at its end
// ({123} or {123+}) and where the announcement starts; -1 for none.
func literalTail(line []byte) (int, int) {
	if len(line) < 3 || line[len(line)-1] != '}' {
		return -1, 0
	}
	open := bytes.LastIndexByte(line, '{')
	if open < 0 {
		return -1, 0
	}
	digits := strings.TrimSuffix(string(line[open+1:len(line)-1]), "+")
	n, err := strconv.Atoi(digits)
	if err != nil || n < 0 {
		return -1, 0
	}
	return n, open
}

// parseResponse reads a response whose literals were taken out: each is a
// NUL in text, in order.
func parseResponse(text string, lits [][]byte) (*response, error) {
	tag, rest, _ := strings.Cut(text, " ")
	r := &response{Tag: tag}
	if tag == "+" {
		r.Kind, r.Text = "+", restore(rest, lits)
		return r, nil
	}
	word, after, _ := strings.Cut(rest, " ")
	if tag == "*" {
		if n, err := strconv.ParseUint(word, 10, 32); err == nil {
			r.Num = uint32(n)
			word, after, _ = strings.Cut(after, " ")
		}
	}
	r.Kind = strings.ToUpper(word)
	switch r.Kind {
	case "OK", "NO", "BAD", "BYE", "PREAUTH":
		r.Text = restore(after, lits)
		return r, nil
	}
	if tag != "*" {
		return nil, fmt.Errorf("the mail server answered %q", text)
	}
	p := &parser{s: after, lits: lits}
	vals, err := p.list(0)
	if err != nil {
		return nil, fmt.Errorf("the mail server's %s answer could not be read: %w", r.Kind, err)
	}
	r.Values = vals
	return r, nil
}

func restore(s string, lits [][]byte) string {
	if !strings.Contains(s, "\x00") {
		return s
	}
	var b strings.Builder
	for i, part := range strings.Split(s, "\x00") {
		if i > 0 && i-1 < len(lits) {
			b.Write(lits[i-1])
		}
		b.WriteString(part)
	}
	return b.String()
}

type parser struct {
	s    string
	i    int
	lits [][]byte
	li   int
}

// list reads values up to the end, or up to the ')' that closes a list
// (end ')').
func (p *parser) list(end byte) ([]any, error) {
	out := []any{}
	for {
		for p.i < len(p.s) && p.s[p.i] == ' ' {
			p.i++
		}
		if p.i >= len(p.s) {
			if end != 0 {
				return nil, errors.New("a list is not closed")
			}
			return out, nil
		}
		if end != 0 && p.s[p.i] == end {
			p.i++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func (p *parser) value() (any, error) {
	switch c := p.s[p.i]; c {
	case '(':
		p.i++
		return p.list(')')
	case ')':
		return nil, errors.New("a list closes that was not opened")
	case '"':
		p.i++
		var b strings.Builder
		for p.i < len(p.s) {
			ch := p.s[p.i]
			p.i++
			switch ch {
			case '\\':
				if p.i < len(p.s) {
					b.WriteByte(p.s[p.i])
					p.i++
				}
			case '"':
				return b.String(), nil
			default:
				b.WriteByte(ch)
			}
		}
		return nil, errors.New("a quoted string is not closed")
	case 0:
		p.i++
		if p.li >= len(p.lits) {
			return nil, errors.New("a literal is missing")
		}
		p.li++
		return string(p.lits[p.li-1]), nil
	}
	start, depth := p.i, 0
	for p.i < len(p.s) {
		ch := p.s[p.i]
		if depth == 0 && (ch == ' ' || ch == '(' || ch == ')') {
			break
		}
		if ch == '[' {
			depth++
		} else if ch == ']' && depth > 0 {
			depth--
		}
		p.i++
	}
	atom := p.s[start:p.i]
	if strings.EqualFold(atom, "NIL") {
		return nil, nil
	}
	return atom, nil
}

// ---------------------------------------------------------------- values

// asString is a value as a string ("" for NIL or a list).
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// asStrings is a list of values as strings.
func asStrings(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// fetchItems is a FETCH response's items by name, in capitals ("UID",
// "X-GM-LABELS", "BODY[]" …).
func fetchItems(r *response) map[string]any {
	out := map[string]any{}
	if len(r.Values) == 0 {
		return out
	}
	l, _ := r.Values[0].([]any)
	for i := 0; i+1 < len(l); i += 2 {
		out[strings.ToUpper(asString(l[i]))] = l[i+1]
	}
	return out
}

// bodyItem is the first BODY[…] item of a fetch.
func bodyItem(items map[string]any) string {
	for k, v := range items {
		if strings.HasPrefix(k, "BODY[") {
			return asString(v)
		}
	}
	return ""
}

func uintOf(v any) uint64 {
	n, _ := strconv.ParseUint(asString(v), 10, 64)
	return n
}

// uidSet is uids as a sequence set (1,5,9).
func uidSet(uids []uint32) string {
	parts := make([]string, len(uids))
	for i, u := range uids {
		parts[i] = strconv.FormatUint(uint64(u), 10)
	}
	return strings.Join(parts, ",")
}
