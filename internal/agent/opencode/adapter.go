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
	// idleWait is how long an account switch waits for an interrupted turn.
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
	srv     *server
	cl      *client
	gen     int
	cancel  context.CancelFunc
	session string
	account string
	model   string
	effort  string
	agentID string
	state   persisted
	ending  bool
	// switching is an account switch in progress: the old process's exit is
	// expected.
	switching bool

	// posting is a prompt on its way to OpenCode; turn the turn running.
	// Either one makes the adapter busy, and a message sent while it is
	// waits in queue: OpenCode would take a prompt sent during a turn into
	// that turn's next step, and an abort drops what it holds unanswered.
	posting bool
	turn    *turnState
	queue   []job

	users      map[string]*userMsg
	assistants map[string]*asstMsg
	parts      map[string]*seenPart
	children   map[string]childSession
	windows    map[string]int64
	lastModel  string
	retries    int
	commands   []Command
}

// New is an adapter, unstarted.
func New() *Adapter {
	return &Adapter{
		users: map[string]*userMsg{}, assistants: map[string]*asstMsg{}, parts: map[string]*seenPart{},
		children: map[string]childSession{}, windows: map[string]int64{},
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

type asstMsg struct {
	parent string
	done   bool
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
}

// childSession is a subagent's session: its parts go inside the task tool
// part that runs it.
type childSession struct {
	msg    string
	parent string
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
	context    int64
	model      string
}

// persisted is what the adapter keeps beside the journal: the session, the
// agent the chat runs as, and which OpenCode message each journaled message
// became - for a revert or a fork to a message, and to recognise a message
// the previous host already handed over.
type persisted struct {
	Session  string            `json:"session,omitempty"`
	Agent    string            `json:"agent,omitempty"`
	Messages map[string]string `json:"messages,omitempty"`
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
	a.account = spec.Account
	if a.account == "" {
		a.account = DefaultAccount
	}
	if spec.DataDir == "" {
		return errors.New("opencode: the data folder is not known")
	}
	a.layout = NewLayout(spec.DataDir)
	bin, err := findBinary(spec.Env)
	if err != nil {
		return fmt.Errorf("opencode: %w", err)
	}
	a.bin = bin
	a.loadState()
	a.agentID = a.state.Agent
	if err := a.layout.PrepareAccount(a.account); err != nil {
		return err
	}
	if err := a.layout.Migrate(ctx, bin, processEnv(spec.Env, a.layout, a.account, "", nil)); err != nil {
		return err
	}
	if err := a.startProcess(ctx); err != nil {
		return err
	}
	if err := a.openSession(ctx); err != nil {
		a.stopProcess(false)
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
	spawn := a.spec.Spawn
	if spawn == nil {
		spawn = func(cmd *exec.Cmd) error {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			return cmd.Start()
		}
	}
	base, account := a.spec.Env, a.account
	srv, err := startServer(ctx, spawn, a.bin, a.spec.Workdir, func(pass string) []string {
		return processEnv(base, a.layout, account, pass, config)
	})
	if err != nil {
		return err
	}
	cl := newClient(srv.base, srv.pass, a.spec.Workdir)
	if err := waitHealthy(ctx, cl, srv); err != nil {
		srv.stop(stopGrace)
		return err
	}
	a.mu.Lock()
	a.gen++
	gen := a.gen
	a.srv, a.cl = srv, cl
	sctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.mu.Unlock()
	connected := make(chan struct{})
	go a.follow(sctx, gen, cl, connected)
	go a.watch(gen, srv)
	select {
	case <-connected:
	case <-time.After(connectedWait):
		a.stopProcess(false)
		return errors.New("opencode: the event stream did not open")
	case <-srv.exited():
		return fmt.Errorf("opencode serve exited: %v: %s", srv.err, srv.out.String())
	}
	a.loadWindows(ctx, cl)
	return nil
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
	a.out.Emit(agent.Event{Type: agent.TypeConversation, ConversationID: id})
	msgs, err := a.messages(ctx, cl, id)
	if err != nil {
		return fmt.Errorf("opencode: read session %s: %w", id, err)
	}
	if a.spec.Rebuild {
		events := a.history(ctx, cl, msgs)
		a.mu.Lock()
		a.seed(msgs)
		a.mu.Unlock()
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
// §3.3). They are interrupted now. Called with mu held.
func (a *Adapter) dangling(msgs []ocMessageWithParts) []agent.Event {
	var events []agent.Event
	for _, m := range msgs {
		if m.Info.Role != "assistant" || m.Info.Time.Completed > 0 {
			continue
		}
		for _, p := range m.Parts {
			c, ok := convertPart(p)
			if !ok || (c.state != agent.PartRunning && c.state != agent.PartStreaming) {
				continue
			}
			events = append(events, agent.Event{Type: agent.TypePart, MessageID: m.Info.ID, PartID: p.ID,
				Kind: c.kind, State: agent.PartInterrupted, Text: c.text, Content: c.content})
		}
		events = append(events, agent.Event{Type: agent.TypeMessageState, MessageID: m.Info.ID, State: agent.MessageInterrupted})
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
				a.parts[p.ID] = &seenPart{msg: m.Info.ID, kind: c.kind, state: c.state, text: c.text, content: c.content, diff: c.diff != nil}
				if c.child != "" {
					a.children[c.child] = childSession{msg: m.Info.ID, parent: p.ID}
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

// loadCommands reads OpenCode's commands and tells the chat, for the
// composer's palette. compact is the adapter's own.
func (a *Adapter) loadCommands(ctx context.Context) {
	var list []Command
	if err := a.client().call(ctx, http.MethodGet, "/command", nil, &list); err != nil {
		log.Printf("opencode: list the commands: %v", err)
	}
	all := append([]Command{{Name: "compact", Description: "Summarize the conversation to free context", Source: "socrates"}}, list...)
	for i := range all {
		all[i] = Command{Name: all[i].Name, Description: all[i].Description, Source: all[i].Source, Hints: all[i].Hints}
	}
	a.mu.Lock()
	a.commands = all
	a.mu.Unlock()
	raw, _ := json.Marshal(all)
	a.out.Emit(agent.Event{Type: agent.TypeCommands, Content: raw})
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
			ready := make(chan struct{})
			var once sync.Once
			signal := func() {
				once.Do(func() {
					close(ready)
					if first {
						close(connected)
					}
				})
			}
			go func() {
				<-ready
				if !first {
					a.reconcile(ctx, gen, cl)
				}
			}()
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
			once.Do(func() {})
			first = false
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
// messages as OpenCode stored them, and whether it is still busy.
func (a *Adapter) reconcile(ctx context.Context, gen int, cl *client) {
	a.mu.Lock()
	session := a.session
	children := make([]string, 0, len(a.children))
	for id := range a.children {
		children = append(children, id)
	}
	a.mu.Unlock()
	if session == "" {
		return
	}
	for _, sid := range append([]string{session}, children...) {
		msgs, err := a.messages(ctx, cl, sid)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			a.handleMessage(gen, m.Info)
			for _, p := range m.Parts {
				a.handlePart(gen, p)
			}
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
	return a.submit(ctx, j)
}

// submit runs a job now, or queues it behind the running turn.
func (a *Adapter) submit(ctx context.Context, j job) error {
	a.mu.Lock()
	if a.posting || a.turn != nil || len(a.queue) > 0 {
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
	if a.ending || a.posting || a.turn != nil || len(a.queue) == 0 {
		a.mu.Unlock()
		return
	}
	j := a.queue[0]
	a.queue = a.queue[1:]
	ctx, cancel := context.WithTimeout(context.Background(), requestWait)
	defer cancel()
	if err := a.runLocked(ctx, j); err != nil && j.msg.MessageID != "" {
		a.out.Emit(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: j.msg.MessageID, Text: err.Error()})
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
		return nil
	}
	// A shell line, a command and a compaction answer when they are done, so
	// they run beside the host's command path; their turn starts now.
	a.beginTurnLocked(oc)
	turnID := a.turn.id
	a.mu.Unlock()
	path := map[string]string{jobShell: "/shell", jobCommand: "/command", jobCompact: "/summarize"}[j.kind]
	go func() {
		err := cl.call(context.Background(), http.MethodPost, "/session/"+session+path, body, nil)
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.turn == nil || a.turn.id != turnID {
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

// Answer: OpenCode's own question tool is denied, and ask_user (WP6) is not
// here yet, so no call ever waits on an answer through this adapter.
func (a *Adapter) Answer(context.Context, string, json.RawMessage) error { return agent.ErrAskGone }

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
	known := false
	for _, c := range a.commands {
		if c.Name == name && c.Source != "socrates" {
			known = true
		}
	}
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
// caller (WP7) journals account.switched.
func (a *Adapter) SwitchAccount(ctx context.Context, account string, env []string) error {
	if account == "" {
		account = DefaultAccount
	}
	if err := a.layout.PrepareAccount(account); err != nil {
		return err
	}
	a.mu.Lock()
	busy := a.turn != nil || a.posting
	a.mu.Unlock()
	if busy {
		if err := a.Interrupt(ctx); err != nil {
			return err
		}
		deadline := time.Now().Add(idleWait)
		for {
			a.mu.Lock()
			busy = a.turn != nil || a.posting
			a.mu.Unlock()
			if !busy {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("the turn did not end after the interrupt")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	a.mu.Lock()
	a.switching = true
	a.mu.Unlock()
	a.stopProcess(false)
	a.mu.Lock()
	a.account, a.spec.Account, a.spec.Env = account, account, env
	a.mu.Unlock()
	err := a.startProcess(ctx)
	a.mu.Lock()
	a.switching = false
	a.mu.Unlock()
	if err != nil {
		return err
	}
	go a.next()
	return nil
}

func newTurnID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "t_" + hex.EncodeToString(b[:])
}
