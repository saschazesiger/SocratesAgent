package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// frame is one line of the protocol, in either direction. Which of the four
// shapes it is follows from which fields are set:
//
//	method + id  a ServerRequest, which must be answered (F-8)
//	method       a notification
//	id + result  the answer to one of our requests
//	id + error   the refusal of one of our requests
type frame struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is JSON-RPC's error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("codex reported error %d", e.Code)
	}
	return e.Message
}

// errClosed is what a call gets when the process is gone.
var errClosed = errors.New("the codex app-server is not running")

// rpc is the newline-delimited JSON-RPC 2.0 conversation with one
// `codex app-server` process: request/response correlation on one side and a
// stream of notifications and ServerRequests on the other.
type rpc struct {
	w   io.Writer
	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *frame
	closed  bool
	err     error
}

func newRPC(w io.Writer) *rpc {
	return &rpc{w: w, pending: map[int64]chan *frame{}}
}

// call sends a request and waits for its answer.
func (r *rpc) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	r.mu.Lock()
	if r.closed {
		err := r.err
		r.mu.Unlock()
		if err == nil {
			err = errClosed
		}
		return nil, err
	}
	r.nextID++
	id := r.nextID
	ch := make(chan *frame, 1)
	r.pending[id] = ch
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	}()

	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if err := r.write(frame{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: method, Params: raw}); err != nil {
		return nil, err
	}

	select {
	case fr := <-ch:
		if fr == nil {
			return nil, errClosed
		}
		if fr.Error != nil {
			return nil, fmt.Errorf("codex refused %s: %w", method, fr.Error)
		}
		return fr.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// notify sends a notification, which has no answer.
func (r *rpc) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return r.write(frame{JSONRPC: "2.0", Method: method, Params: raw})
}

// respondError answers a ServerRequest with a JSON-RPC error. An error is a
// valid response to *every* request method, whereas a decision object is only
// the shape one particular approval request expects, so this is the one reply
// that can never itself be malformed (F-8).
func (r *rpc) respondError(id json.RawMessage, code int, message string) error {
	return r.write(frame{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (r *rpc) write(f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	r.wmu.Lock()
	defer r.wmu.Unlock()
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return errClosed
	}
	if _, err := r.w.Write(b); err != nil {
		return err
	}
	return nil
}

// deliver hands a response frame to whoever is waiting for it.
func (r *rpc) deliver(fr *frame) {
	var id int64
	if err := json.Unmarshal(fr.ID, &id); err != nil {
		return
	}
	r.mu.Lock()
	ch := r.pending[id]
	delete(r.pending, id)
	r.mu.Unlock()
	if ch != nil {
		ch <- fr
	}
}

// shutdown fails every call in flight and every later one, so nothing waits
// on a process that has gone.
func (r *rpc) shutdown(err error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.err = err
	pending := r.pending
	r.pending = map[int64]chan *frame{}
	r.mu.Unlock()
	for _, ch := range pending {
		ch <- nil
	}
}

// scanLines returns a scanner sized for codex's frames: one turn's item can
// carry a whole diff, so the 64 KiB default is far too small.
func scanLines(rd io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return sc
}

// tail keeps the last few kilobytes written to it, which is all anyone wants
// of a dead process's stderr.
type tail struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newTail(limit int) *tail { return &tail{limit: limit} }

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
