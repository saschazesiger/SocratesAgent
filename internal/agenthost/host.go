package agenthost

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// ErrRunning is a host started for a chat that already has one. It is not a
// failure: the one that is there is the one to talk to.
var ErrRunning = errors.New("another agent host already runs this chat")

// DefaultParkAfter is how long a host may be idle before it parks, when the
// spec does not say (REBUILD.md §2.1: 20 minutes, a setting).
const DefaultParkAfter = 20 * time.Minute

// The host's clock.
const (
	// helloWait is how long a client has to say hello after connecting.
	helloWait = 10 * time.Second
	// frameWriteWait is the slow-reader guard: a client that cannot take one
	// line in this long is dropped, and reconnects with since.
	frameWriteWait = 30 * time.Second
	// drainWait is how long a host that is going away waits for its clients
	// to have read everything it wrote.
	drainWait = 2 * time.Second
	// startWait bounds the adapter's start, and parkWait its park.
	startWait = 2 * time.Minute
	parkWait  = 30 * time.Second
	// failLinger is how long a host whose CLI could not start stays up, so
	// that the server reads why before the socket goes.
	failLinger = 2 * time.Second
	// maxCommandLine is the longest command a client may send: a message
	// with a long paste in it, not a file.
	maxCommandLine = 16 << 20
)

// Main is the `socrates agent-host` subcommand, and its exit code. It must run
// on the main goroutine of a process that locked its main thread in init: the
// CLI child is forked from that thread, and its parent-death signal is tied to
// it.
func Main(args []string, version string) int {
	fs := flag.NewFlagSet("agent-host", flag.ContinueOnError)
	data := fs.String("data", "", "the Socrates data directory")
	chat := fs.String("chat", "", "the chat to run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *data == "" || *chat == "" {
		fmt.Fprintln(os.Stderr, "socrates agent-host: -data and -chat are required")
		return 2
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.SetPrefix(fmt.Sprintf("agent-host %s[%d] ", *chat, os.Getpid()))
	err := Run(Options{DataDir: *data, ChatID: *chat, Version: version})
	switch {
	case errors.Is(err, ErrRunning):
		log.Print(err)
		return 0
	case err != nil:
		log.Print(err)
		return 1
	}
	return 0
}

// Options is one host to run.
type Options struct {
	DataDir string
	ChatID  string
	// Version is the binary's version, in the hello and the process events.
	Version string
}

// host is one running agent host.
type host struct {
	opts   Options
	spec   Spec
	dir    string
	sock   string
	rec    *agent.Recorder
	ln     net.Listener
	spawns chan spawnReq

	// cmdMu makes commands one at a time: each is journaled and handed to
	// the adapter before the next is looked at, which is what keeps sends in
	// the order they arrived and the dedupe check honest.
	cmdMu   sync.Mutex
	adapter agent.Adapter
	// live is adapter, under mu, for the watch (watch.go), which must not
	// wait for the command lock; setAdapter sets both.
	live agent.Adapter
	// cliPID is the adapter's child, for the hello, which must not wait for
	// the command lock.
	cliPID atomic.Int64

	mu         sync.Mutex
	lastActive time.Time
	parkWanted bool
	ending     bool
	conns      map[*hostConn]struct{}
	parkAfter  time.Duration
	// watch is what the host knows of the running turn's life (watch.go).
	watch watch
	// tools are the asks a local tool's connection waits on (OpAsk), by ask
	// id: the answer goes back on that connection.
	tools map[string]*hostConn
	// account is the account the CLI runs on now, and env its whole
	// environment, the account's key opened into it; a switch changes both
	// (switch.go). pending is a switch waiting for the agent to be done.
	account string
	env     []string
	pending *pendingSwitch
	// mcpSaid is what the chat was last told about its MCP servers
	// (chatMCP), so that a switch does not say it again.
	mcpSaid string

	done     chan struct{}
	doneOnce sync.Once
}

type spawnReq struct {
	cmd  *exec.Cmd
	done chan error
}

// Run is the host from start to exit. It returns once the host has parked,
// stopped, or lost its CLI, and every client has read what it wrote.
func Run(opts Options) error {
	if err := supported(); err != nil {
		return err
	}
	if !ValidChatID(opts.ChatID) {
		return fmt.Errorf("%q is not a chat id", opts.ChatID)
	}
	scrubEnv()
	dir := ChatDir(opts.DataDir, opts.ChatID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := takeLock(filepath.Join(dir, lockName))
	if err != nil {
		return err
	}
	defer lock.Close()
	spec, err := ReadSpec(opts.DataDir, opts.ChatID)
	if err != nil {
		return fmt.Errorf("read the launch spec: %w", err)
	}
	journal, events, err := agent.OpenJournal(JournalPath(opts.DataDir, opts.ChatID))
	if err != nil {
		return fmt.Errorf("open the journal: %w", err)
	}
	h := &host{
		opts:       opts,
		spec:       spec,
		dir:        dir,
		sock:       SocketPath(opts.DataDir, opts.ChatID),
		rec:        agent.NewRecorder(journal, events, 0),
		spawns:     make(chan spawnReq),
		conns:      map[*hostConn]struct{}{},
		tools:      map[string]*hostConn{},
		lastActive: time.Now(),
		watch:      newWatch(spec.Watch),
		parkAfter:  time.Duration(spec.ParkAfterMS) * time.Millisecond,
		done:       make(chan struct{}),
	}
	if h.parkAfter <= 0 {
		h.parkAfter = DefaultParkAfter
	}
	if err := h.recover(); err != nil {
		h.rec.Close()
		return err
	}
	if err := h.listen(); err != nil {
		h.rec.Close()
		return err
	}
	log.Printf("serving %s (%s) on %s", opts.ChatID, spec.Harness, h.sock)
	h.start()
	h.serveSpawns()
	h.shutdown()
	return nil
}

// takeLock is the chat's lock, with a moment's patience for a server that is
// looking at it to tell whether a host is alive.
func takeLock(path string) (*os.File, error) {
	deadline := time.Now().Add(time.Second)
	for {
		f, err := lockFile(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, errLocked) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, ErrRunning
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// scrubEnv removes what systemd gave the scope. A CLI that inherits
// INVOCATION_ID with a parent of PID 1 passes Socrates' own "am I the
// service" test, and NOTIFY_SOCKET and WATCHDOG_* belong to the service (spike
// S4 §6).
func scrubEnv() {
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch {
		case key == "INVOCATION_ID", key == "NOTIFY_SOCKET", key == "JOURNAL_STREAM",
			key == "LISTEN_FDS", key == "LISTEN_PID", key == "LISTEN_FDNAMES", strings.HasPrefix(key, "WATCHDOG_"):
			os.Unsetenv(key)
		}
	}
}

// stoppedContent is a part's content with every "status": "running" - its
// own, or that of an object directly in it (a subagent, a workflow, a
// background task) - turned into "killed", so that what the content says
// agrees with the interrupted part. Anything else is left as it is.
func stoppedContent(raw json.RawMessage) json.RawMessage {
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return raw
	}
	changed := false
	stop := func(m map[string]any) {
		if m["status"] == "running" {
			m["status"], changed = "killed", true
		}
	}
	stop(doc)
	for _, v := range doc {
		if m, ok := v.(map[string]any); ok {
			stop(m)
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// recover writes down what the journal says about the host before this one:
// a turn that was running when it stopped is interrupted, and the chat says
// that it resumed.
func (h *host) recover() error {
	var events []agent.Event
	h.rec.View(func(p *agent.Projection) {
		// A part still running belongs to work that died with the host's
		// CLI: the turn below, or a subagent or workflow that outlived its
		// turn (WP3), whose group would otherwise run for ever. So do the
		// background tasks it counted.
		var closed int
		events, closed = leftovers(p)
		if p.Turn == nil {
			if closed > 0 {
				events = append(events, agent.Event{Type: agent.TypeNotice, Code: agent.NoticeResumed,
					Text: "Resumed after a restart. Background work that was running was stopped."})
			}
			return
		}
		events = append(events,
			agent.Event{Type: agent.TypeTurnEnded, TurnID: p.Turn.ID, Outcome: agent.OutcomeInterrupted,
				Text: "The agent host stopped while this turn was running."},
			agent.Event{Type: agent.TypeNotice, Code: agent.NoticeResumed,
				Text: "Resumed after a restart. The turn that was running was interrupted."})
	})
	if len(events) == 0 {
		return nil
	}
	_, err := h.rec.EmitDurable(events...)
	return err
}

// leftovers is what the journal must say once the CLI it describes is gone:
// every part still running or streaming is interrupted (and what its content
// says still runs, killed), and no background task runs any more - they were
// the process's own. closed is how many parts that ends. The adapters close
// what they know of themselves; this is for what they could not (a host
// that died, an adapter that missed an end).
func leftovers(p *agent.Projection) (events []agent.Event, closed int) {
	var walk func(parts []*agent.Part)
	walk = func(parts []*agent.Part) {
		for _, part := range parts {
			if part.State == agent.PartRunning || part.State == agent.PartStreaming {
				events = append(events, agent.Event{Type: agent.TypePart, MessageID: part.MessageID, PartID: part.ID,
					ParentPartID: part.ParentID, Kind: part.Kind, State: agent.PartInterrupted, Text: part.Text, Content: stoppedContent(part.Content)})
			}
			walk(part.Children)
		}
	}
	for _, m := range p.Messages {
		walk(m.Parts)
	}
	closed = len(events)
	if p.Tasks != 0 || p.Agents != 0 {
		events = append(events, agent.Event{Type: agent.TypeTasks})
	}
	return events, closed
}

// closeLeftovers journals the leftovers of a CLI that is gone.
func (h *host) closeLeftovers() {
	var events []agent.Event
	h.rec.View(func(p *agent.Projection) { events, _ = leftovers(p) })
	if len(events) > 0 {
		h.record(events...)
	}
}

func (h *host) listen() error {
	if err := os.MkdirAll(filepath.Dir(h.sock), 0o700); err != nil {
		return err
	}
	// The lock is ours, so a socket file that is there is a dead host's.
	_ = os.Remove(h.sock)
	ln, err := net.Listen("unix", h.sock)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", h.sock, err)
	}
	_ = os.Chmod(h.sock, 0o600)
	h.ln = ln
	return nil
}

// serveSpawns is the main goroutine's whole job while the host runs: it
// starts the CLI children, because a child's parent-death signal fires when
// the thread that forked it ends, and the main thread is the one thread that
// lives exactly as long as the host.
func (h *host) serveSpawns() {
	for {
		select {
		case req := <-h.spawns:
			req.done <- req.cmd.Start()
		case <-h.done:
			return
		}
	}
}

// spawn is StartSpec.Spawn.
func (h *host) spawn(cmd *exec.Cmd) error {
	childAttr(cmd)
	req := spawnReq{cmd: cmd, done: make(chan error, 1)}
	select {
	case h.spawns <- req:
	case <-h.done:
		return errors.New("the agent host is stopping")
	}
	return <-req.done
}

// start lets clients in and boots the agent. The command lock is boot's from
// before the first connection is accepted: a server sends its outbox the
// moment it connects, and a command handled before boot had the lock found no
// agent and was refused as "not running" - which a server keeps for the next
// host, not this one, so a message sent as a host started stayed undelivered
// for as long as it ran.
func (h *host) start() {
	h.cmdMu.Lock()
	go h.accept()
	go h.boot()
	go h.idleLoop()
	go h.watchLoop()
}

// bootHook is for tests only: it runs as boot begins.
var bootHook func()

// boot starts the adapter, resuming the conversation the journal knows, and
// hands it what the last host could not: messages it never took up, and
// answers that never reached it. Commands wait for it, because it holds the
// command lock, which start took for it: nothing new goes to the agent ahead
// of what was already owed, and nothing finds the agent not there yet.
func (h *host) boot() {
	defer h.cmdMu.Unlock()
	if bootHook != nil {
		bootHook()
	}
	if _, err := h.rec.EmitDurable(agent.Event{Type: agent.TypeProcess, State: agent.ProcessStarting,
		PID: os.Getpid(), Version: h.opts.Version, Detail: h.spec.Harness}); err != nil {
		h.failStart(err)
		return
	}
	factory, ok := agent.Lookup(h.spec.Harness)
	if !ok {
		h.failStart(fmt.Errorf("there is no adapter for %q", h.spec.Harness))
		return
	}
	var resume, held string
	var pending []agent.Outgoing
	var unsent []agent.Ask
	empty := false
	h.rec.View(func(p *agent.Projection) {
		resume, empty = p.ConversationID, len(p.Messages) == 0
		// A host before this one that stopped at the limit of the account
		// this one starts on held what the agent had not taken up; the new
		// adapter holds it too, and the person's next message lets it go.
		held = heldFor(p, h.spec.Account)
		for _, m := range p.Undelivered() {
			pending = append(pending, outgoing(m))
		}
		for _, a := range p.UnsentAnswers() {
			unsent = append(unsent, *a)
		}
	})
	// A journal that names no conversation while the server knows one was
	// lost (or the chat was made from an existing conversation, a fork): the
	// conversation is resumed, and an empty journal gets its history back
	// from the CLI's own transcript (REBUILD.md §2.2).
	rebuild := false
	if resume == "" && h.spec.Conversation != "" {
		resume, rebuild = h.spec.Conversation, empty
	}
	env := h.spec.Env
	if len(env) == 0 {
		env = os.Environ()
	}
	secret, err := h.openSecretEnv(h.spec.SecretEnv)
	if err != nil {
		h.failStart(err)
		return
	}
	// Ultracode is the chat's option, not the account's: it goes on top of
	// the account's environment, here and at every switch.
	env = withUltracode(joinEnv(env, secret), h.spec.Ultracode)
	h.mu.Lock()
	h.account, h.env = h.spec.Account, env
	h.mu.Unlock()
	h.noteBootAccount()
	spec := agent.StartSpec{
		ChatID: h.opts.ChatID, Workdir: h.spec.Workdir, StateDir: filepath.Join(h.dir, stateDir),
		DataDir: h.opts.DataDir, Env: env, Account: h.spec.Account, Model: h.spec.Model, Effort: h.spec.Effort,
		ResumeID: resume, Rebuild: rebuild, Held: held, Spawn: h.spawn, Binary: h.spec.Binary, AskCommand: h.askCommand(),
		MCPServers: h.chatMCP(env),
	}
	if resume == "" && len(pending) > 0 {
		spec.First, pending = &pending[0], pending[1:]
	}
	ad := factory()
	ctx, cancel := context.WithTimeout(context.Background(), startWait)
	defer cancel()
	err = ad.Start(ctx, spec, emitter{h})
	if err != nil && resume != "" && errors.Is(err, agent.ErrConversationNotFound) {
		// The CLI no longer has the conversation (its transcript is gone, or
		// it belongs to an account this chat no longer uses). A host that
		// insisted would fail on every message for ever; a fresh conversation
		// is the way on, and the chat says so.
		log.Printf("the agent does not know conversation %s, starting a new one: %v", resume, err)
		h.record(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeConversationLost,
			Text: "The agent no longer has this conversation, so a new one was started. " +
				"The messages above stay here, but the agent does not remember them."})
		spec.ResumeID, spec.Rebuild = "", false
		if spec.First == nil && len(pending) > 0 {
			spec.First, pending = &pending[0], pending[1:]
		}
		ad = factory()
		err = ad.Start(ctx, spec, emitter{h})
	}
	if err != nil {
		h.failStart(err)
		return
	}
	h.setAdapter(ad)
	h.cliPID.Store(int64(ad.PID()))
	h.record(agent.Event{Type: agent.TypeProcess, State: agent.ProcessReady, PID: ad.PID()})
	for _, msg := range pending {
		if err := ad.Send(ctx, msg); err != nil {
			h.record(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: msg.MessageID, Text: err.Error()})
		}
	}
	for i := range unsent {
		h.deliverAsMessage(ctx, &unsent[i], unsent[i].Answer, unsent[i].Deliver != agent.ViaMessage)
	}
	h.touch()
}

// askCommand is `socrates mcp-ask --socket <this host's socket>`, ask_user as
// a stdio MCP server for a CLI that starts its MCP servers itself (OpenCode):
// this very binary, which is what the host was started as.
func (h *host) askCommand() []string {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("ask_user has no command: %v", err)
		return nil
	}
	return []string{exe, "mcp-ask", "--socket", h.sock}
}

// chatMCP is the MCP servers the chat's CLI starts with on env
// (StartSpec.MCPServers): the dashboard's (Spec.SharedMCP) and its folder's,
// those the chat has on (agent.StartMCP). The folder is read again at every
// start so that a changed .mcp.json counts from the next one. What was left
// out is said in the chat, once for as long as it stays the same.
func (h *host) chatMCP(env []string) []agent.MCPServer {
	shared, err := h.openSharedMCP(h.spec.SharedMCP)
	servers, problems := agent.StartMCP(h.spec.Workdir, shared, h.spec.MCPChoices, env)
	if err != nil {
		problems = append(problems, "The MCP servers of Admin were left out: "+err.Error())
	}
	text := strings.Join(problems, "\n")
	h.mu.Lock()
	say := text != "" && text != h.mcpSaid
	h.mcpSaid = text
	h.mu.Unlock()
	if say {
		h.record(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeMCP, Text: text})
	}
	return servers
}

// failStart records why the CLI did not come up and ends the host after the
// server has had a moment to read it.
func (h *host) failStart(err error) {
	log.Printf("the agent did not start: %v", err)
	h.mu.Lock()
	h.ending = true
	h.mu.Unlock()
	_, _ = h.rec.EmitDurable(agent.Event{Type: agent.TypeProcess, State: agent.ProcessFailed, Detail: err.Error()})
	time.AfterFunc(failLinger, h.finish)
}

func outgoing(m *agent.Message) agent.Outgoing {
	return agent.Outgoing{MessageID: m.ID, ClientID: m.ClientID, Text: m.Text, Attachments: m.Attachments, AnswerTo: m.AnswerTo,
		AfterTurn: m.AfterTurn}
}

// afterTurn is where the journal put message id among the turns: the turn
// that had ended last when it was written (agent.Outgoing.AfterTurn). It is
// fixed when the message is journaled, so a turn that ends before the
// adapter is handed the message does not move it.
func (h *host) afterTurn(id string) string {
	after := ""
	h.rec.View(func(p *agent.Projection) {
		if m := p.Message(id); m != nil {
			after = m.AfterTurn
		}
	})
	return after
}

// emitter is the adapter's Emitter.
type emitter struct{ h *host }

// Emit records the adapter's events. A turn is stamped with the account it
// runs on, which only the host knows (AGENT-HOST.md §2). Each is a sign of
// life (watch.go).
func (e emitter) Emit(events ...agent.Event) {
	for i := range events {
		if events[i].Type == agent.TypeTurnStarted && events[i].Account == "" {
			events[i].Account = e.h.currentAccount()
		}
	}
	if heard := e.h.alive(); heard != nil {
		events = append(heard, events...)
	}
	e.h.record(events...)
}

func (e emitter) Delta(messageID, partID, text string) {
	if heard := e.h.alive(); heard != nil {
		e.h.record(heard...)
	}
	e.h.rec.Delta(messageID, partID, text)
	e.h.touch()
}

func (e emitter) Exited(err error) { go e.h.cliExited(err) }

func (h *host) record(events ...agent.Event) {
	if _, err := h.rec.Emit(events...); err != nil {
		log.Printf("journal: %v", err)
	}
	h.touch()
	for _, ev := range events {
		if ev.Type == agent.TypeTurnEnded {
			// A switch that waits for the agent to be done may be due.
			go h.runPending()
			break
		}
	}
}

func (h *host) touch() {
	h.mu.Lock()
	h.lastActive = time.Now()
	h.mu.Unlock()
}

// beginEnding marks the host as going away, and reports false when it
// already was.
func (h *host) beginEnding() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ending {
		return false
	}
	h.ending = true
	return true
}

func (h *host) isEnding() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ending
}

// cliExited is the CLI ending on its own. The host has nothing left to hold,
// so it records the end and goes; the next command starts a new host, which
// resumes the conversation.
func (h *host) cliExited(err error) {
	if !h.beginEnding() {
		return
	}
	h.cmdMu.Lock()
	defer h.cmdMu.Unlock()
	detail := "the agent exited"
	if err != nil {
		detail = err.Error()
	}
	log.Printf("the agent exited: %s", detail)
	var events []agent.Event
	h.rec.View(func(p *agent.Projection) {
		if p.Turn != nil {
			events = append(events, agent.Event{Type: agent.TypeTurnEnded, TurnID: p.Turn.ID,
				Outcome: agent.OutcomeFailed, Text: "The agent exited during the turn: " + detail})
		}
		// What outlived the turn died with the CLI too.
		left, _ := leftovers(p)
		events = append(events, left...)
	})
	events = append(events, agent.Event{Type: agent.TypeProcess, State: agent.ProcessFailed, Detail: detail})
	_, _ = h.rec.EmitDurable(events...)
	if h.adapter != nil {
		_ = h.adapter.Stop()
	}
	h.finish()
}

// ------------------------------------------------------------- the commands

// handle runs one command and answers it. Every command with a client_id is
// journaled (and synced) before the adapter hears of it, and a client_id the
// journal already has is acknowledged again without being acted on: that is
// what makes a command sent twice - by a server that lost the first ack, or
// by a new server after a crash - arrive once.
func (h *host) handle(c *hostConn, cmd Command) any {
	if cmd.Op == OpStop {
		go h.stop(cmd.ClientID)
		return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op}
	}
	h.cmdMu.Lock()
	defer h.cmdMu.Unlock()
	if h.isEnding() {
		return fail(cmd, CodeStopping, "the agent host is stopping; send it again to the next one")
	}
	h.touch()
	if cmd.ClientID != "" {
		var seq int64
		var seen bool
		h.rec.View(func(p *agent.Projection) { seq, seen = p.Seen(cmd.ClientID) })
		if seen {
			return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op, Seq: seq, Dup: true, Pending: h.isPending(cmd.ClientID)}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	switch cmd.Op {
	case OpSend:
		return h.send(ctx, cmd)
	case OpAnswer:
		return h.answer(ctx, cmd)
	case OpAsk:
		return h.toolAsk(c, cmd)
	case OpInterrupt, OpSetModel, OpCommand, OpPark, OpStopTask:
		return h.control(ctx, cmd)
	case OpSwitchAccount:
		return h.switchCmd(cmd)
	}
	return fail(cmd, CodeBadRequest, "unknown command "+cmd.Op)
}

func fail(cmd Command, code, msg string) Fail {
	return Fail{T: FrameError, ClientID: cmd.ClientID, Op: cmd.Op, Code: code, Msg: msg}
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// journaledHook is for tests only: it runs in send between the message's
// journal write and its handing to the adapter, where the running turn can
// end.
var journaledHook func()

func (h *host) send(ctx context.Context, cmd Command) any {
	if cmd.ClientID == "" {
		return fail(cmd, CodeBadRequest, "a message needs a client_id")
	}
	if h.adapter == nil {
		return fail(cmd, CodeNotRunning, "the agent is not running")
	}
	msg := agent.Event{Type: agent.TypeMessage, MessageID: newID("m_"), Role: agent.RoleUser,
		State: agent.MessagePending, ClientID: cmd.ClientID, Text: cmd.Text, Attachments: cmd.Attachments}
	// A question no call waits on any more is left behind by this message:
	// the person moved on (pressed Stop and wrote something else, or wrote
	// instead of answering a question meant for the next message). It stops
	// counting as waiting; an answer that still comes goes as a message.
	batch := []agent.Event{msg}
	at := 0
	if cmd.Notice != "" {
		batch = []agent.Event{{Type: agent.TypeNotice, Code: cmd.Notice, Text: cmd.Reason}, msg}
		at = 1
	}
	h.rec.View(func(p *agent.Projection) {
		for _, a := range p.Unwaited() {
			batch = append(batch, agent.Event{Type: agent.TypeAskClosed, AskID: a.ID})
		}
	})
	events, err := h.rec.EmitDurable(batch...)
	if err != nil {
		return fail(cmd, CodeFailed, "the message could not be journaled: "+err.Error())
	}
	if journaledHook != nil {
		journaledHook()
	}
	out := agent.Outgoing{MessageID: msg.MessageID, ClientID: cmd.ClientID, Text: cmd.Text, Attachments: cmd.Attachments,
		AfterTurn: h.afterTurn(msg.MessageID)}
	if err := h.adapter.Send(ctx, out); err != nil {
		// Journaled and acknowledged all the same: it stays pending, and the
		// next host hands it to the agent.
		h.record(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: msg.MessageID, Text: err.Error()})
	}
	return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op, Seq: events[at].Seq}
}

func (h *host) answer(ctx context.Context, cmd Command) any {
	if cmd.ClientID == "" || cmd.AskID == "" {
		return fail(cmd, CodeBadRequest, "an answer needs a client_id and an ask_id")
	}
	if len(cmd.Answer) == 0 || !json.Valid(cmd.Answer) {
		return fail(cmd, CodeBadRequest, "an answer must be JSON")
	}
	var ask *agent.Ask
	h.rec.View(func(p *agent.Projection) {
		if a := p.Ask(cmd.AskID); a != nil {
			c := *a
			ask = &c
		}
	})
	if ask == nil {
		return fail(cmd, CodeUnknownAsk, "this chat never asked that question")
	}
	if ask.Answered {
		return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op, AnsweredElsewhere: true, ByDevice: ask.ByDevice}
	}
	events, err := h.rec.EmitDurable(agent.Event{Type: agent.TypeAskAnswered, AskID: ask.ID,
		ClientID: cmd.ClientID, Answer: cmd.Answer, Device: cmd.Device})
	if err != nil {
		return fail(cmd, CodeFailed, "the answer could not be journaled: "+err.Error())
	}
	h.deliver(ctx, ask, cmd.Answer)
	return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op, Seq: events[0].Seq}
}

// toolAsk is a local tool's question (OpAsk, `socrates mcp-ask`): an ask of
// the running turn whose answer goes back on the connection that asked, for
// as long as it stays open. A connection that closes first (the CLI withdrew
// the call, or exited) leaves the ask open; its answer then goes as a message.
// A question outside a turn is refused: the CLI calls its tools only inside
// one, so it comes from a call already withdrawn, whose cancel overtook it.
func (h *host) toolAsk(c *hostConn, cmd Command) any {
	if c == nil || c.proto < AskProtocol {
		return fail(cmd, CodeBadRequest, fmt.Sprintf("ask needs protocol %d", AskProtocol))
	}
	if h.adapter == nil {
		return fail(cmd, CodeNotRunning, "the agent is not running")
	}
	if askuser.Decode(cmd.Payload) == nil {
		return fail(cmd, CodeBadRequest, "an ask needs an ask_user payload")
	}
	ev := agent.Event{Type: agent.TypeAsk, AskID: newID("ask_"), Payload: cmd.Payload}
	h.rec.View(func(p *agent.Projection) {
		if p.Turn != nil {
			ev.TurnID = p.Turn.ID
		}
	})
	if ev.TurnID == "" {
		return fail(cmd, CodeBadRequest, "no turn is running, so nothing can wait for an answer")
	}
	h.mu.Lock()
	h.tools[ev.AskID] = c
	h.mu.Unlock()
	events, err := h.rec.EmitDurable(ev)
	if err != nil {
		h.mu.Lock()
		delete(h.tools, ev.AskID)
		h.mu.Unlock()
		return fail(cmd, CodeFailed, "the question could not be journaled: "+err.Error())
	}
	return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op, Seq: events[0].Seq, AskID: ev.AskID}
}

// answerTool hands an answer to the local tool waiting on the ask, and
// reports whether one took it. A call outlives neither its connection nor
// its turn: once the turn ended (an interrupt), the CLI has withdrawn the
// call even if the tool's connection has not closed yet.
func (h *host) answerTool(ask *agent.Ask, answer json.RawMessage) bool {
	h.mu.Lock()
	c := h.tools[ask.ID]
	delete(h.tools, ask.ID)
	h.mu.Unlock()
	if c == nil {
		return false
	}
	live := false
	h.rec.View(func(p *agent.Projection) {
		live = p.Turn != nil && p.Turn.ID == ask.TurnID
	})
	if !live {
		return false
	}
	raw, _ := json.Marshal(Answer{T: FrameAnswer, AskID: ask.ID, Answer: answer})
	return c.sendNow(raw)
}

// deliver gets an answer to the agent: through the call that asked, if it is
// still waiting, and as a user message if it is not.
func (h *host) deliver(ctx context.Context, ask *agent.Ask, answer json.RawMessage) {
	if ask.Deliver == agent.ViaMessage {
		// The agent asked for the answer as its next message.
		h.deliverAsMessage(ctx, ask, answer, false)
		return
	}
	if h.answerTool(ask, answer) {
		h.record(agent.Event{Type: agent.TypeAskDelivered, AskID: ask.ID, Via: agent.ViaTool})
		return
	}
	if h.adapter != nil {
		err := h.adapter.Answer(ctx, ask.ID, answer)
		switch {
		case err == nil:
			h.record(agent.Event{Type: agent.TypeAskDelivered, AskID: ask.ID, Via: agent.ViaTool})
			return
		case errors.Is(err, agent.ErrAskDeferred):
			h.deliverAsMessage(ctx, ask, answer, false)
			return
		case !errors.Is(err, agent.ErrAskGone):
			log.Printf("the answer to %s did not reach the agent, so it goes as a message: %v", ask.ID, err)
		}
	}
	h.deliverAsMessage(ctx, ask, answer, true)
}

// deliverAsMessage sends an answer as a user message. lost says the call that
// asked was gone, which the chat says in a notice; an answer that was meant to
// arrive this way needs none.
func (h *host) deliverAsMessage(ctx context.Context, ask *agent.Ask, answer json.RawMessage, lost bool) {
	msg := agent.Event{Type: agent.TypeMessage, MessageID: newID("m_"), Role: agent.RoleUser,
		State: agent.MessagePending, Text: agent.AnswerText(ask, answer), AnswerTo: ask.ID}
	events := []agent.Event{msg, {Type: agent.TypeAskDelivered, AskID: ask.ID, Via: agent.ViaMessage}}
	if lost {
		events = append(events, agent.Event{Type: agent.TypeNotice, Code: agent.NoticeAnswerAsMessage, AskID: ask.ID,
			Text: "The question was no longer being waited on, so the answer was sent as a message."})
	}
	if _, err := h.rec.EmitDurable(events...); err != nil {
		log.Printf("journal: %v", err)
		return
	}
	if h.adapter == nil {
		return
	}
	out := agent.Outgoing{MessageID: msg.MessageID, Text: msg.Text, AnswerTo: ask.ID, AfterTurn: h.afterTurn(msg.MessageID)}
	if err := h.adapter.Send(ctx, out); err != nil {
		h.record(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: msg.MessageID, Text: err.Error()})
	}
}

// control is every command that is journaled as a command event: interrupt,
// set_model, command, park, stop_task.
func (h *host) control(ctx context.Context, cmd Command) any {
	if h.adapter == nil && cmd.Op != OpPark {
		return fail(cmd, CodeNotRunning, "the agent is not running")
	}
	if cmd.Op == OpStopTask && strings.TrimSpace(cmd.Task) == "" {
		return fail(cmd, CodeBadRequest, "stop_task needs the task's id")
	}
	var seq int64
	if cmd.ClientID != "" {
		ev := agent.Event{Type: agent.TypeCommand, ClientID: cmd.ClientID, Op: cmd.Op}
		switch cmd.Op {
		case OpSetModel:
			ev.Model, ev.Effort = cmd.Model, cmd.Effort
		case OpCommand:
			ev.Text, ev.Args = cmd.Name, cmd.Args
		case OpStopTask:
			ev.Args = cmd.Task
		}
		events, err := h.rec.EmitDurable(ev)
		if err != nil {
			return fail(cmd, CodeFailed, "the command could not be journaled: "+err.Error())
		}
		seq = events[0].Seq
	}
	var err error
	switch cmd.Op {
	case OpInterrupt:
		// A Stop the agent does not act on is not the end of it: the watch
		// starts the CLI again (watch.go).
		h.stopAsked()
		err = h.adapter.Interrupt(ctx)
	case OpSetModel:
		err = h.adapter.SetModel(ctx, cmd.Model, cmd.Effort)
	case OpCommand:
		err = h.adapter.Command(ctx, cmd.Name, cmd.Args)
	case OpStopTask:
		err = agent.ErrUnknownCommand
		if ts, ok := h.adapter.(agent.TaskStopper); ok {
			err = ts.StopTask(ctx, strings.TrimSpace(cmd.Task))
		}
	case OpPark:
		h.mu.Lock()
		h.parkWanted = true
		h.mu.Unlock()
		go h.park(false)
	}
	switch {
	case errors.Is(err, agent.ErrUnknownCommand):
		return fail(cmd, CodeUnsupported, err.Error())
	case err != nil:
		return fail(cmd, CodeFailed, err.Error())
	}
	return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op, Seq: seq}
}

// ------------------------------------------------------- parking and ending

// idle is whether the host may park: the CLI is up and doing nothing that
// parking would cut short - no turn (a question being waited on is always
// inside one: a local tool's too, whose call does not outlive its turn), no
// background task, and no user message it has not taken up yet (one that
// was acknowledged a moment ago and is still on its way into the agent).
//
// An open ask outside a running turn does not keep the host: its call is
// gone (the turn was interrupted, or the host before this one died) or never
// waited (an ask answered as the next message), so its answer goes to the
// agent as a message whenever it comes, through whichever host is up then.
// The ask itself stays open across parks, host deaths and recoveries, and
// the chat stays "waiting" until it is answered or the person writes
// something else (REBUILD.md §5.3: a question survives everything).
func (h *host) idle() bool {
	if h.adapter == nil || h.isEnding() || h.hasPending() {
		return false
	}
	busy := false
	h.rec.View(func(p *agent.Projection) {
		busy = p.Turn != nil || len(p.Undelivered()) > 0
	})
	return !busy && h.adapter.Background() == 0
}

// idleLoop parks the host once it has been idle for the spec's time, or as
// soon as it is idle after a park command.
func (h *host) idleLoop() {
	every := min(max(h.parkAfter/4, 50*time.Millisecond), time.Second)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-tick.C:
		}
		h.runPending()
		h.mu.Lock()
		quiet, wanted := time.Since(h.lastActive), h.parkWanted
		h.mu.Unlock()
		if wanted || quiet >= h.parkAfter {
			h.park(false)
		}
	}
}

// park stops the CLI cleanly and ends the host, keeping the conversation for
// the next one. Unless forced, it only does so while the host is idle; the
// command lock keeps a message from slipping in between the look and the
// stop.
func (h *host) park(force bool) {
	if !h.cmdMu.TryLock() {
		return
	}
	defer h.cmdMu.Unlock()
	if !force && !h.idle() {
		return
	}
	if !h.beginEnding() {
		return
	}
	log.Print("parking")
	h.rec.Flush()
	if h.adapter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), parkWait)
		if err := h.adapter.Park(ctx); err != nil {
			log.Printf("the agent did not park cleanly: %v", err)
			_ = h.adapter.Stop()
		}
		cancel()
	}
	h.closeLeftovers()
	_, _ = h.rec.EmitDurable(agent.Event{Type: agent.TypeProcess, State: agent.ProcessParked})
	h.finish()
}

// stop ends the CLI now, whatever it is doing.
func (h *host) stop(clientID string) {
	if !h.beginEnding() {
		return
	}
	h.cmdMu.Lock()
	defer h.cmdMu.Unlock()
	log.Print("stopping")
	h.mu.Lock()
	h.pending = nil
	h.mu.Unlock()
	if h.adapter != nil {
		_ = h.adapter.Stop()
	}
	var events []agent.Event
	if clientID != "" {
		events = append(events, agent.Event{Type: agent.TypeCommand, ClientID: clientID, Op: OpStop})
	}
	h.rec.View(func(p *agent.Projection) {
		if p.Turn != nil {
			events = append(events, agent.Event{Type: agent.TypeTurnEnded, TurnID: p.Turn.ID,
				Outcome: agent.OutcomeInterrupted, Text: "The chat was stopped."})
		}
		// Stop ends the background work too.
		left, _ := leftovers(p)
		events = append(events, left...)
	})
	events = append(events, agent.Event{Type: agent.TypeProcess, State: agent.ProcessExited, Detail: "stopped"})
	_, _ = h.rec.EmitDurable(events...)
	h.finish()
}

func (h *host) finish() { h.doneOnce.Do(func() { close(h.done) }) }

// shutdown closes the socket and gives every client a moment to read the end
// of the journal, so the server sees the park or the exit it is told of.
func (h *host) shutdown() {
	_ = h.ln.Close()
	_ = os.Remove(h.sock)
	last := h.rec.Feed().Last()
	deadline := time.Now().Add(drainWait)
	for time.Now().Before(deadline) {
		if h.drained(last) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.mu.Lock()
	for c := range h.conns {
		c.close()
	}
	h.mu.Unlock()
	if err := h.rec.Close(); err != nil {
		log.Printf("journal: %v", err)
	}
	log.Print("exiting")
}

func (h *host) drained(last int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c.sent.Load() < last {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------- the socket

// hostConn is one connected client.
type hostConn struct {
	h       *host
	c       net.Conn
	proto   int
	replies chan []byte
	// urgent are lines written in their turn like replies, by a sender that
	// waits to learn whether they were.
	urgent chan urgentLine
	sent   atomic.Int64
	gone   chan struct{}
	once   sync.Once
}

type urgentLine struct {
	line []byte
	done chan bool
}

// sendNow writes one line to the client and reports whether it was written.
// Once the writer has taken the line it always says how the write went, and
// that is the answer even when the client hangs up the moment it has read
// the line, as a local tool does with its answer: the connection being gone
// by then does not unsay the write.
func (c *hostConn) sendNow(line []byte) bool {
	u := urgentLine{line: line, done: make(chan bool, 1)}
	select {
	case c.urgent <- u:
	case <-c.gone:
		return false
	}
	return <-u.done
}

func (c *hostConn) close() {
	c.once.Do(func() {
		close(c.gone)
		_ = c.c.Close()
	})
}

func (h *host) accept() {
	for {
		c, err := h.ln.Accept()
		if err != nil {
			return
		}
		go h.serve(c)
	}
}

// serve runs one client: hello, then commands in and frames out.
func (h *host) serve(nc net.Conn) {
	defer nc.Close()
	r := bufio.NewReaderSize(nc, 64<<10)
	_ = nc.SetReadDeadline(time.Now().Add(helloWait))
	line, err := readLine(r)
	if err != nil {
		return
	}
	var hello Command
	if json.Unmarshal(line, &hello) != nil || hello.Op != OpHello {
		writeFrame(nc, Fail{T: FrameError, Code: CodeBadRequest, Msg: "the first line must be a hello"})
		return
	}
	if hello.Proto < MinProtocol {
		writeFrame(nc, Fail{T: FrameError, Code: CodeProtocol,
			Msg: fmt.Sprintf("this host speaks protocol %d to %d", MinProtocol, Protocol)})
		return
	}
	_ = nc.SetReadDeadline(time.Time{})
	c := &hostConn{h: h, c: nc, proto: min(hello.Proto, Protocol), replies: make(chan []byte, 256),
		urgent: make(chan urgentLine), gone: make(chan struct{})}
	c.sent.Store(hello.Since)
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		c.close()
		h.mu.Lock()
		delete(h.conns, c)
		// A local tool that went away no longer waits on its asks.
		for id, t := range h.tools {
			if t == c {
				delete(h.tools, id)
			}
		}
		h.mu.Unlock()
	}()
	var process string
	h.rec.View(func(p *agent.Projection) { process = p.Process })
	if err := writeFrame(nc, Hello{T: FrameHello, Proto: c.proto, Version: h.opts.Version,
		Chat: h.opts.ChatID, Harness: h.spec.Harness, HostPID: os.Getpid(), CLIPID: int(h.cliPID.Load()),
		LastSeq: h.rec.Feed().Last(), Process: process}); err != nil {
		return
	}
	go c.write(hello.Since)
	for {
		line, err := readLine(r)
		if err != nil {
			return
		}
		var cmd Command
		var reply any
		if err := json.Unmarshal(line, &cmd); err != nil {
			reply = Fail{T: FrameError, Code: CodeBadRequest, Msg: "a command must be JSON: " + err.Error()}
		} else {
			reply = h.handle(c, cmd)
		}
		raw, _ := json.Marshal(reply)
		select {
		case c.replies <- raw:
		case <-c.gone:
			return
		}
	}
}

// write is the client's one writer: the journal from since, live deltas, and
// the replies to its commands. A reply goes out after every event before it,
// so an ack never arrives ahead of the event it names.
func (c *hostConn) write(since int64) {
	sub := c.h.rec.Feed().Subscribe()
	defer sub.Close()
	next := since
	var replies [][]byte
	for {
		events, deltas := sub.Pull(next)
		if since >= NoEvents {
			// A connection that asked for no events wants no deltas.
			deltas = nil
		}
		for _, ev := range events {
			if !c.send(EncodeEvent(ev)) {
				return
			}
			next = ev.Seq
			c.sent.Store(next)
		}
		for _, d := range deltas {
			if !c.send(EncodeDelta(d)) {
				return
			}
		}
		for _, r := range replies {
			if !c.send(r) {
				return
			}
		}
		replies = replies[:0]
		select {
		case <-sub.Wake():
		case r := <-c.replies:
			replies = append(replies, r)
		case u := <-c.urgent:
			ok := c.send(u.line)
			u.done <- ok
			if !ok {
				return
			}
		case <-c.gone:
			return
		}
	}
}

func (c *hostConn) send(line []byte) bool {
	_ = c.c.SetWriteDeadline(time.Now().Add(frameWriteWait))
	if _, err := c.c.Write(append(line, '\n')); err != nil {
		c.close()
		return false
	}
	return true
}

func writeFrame(c net.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_ = c.SetWriteDeadline(time.Now().Add(frameWriteWait))
	_, err = c.Write(append(raw, '\n'))
	return err
}

// readLine reads one line, refusing one longer than a command may be.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxCommandLine {
			return nil, errors.New("the line is too long")
		}
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// UltracodeEnv is the variable that switches the Claude adapter's ultracode
// setting on (AGENT-HOST.md §5.1).
const UltracodeEnv = "SOCRATES_CLAUDE_ULTRACODE"

// withUltracode is the CLI's environment - the spec's, or this host's own -
// with the ultracode instruction as the chat's option says, whatever the
// environment it came from held.
func withUltracode(spec []string, on bool) []string {
	base := spec
	if len(base) == 0 {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, UltracodeEnv+"=") {
			env = append(env, kv)
		}
	}
	if on {
		env = append(env, UltracodeEnv+"=1")
	}
	return env
}
