package agenthost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// hostLinger is how long a finished session keeps answering, so that a caller
// which attaches late still sees how it ended.
const hostLinger = 30 * time.Second

// writeDeadline is how long a broadcast waits on one connection before that
// connection is dropped. A broadcast happens under the journal lock, so a
// frozen peer - SIGSTOP, a wedged database, a deadlock somewhere else - would
// otherwise stall the host, and through it the agent, for as long as it stays
// frozen. A dropped peer reconnects and replays; that path is exercised on
// every restart anyway.
const writeDeadline = 30 * time.Second

// The restart budget. A CLI that dies mid-turn must not cost the user their
// chat, so the next send restarts it with resume - but a crash loop has to end
// somewhere, and a chat that says what happened is better than one that
// quietly stalls.
const (
	maxRestarts   = 3
	restartWindow = 5 * time.Minute
)

// File names inside a host directory.
const (
	fileSpec   = "spec.json"
	fileSock   = "sock"
	fileFinal  = "final.json"
	fileLog    = "host.log"
	fileEvents = "events.jsonl"
)

// RunHost is the "socrates agent-host" subcommand: it owns one agent session
// and serves it on a unix socket. It is started detached from Socrates on
// purpose - that is what lets a turn in flight, and everything the agent
// spawned, keep working across a restart of the web server.
func RunHost(dir string) error {
	spec, ok := readSpec(dir)
	if !ok {
		return fmt.Errorf("read the host spec in %s", dir)
	}
	desc, known := harness.Get(spec.Spec.Agent)
	if !known {
		writeFinal(dir, Final{
			Status:  Status{ID: spec.ID, ChatID: spec.ChatID(), Agent: spec.Spec.Agent, Error: "unknown agent " + spec.Spec.Agent},
			EndedAt: time.Now().UnixMilli(),
		})
		return fmt.Errorf("unknown agent %q", spec.Spec.Agent)
	}

	sockPath := spec.Socket
	if sockPath == "" {
		sockPath = filepath.Join(dir, fileSock)
	}
	_ = os.Remove(sockPath)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		writeFinal(dir, Final{
			Status:  Status{ID: spec.ID, ChatID: spec.ChatID(), Agent: spec.Spec.Agent, Error: err.Error()},
			EndedAt: time.Now().UnixMilli(),
		})
		return fmt.Errorf("listen on %s: %w", sockPath, err)
	}
	defer listener.Close()
	// The socket lives outside the host directory so a long data directory
	// cannot blow the sun_path limit, so it is removed here rather than by
	// whoever removes the directory.
	defer os.Remove(sockPath)
	if sockPath != filepath.Join(dir, fileSock) {
		// A human looking at the directory should still find the socket.
		_ = os.Remove(filepath.Join(dir, fileSock))
		_ = os.Symlink(sockPath, filepath.Join(dir, fileSock))
	}

	j, err := openJournal(dir)
	if err != nil {
		return fmt.Errorf("open the journal: %w", err)
	}
	defer j.close()

	h := &host{
		dir:     dir,
		spec:    spec,
		desc:    desc,
		journal: j,
		clients: map[*hostClient]struct{}{},
		conns:   map[*hostClient]struct{}{},
		done:    make(chan struct{}),
		status: Status{
			ID: spec.ID, ChatID: spec.ChatID(), Agent: spec.Spec.Agent,
			Model: spec.Spec.Model, Effort: spec.Spec.Effort, Cwd: spec.Spec.Cwd,
			SessionID: spec.Spec.SessionID, Seq: j.seq, Started: spec.Created,
		},
	}

	adapter := desc.New()
	// Close on every exit path is what stops the CLI and, through the process
	// group proc.Configure gives it, everything it spawned. A host that is
	// SIGKILLed skips this and leaves the CLI orphaned; that is accepted, the
	// alternative being a third process to go wrong.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = adapter.Close(ctx, 5*time.Second)
		cancel()
	}()

	startCtx, cancelStart := context.WithTimeout(context.Background(), 2*time.Minute)
	err = adapter.Start(startCtx, spec.Spec)
	cancelStart()
	if err != nil {
		h.appendEvent(harness.Event{Kind: harness.KindFatal, Error: err.Error()})
		h.mu.Lock()
		h.status.Running = false
		h.status.Error = err.Error()
		h.status.Ended = time.Now().UnixMilli()
		final := h.status
		h.mu.Unlock()
		writeFinal(dir, Final{Status: final, EndedAt: time.Now().UnixMilli()})
		return err
	}
	h.setAdapter(adapter)

	go h.pump(adapter)

	// A host has no reason to outlive the machine's shutdown sequence, but it
	// must not die with the terminal Socrates was started from.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	go func() {
		select {
		case <-signals:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = h.current().Close(ctx, 5*time.Second)
			cancel()
		case <-h.done:
		}
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

	<-h.done
	// The record goes to disk first, so that it is already there by the time
	// anybody can observe that the session has stopped.
	h.mu.Lock()
	final := h.status
	asked := h.asked
	h.mu.Unlock()
	writeFinal(dir, Final{Status: final, EndedAt: time.Now().UnixMilli()})
	// An agent host that merely has no turn running is not finished: the next
	// message has to land in the same session. It is only here at all because
	// the adapter is gone, and a caller that attaches a moment late should
	// still learn how that happened. Someone who asked for the close has
	// already seen the answer, so that path skips the wait.
	if !asked {
		time.Sleep(hostLinger)
	}
	return nil
}

type host struct {
	dir     string
	spec    HostSpec
	desc    harness.Descriptor
	journal *journal

	mu      sync.Mutex
	adapter harness.Adapter
	// clients are the connections that asked to be sent journal events. A
	// connection becomes one inside subscribe, under the journal lock, and not
	// when it is accepted: that is what keeps a replay free of gaps and
	// duplicates.
	clients map[*hostClient]struct{}
	// conns is every attached connection, subscribed or not. It carries one
	// thing only: the unsolicited status push below.
	conns    map[*hostClient]struct{}
	status   Status
	asked    bool
	restarts []time.Time
	// pendingNotice is journaled inside the next turn rather than before it,
	// so the sentence explaining a restart lands where the user can see it.
	pendingNotice string
	doneOnce      sync.Once
	done          chan struct{}
}

func (h *host) current() harness.Adapter {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.adapter
}

func (h *host) setAdapter(a harness.Adapter) {
	h.mu.Lock()
	h.adapter = a
	h.status.Running = true
	h.status.Error = ""
	h.status.Ended = 0
	h.mu.Unlock()
	h.broadcastStatus()
}

// broadcastStatus pushes the current Status to every attached connection,
// unsolicited and with id 0. It is sent when Running changes and at no other
// time: an adapter that has died is not otherwise observable to a client that
// is not subscribed - there is no event for it - and Manager.Prune,
// Handle.Alive and the engine's ensureHost all read that flag. It goes to
// every connection rather than to the subscribers, because it is not part of
// the ordered journal stream and carries no seq.
func (h *host) broadcastStatus() {
	h.mu.Lock()
	st := h.status
	conns := make([]*hostClient, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		if !c.send(Response{Type: TypeStatus, Status: &st}) {
			h.drop(c)
		}
	}
}

func (h *host) finish() { h.doneOnce.Do(func() { close(h.done) }) }

// pump is the one writer of the journal and of Status. It ranges over the
// adapter's events and, for each one, appends and broadcasts under the journal
// lock - which is what makes the subscribe rule below true.
func (h *host) pump(a harness.Adapter) {
	for ev := range a.Events() {
		h.appendEvent(ev)
	}
	// The adapter is finished. The host is not: on the next send it is
	// restarted with resume, because a crashed CLI must not cost the user
	// their chat.
	h.mu.Lock()
	h.status.Running = false
	h.status.Busy = false
	h.status.TurnID = ""
	if h.status.Error == "" {
		h.status.Error = "the agent stopped"
	}
	h.status.Ended = time.Now().UnixMilli()
	asked := h.asked
	h.mu.Unlock()
	h.broadcastStatus()
	if asked {
		h.finish()
	}
}

// appendEvent is append + broadcast + Status update, as one indivisible step
// against the journal.
func (h *host) appendEvent(ev harness.Event) harness.Event {
	h.journal.mu.Lock()
	stamped, err := h.journal.append(ev)
	if err != nil {
		h.journal.mu.Unlock()
		log.Printf("agent host: journal: %v", err)
		return stamped
	}
	h.noteStatus(stamped)
	clients := h.snapshotClients()
	for _, c := range clients {
		if !c.send(Response{Type: TypeEvent, Event: &stamped}) {
			h.drop(c)
		}
	}
	h.journal.mu.Unlock()

	if stamped.Kind == harness.KindSessionID && stamped.Session != "" {
		h.rememberSession(stamped.Session)
	}
	return stamped
}

// noteStatus is called with the journal lock held, which is also how a
// subscribe snapshots a Status that is exactly as old as the last event it was
// sent - and why OpStatus reads it under the same lock.
func (h *host) noteStatus(ev harness.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.Seq = ev.Seq
	switch ev.Kind {
	case harness.KindTurnStarted:
		h.status.Busy = true
		h.status.TurnID = ev.TurnID
	case harness.KindTurnFinished:
		h.status.Busy = false
		h.status.TurnID = ""
	case harness.KindSessionID:
		if ev.Session != "" {
			h.status.SessionID = ev.Session
		}
	case harness.KindFatal:
		h.status.Error = ev.Error
	}
}

// rememberSession writes the native session id into spec.json, so a host
// restarted by hand - or an adapter restarted after a crash - resumes rather
// than starting a fresh conversation. Written once per lifetime, atomically.
func (h *host) rememberSession(session string) {
	h.mu.Lock()
	if h.spec.Spec.SessionID == session {
		h.mu.Unlock()
		return
	}
	h.spec.Spec.SessionID = session
	spec := h.spec
	h.mu.Unlock()
	if err := writeSpec(h.dir, spec); err != nil {
		log.Printf("agent host: could not record the session id: %v", err)
	}
}

func (h *host) snapshotClients() []*hostClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*hostClient, 0, len(h.clients))
	for c := range h.clients {
		out = append(out, c)
	}
	return out
}

func (h *host) drop(c *hostClient) {
	h.mu.Lock()
	delete(h.clients, c)
	delete(h.conns, c)
	h.mu.Unlock()
	_ = c.conn.Close()
}

type hostClient struct {
	conn net.Conn
	enc  *json.Encoder
	mu   sync.Mutex
}

// send writes one frame under this connection's encoder mutex and reports
// whether the peer is still worth keeping. A write that misses its deadline is
// a frozen peer, and the host keeps serving everyone else.
func (c *hostClient) send(resp Response) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	err := c.enc.Encode(resp)
	_ = c.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		return false
	}
	return true
}

func (h *host) serve(conn net.Conn) {
	client := &hostClient{conn: conn, enc: json.NewEncoder(conn)}
	h.mu.Lock()
	h.conns[client] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, client)
		delete(h.conns, client)
		h.mu.Unlock()
		_ = conn.Close()
	}()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64<<10), 32<<20)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			client.send(Response{Type: TypeError, Error: "malformed request: " + err.Error()})
			continue
		}
		if !h.handle(client, req) {
			return
		}
	}
}

// handle answers one request and reports whether the connection lives on.
func (h *host) handle(client *hostClient, req Request) bool {
	reply := func(r Response) bool {
		r.ID = req.ID
		return client.send(r)
	}
	fail := func(err error) bool {
		return reply(Response{Type: TypeError, Error: err.Error()})
	}

	switch req.Op {
	case OpSubscribe:
		return h.subscribe(client, req)

	case OpSend:
		seq, err := h.send(req)
		if err != nil {
			return fail(err)
		}
		return reply(Response{Type: TypeOK, Seq: seq})

	case OpInterrupt:
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := h.current().Interrupt(ctx)
		cancel()
		if err != nil {
			return fail(err)
		}
		return reply(Response{Type: TypeOK})

	case OpStatus:
		st := h.statusSnapshot()
		return reply(Response{Type: TypeStatus, Status: &st})

	case OpClose:
		grace := time.Duration(req.GraceMS) * time.Millisecond
		if grace <= 0 {
			grace = 5 * time.Second
		}
		h.mu.Lock()
		h.asked = true
		a := h.adapter
		h.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), grace+10*time.Second)
		err := a.Close(ctx, grace)
		cancel()
		ok := reply(Response{Type: TypeOK})
		if err != nil {
			log.Printf("agent host: close: %v", err)
		}
		// A close on an adapter that is already finished has nothing left to
		// end the pump for, so the host is released here as well.
		h.mu.Lock()
		running := h.status.Running
		h.mu.Unlock()
		if !running {
			h.finish()
		}
		return ok
	}
	return fail(errors.New("unknown operation " + req.Op))
}

// statusSnapshot reads Status under the journal lock. The pump writes those
// fields while holding it, so a lock-free read here would be a data race - and
// a Status that is not aligned with the events already sent is exactly the
// thing the adopt guard must not be handed.
func (h *host) statusSnapshot() Status {
	h.journal.mu.Lock()
	defer h.journal.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

// subscribe registers the connection, replays the journal and answers with the
// Status that closes the replay - all under journal.mu.
//
// Every event the pump appends afterwards therefore has a higher seq than
// anything replayed and is delivered to this connection in order: nothing is
// delivered twice and nothing falls between. Registering on accept would send
// live events during the replay and deliver everything appended before EOF
// twice; registering after the replay would lose whatever was appended in
// between - and if that is the turn's turn_finished, a run waits forever.
//
// The lock is held for the length of one replay - at most what rotation
// leaves - which is why every connection has a goroutine of its own: other
// connections are never blocked by a replay, only the pump, and only briefly.
func (h *host) subscribe(client *hostClient, req Request) bool {
	h.journal.mu.Lock()
	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	err := h.journal.replay(req.FromSeq, func(ev harness.Event) bool {
		return client.send(Response{Type: TypeEvent, Event: &ev})
	})
	if err != nil {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
		h.journal.mu.Unlock()
		return client.send(Response{ID: req.ID, Type: TypeError, Error: err.Error()})
	}
	h.mu.Lock()
	st := h.status
	h.mu.Unlock()
	ok := client.send(Response{ID: req.ID, Type: TypeStatus, Status: &st})
	if !ok {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
	}
	h.journal.mu.Unlock()
	return ok
}

// send delivers one user message. The reply's Seq is the journal position the
// turn starts after, which is what makes replaying a whole turn possible.
func (h *host) send(req Request) (int64, error) {
	h.mu.Lock()
	busy := h.status.Busy
	running := h.status.Running
	h.mu.Unlock()
	if busy {
		return 0, errors.New("this chat is still working on the previous message")
	}
	if !running {
		if err := h.restart(); err != nil {
			return 0, err
		}
	}

	// The floor is read under the journal lock so that no event can slip in
	// between reading it and the turn beginning.
	h.journal.mu.Lock()
	floor := h.journal.seq
	h.journal.mu.Unlock()

	// A notice about a restart is journaled after the floor snapshot on
	// purpose: it belongs inside the turn it explains, not before it, or the
	// engine's `Seq <= floor` filter would drop it.
	h.mu.Lock()
	notice := h.pendingNotice
	h.pendingNotice = ""
	h.mu.Unlock()
	if notice != "" {
		h.appendEvent(harness.Event{Kind: harness.KindNotice, TurnID: req.TurnID, Error: notice})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := h.current().Send(ctx, req.TurnID, req.Text); err != nil {
		return 0, err
	}
	return floor, nil
}

// restart brings the adapter back after the CLI died, resuming the native
// session that spec.json now carries. Rate limited: a crash loop ends with an
// error the chat can show rather than a host that quietly retries forever.
func (h *host) restart() error {
	h.mu.Lock()
	now := time.Now()
	kept := h.restarts[:0]
	for _, t := range h.restarts {
		if now.Sub(t) < restartWindow {
			kept = append(kept, t)
		}
	}
	h.restarts = kept
	if len(h.restarts) >= maxRestarts {
		h.mu.Unlock()
		h.finish()
		return fmt.Errorf("the agent has crashed %d times in %s and is not being restarted again",
			maxRestarts, restartWindow)
	}
	h.restarts = append(h.restarts, now)
	spec := h.spec.Spec
	h.mu.Unlock()

	a := h.desc.New()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	err := a.Start(ctx, spec)
	cancel()
	if err != nil {
		return err
	}
	h.setAdapter(a)
	h.mu.Lock()
	h.pendingNotice = "the agent was restarted and the conversation resumed"
	h.mu.Unlock()
	go h.pump(a)
	return nil
}

func writeFinal(dir string, f Final) {
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, fileFinal), raw, 0o600)
}
