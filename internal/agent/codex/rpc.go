package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// frame is one line of the app-server protocol, either way. Its shape says
// what it is:
//
//	method + id   a request from the server, which must be answered
//	method        a notification
//	id + result   the answer to one of ours
//	id + error    the refusal of one of ours
//
// The app-server writes no "jsonrpc" field and needs none.
type frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// rpcError is JSON-RPC's error object. Codex puts a TurnError in Data for the
// refusals that have one (e.g. a turn that cannot be steered).
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("codex error %d", e.Code)
	}
	return e.Message
}

// errClosed is a call made to an app-server that is gone.
var errClosed = errors.New("the codex app-server is not running")

// conn is the JSON-RPC conversation with one app-server: requests and their
// answers, notifications and server requests handed to a handler on the one
// reading goroutine, in the order they arrived.
type conn struct {
	w   io.Writer
	wmu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[int64]chan *frame
	err     error
}

func newConn(w io.Writer) *conn { return &conn{w: w, pending: map[int64]chan *frame{}} }

// call sends a request and waits for its answer.
func (c *conn) call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.next++
	id := c.next
	ch := make(chan *frame, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if params == nil {
		params = struct{}{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := c.write(frame{ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case fr := <-ch:
		if fr == nil {
			c.mu.Lock()
			err := c.err
			c.mu.Unlock()
			return err
		}
		if fr.Error != nil {
			return fr.Error
		}
		if result != nil && len(fr.Result) > 0 {
			if err := json.Unmarshal(fr.Result, result); err != nil {
				return fmt.Errorf("codex answered %s with something unreadable: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *conn) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(frame{Method: method, Params: raw})
}

// reply answers a server request.
func (c *conn) reply(id json.RawMessage, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return c.write(frame{ID: id, Result: raw})
}

// refuse answers a server request with an error, which is a valid answer to
// every request method.
func (c *conn) refuse(id json.RawMessage, code int, msg string) error {
	return c.write(frame{ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (c *conn) write(f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.mu.Lock()
	err = c.err
	c.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// read is the reading loop: every frame of r, answers to the waiting calls,
// the rest to handle. It returns at the end of r.
func (c *conn) read(r io.Reader, handle func(*frame)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var fr frame
		if json.Unmarshal(line, &fr) != nil {
			continue
		}
		if fr.Method == "" && len(fr.ID) > 0 {
			c.deliver(&fr)
			continue
		}
		handle(&fr)
	}
}

func (c *conn) deliver(fr *frame) {
	id, err := strconv.ParseInt(string(fr.ID), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- fr
	}
}

// close fails every call in flight and every later one with err.
func (c *conn) close(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

// tail keeps the end of what is written to it: a dead process's last words.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailSize = 8 << 10

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailSize {
		t.buf = t.buf[len(t.buf)-tailSize:]
	}
	return len(p), nil
}

// last is the last non-empty line written.
func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
