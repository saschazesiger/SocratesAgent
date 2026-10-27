//go:build !windows

// Package claude is the Claude Code adapter: one long-lived
// `claude -p --input-format stream-json --output-format stream-json` process
// per chat, driven through the CLI's control protocol, with its stream mapped
// onto the normalized chat events (REBUILD.md §3.1, AGENT-HOST.md §5).
//
// The protocol facts it relies on are docs/design/research/03-claude-code.md
// and 09-claude-subagents-workflows.md, observed against claude 2.1.309.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// Harness is the name the adapter is registered under, the same as the
// harness kind of the terminal launcher.
const Harness = "claude"

// APIKeyEnv is how an API-key account hands its key to the adapter: the
// variable in StartSpec.Env becomes ANTHROPIC_API_KEY in the CLI's
// environment. Every other ANTHROPIC_* variable is dropped, so a key that is
// merely in the server's own environment never reaches a chat.
const APIKeyEnv = "SOCRATES_CLAUDE_API_KEY"

// UltracodeEnv set to 1 in StartSpec.Env starts the CLI with ultracode on
// (dynamic workflows on every task), the per-session option of the terminal
// launcher.
const UltracodeEnv = "SOCRATES_CLAUDE_ULTRACODE"

// transcriptDays is how long Claude Code keeps a transcript: a chat has to be
// resumable after weeks away (HARNESS-POLICY.md).
const transcriptDays = 90

// stopWait is how long Park waits for the CLI to leave after its stdin
// closed, and termWait how long it then gets after SIGTERM (and Stop for it
// to be reaped after SIGKILL). drainWait is how long the CLI's stdout may
// stay open once the CLI has exited: a process it left behind that holds
// the pipe must not keep its end from being seen.
const (
	stopWait  = 20 * time.Second
	termWait  = 3 * time.Second
	drainWait = 3 * time.Second
)

// writeWait is how long a line to the CLI may take to be taken: the CLI reads
// its input all the time, during a turn too, so one that has not taken a line
// for this long is stuck, and is ended rather than left to hold up the host.
var writeWait = time.Minute

// quietLimit is how long a turn may go without a sign from the CLI while it
// waits on nobody (agent.QuietLimiter). Nothing Claude Code runs in the
// foreground takes that long: a command is sent to the background after two
// minutes and stopped after ten, a subagent and a workflow report as they
// go, and the API is retried with a notice. A turn this quiet is stuck.
var quietLimit = 30 * time.Minute

func init() {
	agent.Register(Harness, func() agent.Adapter { return New(Options{}) })
}

// Server is an in-process MCP server the CLI reaches over the control
// protocol (sdkMcpServers in initialize, then mcp_message requests). Handle
// gets one JSON-RPC message and returns the JSON-RPC response, nil for a
// notification and for a call the CLI withdrew (which is not answered); it
// may block for as long as the call takes (an ask_user call blocks until the
// person answers). Its context ends when the CLI withdraws the request or
// exits. Every chat has ask_user (askServer) this way; Options.Servers adds
// more.
type Server interface {
	Name() string
	// Config is the server's sdkMcpServerConfigs entry, nil for none.
	Config() json.RawMessage
	Handle(ctx context.Context, message json.RawMessage) (json.RawMessage, error)
}

// Answerer is a Server whose calls wait on asks: Answer hands it the answer
// to one of its asks, and reports agent.ErrAskGone for an ask it does not
// hold.
type Answerer interface {
	Answer(ctx context.Context, askID string, answer json.RawMessage) error
}

// Options are the adapter's own knobs; the zero value is production.
type Options struct {
	// Binary is the claude executable. Empty looks "claude" up on the PATH
	// of the CLI's environment.
	Binary string
	// AppendSystemPrompt is appended to Claude Code's system prompt, after
	// the ask_user guidance every chat gets.
	AppendSystemPrompt string
	// Servers are the in-process MCP servers of a chat, made once per start.
	Servers func(spec agent.StartSpec, out agent.Emitter) []Server
}

// Adapter drives one claude process.
type Adapter struct {
	opts Options

	// writeMu orders lines on the CLI's stdin.
	writeMu sync.Mutex
	stdin   *os.File

	ctlMu   sync.Mutex
	waiting map[string]chan controlResult
	// handling are the CLI's requests being answered on goroutines of their
	// own (MCP calls, elicitations), by request id, so that a withdrawn
	// request ends its handler; life ends them all when the CLI exits.
	// mcpCalls are the MCP requests among them by server and JSON-RPC id,
	// which is what the CLI's notifications/cancelled names.
	handling map[string]context.CancelFunc
	mcpCalls map[string]string
	life     context.Context
	endLife  context.CancelFunc

	mu      sync.Mutex
	out     agent.Emitter
	spec    agent.StartSpec
	cmd     *exec.Cmd
	servers map[string]Server
	ask     *askServer
	ending  bool
	// started is set once Start has succeeded: an exit before is Start's error,
	// not an Exited.
	started bool
	exited  chan struct{}
	stderr  *tailBuffer
	st      *state
	// model is the model the chat was last set to (spec.Model, then
	// SetModel), as the person picked it rather than as the CLI resolves
	// it, and notes is whose instructions the agent has: the next message
	// after a switch carries the new model's (agent.ModelNotes).
	model string
	notes *agent.ModelNotes
}

// New is an adapter with options.
func New(opts Options) *Adapter {
	life, end := context.WithCancel(context.Background())
	return &Adapter{opts: opts, waiting: map[string]chan controlResult{}, handling: map[string]context.CancelFunc{},
		mcpCalls: map[string]string{}, life: life, endLife: end}
}

// Start launches the CLI, says hello over the control protocol and returns
// once it answered: the CLI writes nothing on its own before the first turn,
// so the initialize answer is the readiness signal.
func (a *Adapter) Start(ctx context.Context, spec agent.StartSpec, out agent.Emitter) error {
	env := cliEnv(spec.Env)
	configDir := configDirOf(env)
	st := newState(spec, configDir)
	args := cliArgs(spec, a.opts, hasValue(spec.Env, UltracodeEnv, "1"))
	mcp, err := writeMCPConfig(spec)
	if err != nil {
		return err
	}
	if mcp != "" {
		args = append(args, "--mcp-config", mcp)
	}
	if spec.ResumeID != "" {
		path := findTranscript(configDir, spec.ResumeID)
		if path == "" {
			return fmt.Errorf("%w: no Claude Code conversation %s under %s", agent.ErrConversationNotFound, spec.ResumeID, configDir)
		}
		st.conv = spec.ResumeID
		if spec.Rebuild {
			// The journal lost this conversation: its history goes back in
			// from the transcript before anything new happens.
			if events, err := RebuildTranscript(path, spec.ResumeID); err != nil {
				out.Emit(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeHistoryUnavailable,
					Text: "The earlier history of this chat could not be read back from Claude Code: " + err.Error()})
			} else {
				out.Emit(append(events, agent.Event{Type: agent.TypeNotice, Code: agent.NoticeRebuilt,
					Text: "The chat history was rebuilt from the agent's own transcript."})...)
			}
		}
		out.Emit(agent.Event{Type: agent.TypeConversation, ConversationID: spec.ResumeID})
		st.delivered, st.lastCost = transcriptState(path)
		st.todos = loadTasks(configDir, spec.ResumeID)
		args = append(args, "--resume", spec.ResumeID)
	} else {
		args = append(args, "--session-id", uuid.NewString())
	}
	bin, err := a.binary(spec, env)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = spec.Workdir, env
	// Reaping is not tied to the pipes: a child of the CLI that inherited
	// them must not keep the CLI's end from being seen.
	cmd.WaitDelay = drainWait
	stdinR, stdin, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdin = stdinR
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		_, _ = stdinR.Close(), stdin.Close()
		return err
	}
	cmd.Stdout = stdoutW
	stderr := &tailBuffer{max: 8 << 10}
	cmd.Stderr = stderr
	spawn := spec.Spawn
	if spawn == nil {
		spawn = func(c *exec.Cmd) error {
			c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			return c.Start()
		}
	}
	a.mu.Lock()
	a.out, a.spec, a.cmd, a.stderr, a.st = out, spec, cmd, stderr, st
	a.model, a.notes = strings.TrimSpace(spec.Model), agent.NewModelNotes(spec)
	a.exited = make(chan struct{})
	a.stdin = stdin
	a.mu.Unlock()
	ask := newAskServer(a)
	servers := map[string]Server{ask.Name(): ask}
	if a.opts.Servers != nil {
		for _, s := range a.opts.Servers(spec, out) {
			servers[s.Name()] = s
		}
	}
	a.mu.Lock()
	a.servers, a.ask = servers, ask
	st.a = a
	a.mu.Unlock()
	err = spawn(cmd)
	_, _ = stdinR.Close(), stdoutW.Close()
	if err != nil {
		_, _ = stdout.Close(), stdin.Close()
		close(a.exited)
		return fmt.Errorf("start claude: %w", err)
	}
	read := make(chan struct{})
	go a.read(stdout, read)
	go a.reap(cmd, stdout, read)
	init := map[string]any{"subtype": "initialize"}
	if len(servers) > 0 {
		names := make([]string, 0, len(servers))
		configs := map[string]json.RawMessage{}
		for name, s := range servers {
			names = append(names, name)
			if c := s.Config(); len(c) > 0 {
				configs[name] = c
			}
		}
		init["sdkMcpServers"] = names
		if len(configs) > 0 {
			init["sdkMcpServerConfigs"] = configs
		}
	}
	raw, err := a.request(ctx, init)
	if err != nil {
		a.mu.Lock()
		a.ending = true
		a.mu.Unlock()
		a.kill()
		tail := stderr.suffix()
		if strings.Contains(tail, "No conversation found") {
			return fmt.Errorf("%w%s", agent.ErrConversationNotFound, tail)
		}
		return fmt.Errorf("claude did not start: %w%s", err, tail)
	}
	a.mu.Lock()
	st.readInfo(raw)
	a.started = true
	a.mu.Unlock()
	// Thinking arrives without its text unless the display asks for a
	// summary; a refusal leaves the session as it was.
	_, _ = a.request(ctx, map[string]any{"subtype": "set_max_thinking_tokens", "thinking_display": "summarized"})
	if spec.First != nil {
		return a.Send(ctx, *spec.First)
	}
	return nil
}

// cliArgs is the fixed launch policy (REBUILD.md §3.1, HARNESS-POLICY.md):
// stream-json both ways, every tool allowed, the CLI's own question tool off
// and the system prompt pointing at ask_user instead, the user's settings and
// connectors left out, the replay echo on as the delivery receipt.
func cliArgs(spec agent.StartSpec, opts Options, ultracode bool) []string {
	settings, _ := json.Marshal(map[string]any{
		"skipDangerousModePermissionPrompt": true,
		"disableRemoteControl":              true,
		"cleanupPeriodDays":                 transcriptDays,
		"ultracode":                         ultracode,
	})
	args := []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--replay-user-messages", "--forward-subagent-text",
		"--permission-prompt-tool", "stdio",
		"--setting-sources", "project,local", "--strict-mcp-config",
		"--dangerously-skip-permissions", "--disallowedTools", "AskUserQuestion",
		"--settings", string(settings),
	}
	prompt := askGuidance
	if p := strings.TrimSpace(opts.AppendSystemPrompt); p != "" {
		prompt += "\n\n" + p
	}
	if p := agent.ModelPrompt(strings.TrimSpace(spec.Model), spec.InstructionsFor(spec.Model)); p != "" {
		prompt += "\n\n" + p
	}
	args = append(args, "--append-system-prompt", prompt)
	if m := strings.TrimSpace(spec.Model); m != "" {
		args = append(args, "--model", m)
	}
	if e := effortLevel(spec.Effort); e != "" {
		args = append(args, "--effort", e)
	}
	return args
}

// effortLevel passes a level --effort takes and drops any other.
func effortLevel(e string) string {
	switch e = strings.TrimSpace(e); e {
	case "low", "medium", "high", "xhigh", "max":
		return e
	}
	return ""
}

// cliEnv builds the CLI's environment from the account's: every CLAUDE* and
// ANTHROPIC* variable goes except CLAUDE_CONFIG_DIR (the account's directory),
// an API-key account's key comes in as ANTHROPIC_API_KEY, and the fixed policy
// is added. IS_SANDBOX=1 is what lets --dangerously-skip-permissions run as
// root at all; CLAUDE_CODE_ENABLE_TODO_TOOLS=1 gives the model its task list.
func cliEnv(base []string) []string {
	var out []string
	key := ""
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == APIKeyEnv:
			key = v
		case k == UltracodeEnv:
		case k == "CLAUDE_CONFIG_DIR":
			out = append(out, kv)
		case strings.HasPrefix(k, "CLAUDE"), strings.HasPrefix(k, "ANTHROPIC"), k == "IS_SANDBOX":
		default:
			out = append(out, kv)
		}
	}
	if key != "" {
		out = append(out, "ANTHROPIC_API_KEY="+key)
	}
	if os.Geteuid() == 0 {
		out = append(out, "IS_SANDBOX=1")
	}
	return append(out, "CLAUDE_CODE_ENABLE_TODO_TOOLS=1")
}

func hasValue(env []string, key, value string) bool {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v == value
		}
	}
	return false
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// configDirOf is the config directory the CLI will use with env.
func configDirOf(env []string) string {
	if dir := envValue(env, "CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home := envValue(env, "HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".claude")
}

// binary is the program to run: the adapter's own option (tests), the
// dashboard's binary setting, or claude on the PATH of the CLI's
// environment. A setting without a slash is a name looked up on that PATH.
func (a *Adapter) binary(spec agent.StartSpec, env []string) (string, error) {
	name := strings.TrimSpace(a.opts.Binary)
	if name == "" {
		name = strings.TrimSpace(spec.Binary)
	}
	if name == "" {
		name = "claude"
	}
	if strings.ContainsRune(name, '/') {
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("the configured Claude Code binary: %w", err)
		}
		return name, nil
	}
	return lookPath(name, env)
}

// lookPath finds name on the PATH of env.
func lookPath(name string, env []string) (string, error) {
	for _, dir := range filepath.SplitList(envValue(env, "PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return exec.LookPath(name)
}

// ------------------------------------------------------------- the pipes

// write sends one line to the CLI.
func (a *Adapter) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if a.stdin == nil {
		return errors.New("claude is not running")
	}
	_ = a.stdin.SetWriteDeadline(time.Now().Add(writeWait))
	_, err = a.stdin.Write(append(raw, '\n'))
	if errors.Is(err, os.ErrDeadlineExceeded) {
		// Stuck, and its input now ends in half a line: the CLI is ended,
		// and the next one resumes the conversation.
		go a.kill()
		return errors.New("claude stopped reading its input, so it was ended")
	}
	return err
}

// request sends a control request and waits for its answer.
func (a *Adapter) request(ctx context.Context, body map[string]any) (json.RawMessage, error) {
	id := "req_" + randomID()
	ch := make(chan controlResult, 1)
	a.ctlMu.Lock()
	a.waiting[id] = ch
	a.ctlMu.Unlock()
	defer func() {
		a.ctlMu.Lock()
		delete(a.waiting, id)
		a.ctlMu.Unlock()
	}()
	if err := a.write(map[string]any{"type": "control_request", "request_id": id, "request": body}); err != nil {
		return nil, err
	}
	a.mu.Lock()
	exited := a.exited
	a.mu.Unlock()
	select {
	case r := <-ch:
		if r.Subtype == "error" {
			return nil, fmt.Errorf("claude refused %v: %s", body["subtype"], r.Error)
		}
		return r.Response, nil
	case <-exited:
		return nil, errors.New("claude exited")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// respond answers a control request of the CLI's.
func (a *Adapter) respond(id string, response any) {
	_ = a.write(map[string]any{"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": id, "response": response}})
}

func (a *Adapter) refuse(id, msg string) {
	_ = a.write(map[string]any{"type": "control_response",
		"response": map[string]any{"subtype": "error", "request_id": id, "error": msg}})
}

// read is the one reader of the CLI's stdout. It closes done once the pipe
// has ended, or was closed under it (reap).
func (a *Adapter) read(stdout io.Reader, done chan<- struct{}) {
	defer close(done)
	r := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if f, derr := decodeFrame(line); derr == nil {
				a.frame(f)
			}
		}
		if err != nil {
			return
		}
	}
}

// reap waits for the CLI to exit and for its last frames to be read, then
// closes what it left open and reports the exit. The pipe normally ends with
// the CLI; when something the CLI started still holds it, it is closed after
// drainWait so that the end is seen all the same.
func (a *Adapter) reap(cmd *exec.Cmd, stdout *os.File, read <-chan struct{}) {
	werr := cmd.Wait()
	select {
	case <-read:
	case <-time.After(drainWait):
		_ = stdout.Close()
		<-read
	}
	_ = stdout.Close()
	a.mu.Lock()
	ending := a.ending || !a.started
	if a.started {
		// Whatever the reason the CLI went (parked, stopped, switched
		// away from, died), its background work went with it.
		a.st.killTails()
		a.st.closeAll()
	}
	close(a.exited)
	out := a.out
	tail := a.stderr.suffix()
	a.mu.Unlock()
	// Nothing a handler would answer can reach the CLI any more: the calls it
	// waits in are gone, and an answer to one of them goes as a message.
	a.endLife()
	if !ending {
		if werr == nil {
			werr = errors.New("claude exited")
		}
		out.Exited(fmt.Errorf("%v%s", werr, tail))
	}
}

// frame routes one frame: the control protocol here, the rest to the state.
func (a *Adapter) frame(f *frame) {
	switch f.Type {
	case "control_response":
		if f.Response == nil {
			return
		}
		a.ctlMu.Lock()
		ch := a.waiting[f.Response.RequestID]
		a.ctlMu.Unlock()
		if ch != nil {
			// A second answer to one request is dropped: the reader never
			// waits on a requester.
			select {
			case ch <- *f.Response:
			default:
			}
		}
		return
	case "control_request":
		a.controlRequest(f.RequestID, f.Request)
		return
	case "control_cancel_request":
		a.mu.Lock()
		a.st.cancelPlan(f.RequestID)
		a.mu.Unlock()
		a.ctlMu.Lock()
		cancel := a.handling[f.RequestID]
		a.ctlMu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.frame(f)
}

// controlRequest answers what the CLI asks the host. Every tool is allowed
// (REBUILD.md §1: permissions always allow), except ExitPlanMode, which is a
// question for the person: it becomes a plan card and waits for the answer.
func (a *Adapter) controlRequest(id string, raw json.RawMessage) {
	var req controlRequest
	if json.Unmarshal(raw, &req) != nil {
		a.refuse(id, "unreadable request")
		return
	}
	switch req.Subtype {
	case "can_use_tool":
		if req.ToolName == "ExitPlanMode" {
			a.mu.Lock()
			a.st.askPlan(id, req)
			a.mu.Unlock()
			return
		}
		input := req.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		a.respond(id, map[string]any{"behavior": "allow", "updatedInput": input})
	case "mcp_message":
		a.mu.Lock()
		s := a.servers[req.ServerName]
		a.mu.Unlock()
		if s == nil {
			a.refuse(id, "no such server: "+req.ServerName)
			return
		}
		if isNotification(req.Message) {
			// In order with the stream: a call the CLI cancels
			// (notifications/cancelled) is gone before the turn's end that
			// follows is read, so an answer after that end goes as a message.
			// Its handler's context ends here, not in the server, which may
			// not have seen the call yet.
			if rpcID, ok := askuser.Cancelled(req.Message); ok {
				a.ctlMu.Lock()
				cancel := a.handling[a.mcpCalls[req.ServerName+" "+rpcID]]
				a.ctlMu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
			resp, _ := s.Handle(a.life, req.Message)
			if resp == nil {
				resp = json.RawMessage(`{"jsonrpc":"2.0","result":{}}`)
			}
			a.respond(id, map[string]any{"mcp_response": resp})
			return
		}
		call := ""
		if rpcID, ok := askuser.RequestID(req.Message); ok {
			call = req.ServerName + " " + rpcID
		}
		a.handle(id, call, func(ctx context.Context) {
			resp, err := s.Handle(ctx, req.Message)
			switch {
			case ctx.Err() != nil, err == nil && resp == nil:
				// Withdrawn (control_cancel_request, notifications/cancelled),
				// or the CLI is gone: it waits for nothing.
			case err != nil:
				a.refuse(id, err.Error())
			default:
				a.respond(id, map[string]any{"mcp_response": resp})
			}
		})
	case "hook_callback":
		a.respond(id, map[string]any{"continue": true})
	case "elicitation":
		a.mu.Lock()
		ask := a.ask
		a.mu.Unlock()
		a.handle(id, "", func(ctx context.Context) {
			if resp := ask.elicit(ctx, id, req); resp != nil && ctx.Err() == nil {
				a.respond(id, resp)
			}
		})
	default:
		a.refuse(id, "not supported by Socrates: "+req.Subtype)
	}
}

// isNotification is a JSON-RPC message without an id: nothing waits on it.
func isNotification(msg json.RawMessage) bool {
	var head struct {
		ID json.RawMessage `json:"id"`
	}
	return json.Unmarshal(msg, &head) == nil && (len(head.ID) == 0 || string(head.ID) == "null")
}

// handle answers one of the CLI's requests on a goroutine of its own, so that
// a call waiting for the person never holds up the stream. The context ends
// when the CLI withdraws the request (control_cancel_request, or for an MCP
// call, which call names, notifications/cancelled) or exits. It is
// registered before the next frame is read, so a cancel never misses it.
func (a *Adapter) handle(id, call string, fn func(ctx context.Context)) {
	ctx, cancel := context.WithCancel(a.life)
	a.ctlMu.Lock()
	a.handling[id] = cancel
	if call != "" {
		a.mcpCalls[call] = id
	}
	a.ctlMu.Unlock()
	go func() {
		defer func() {
			cancel()
			a.ctlMu.Lock()
			delete(a.handling, id)
			if call != "" && a.mcpCalls[call] == id {
				delete(a.mcpCalls, call)
			}
			a.ctlMu.Unlock()
		}()
		fn(ctx)
	}()
}

// ------------------------------------------------------------ the commands

// Send writes a user message. The uuid is derived from the message id, so the
// replay echo names it, and so does the transcript: a message the previous
// host had already handed over is recognised there and not sent twice.
func (a *Adapter) Send(_ context.Context, msg agent.Outgoing) error {
	id := messageUUID(msg.MessageID)
	content := userContent(msg)
	a.mu.Lock()
	if a.ending {
		a.mu.Unlock()
		return errors.New("claude is stopping")
	}
	if a.st.delivered[id] {
		a.st.markDelivered(msg.MessageID)
		a.mu.Unlock()
		return nil
	}
	if note := a.notes.Switch(a.model); note != "" {
		content[0]["text"] = agent.WithNote(note, content[0]["text"].(string))
	}
	a.st.pending[id] = msg.MessageID
	if a.st.turn != nil {
		a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: msg.MessageID, State: agent.MessageQueued})
	}
	a.mu.Unlock()
	return a.write(map[string]any{"type": "user", "uuid": id,
		"message": map[string]any{"role": "user", "content": content}})
}

// messageUUID is the CLI-side uuid of a Socrates message.
func messageUUID(messageID string) string {
	return uuid.NewSHA1(uuidSpace, []byte(messageID)).String()
}

var uuidSpace = uuid.MustParse("6f1b3c2e-7d0a-4f1e-9a43-5c2d7e9b8a10")

// maxImage is the largest attachment sent inline as an image.
const maxImage = 5 << 20

// userContent is a message as content blocks: the text, every image
// attachment inline, and every other attachment as its path.
func userContent(msg agent.Outgoing) []map[string]any {
	text := msg.Text
	var blocks []map[string]any
	for _, att := range msg.Attachments {
		if isImage(att.Mime) && att.Path != "" {
			if data, err := os.ReadFile(att.Path); err == nil && len(data) <= maxImage {
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": att.Mime, "data": encodeBase64(data)}})
				continue
			}
		}
		if att.Path != "" {
			text += "\n\nAttached file: " + att.Path
		}
	}
	return append([]map[string]any{{"type": "text", "text": text}}, blocks...)
}

func isImage(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// Interrupt ends the running turn gracefully: what was written stays. Once
// the CLI has acknowledged it, the turn's ask_user calls are withdrawn: an
// answer from then on goes as a message, even if the cancel of its call has
// not been read yet.
func (a *Adapter) Interrupt(ctx context.Context) error {
	a.mu.Lock()
	turnID := ""
	if a.st != nil && a.st.turn != nil {
		turnID = a.st.turn.id
	}
	a.mu.Unlock()
	if _, err := a.request(ctx, map[string]any{"subtype": "interrupt"}); err != nil {
		return err
	}
	a.mu.Lock()
	if a.ask != nil {
		a.ask.withdraw(turnID)
	}
	a.mu.Unlock()
	return nil
}

// Answer delivers an answer: to the plan card waiting on ExitPlanMode, or to
// the server that holds the ask.
func (a *Adapter) Answer(ctx context.Context, askID string, answer json.RawMessage) error {
	a.mu.Lock()
	reqID, input, ok := a.st.takePlan(askID)
	servers := make([]Server, 0, len(a.servers))
	for _, s := range a.servers {
		servers = append(servers, s)
	}
	a.mu.Unlock()
	if ok {
		approve, note := planDecision(answer)
		a.mu.Lock()
		a.st.planDecided(askID, approve, note)
		a.mu.Unlock()
		if approve {
			a.respond(reqID, map[string]any{"behavior": "allow", "updatedInput": input})
		} else {
			if note == "" {
				note = "The person wants to keep planning."
			}
			a.respond(reqID, map[string]any{"behavior": "deny", "message": note})
		}
		return nil
	}
	for _, s := range servers {
		if ans, ok := s.(Answerer); ok {
			if err := ans.Answer(ctx, askID, answer); !errors.Is(err, agent.ErrAskGone) {
				return err
			}
		}
	}
	return agent.ErrAskGone
}

// planDecision reads a plan card's answer: {"decision":"approve"} or
// {"decision":"keep_planning","note":"…"}; {"approve":true} and the bare
// strings "approve" and "keep_planning" are accepted too.
func planDecision(answer json.RawMessage) (bool, string) {
	var s string
	if json.Unmarshal(answer, &s) == nil {
		return s == "approve", ""
	}
	var body struct {
		Decision string `json:"decision"`
		Approve  *bool  `json:"approve"`
		Note     string `json:"note"`
	}
	_ = json.Unmarshal(answer, &body)
	if body.Approve != nil {
		return *body.Approve, strings.TrimSpace(body.Note)
	}
	return body.Decision == "approve", strings.TrimSpace(body.Note)
}

// SetModel switches model and effort for the turns that follow.
func (a *Adapter) SetModel(ctx context.Context, model, effort string) error {
	if m := strings.TrimSpace(model); m != "" {
		if _, err := a.request(ctx, map[string]any{"subtype": "set_model", "model": m}); err != nil {
			return err
		}
	}
	if e := effortLevel(effort); e != "" {
		if _, err := a.request(ctx, map[string]any{"subtype": "apply_flag_settings",
			"settings": map[string]any{"effortLevel": e}}); err != nil {
			return err
		}
	}
	a.mu.Lock()
	if m := strings.TrimSpace(model); m != "" {
		a.model = m
	}
	a.st.modelChanged(model, effortLevel(effort))
	a.mu.Unlock()
	return nil
}

// Command runs one of the agent's commands. "plan" switches plan mode on (or
// off with the argument "off") through the control protocol; every other
// name is a slash command and goes to the CLI as user text, which is how the
// CLI takes them headless (/compact, /model, custom commands). The
// terminal-only commands the CLI lists are refused.
func (a *Adapter) Command(ctx context.Context, name, args string) error {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if name == "" {
		return agent.ErrUnknownCommand
	}
	if name == "plan" {
		mode := "plan"
		if strings.TrimSpace(args) == "off" {
			mode = "bypassPermissions"
		}
		_, err := a.request(ctx, map[string]any{"subtype": "set_permission_mode", "mode": mode})
		return err
	}
	a.mu.Lock()
	refused := a.st.terminalOnly[name]
	a.mu.Unlock()
	if refused {
		return agent.ErrUnknownCommand
	}
	text := "/" + name
	if args = strings.TrimSpace(args); args != "" {
		text += " " + args
	}
	return a.write(map[string]any{"type": "user", "uuid": uuid.NewString(),
		"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": text}}}})
}

// Park ends the CLI cleanly: stdin closes, the CLI finishes and leaves, and
// the conversation is on disk for the next start. A CLI that does not leave
// in time is stopped with SIGTERM, then SIGKILL.
func (a *Adapter) Park(ctx context.Context) error { return a.end(ctx, stopWait) }

// releaseWait is how long Release gives a CLI with background work to leave
// on its own once its input closed, before SIGTERM.
const releaseWait = time.Second

// Release ends the CLI for another process to take the conversation over
// (an account switch). Without background work it is Park. With some, the
// CLI would wait for it to finish before it leaves; it is not waited for but
// stopped with SIGTERM, which leaves the transcript and a workflow's run
// journal for the agent on the new process to go on from.
func (a *Adapter) Release(ctx context.Context) error {
	if a.Background() == 0 {
		return a.Park(ctx)
	}
	return a.end(ctx, releaseWait)
}

// end closes the CLI's input and waits up to wait for it to leave, then
// stops it with SIGTERM and, after termWait, SIGKILL.
func (a *Adapter) end(ctx context.Context, wait time.Duration) error {
	a.mu.Lock()
	if a.ending {
		a.mu.Unlock()
		return nil
	}
	a.ending = true
	exited := a.exited
	a.mu.Unlock()
	a.writeMu.Lock()
	if a.stdin != nil {
		_ = a.stdin.Close()
	}
	a.writeMu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
	case <-timer.C:
	}
	a.signal(syscall.SIGTERM)
	select {
	case <-exited:
		return nil
	case <-time.After(termWait):
	}
	a.signal(syscall.SIGKILL)
	<-exited
	return errors.New("claude did not leave after its input closed")
}

// Stop ends the CLI now, and returns once it is reaped (or after termWait):
// what it left open is closed by then, before the host writes the end.
func (a *Adapter) Stop() error {
	a.mu.Lock()
	a.ending = true
	exited := a.exited
	a.mu.Unlock()
	a.kill()
	if exited != nil {
		select {
		case <-exited:
		case <-time.After(termWait):
		}
	}
	return nil
}

func (a *Adapter) kill() { a.signal(syscall.SIGKILL) }

func (a *Adapter) signal(sig syscall.Signal) {
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
		_ = cmd.Process.Signal(sig)
	}
}

// QuietLimit is agent.QuietLimiter's.
func (a *Adapter) QuietLimit() time.Duration { return quietLimit }

// StopTask stops one background task - a shell, a subagent, a workflow -
// by its task id (agent.TaskStopper). The CLI ends it and reports the end
// with the task's notification, which closes its part.
func (a *Adapter) StopTask(ctx context.Context, taskID string) error {
	_, err := a.request(ctx, map[string]any{"subtype": "stop_task", "task_id": taskID})
	return err
}

// Background is how many background tasks (subagents, workflows, shells) the
// CLI reported running.
func (a *Adapter) Background() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.st == nil {
		return 0
	}
	return a.st.background
}

// PID is the CLI's process id.
func (a *Adapter) PID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return 0
	}
	return a.cmd.Process.Pid
}

// tailBuffer keeps the end of the CLI's stderr, for the reason it gives when
// it dies.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

// suffix is ": <last stderr line>", or nothing.
func (t *tailBuffer) suffix() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return ": " + last
	}
	return ""
}

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
