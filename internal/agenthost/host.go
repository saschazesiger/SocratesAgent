package term

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// hostLinger is how long a finished session keeps answering, so that a caller
// which attaches late still sees how it ended.
const hostLinger = 30 * time.Second

// File names inside a session directory.
const (
	fileSpec  = "spec.json"
	fileSock  = "sock"
	fileFinal = "final.json"
	fileLog   = "host.log"
)

// Final is what a host leaves behind when its program has exited, so that a
// Socrates that was not running at the time can still show what happened.
type Final struct {
	State   State  `json:"state"`
	Output  string `json:"output"`
	EndedAt int64  `json:"ended_at"`
}

// RunHost is the "socrates term-host" subcommand: it owns one session and
// serves it on a unix socket until the program exits. It is started detached
// from Socrates on purpose - that is what lets a long running agent keep
// working across a restart of the web server.
func RunHost(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, fileSpec))
	if err != nil {
		return fmt.Errorf("read session spec: %w", err)
	}
	var spec hostSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fmt.Errorf("parse session spec: %w", err)
	}

	sockPath := filepath.Join(dir, fileSock)
	_ = os.Remove(sockPath)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sockPath, err)
	}
	defer listener.Close()

	session, err := Start(spec.ID, spec.Name, spec.ChatID, Spec{
		Command: spec.Command,
		Args:    spec.Args,
		Dir:     spec.Dir,
		Env:     spec.Env,
		Cols:    spec.Cols,
		Rows:    spec.Rows,
	})
	if err != nil {
		writeFinal(dir, Final{
			State:   State{ID: spec.ID, Name: spec.Name, ChatID: spec.ChatID, ExitCode: -1},
			Output:  "could not start: " + err.Error(),
			EndedAt: time.Now().UnixMilli(),
		})
		return err
	}

	h := &host{dir: dir, session: session, clients: map[*hostClient]struct{}{}}
	go h.broadcast()

	// A host has no reason to outlive the machine's shutdown sequence, but it
	// must not die with the terminal Socrates was started from.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	go func() {
		<-signals
		_ = session.Close(5 * time.Second)
	}()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go h.serve(conn)
		}
	}()

	<-session.Done()
	// The record goes to disk first, so that it is already there by the time
	// anybody can observe that the program has stopped.
	writeFinal(dir, Final{
		State:   session.State(),
		Output:  session.Output(64 << 10),
		EndedAt: time.Now().UnixMilli(),
	})
	h.push()
	// A command can finish faster than Socrates can attach to it - `echo` is
	// over in microseconds. The socket therefore stays open for a while after
	// the program has gone, so that a late arrival still gets the exit code
	// and the last screen instead of a connection error. Callers tell the
	// difference from the state, which says the session is no longer running.
	// A session that was closed on request has already been seen by whoever
	// closed it, so it goes away immediately instead.
	h.mu.Lock()
	asked := h.asked
	h.mu.Unlock()
	if !asked {
		time.Sleep(hostLinger)
	}
	listener.Close()
	_ = os.Remove(sockPath)
	return nil
}

type host struct {
	dir     string
	session *Session

	mu      sync.Mutex
	clients map[*hostClient]struct{}
	asked   bool // someone asked for this session to end
}

type hostClient struct {
	enc *json.Encoder
	mu  sync.Mutex
}

func (c *hostClient) send(resp Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.enc.Encode(resp)
}

// broadcast pushes the screen to every connected client whenever it changes,
// at most ten times a second so a chatty program cannot flood the socket.
func (h *host) broadcast() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	last := int64(-1)
	for {
		select {
		case <-h.session.Done():
			return
		case <-ticker.C:
			state := h.session.State()
			if state.Revision == last {
				continue
			}
			last = state.Revision
			h.sendAll(Response{Type: TypeUpdate, State: &state})
		}
	}
}

func (h *host) push() {
	state := h.session.State()
	h.sendAll(Response{Type: TypeUpdate, State: &state})
}

func (h *host) sendAll(resp Response) {
	h.mu.Lock()
	clients := make([]*hostClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.send(resp)
	}
}

func (h *host) serve(conn net.Conn) {
	defer conn.Close()
	client := &hostClient{enc: json.NewEncoder(conn)}
	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
	}()

	// A fresh connection gets the current screen straight away, which is what
	// makes reconnecting after a restart show the live session immediately.
	state := h.session.State()
	client.send(Response{Type: TypeUpdate, State: &state})

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			client.send(Response{Type: TypeError, Error: "malformed request: " + err.Error()})
			continue
		}
		// Waiting operations must not block the other commands on this
		// connection, so they answer from their own goroutine.
		switch req.Op {
		case OpIdle, OpWait:
			go client.send(h.handle(req))
		default:
			client.send(h.handle(req))
		}
	}
}

func (h *host) handle(req Request) Response {
	reply := func(r Response) Response {
		r.ID = req.ID
		return r
	}
	fail := func(err error) Response {
		return reply(Response{Type: TypeError, Error: err.Error()})
	}

	switch req.Op {
	case OpState:
		state := h.session.State()
		return reply(Response{Type: TypeState, State: &state})

	case OpInput:
		if err := h.session.Type(req.Data); err != nil {
			return fail(err)
		}
		return reply(Response{Type: TypeOK})

	case OpKeys:
		if err := h.session.SendKeys(req.Keys); err != nil {
			return fail(err)
		}
		return reply(Response{Type: TypeOK})

	case OpResize:
		if err := h.session.Resize(req.Cols, req.Rows); err != nil {
			return fail(err)
		}
		return reply(Response{Type: TypeOK})

	case OpOutput:
		return reply(Response{Type: TypeOutput, Output: h.session.Output(req.Max)})

	case OpIdle:
		ok, err := h.session.WaitIdle(context.Background(), ms(req.QuietMS), ms(req.LimitMS))
		if err != nil {
			return fail(err)
		}
		state := h.session.State()
		return reply(Response{Type: TypeWaited, Matched: ok, State: &state})

	case OpWait:
		ok, err := h.session.WaitFor(context.Background(), req.Pattern, ms(req.LimitMS))
		if err != nil {
			return fail(err)
		}
		state := h.session.State()
		return reply(Response{Type: TypeWaited, Matched: ok, State: &state})

	case OpSignal:
		if err := h.session.Interrupt(); err != nil {
			return fail(err)
		}
		return reply(Response{Type: TypeOK})

	case OpClose:
		grace := ms(req.LimitMS)
		if grace <= 0 {
			grace = 5 * time.Second
		}
		h.mu.Lock()
		h.asked = true
		h.mu.Unlock()
		if err := h.session.Close(grace); err != nil {
			return fail(err)
		}
		return reply(Response{Type: TypeOK})
	}
	return fail(errors.New("unknown operation " + req.Op))
}

func ms(v int) time.Duration { return time.Duration(v) * time.Millisecond }

func writeFinal(dir string, f Final) {
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, fileFinal), raw, 0o600)
}
