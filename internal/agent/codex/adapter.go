//go:build !windows

package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// Harness is the name the adapter is registered under.
const Harness = "codex"

// BinEnv names the codex binary in a chat's environment, for a binary that is
// not the first `codex` on its PATH when the dashboard configured none (a
// fake, in the tests).
const BinEnv = "SOCRATES_CODEX_BIN"

// Version is what the adapter calls itself in initialize's clientInfo; it ends
// up in the app-server's user agent and the thread's originator.
var Version = "dev"

func init() {
	agent.Register(Harness, func() agent.Adapter { return newAdapter() })
}

// The adapter's clock.
var (
	// errorGrace is how long a turn whose model call failed for good waits
	// for the turn/completed that 0.164 sends right after; past it, the turn
	// is ended here so that a chat never stays busy on a lost notification.
	errorGrace = 30 * time.Second
	// goneWait is how long a steer that codex answers with "no active turn"
	// waits for that turn's turn/completed: codex lets a turn go before it
	// sends its end, and how it ended (a limit) decides what the message does.
	goneWait = 5 * time.Second
	// diffEvery is the most often a turn's aggregated diff is journaled while
	// it changes. Codex re-sends the whole diff each time.
	diffEvery = time.Second
	// stopWait is how long Stop waits for the killed process to be reaped.
	stopWait = 2 * time.Second
)

// adapter is one chat's app-server.
type adapter struct {
	spec   agent.StartSpec
	out    agent.Emitter
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tail
	rpc    *conn
	// exited is closed once the process is reaped.
	exited chan struct{}

	// sendMu makes turn starts, steers and the recovery chain one at a time,
	// whichever goroutine they come from.
	sendMu sync.Mutex

	mu sync.Mutex
	// thread is the chat's codex thread and path its rollout file; model is
	// what the thread runs on when the chat did not choose (for plan mode).
	thread      string
	path        string
	threadModel string
	model       string
	effort      string
	plan        bool
	ending      bool
	// active is the running root turn. starting is the message whose
	// turn/start is in flight, so the turn/started that answers it is known
	// as its turn.
	active   *turn
	starting *agent.Outgoing
	// expect is the kind of the next turn codex starts on its own after a
	// command (a compaction, a review, a shell command), which cannot be
	// steered, and expectMsg the "!" line that asked for a shell command:
	// codex answers the call before it starts the turn.
	expect    turnKind
	expectMsg string
	// turns are the root turns by codex turn id, kept for their subagents'
	// late events; retry is the journal turn a recovery restarts.
	turns map[string]*turn
	retry *turn
	// queue holds messages codex cannot take now (a compaction or review
	// runs, or a "!" line waits for the turn to end); steered are messages
	// handed to the running turn, by client id, until codex takes them up.
	queue   []agent.Outgoing
	steered map[string]agent.Outgoing
	// held is the turn the account's limit ended, which stops the queue:
	// what was queued behind it would go into the account at its limit and
	// end limited too. An account switch (a new adapter, which the host
	// hands what was never taken up) lets it go, and so does a message the
	// person wrote after that turn's end; one written before it, though the
	// host hands it over only now, waits with the rest (AGENT-HOST.md §5.2,
	// agent.Outgoing.AfterTurn). Empty when nothing holds.
	held    string
	agents  map[string]*subagent
	running int
	waits   agent.Waits
	calls   map[string]*call
	limits  map[string]agent.Event
	context int64
	window  int64
	// recovered is how far the invalid_encrypted_content fallback chain got
	// in this process (§4.3): 0 none, 1 stripped, 2 handed off.
	recovered int
}

func newAdapter() *adapter {
	return &adapter{
		exited:  make(chan struct{}),
		turns:   map[string]*turn{},
		steered: map[string]agent.Outgoing{},
		agents:  map[string]*subagent{},
		calls:   map[string]*call{},
		limits:  map[string]agent.Event{},
	}
}

// ------------------------------------------------------------------- start

func (a *adapter) Start(ctx context.Context, spec agent.StartSpec, out agent.Emitter) error {
	a.spec, a.out = spec, out
	a.model, a.effort = spec.Model, spec.Effort
	if err := a.launch(); err != nil {
		return err
	}
	if err := a.handshake(ctx); err != nil {
		a.kill()
		return err
	}
	if spec.First != nil {
		if err := a.Send(ctx, *spec.First); err != nil {
			out.Emit(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: spec.First.MessageID, Text: err.Error()})
		}
	}
	return nil
}

// launch starts `codex app-server` on stdio with the fixed policy.
func (a *adapter) launch() error {
	if len(a.spec.Env) == 0 {
		// exec would hand codex the host's whole environment; the chat's
		// environment is always explicit (AGENT-HOST.md §5.2).
		return errors.New("codex needs the chat's environment, and the launch spec has none")
	}
	bin, err := codexBinary(a.spec)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, appServerArgs(a.spec.Workdir)...)
	cmd.Env = a.spec.Env
	cmd.Dir = a.spec.Workdir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	a.stderr = &tail{}
	cmd.Stderr = a.stderr
	if a.spec.Spawn != nil {
		err = a.spec.Spawn(cmd)
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		err = cmd.Start()
	}
	if err != nil {
		return fmt.Errorf("start codex: %w", err)
	}
	a.cmd, a.stdin, a.rpc = cmd, stdin, newConn(stdin)
	go a.read(stdout)
	return nil
}

// appServerArgs is the fixed command line (HARNESS-POLICY.md, Codex): stdio,
// no update check, and the working directory trusted, so that its project
// config and AGENTS.md apply without codex writing a trust entry of its own.
func appServerArgs(workdir string) []string {
	args := []string{"app-server", "--listen", "stdio://", "-c", "check_for_update_on_startup=false"}
	if workdir != "" {
		args = append(args, "-c", trustOverride(workdir))
	}
	return args
}

// trustOverride trusts one directory, as a whole inline table: codex splits a
// dotted -c key on every dot, quoted or not.
func trustOverride(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return "projects={" + tomlString(dir) + "={trust_level=" + tomlString("trusted") + "}}"
}

// tomlString quotes a TOML basic string; JSON's escaping is TOML's, but for
// DEL.
func tomlString(s string) string {
	quoted, _ := json.Marshal(s)
	return strings.ReplaceAll(string(quoted), "\u007f", `\u007F`)
}

// codexBinary is the program to run: the dashboard's binary setting, else
// BinEnv from the chat's environment, else codex on its PATH.
func codexBinary(spec agent.StartSpec) (string, error) {
	bin, err := agent.ResolveBinary(spec.Env, "codex", spec.Binary, envValue(spec.Env, BinEnv))
	if err != nil {
		return "", fmt.Errorf("codex is not installed: %w", err)
	}
	return bin, nil
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// handshake is initialize, then the thread: resumed, rebuilt or new.
func (a *adapter) handshake(ctx context.Context) error {
	var init struct {
		CodexHome string `json:"codexHome"`
	}
	if err := a.rpc.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "socrates", "title": "Socrates", "version": Version},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &init); err != nil {
		return a.startErr("codex refused the handshake", err)
	}
	if err := a.rpc.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	if err := a.resumeOrStart(ctx, a.spec.ResumeID); err != nil {
		return err
	}
	// The journal lost this conversation (AGENT-HOST.md §6): its history goes
	// back in from the thread before anything new happens.
	if a.spec.Rebuild {
		if err := a.rebuild(ctx, a.spec.ResumeID); err != nil {
			a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeHistoryUnavailable,
				Text: "The earlier history of this chat could not be read back from Codex: " + err.Error()})
		}
	}
	info, _ := json.Marshal(agent.AgentInfo{Agent: Harness, Commands: Commands})
	a.out.Emit(agent.Event{Type: agent.TypeAgentInfo, Content: info})
	a.mu.Lock()
	thread := a.thread
	a.mu.Unlock()
	a.out.Emit(agent.Event{Type: agent.TypeConversation, ConversationID: thread})
	return nil
}

// startErr names a failure, with the process's last words when it died.
func (a *adapter) startErr(what string, err error) error {
	if errors.Is(err, errClosed) {
		if last := a.stderr.last(); last != "" {
			return fmt.Errorf("%s: codex exited: %s", what, last)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (a *adapter) resumeOrStart(ctx context.Context, id string) error {
	if id == "" {
		return a.startThread(ctx, "")
	}
	return a.resume(ctx, id)
}

// threadResult is the part of thread/start's and thread/resume's answer the
// adapter reads.
type threadResult struct {
	Thread struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"thread"`
	Model string `json:"model"`
}

// approvalPolicy is "never" for everything that is a permission (decision §1:
// always allow; with danger-full-access nothing needs one), but lets MCP
// elicitations through: those are questions a server puts to the person,
// which "never" would decline on its own (research/04 §2.1), and they are
// asked like ask_user. The model's instructions say the same as for "never":
// every category it could ask for is rejected.
var approvalPolicy = map[string]any{"granular": map[string]any{"sandbox_approval": false, "rules": false,
	"skill_approval": false, "request_permissions": false, "mcp_elicitations": true}}

// policy is what every thread is started and resumed with (decision §1:
// always allow everything).
func (a *adapter) policy(params map[string]any) map[string]any {
	params["approvalPolicy"] = approvalPolicy
	params["sandbox"] = "danger-full-access"
	if a.spec.Workdir != "" {
		params["cwd"] = a.spec.Workdir
	}
	return params
}

// startThread starts a new thread. instructions, when set, are developer
// instructions for it (a handoff summary), after the ask_user guidance that
// every thread's developer instructions begin with.
func (a *adapter) startThread(ctx context.Context, instructions string) error {
	params := a.policy(map[string]any{})
	a.mu.Lock()
	model, effort := a.model, a.effort
	a.mu.Unlock()
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": effort}
	}
	if tools := dynamicToolSpecs(); len(tools) > 0 {
		params["dynamicTools"] = tools
	}
	developer := askGuidance
	if instructions != "" {
		developer += "\n\n" + instructions
	}
	params["developerInstructions"] = developer
	var res threadResult
	if err := a.rpc.call(ctx, "thread/start", params, &res); err != nil {
		return a.startErr("codex could not start a thread", err)
	}
	if res.Thread.ID == "" {
		return errors.New("codex started a thread without an id")
	}
	return a.setThread(res)
}

// resume loads an existing thread, without its turns: the journal has them.
func (a *adapter) resume(ctx context.Context, id string) error {
	var res threadResult
	if err := a.rpc.call(ctx, "thread/resume", a.policy(map[string]any{"threadId": id, "excludeTurns": true}), &res); err != nil {
		var rerr *rpcError
		if errors.As(err, &rerr) && strings.Contains(rerr.Message, "no rollout found") {
			return fmt.Errorf("%w: codex has no thread %s: %s", agent.ErrConversationNotFound, id, rerr.Message)
		}
		return a.startErr("codex could not resume the conversation "+id, err)
	}
	if res.Thread.ID == "" {
		res.Thread.ID = id
	}
	return a.setThread(res)
}

func (a *adapter) setThread(res threadResult) error {
	a.mu.Lock()
	a.thread, a.path = res.Thread.ID, res.Thread.Path
	if res.Model != "" {
		a.threadModel = res.Model
	}
	a.mu.Unlock()
	return nil
}

// read consumes the process's stdout until it ends, then reaps it.
func (a *adapter) read(stdout io.Reader) {
	broken := a.rpc.read(stdout, a.handle)
	if broken != nil {
		// Nothing more can be read, so nothing could be answered: the
		// app-server goes, and the host sees an exit rather than a chat
		// that waits for ever.
		a.rpc.close(errClosed)
		_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGKILL)
	}
	err := a.cmd.Wait()
	a.rpc.close(errClosed)
	a.mu.Lock()
	ending := a.ending
	a.ending = true
	for _, c := range a.calls {
		c.cancel()
	}
	a.mu.Unlock()
	close(a.exited)
	if ending {
		return
	}
	what := "codex exited"
	switch {
	case broken != nil:
		what = "codex was stopped because its output could not be read: " + broken.Error()
	case err != nil:
		what += ": " + err.Error()
	}
	if last := a.stderr.last(); last != "" {
		what += ": " + last
	}
	a.out.Exited(errors.New(what))
}

// ------------------------------------------------------------------- send

func (a *adapter) Send(ctx context.Context, msg agent.Outgoing) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	return a.send(ctx, msg)
}

// send hands one message to codex: a new turn when none runs, steered into
// the running one when it does, held when codex cannot take it now. The
// caller holds sendMu.
func (a *adapter) send(ctx context.Context, msg agent.Outgoing) error {
	a.mu.Lock()
	if a.ending {
		a.mu.Unlock()
		return errClosed
	}
	if a.heldBackLocked(msg) {
		a.mu.Unlock()
		a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: msg.MessageID, State: agent.MessageQueued})
		return nil
	}
	// The person writing after a limit is the person going on with this
	// account: what the limit held goes first, then this.
	released := a.held != ""
	a.held = ""
	active := a.active
	shell := isShellLine(msg.Text)
	if active != nil && (shell || active.kind != turnNormal) || len(a.queue) > 0 {
		a.queue = append(a.queue, msg)
		a.mu.Unlock()
		a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: msg.MessageID, State: agent.MessageQueued})
		if released {
			go a.flush()
		}
		return nil
	}
	a.mu.Unlock()
	if active == nil {
		return a.startTurn(ctx, msg)
	}
	err := a.steer(ctx, active, msg)
	var rerr *rpcError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rerr) && notSteerable(rerr):
		a.mu.Lock()
		a.queue = append(a.queue, msg)
		a.mu.Unlock()
		a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: msg.MessageID, State: agent.MessageQueued})
		return nil
	case errors.As(err, &rerr) && strings.Contains(rerr.Message, "no active turn"):
		// The turn ended between the look and the steer; if the limit
		// ended it, the message was written before that end. Codex says
		// the turn is gone before its turn/completed comes, so the end is
		// waited for: a turn started now would run into the same limit.
		a.awaitGone(ctx, active)
		a.mu.Lock()
		held := a.heldBackLocked(msg)
		a.mu.Unlock()
		if held {
			a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: msg.MessageID, State: agent.MessageQueued})
			return nil
		}
		return a.startTurn(ctx, msg)
	}
	return err
}

// awaitGone waits, for at most goneWait, until the adapter has seen the end of
// t, a turn codex says is gone: t is no longer the active turn once its
// turn/completed closed it (or a recovery took it over, or codex started
// another). The caller holds sendMu, not mu.
func (a *adapter) awaitGone(ctx context.Context, t *turn) {
	deadline := time.Now().Add(goneWait)
	for {
		a.mu.Lock()
		gone := a.active != t || a.ending
		a.mu.Unlock()
		if gone || ctx.Err() != nil || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// heldBackLocked queues msg with what the limit holds when the journal has
// it from before the limited turn's end: the person wrote it while that turn
// ran, whenever it arrives. It reports whether it did. Called with mu held.
func (a *adapter) heldBackLocked(msg agent.Outgoing) bool {
	if a.held == "" || msg.AfterTurn == a.held {
		return false
	}
	a.queue = append(a.queue, msg)
	return true
}

// isShellLine is a "!" line, which runs as a shell command of its own.
func isShellLine(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "!") && len(t) > 1 && !strings.Contains(t, "\n")
}

// notSteerable is codex's "cannot steer a compact (or review) turn".
func notSteerable(e *rpcError) bool {
	var data struct {
		CodexErrorInfo map[string]json.RawMessage `json:"codexErrorInfo"`
	}
	if json.Unmarshal(e.Data, &data) == nil {
		if _, ok := data.CodexErrorInfo["activeTurnNotSteerable"]; ok {
			return true
		}
	}
	return strings.Contains(e.Message, "cannot steer")
}

// input is a message as codex input items: the text, images as local images
// and any other file named by its path.
func input(msg agent.Outgoing) []any {
	text := msg.Text
	var items []any
	for _, at := range msg.Attachments {
		if at.Path == "" {
			continue
		}
		if strings.HasPrefix(at.Mime, "image/") {
			items = append(items, map[string]any{"type": "localImage", "path": at.Path})
			continue
		}
		text += fmt.Sprintf("\n\nAttached file %s: %s", at.Name, at.Path)
	}
	return append([]any{map[string]any{"type": "text", "text": text, "text_elements": []any{}}}, items...)
}

// startTurn opens a turn with msg: turn/start with the model and effort on
// every turn, so a resumed thread runs on what the chat chose, or a shell
// command for a "!" line.
func (a *adapter) startTurn(ctx context.Context, msg agent.Outgoing) error {
	a.mu.Lock()
	thread, model, effort, plan := a.thread, a.model, a.effort, a.plan
	threadModel := a.threadModel
	a.starting = &msg
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.starting != nil && a.starting.MessageID == msg.MessageID {
			a.starting = nil
		}
		a.mu.Unlock()
	}()
	if isShellLine(msg.Text) {
		command := strings.TrimPrefix(strings.TrimSpace(msg.Text), "!")
		a.mu.Lock()
		a.starting = nil
		a.expect, a.expectMsg = turnShell, msg.MessageID
		a.mu.Unlock()
		return a.rpc.call(ctx, "thread/shellCommand", map[string]any{"threadId": thread, "command": command}, nil)
	}
	params := map[string]any{"threadId": thread, "input": input(msg), "clientUserMessageId": msg.MessageID}
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["effort"] = effort
	}
	if plan {
		if model == "" {
			model = threadModel
		}
		var re any
		if effort != "" {
			re = effort
		}
		params["collaborationMode"] = map[string]any{"mode": "plan",
			"settings": map[string]any{"model": model, "reasoning_effort": re, "developer_instructions": nil}}
	}
	var res struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := a.rpc.call(ctx, "turn/start", params, &res); err != nil {
		return err
	}
	a.mu.Lock()
	t := a.openTurnLocked(res.Turn.ID, msg.MessageID, turnNormal)
	t.addInput(msg)
	a.mu.Unlock()
	return nil
}

// steer adds msg to the running turn. It is queued until codex takes it up at
// its next step, which the userMessage item carrying its id says.
func (a *adapter) steer(ctx context.Context, t *turn, msg agent.Outgoing) error {
	// Registered before the call: codex may take the message up, and send
	// its userMessage item, before it answers the steer.
	a.mu.Lock()
	thread := a.thread
	a.steered[msg.MessageID] = msg
	t.addInput(msg)
	a.mu.Unlock()
	err := a.rpc.call(ctx, "turn/steer", map[string]any{"threadId": thread, "expectedTurnId": t.codexID,
		"input": input(msg), "clientUserMessageId": msg.MessageID}, nil)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, waiting := a.steered[msg.MessageID]
	if err != nil {
		// Not steered: whatever took it meanwhile (a turn that ended and
		// held it for the next one) lets go, so the caller decides once.
		delete(a.steered, msg.MessageID)
		t.removeInput(msg.MessageID)
		for i, q := range a.queue {
			if q.MessageID == msg.MessageID {
				a.queue = append(a.queue[:i], a.queue[i+1:]...)
				break
			}
		}
		return err
	}
	if waiting {
		a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: msg.MessageID, State: agent.MessageQueued})
	}
	return nil
}

// flush starts the next turn with what was held, once a turn has ended.
func (a *adapter) flush() {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	if a.ending || a.active != nil || a.held != "" || len(a.queue) == 0 {
		a.mu.Unlock()
		return
	}
	queue := a.queue
	a.queue = nil
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for i, msg := range queue {
		if err := a.send(ctx, msg); err != nil {
			a.out.Emit(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: msg.MessageID, Text: err.Error()})
		}
		a.mu.Lock()
		held := len(a.queue) > 0
		if held {
			// codex could not take this one; the rest wait behind it.
			a.queue = append(a.queue, queue[i+1:]...)
		}
		a.mu.Unlock()
		if held {
			return
		}
	}
}

// ---------------------------------------------------------------- commands

// Interrupt ends the active turn. Once codex has taken the interrupt, the
// turn's requests are withdrawn at once, as its end would withdraw them: an
// answer from then on goes as a message, never to a call codex dropped.
func (a *adapter) Interrupt(ctx context.Context) error {
	a.mu.Lock()
	t, thread := a.active, a.thread
	a.mu.Unlock()
	if t == nil {
		return nil
	}
	err := a.rpc.call(ctx, "turn/interrupt", map[string]any{"threadId": thread, "turnId": t.codexID}, nil)
	var rerr *rpcError
	if errors.As(err, &rerr) {
		// The turn ended on its own meanwhile.
		return nil
	}
	if err != nil {
		return err
	}
	a.mu.Lock()
	for _, c := range a.calls {
		if c.turn == t {
			c.cancel()
		}
	}
	a.mu.Unlock()
	return nil
}

// Answer hands an answer to the call waiting on it (agent.Waits). A call
// whose wait ended (its turn ended or was interrupted, codex withdrew it) is
// gone even before its goroutine has let go of it.
func (a *adapter) Answer(_ context.Context, askID string, answer json.RawMessage) error {
	return a.waits.Answer(askID, answer)
}

func (a *adapter) SetModel(_ context.Context, model, effort string) error {
	a.mu.Lock()
	if model != "" {
		a.model = model
	}
	a.effort = effort
	a.mu.Unlock()
	a.out.Emit(agent.Event{Type: agent.TypeModelChanged, Model: model, Effort: effort})
	return nil
}

func (a *adapter) Command(ctx context.Context, name, args string) error {
	// One at a time with sends and the recovery chain: a compaction asked
	// for while a refused turn is being retried waits for the retry, so its
	// turn is never taken for the retried one.
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	thread := a.thread
	a.mu.Unlock()
	args = strings.TrimSpace(args)
	switch strings.TrimPrefix(name, "/") {
	case "compact":
		a.expectTurn(turnOther)
		return a.rpc.call(ctx, "thread/compact/start", map[string]any{"threadId": thread}, nil)
	case "review":
		a.expectTurn(turnOther)
		return a.rpc.call(ctx, "review/start", map[string]any{"threadId": thread, "target": reviewTarget(args)}, nil)
	case "plan":
		on := args != "off"
		a.mu.Lock()
		a.plan = on
		a.mu.Unlock()
		text := "Plan mode is on: Codex plans before it changes anything."
		if !on {
			text = "Plan mode is off."
		}
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: "plan_mode", Text: text})
		return nil
	case "shell", "!":
		if args == "" {
			return errors.New("the shell command is empty")
		}
		a.expectTurn(turnShell)
		return a.rpc.call(ctx, "thread/shellCommand", map[string]any{"threadId": thread, "command": args}, nil)
	}
	return agent.ErrUnknownCommand
}

func (a *adapter) expectTurn(k turnKind) {
	a.mu.Lock()
	a.expect, a.expectMsg = k, ""
	a.mu.Unlock()
}

// reviewTarget reads /review's argument: nothing for the uncommitted changes,
// "base <branch>", "commit <sha>", or instructions of one's own.
func reviewTarget(args string) map[string]any {
	word, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch {
	case args == "":
		return map[string]any{"type": "uncommittedChanges"}
	case word == "base" && rest != "":
		return map[string]any{"type": "baseBranch", "branch": rest}
	case word == "commit" && rest != "":
		return map[string]any{"type": "commit", "sha": rest, "title": nil}
	}
	return map[string]any{"type": "custom", "instructions": args}
}

// Park closes stdin, which ends the app-server at once; the thread is on disk
// and the next host resumes it.
func (a *adapter) Park(ctx context.Context) error {
	a.mu.Lock()
	a.ending = true
	a.mu.Unlock()
	if a.stdin != nil {
		_ = a.stdin.Close()
	}
	select {
	case <-a.exited:
		return nil
	case <-ctx.Done():
		a.kill()
		return ctx.Err()
	}
}

func (a *adapter) Stop() error {
	a.mu.Lock()
	a.ending = true
	a.mu.Unlock()
	a.kill()
	return nil
}

// kill ends the app-server and everything it started.
func (a *adapter) kill() {
	a.mu.Lock()
	a.ending = true
	a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-a.exited:
	case <-time.After(stopWait):
	}
}

// Background counts what parking would cut short: subagents still at work
// and background terminals.
func (a *adapter) Background() int {
	a.mu.Lock()
	n, thread, ending := a.running, a.thread, a.ending
	a.mu.Unlock()
	if ending || thread == "" {
		return n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := a.rpc.call(ctx, "thread/backgroundTerminals/list", map[string]any{"threadId": thread}, &res); err == nil {
		n += len(res.Data)
	}
	return n
}

func (a *adapter) PID() int {
	if a.cmd == nil || a.cmd.Process == nil {
		return 0
	}
	return a.cmd.Process.Pid
}

func newID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
