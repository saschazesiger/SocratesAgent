//go:build !windows

package opencode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// Harness is the name the adapter is registered under, the same as the
// terminal harness's.
const Harness = "opencode"

func init() { agent.Register(Harness, func() agent.Adapter { return New() }) }

// The adapter's clock.
const (
	// healthWait is how long a server that printed its address has to answer.
	healthWait = 15 * time.Second
	// connectedWait is how long /event has to say server.connected.
	connectedWait = 10 * time.Second
	// abortWait bounds the abort a stop starts with.
	abortWait = 3 * time.Second
	// idleWait is how long an account switch waits for an interrupted turn
	// before it stops the process under it.
	idleWait = 30 * time.Second
	// reconnectMax is the longest pause between two attempts to reopen
	// /event.
	reconnectMax = 2 * time.Second
	// maxImage is the largest image sent inline.
	maxImage = 20 << 20
)

// Agents a prompt can run as: OpenCode's two primary agents.
const (
	AgentBuild = "build"
	AgentPlan  = "plan"
)

// Notice codes this adapter writes.
const (
	NoticePermission = "permission_allowed"
	NoticeQuestion   = "question_rejected"
	NoticeCompacted  = "compacted"
	NoticeRetry      = "retry"
	NoticeReverted   = "reverted"
	NoticeUnreverted = "unreverted"
	NoticeForked     = "forked"
	NoticeAgent      = "agent"
)

// Adapter drives one chat's `opencode serve`.
type Adapter struct {
	out    agent.Emitter
	spec   agent.StartSpec
	layout Layout
	bin    string

	mu sync.Mutex
	// srv and cl are the running process and its client; gen counts the
	// processes this adapter started, so that what an old one says after an
	// account switch is not taken for the new one's.
	srv *server
	cl  *client
	gen int
	// life lasts as long as the process does: cancel ends it when the
	// process is stopped.
	life    context.Context
	cancel  context.CancelFunc
	session string
	account string
	model   string
	effort  string
	agentID string
	state   persisted
	ending  bool
	// switching is an account switch in progress: the old process's exit is
	// expected, and the queue waits for the new one.
	switching bool

	// posting is a prompt on its way to OpenCode; turn the turn running.
	// Either one makes the adapter busy, and a message sent while it is
	// waits in queue: OpenCode would take a prompt sent during a turn into
	// that turn's next step, and an abort drops what it holds unanswered.
	posting bool
	turn    *turnState
	queue   []job
	// held is the turn the account's limit ended, which stops the queue:
	// what was queued behind it would go into the account at its limit and
	// end limited too. An account switch lets it go, and so does a message
	// the person wrote after that turn's end; one written before it, though
	// the host hands it over only now, waits with the rest (AGENT-HOST.md
	// §5.3, agent.Outgoing.AfterTurn). An adapter a new host starts on the
	// account of a limited turn the journal ends with holds from the start
	// (StartSpec.Held). Empty when nothing holds.
	held string

	users      map[string]*userMsg
	assistants map[string]*asstMsg
	parts      map[string]*seenPart
	children   map[string]childSession
	// childPrompts are the user messages of subagent sessions.
	childPrompts map[string]bool
	windows      map[string]int64
	lastModel    string
	retries      int
	// failedTurn is a turn that ended without completing, the last one to
	// end. OpenCode says a failed turn's error once more after
	// session.idle (the rejected prompt, with a stack trace); the turn's end
	// said it already. owed is what failed turns failed with whose repeat has
	// not come yet: the idle posts the next queued message, so the repeat
	// can come once that message's turn began, and is known by what it says
	// (repeatLocked).
	failedTurn bool
	owed       []string
	// commands are the names GET /command listed: what Command posts to
	// /command.
	commands map[string]bool
	// questions are OpenCode's own questions being asked, by ask id;
	// questionsSeen every one ever asked, so that a question the event and a
	// reconcile both bring is asked once.
	questions     map[string]*question
	questionsSeen map[string]bool
	// allowed are the permissions already answered.
	allowed map[string]bool

	// wait bounds the calls to OpenCode that are not long ones; zero is
	// requestWait. Tests shorten it.
	wait time.Duration
}

// New is an adapter, unstarted.
func New() *Adapter {
	return &Adapter{
		users: map[string]*userMsg{}, assistants: map[string]*asstMsg{}, parts: map[string]*seenPart{},
		children: map[string]childSession{}, childPrompts: map[string]bool{}, windows: map[string]int64{},
		questions: map[string]*question{}, questionsSeen: map[string]bool{},
		allowed: map[string]bool{},
	}
}

// job is one thing that becomes a turn: a prompt, a shell line, a slash
// command, a compaction.
type job struct {
	kind    string
	msg     agent.Outgoing
	name    string
	args    string
	display string
}

const (
	jobPrompt  = "prompt"
	jobShell   = "shell"
	jobCommand = "command"
	jobCompact = "compact"
)

// userMsg is a user message OpenCode has or is about to have.
type userMsg struct {
	// our is the journal's id of the message: the host's for a message it
	// journaled, OpenCode's own for one the adapter announced itself.
	our string
	// journaled is a message the host wrote; the others the adapter writes
	// when they become a turn.
	journaled bool
	display   string
	announced bool
	started   bool
	// adopt: the turn takes the first parent an assistant message names
	// (a compaction's user message has an id OpenCode chooses).
	adopt bool
}

// asstMsg is an assistant message of the session, or of a subagent's
// (child), whose usage and cost count into the turn that ran it.
type asstMsg struct {
	parent string
	done   bool
	child  bool
	tokens ocTokens
	cost   float64
}

type seenPart struct {
	msg     string
	parent  string
	kind    string
	state   string
	text    string
	content []byte
	diff    bool
	// child is the subagent session a task part named.
	child string
}

// childSession is a subagent's session: its parts go inside the task tool
// part that runs it, the newest one to name it (a task can resume an
// earlier task's subagent). A seeded one ran before this process (seed),
// and the journal has what it did. since is when the task part started: the
// session's messages from before it are an earlier task part's, which the
// journal has too.
type childSession struct {
	msg    string
	parent string
	seeded bool
	since  int64
}

type turnState struct {
	id         string
	user       string
	our        string
	adopt      bool
	assistants []string
	aborted    bool
	limited    bool
	limitSaid  bool
	failure    string
	// errors are the texts of the errors the turn failed with, which
	// OpenCode may say again after its end.
	errors  []string
	context int64
	model   string
}

// persisted is what the adapter keeps beside the journal: the session, the
// agent the chat runs as, which OpenCode message each journaled message
// became - for a revert or a fork to a message, and to recognise a message
// the previous host already handed over - and the assistant messages a dead
// process left incomplete that were already interrupted (dangling).
type persisted struct {
	Session     string            `json:"session,omitempty"`
	Agent       string            `json:"agent,omitempty"`
	Messages    map[string]string `json:"messages,omitempty"`
	Interrupted []string          `json:"interrupted,omitempty"`
}

func (a *Adapter) statePath() string { return filepath.Join(a.spec.StateDir, "opencode.json") }

func (a *Adapter) loadState() {
	raw, err := os.ReadFile(a.statePath())
	if err == nil {
		_ = json.Unmarshal(raw, &a.state)
	}
	if a.state.Messages == nil {
		a.state.Messages = map[string]string{}
	}
}

// saveState writes the state, atomically. Called with mu held.
func (a *Adapter) saveState() {
	if a.spec.StateDir == "" {
		return
	}
	raw, _ := json.Marshal(a.state)
	if err := os.MkdirAll(a.spec.StateDir, 0o700); err != nil {
		log.Printf("opencode: %v", err)
		return
	}
	tmp := a.statePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		err = os.Rename(tmp, a.statePath())
		if err != nil {
			log.Printf("opencode: %v", err)
		}
	}
}

// ------------------------------------------------------------------ start

// Start brings the chat's server up on its session: the one spec.ResumeID
// names, or a new one.
func (a *Adapter) Start(ctx context.Context, spec agent.StartSpec, out agent.Emitter) error {
	a.out, a.spec = out, spec
	a.model, a.effort = spec.Model, spec.Effort
	// Set before anything is sent, First included: what the host hands over
	// goes through the same hold as on the adapter before this one.
	a.held = spec.Held
	a.account = spec.Account
	if a.account == "" {
		a.account = DefaultAccount
	}
	if spec.DataDir == "" {
		return errors.New("opencode: the data folder is not known")
	}
	a.layout = NewLayout(spec.DataDir)
	// The dashboard's binary setting, else BinaryEnv, else opencode on the
	// chat's PATH.
	bin, err := agent.ResolveBinary(spec.Env, "opencode", spec.Binary, envValue(spec.Env, BinaryEnv))
	if err != nil {
		return fmt.Errorf("opencode: %w", err)
	}
	a.bin = bin
	a.loadState()
	a.agentID = a.state.Agent
	if err := a.layout.PrepareAccount(a.account); err != nil {
		return err
	}
	if err := a.layout.Migrate(ctx, a.spawner(), bin, processEnv(spec.Env, a.layout, a.account, "", nil)); err != nil {
		return err
	}
	if err := a.startProcess(ctx); err != nil {
		return err
	}
	if err := a.openSession(ctx); err != nil {
		// This adapter is done; the host starts a new one if it goes on.
		a.stopProcess(true)
		return err
	}
	a.loadCommands(ctx)
	if spec.First != nil {
		if err := a.Send(ctx, *spec.First); err != nil {
			a.out.Emit(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: spec.First.MessageID, Text: err.Error()})
		}
	}
	return nil
}

// startProcess starts a server for the adapter's account and follows its
// events. It returns once /event is open, so nothing the server does after
// that is missed.
func (a *Adapter) startProcess(ctx context.Context) error {
	config, err := configContent(a.spec)
	if err != nil {
		return err
	}
	base, account := a.spec.Env, a.account
	srv, err := startServer(ctx, a.spawner(), a.bin, a.spec.Workdir, func(pass string) []string {
		return processEnv(base, a.layout, account, pass, config)
	})
	if err != nil {
		return err
	}
	cl := newClient(srv.base, srv.pass, a.spec.Workdir)
	if a.wait > 0 {
		cl.wait = a.wait
	}
	if err := waitHealthy(ctx, cl, srv); err != nil {
		srv.stop(stopGrace)
		return err
	}
	a.mu.Lock()
	a.gen++
	gen := a.gen
	a.srv, a.cl = srv, cl
	sctx, cancel := context.WithCancel(context.Background())
	a.life, a.cancel = sctx, cancel
	a.mu.Unlock()
	connected := make(chan struct{})
	go a.follow(sctx, gen, cl, connected)
	select {
	case <-connected:
	case <-time.After(connectedWait):
		cancel()
		srv.stop(stopGrace)
		return errors.New("opencode: the event stream did not open")
	case <-srv.exited():
		cancel()
		srv.stop(0)
		return fmt.Errorf("opencode serve exited: %v: %s", srv.err, srv.out.String())
	}
	// From here on, an end nobody asked for is the chat's process dying; one
	// before it is this start failing, which the caller reports.
	go a.watch(gen, srv)
	a.loadWindows(ctx, cl)
	return nil
}

// spawner is StartSpec.Spawn, or, for a spec without one, a plain start in a
// process group of its own.
func (a *Adapter) spawner() func(*exec.Cmd) error {
	if a.spec.Spawn != nil {
		return a.spec.Spawn
	}
	return func(cmd *exec.Cmd) error {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd.Start()
	}
}

func waitHealthy(ctx context.Context, cl *client, srv *server) error {
	deadline := time.Now().Add(healthWait)
	for {
		err := cl.call(ctx, http.MethodGet, "/global/health", nil, nil)
		if err == nil {
			return nil
		}
		select {
		case <-srv.exited():
			return fmt.Errorf("opencode serve exited: %v: %s", srv.err, srv.out.String())
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("opencode serve does not answer: %v", err)
		}
	}
}

// watch reports the server's end when nobody asked for it.
func (a *Adapter) watch(gen int, srv *server) {
	<-srv.exited()
	a.mu.Lock()
	expected := a.ending || a.switching || a.gen != gen
	a.mu.Unlock()
	if !expected {
		a.out.Exited(fmt.Errorf("opencode serve exited: %v", srv.err))
	}
}

// openSession creates the chat's session or checks the one to resume, and
// catches the journal up with it.
func (a *Adapter) openSession(ctx context.Context) error {
	cl := a.client()
	id := strings.TrimSpace(a.spec.ResumeID)
	if id == "" {
		var s ocSession
		if err := cl.call(ctx, http.MethodPost, "/session", map[string]any{}, &s); err != nil {
			return fmt.Errorf("opencode: create a session: %w", err)
		}
		a.mu.Lock()
		a.session = s.ID
		a.state.Session = s.ID
		a.saveState()
		a.mu.Unlock()
		a.out.Emit(agent.Event{Type: agent.TypeConversation, ConversationID: s.ID})
		return nil
	}
	if !validID(id, "ses") {
		return fmt.Errorf("opencode: %q: %w", id, agent.ErrConversationNotFound)
	}
	var s ocSession
	if err := cl.call(ctx, http.MethodGet, "/session/"+id, nil, &s); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return fmt.Errorf("opencode: no session %s: %w", id, agent.ErrConversationNotFound)
		}
		return fmt.Errorf("opencode: open session %s: %w", id, err)
	}
	a.mu.Lock()
	a.session = id
	a.state.Session = id
	a.saveState()
	a.mu.Unlock()
	// A rebuilt history is written before the conversation it belongs to:
	// a host that dies while it reads leaves a journal that still names no
	// conversation, and the next one rebuilds again.
	conversation := agent.Event{Type: agent.TypeConversation, ConversationID: id}
	if !a.spec.Rebuild {
		a.out.Emit(conversation)
	}
	msgs, err := a.messages(ctx, cl, id)
	if err != nil && a.spec.Rebuild {
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeHistoryUnavailable,
			Text: "The earlier history of this chat could not be read back from OpenCode: " + err.Error()}, conversation)
		return nil
	}
	if err != nil {
		return fmt.Errorf("opencode: read session %s: %w", id, err)
	}
	if a.spec.Rebuild {
		events := a.history(ctx, cl, msgs)
		a.mu.Lock()
		a.seed(msgs)
		a.mu.Unlock()
		events = append(events, agent.Event{Type: agent.TypeNotice, Code: agent.NoticeRebuilt,
			Text: "The chat history was read back from OpenCode's session."}, conversation)
		a.out.Emit(events...)
		return nil
	}
	a.mu.Lock()
	events := a.dangling(msgs)
	a.seed(msgs)
	a.mu.Unlock()
	if len(events) > 0 {
		a.out.Emit(events...)
	}
	return nil
}

func (a *Adapter) messages(ctx context.Context, cl *client, session string) ([]ocMessageWithParts, error) {
	var msgs []ocMessageWithParts
	err := cl.call(ctx, http.MethodGet, "/session/"+session+"/message", nil, &msgs)
	return msgs, err
}

// dangling is what a process that died mid-turn left: an assistant message
// that never completed, with tool parts still running for ever (research/06
// §3.3). They are interrupted now, once: OpenCode never completes such a
// message, and every later start would write the same again. Text and
// reasoning are left to the journal, whose streamed text OpenCode does not
// have (it stores a part's text when the part ends), and whose parts the
// host interrupts itself; a tool part's output OpenCode stores as it
// streams. Called with mu held.
func (a *Adapter) dangling(msgs []ocMessageWithParts) []agent.Event {
	done := map[string]bool{}
	for _, id := range a.state.Interrupted {
		done[id] = true
	}
	var events []agent.Event
	for _, m := range msgs {
		if m.Info.Role != "assistant" || m.Info.Time.Completed > 0 || done[m.Info.ID] {
			continue
		}
		for _, p := range m.Parts {
			c, ok := convertPart(p)
			if !ok || c.kind == agent.PartText || c.kind == agent.PartReasoning ||
				(c.state != agent.PartRunning && c.state != agent.PartStreaming) {
				continue
			}
			events = append(events, agent.Event{Type: agent.TypePart, MessageID: m.Info.ID, PartID: p.ID,
				Kind: c.kind, State: agent.PartInterrupted, Text: c.text, Content: c.content})
		}
		events = append(events, agent.Event{Type: agent.TypeMessageState, MessageID: m.Info.ID, State: agent.MessageInterrupted})
		a.state.Interrupted = append(a.state.Interrupted, m.Info.ID)
	}
	if len(events) > 0 {
		a.saveState()
	}
	return events
}

// seed records what the session already holds as seen, so that the live
// mapping and a reconcile only write what changes from here on. Called with
// mu held.
func (a *Adapter) seed(msgs []ocMessageWithParts) {
	for _, m := range msgs {
		switch m.Info.Role {
		case "user":
			a.users[m.Info.ID] = &userMsg{our: m.Info.ID, announced: true, started: true}
		case "assistant":
			a.assistants[m.Info.ID] = &asstMsg{parent: m.Info.ParentID, done: true, tokens: m.Info.Tokens, cost: m.Info.Cost}
			if model := m.Info.model(); model != "" {
				a.lastModel = model
			}
			for _, p := range m.Parts {
				c, ok := convertPart(p)
				if !ok {
					continue
				}
				a.parts[p.ID] = &seenPart{msg: m.Info.ID, kind: c.kind, state: c.state, text: c.text, content: c.content,
					diff: c.diff != nil, child: c.child}
				if c.child != "" {
					a.children[c.child] = childSession{msg: m.Info.ID, parent: p.ID, seeded: true}
				}
			}
		}
	}
	for our, oc := range a.state.Messages {
		if u := a.users[oc]; u != nil {
			u.our, u.journaled = our, true
		}
	}
}

func (a *Adapter) loadWindows(ctx context.Context, cl *client) {
	var p ocProviders
	if err := cl.call(ctx, http.MethodGet, "/config/providers", nil, &p); err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, prov := range p.Providers {
		for id, m := range prov.Models {
			if m.Limit.Context > 0 {
				a.windows[prov.ID+"/"+id] = m.Limit.Context
			}
		}
	}
}

// ownCommands are the commands the adapter implements itself, at the top
// of the palette.
var ownCommands = []agent.CommandInfo{
	{Name: "compact", Description: "Summarize the conversation to free context"},
	{Name: "shell", Args: "<command>", Description: "Run a shell command in the working directory (same as a line starting with !)"},
	{Name: "agent", Args: "build|plan", Description: "Plan without editing files, or build"},
}

// loadCommands reads OpenCode's version and commands and tells the chat
// (agent.info: the composer's palette).
func (a *Adapter) loadCommands(ctx context.Context) {
	cl := a.client()
	var health struct {
		Version string `json:"version"`
	}
	_ = cl.call(ctx, http.MethodGet, "/global/health", nil, &health)
	var list []ocCommand
	if err := cl.call(ctx, http.MethodGet, "/command", nil, &list); err != nil {
		log.Printf("opencode: list the commands: %v", err)
	}
	info := agent.AgentInfo{Agent: Harness, Version: health.Version, Commands: append([]agent.CommandInfo{}, ownCommands...)}
	// One of OpenCode's commands named like one Command handles itself
	// could never be reached.
	taken := map[string]bool{"summarize": true, "revert": true, "unrevert": true, "fork": true}
	for _, c := range ownCommands {
		taken[c.Name] = true
	}
	names := map[string]bool{}
	for _, c := range list {
		if c.Name == "" || names[c.Name] || taken[c.Name] {
			continue
		}
		names[c.Name] = true
		info.Commands = append(info.Commands, agent.CommandInfo{Name: c.Name, Args: hintArgs(c.Hints), Description: c.Description})
	}
	a.mu.Lock()
	a.commands = names
	a.mu.Unlock()
	raw, _ := json.Marshal(info)
	a.out.Emit(agent.Event{Type: agent.TypeAgentInfo, Content: raw})
}

// hintArgs is what a command's hints say in the palette: OpenCode names the
// placeholders of its template ($ARGUMENTS, $1, $2, …).
func hintArgs(hints []string) string {
	out := make([]string, 0, len(hints))
	for _, h := range hints {
		switch n := strings.TrimPrefix(h, "$"); {
		case h == "$ARGUMENTS":
			h = "<arguments>"
		case n != h && n != "" && strings.Trim(n, "0123456789") == "":
			h = "<arg" + n + ">"
		}
		out = append(out, h)
	}
	return strings.Join(out, " ")
}

func (a *Adapter) client() *client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cl
}

// ------------------------------------------------------------- the stream

// follow reads /event for as long as the process runs, reopening it when it
// ends, and catches up after every reopen from what OpenCode stored.
func (a *Adapter) follow(ctx context.Context, gen int, cl *client, connected chan struct{}) {
	pause := 100 * time.Millisecond
	first := true
	for ctx.Err() == nil {
		body, err := cl.events(ctx)
		if err == nil {
			// The first stream is the one Start waits for; every later one
			// catches up with what was missed while none was open.
			catchUp := !first
			var once sync.Once
			signal := func() {
				once.Do(func() {
					if catchUp {
						go a.reconcile(ctx, gen, cl)
					} else {
						close(connected)
					}
				})
			}
			_ = readSSE(body, func(data []byte) {
				var ev ocEvent
				if json.Unmarshal(data, &ev) != nil {
					return
				}
				if ev.Type == "server.connected" {
					signal()
					return
				}
				a.handle(gen, ev)
			})
			body.Close()
			select {
			case <-connected:
				first = false
			default:
			}
			pause = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(pause*2, reconnectMax)
	}
}

// reconcile writes what happened while /event was closed: the session's
// messages as OpenCode stored them, its subagents', the permissions and the
// questions of its own it asked meanwhile (research/05 §2: a session waiting
// on one still reports busy), and whether it is still busy (§2.2).
func (a *Adapter) reconcile(ctx context.Context, gen int, cl *client) {
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()
	if session == "" {
		return
	}
	a.catchUp(ctx, gen, cl, session, 0)
	// The subagents this process ran, the ones the session's parts just
	// named among them; a seeded one's history is the journal's already,
	// and so is what a resumed one did before this process resumed it.
	a.mu.Lock()
	children := map[string]int64{}
	for id, c := range a.children {
		if !c.seeded {
			children[id] = c.since
		}
	}
	a.mu.Unlock()
	for sid, since := range children {
		a.catchUp(ctx, gen, cl, sid, since)
	}
	// A permission asked while no stream was open would wait for an answer
	// for ever.
	var asked []ocPermission
	if err := cl.call(ctx, http.MethodGet, "/permission", nil, &asked); err == nil {
		for _, p := range asked {
			a.handlePermission(gen, p)
		}
	}
	var pending []Question
	if err := cl.call(ctx, http.MethodGet, "/question", nil, &pending); err == nil {
		for _, q := range pending {
			a.handleQuestion(gen, q)
		}
	}
	var status map[string]ocStatus
	if err := cl.call(ctx, http.MethodGet, "/session/status", nil, &status); err != nil {
		return
	}
	if _, busy := status[session]; !busy {
		a.mu.Lock()
		if a.gen == gen && a.turn != nil && !a.posting {
			a.endTurnLocked()
		}
		a.mu.Unlock()
	}
}

// catchUp maps one session's stored messages as if /event had said them,
// those created since then.
func (a *Adapter) catchUp(ctx context.Context, gen int, cl *client, session string, since int64) {
	msgs, err := a.messages(ctx, cl, session)
	if err != nil {
		return
	}
	for _, m := range msgs {
		if m.Info.Time.Created < since {
			continue
		}
		a.handleMessage(gen, m.Info)
		for _, p := range m.Parts {
			a.handlePart(gen, p)
		}
	}
}

// ------------------------------------------------------------ the commands

// Send hands a message over: at once when OpenCode is idle, otherwise when
// the running turn ends. A line starting with "!" runs as a shell command.
func (a *Adapter) Send(ctx context.Context, msg agent.Outgoing) error {
	a.mu.Lock()
	if a.ending {
		a.mu.Unlock()
		return errStopped
	}
	oc := a.state.Messages[msg.MessageID]
	cl, session := a.cl, a.session
	a.mu.Unlock()
	if oc != "" {
		// The previous host handed this one over and died before it said
		// so: OpenCode has it if its message is there.
		var m ocMessageWithParts
		err := cl.call(ctx, http.MethodGet, "/session/"+session+"/message/"+oc, nil, &m)
		if err == nil && m.Info.ID == oc {
			a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: msg.MessageID, State: agent.MessageDelivered})
			return nil
		}
	}
	j := job{kind: jobPrompt, msg: msg}
	if cmd, ok := strings.CutPrefix(strings.TrimSpace(msg.Text), "!"); ok && len(msg.Attachments) == 0 && strings.TrimSpace(cmd) != "" {
		j.kind, j.args = jobShell, strings.TrimSpace(cmd)
	}
	// The person writing after a limit is the person going on with this
	// account: what the limit held goes first, then this. A message the
	// journal has from before the limited turn's end was written while it
	// ran, whenever it arrives: it waits with what the limit held.
	a.mu.Lock()
	if a.held != "" && msg.AfterTurn != a.held {
		a.queue = append(a.queue, j)
		a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: msg.MessageID, State: agent.MessageQueued})
		a.mu.Unlock()
		return nil
	}
	released := a.held != ""
	a.held = ""
	a.mu.Unlock()
	err := a.submit(ctx, j)
	if released {
		go a.next()
	}
	return err
}

// submit runs a job now, or queues it behind the running turn (or the
// account switch).
func (a *Adapter) submit(ctx context.Context, j job) error {
	a.mu.Lock()
	if a.posting || a.turn != nil || a.switching || len(a.queue) > 0 {
		a.queue = append(a.queue, j)
		if j.msg.MessageID != "" {
			a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: j.msg.MessageID, State: agent.MessageQueued})
		}
		a.mu.Unlock()
		return nil
	}
	return a.runLocked(ctx, j)
}

// next runs the first queued job, if the adapter is free for it.
func (a *Adapter) next() {
	a.mu.Lock()
	if a.ending || a.posting || a.turn != nil || a.switching || a.held != "" || len(a.queue) == 0 {
		a.mu.Unlock()
		return
	}
	j := a.queue[0]
	a.queue = a.queue[1:]
	ctx, cancel := context.WithTimeout(context.Background(), requestWait)
	defer cancel()
	err := a.runLocked(ctx, j)
	switch {
	case err == nil:
	case j.msg.MessageID != "":
		a.out.Emit(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: j.msg.MessageID, Text: err.Error()})
	default:
		a.out.Emit(agent.Event{Type: agent.TypeError, Code: "command_failed", Text: j.display + ": " + err.Error()})
	}
}

// runLocked starts a job. It is called with mu held and releases it.
func (a *Adapter) runLocked(ctx context.Context, j job) error {
	cl, session := a.cl, a.session
	oc := ascendingID("msg")
	u := &userMsg{our: oc, display: j.display}
	if j.msg.MessageID != "" {
		u.our, u.journaled = j.msg.MessageID, true
		a.state.Messages[j.msg.MessageID] = oc
		a.saveState()
	}
	u.adopt = j.kind == jobCompact
	a.users[oc] = u
	body, err := a.bodyLocked(j, oc)
	if err != nil {
		delete(a.users, oc)
		a.mu.Unlock()
		return err
	}
	if j.kind == jobPrompt {
		a.posting = true
		a.mu.Unlock()
		err := cl.call(ctx, http.MethodPost, "/session/"+session+"/prompt_async", body, nil)
		a.mu.Lock()
		a.posting = false
		if err != nil {
			delete(a.users, oc)
			delete(a.state.Messages, j.msg.MessageID)
			a.saveState()
			a.mu.Unlock()
			go a.next()
			return err
		}
		a.beginTurnLocked(oc)
		a.mu.Unlock()
		// The turn may be over already: one that fails at once can end
		// on /event before the post is answered, and the next() its end
		// started found the post still on its way.
		go a.next()
		return nil
	}
	// A shell line, a command and a compaction answer when they are done, so
	// they run beside the host's command path, for as long as they take or
	// until the process is stopped; their turn starts now.
	a.beginTurnLocked(oc)
	turnID, life := a.turn.id, a.life
	a.mu.Unlock()
	path := map[string]string{jobShell: "/shell", jobCommand: "/command", jobCompact: "/summarize"}[j.kind]
	go func() {
		err := cl.long(life, http.MethodPost, "/session/"+session+path, body, nil)
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.turn == nil || a.turn.id != turnID || life.Err() != nil {
			// The turn ended on its session.idle, or the process is being
			// stopped, and what stops it ends the turn.
			return
		}
		if err != nil {
			a.turn.failure = err.Error()
			a.endTurnLocked()
			return
		}
		a.settleLocked(cl, session, turnID)
	}()
	return nil
}

// settleLocked ends a turn whose call returned, if OpenCode is idle and the
// session.idle that ends it normally was missed.
func (a *Adapter) settleLocked(cl *client, session, turnID string) {
	go func() {
		time.Sleep(500 * time.Millisecond)
		var status map[string]ocStatus
		if cl.call(context.Background(), http.MethodGet, "/session/status", nil, &status) != nil {
			return
		}
		if _, busy := status[session]; busy {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.turn != nil && a.turn.id == turnID {
			a.endTurnLocked()
		}
	}()
}

// bodyLocked is the request body of a job.
func (a *Adapter) bodyLocked(j job, oc string) (any, error) {
	provider, model, hasModel := splitModel(a.model)
	agentID := a.agentID
	switch j.kind {
	case jobPrompt:
		parts, err := promptParts(j.msg)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"messageID": oc, "parts": parts}
		if hasModel {
			body["model"] = ocModelRef{ProviderID: provider, ModelID: model}
		}
		if agentID != "" {
			body["agent"] = agentID
		}
		if a.effort != "" {
			body["variant"] = a.effort
		}
		// OpenCode adds a prompt's system to its own for that prompt's
		// turn, so every prompt carries the instructions of the model it
		// runs on, and a switch needs nothing more.
		if p := agent.ModelPrompt(a.model, a.spec.InstructionsFor(a.model)); p != "" {
			body["system"] = p
		}
		return body, nil
	case jobShell:
		body := map[string]any{"messageID": oc, "command": j.args, "agent": firstNonEmpty(agentID, AgentBuild)}
		if hasModel {
			body["model"] = ocModelRef{ProviderID: provider, ModelID: model}
		}
		return body, nil
	case jobCommand:
		body := map[string]any{"messageID": oc, "command": j.name, "arguments": j.args}
		if hasModel {
			body["model"] = provider + "/" + model
		}
		if agentID != "" {
			body["agent"] = agentID
		}
		if a.effort != "" {
			body["variant"] = a.effort
		}
		return body, nil
	case jobCompact:
		if !hasModel {
			provider, model, hasModel = splitModel(a.lastModel)
		}
		if !hasModel {
			return nil, errors.New("compacting needs a model; choose one first")
		}
		return map[string]any{"providerID": provider, "modelID": model, "auto": false}, nil
	}
	return nil, fmt.Errorf("unknown job %q", j.kind)
}

// promptParts is a message as prompt parts: its text, each image inline as a
// data URL, every other file by its path, which OpenCode reads itself.
func promptParts(msg agent.Outgoing) ([]any, error) {
	var parts []any
	if msg.Text != "" {
		parts = append(parts, ocTextPart{Type: "text", Text: msg.Text})
	}
	for _, att := range msg.Attachments {
		if att.Path == "" {
			continue
		}
		name := firstNonEmpty(att.Name, filepath.Base(att.Path))
		mime := firstNonEmpty(att.Mime, "application/octet-stream")
		if strings.HasPrefix(mime, "image/") && att.Size <= maxImage {
			raw, err := os.ReadFile(att.Path)
			if err != nil {
				return nil, fmt.Errorf("read the attachment %s: %w", name, err)
			}
			if len(raw) <= maxImage {
				parts = append(parts, ocFilePart{Type: "file", Mime: mime, Filename: name,
					URL: "data:" + mime + ";base64," + encodeBase64(raw)})
				continue
			}
		}
		parts = append(parts, ocFilePart{Type: "file", Mime: mime, Filename: name, URL: "file://" + att.Path})
	}
	if len(parts) == 0 {
		parts = append(parts, ocTextPart{Type: "text", Text: " "})
	}
	return parts, nil
}

// Interrupt aborts the running turn. What was written stays; queued
// messages run after it.
func (a *Adapter) Interrupt(ctx context.Context) error {
	a.mu.Lock()
	cl, session := a.cl, a.session
	if a.turn != nil {
		a.turn.aborted = true
	}
	a.mu.Unlock()
	if cl == nil {
		return errStopped
	}
	return cl.call(ctx, http.MethodPost, "/session/"+session+"/abort", nil, nil)
}

// Answer answers one of OpenCode's own questions. ask_user's own calls never
// get here: they wait in the host, on the connection of `socrates mcp-ask`
// (AGENT-HOST.md §4.4), and an answer the host could not hand to one is
// agent.ErrAskGone here.
func (a *Adapter) Answer(ctx context.Context, askID string, answer json.RawMessage) error {
	return a.answerQuestion(ctx, askID, answer)
}

// SetModel takes effect from the next prompt: model and variant go with
// every prompt.
func (a *Adapter) SetModel(_ context.Context, model, effort string) error {
	model = strings.TrimSpace(model)
	if model != "" {
		if _, _, ok := splitModel(model); !ok {
			return fmt.Errorf("%q is not provider/model", model)
		}
	}
	a.mu.Lock()
	if model != "" {
		a.model = model
	}
	a.effort = strings.TrimSpace(effort)
	model, effort = a.model, a.effort
	a.mu.Unlock()
	a.out.Emit(agent.Event{Type: agent.TypeModelChanged, Model: model, Effort: effort})
	return nil
}

// Command runs one of the agent's commands:
//
//	compact            POST /summarize, as a turn
//	shell <cmd>        POST /shell, as a turn (what a "!" line does)
//	agent build|plan   the agent the next prompts run as (plan mode)
//	revert <message>   undo the files from that message on (POST /revert)
//	unrevert           take the revert back
//	fork [<message>]   copy the session (up to that message) into a new one,
//	                   reported as notice{forked, conversation_id}
//	<name> [args]      one of GET /command, as a turn
func (a *Adapter) Command(ctx context.Context, name, args string) error {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	args = strings.TrimSpace(args)
	switch name {
	case "compact", "summarize":
		return a.submit(ctx, job{kind: jobCompact, display: "/compact"})
	case "shell":
		if args == "" {
			return errors.New("shell needs a command")
		}
		return a.submit(ctx, job{kind: jobShell, args: args, display: "!" + args})
	case "agent":
		return a.setAgent(args)
	case "revert":
		return a.revert(ctx, args)
	case "unrevert":
		return a.unrevert(ctx)
	case "fork":
		return a.fork(ctx, args)
	}
	a.mu.Lock()
	known := a.commands[name]
	a.mu.Unlock()
	if !known {
		return agent.ErrUnknownCommand
	}
	display := "/" + name
	if args != "" {
		display += " " + args
	}
	return a.submit(ctx, job{kind: jobCommand, name: name, args: args, display: display})
}

func (a *Adapter) setAgent(id string) error {
	if id != AgentBuild && id != AgentPlan {
		return fmt.Errorf("the agent is %s or %s, not %q", AgentBuild, AgentPlan, id)
	}
	a.mu.Lock()
	a.agentID = id
	a.state.Agent = id
	a.saveState()
	a.mu.Unlock()
	text := "Plan mode: the agent plans and does not edit files."
	if id == AgentBuild {
		text = "Build mode: the agent edits files and runs commands."
	}
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticeAgent, Text: text, Args: id})
	return nil
}

// ocMessage is OpenCode's id for a message the journal names (or an
// OpenCode id as it is).
func (a *Adapter) ocMessage(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if oc := a.state.Messages[id]; oc != "" {
		return oc
	}
	return id
}

func (a *Adapter) revert(ctx context.Context, message string) error {
	oc := a.ocMessage(message)
	if !validID(oc, "msg") {
		return fmt.Errorf("revert needs a message, not %q", message)
	}
	a.mu.Lock()
	cl, session, busy := a.cl, a.session, a.turn != nil || a.posting
	a.mu.Unlock()
	if busy {
		return errors.New("the agent is working; stop it before reverting")
	}
	if err := cl.call(ctx, http.MethodPost, "/session/"+session+"/revert", map[string]string{"messageID": oc}, nil); err != nil {
		return err
	}
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticeReverted, MessageID: message,
		Text: "The files are back as they were before this message. The next message continues from here."})
	return nil
}

func (a *Adapter) unrevert(ctx context.Context) error {
	a.mu.Lock()
	cl, session := a.cl, a.session
	a.mu.Unlock()
	if err := cl.call(ctx, http.MethodPost, "/session/"+session+"/unrevert", nil, nil); err != nil {
		return err
	}
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticeUnreverted, Text: "The revert was taken back."})
	return nil
}

func (a *Adapter) fork(ctx context.Context, message string) error {
	body := map[string]string{}
	if message != "" {
		oc := a.ocMessage(message)
		if !validID(oc, "msg") {
			return fmt.Errorf("fork needs a message, not %q", message)
		}
		body["messageID"] = oc
	}
	a.mu.Lock()
	cl, session := a.cl, a.session
	a.mu.Unlock()
	var s ocSession
	if err := cl.call(ctx, http.MethodPost, "/session/"+session+"/fork", body, &s); err != nil {
		return err
	}
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticeForked, ConversationID: s.ID, MessageID: message,
		Text: "The conversation was copied into a new one."})
	return nil
}

// Park stops the server; the session stays in the shared database for the
// next start.
func (a *Adapter) Park(context.Context) error {
	a.stopProcess(true)
	return nil
}

// Stop ends the server now: abort, SIGTERM, SIGKILL after five seconds.
func (a *Adapter) Stop() error {
	a.stopProcess(true)
	return nil
}

// stopProcess ends the server, aborting a running turn first so that what
// it wrote is closed properly. final marks the adapter as done.
func (a *Adapter) stopProcess(final bool) {
	a.mu.Lock()
	if final {
		if a.ending {
			a.mu.Unlock()
			return
		}
		a.ending = true
	}
	srv, cl, session, cancel, busy := a.srv, a.cl, a.session, a.cancel, a.turn != nil || a.posting
	a.mu.Unlock()
	if busy && cl != nil && session != "" {
		ctx, done := context.WithTimeout(context.Background(), abortWait)
		_ = cl.call(ctx, http.MethodPost, "/session/"+session+"/abort", nil, nil)
		done()
	}
	if cancel != nil {
		cancel()
	}
	srv.stop(stopGrace)
}

// Background: OpenCode's subagents run inside the turn that started them.
func (a *Adapter) Background() int { return 0 }

// PID is the server's.
func (a *Adapter) PID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.srv.pid()
}

// SwitchAccount moves the chat to another account: the running turn is
// interrupted and waited for, the server is stopped, and one for the other
// account's data home and environment continues the same session
// (research/06 §3.3). Two processes never serve the session at once. The
// queue holds while it switches, so that what was queued or is sent
// meanwhile runs on the new account. When the new process does not come
// up, the adapter has none left: it reports Exited, and the host that
// follows resumes. The caller (WP7) journals account.switched.
func (a *Adapter) SwitchAccount(ctx context.Context, account string, env []string) error {
	if account == "" {
		account = DefaultAccount
	}
	if err := a.layout.PrepareAccount(account); err != nil {
		return err
	}
	a.mu.Lock()
	switch {
	case a.ending:
		a.mu.Unlock()
		return errStopped
	case a.switching:
		a.mu.Unlock()
		return errors.New("opencode: an account switch is already under way")
	}
	a.switching = true
	busy := a.turn != nil || a.posting
	a.mu.Unlock()
	if busy {
		// An abort that fails or does not end the turn in time is not the
		// end of the switch: the stop below ends the turn either way.
		if err := a.Interrupt(ctx); err != nil {
			log.Printf("opencode: interrupt for an account switch: %v", err)
		}
		deadline := time.Now().Add(idleWait)
		for time.Now().Before(deadline) {
			a.mu.Lock()
			busy = a.turn != nil || a.posting
			a.mu.Unlock()
			if !busy {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	a.stopProcess(false)
	a.mu.Lock()
	if a.turn != nil {
		a.turn.aborted = true
		a.endTurnLocked()
	}
	if a.ending {
		// Stopped meanwhile: there is nothing to switch.
		a.switching = false
		a.mu.Unlock()
		return errStopped
	}
	a.account, a.spec.Account, a.spec.Env = account, account, env
	a.mu.Unlock()
	if err := a.startProcess(ctx); err != nil {
		a.mu.Lock()
		a.ending, a.switching = true, false
		queue := a.queue
		a.queue = nil
		a.mu.Unlock()
		// A queued message is still undelivered in the journal, and the next
		// host sends it; a queued command is lost with this one.
		for _, j := range queue {
			if j.msg.MessageID == "" {
				a.out.Emit(agent.Event{Type: agent.TypeError, Code: "command_failed", Text: j.display + ": the account switch failed"})
			}
		}
		err = fmt.Errorf("opencode: start on account %s: %w", account, err)
		a.out.Exited(err)
		return err
	}
	a.mu.Lock()
	a.switching, a.held = false, ""
	srv, cancel, stopped := a.srv, a.cancel, a.ending
	a.mu.Unlock()
	if stopped {
		// Stopped while the new process came up: it goes too.
		cancel()
		srv.stop(stopGrace)
		return errStopped
	}
	go a.next()
	return nil
}

func newTurnID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "t_" + hex.EncodeToString(b[:])
}
