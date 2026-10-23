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
// closed, and termWait how long it then gets after SIGTERM.
const (
	stopWait = 20 * time.Second
	termWait = 3 * time.Second
)

func init() {
	agent.Register(Harness, func() agent.Adapter { return New(Options{}) })
}

// Server is an in-process MCP server the CLI reaches over the control
// protocol (sdkMcpServers in initialize, then mcp_message requests). Handle
// gets one JSON-RPC message and returns the JSON-RPC response, nil for a
// notification; it may block for as long as the call takes (an ask_user call
// blocks until the person answers). This is the hook WP6 registers ask_user
// through.
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
	// AppendSystemPrompt is appended to Claude Code's system prompt.
	AppendSystemPrompt string
	// Servers are the in-process MCP servers of a chat, made once per start.
	Servers func(spec agent.StartSpec, out agent.Emitter) []Server
}

// Adapter drives one claude process.
type Adapter struct {
	opts Options

	// writeMu orders lines on the CLI's stdin.
	writeMu sync.Mutex
	stdin   io.WriteCloser

	ctlMu   sync.Mutex
	waiting map[string]chan controlResult

	mu      sync.Mutex
	out     agent.Emitter
	spec    agent.StartSpec
	cmd     *exec.Cmd
	servers map[string]Server
	ending  bool
	exited  chan struct{}
	stderr  *tailBuffer
	st      *state
}

// New is an adapter with options.
func New(opts Options) *Adapter {
	return &Adapter{opts: opts, waiting: map[string]chan controlResult{}}
}

// Start launches the CLI, says hello over the control protocol and returns
// once it answered: the CLI writes nothing on its own before the first turn,
// so the initialize answer is the readiness signal.
func (a *Adapter) Start(ctx context.Context, spec agent.StartSpec, out agent.Emitter) error {
	env := cliEnv(spec.Env)
	configDir := configDirOf(env)
	st := newState(spec, configDir)
	args := cliArgs(spec, a.opts, hasValue(spec.Env, UltracodeEnv, "1"))
	if spec.ResumeID != "" {
		path := findTranscript(configDir, spec.ResumeID)
		if path == "" {
			return fmt.Errorf("%w: no Claude Code conversation %s under %s", agent.ErrConversationNotFound, spec.ResumeID, configDir)
		}
		st.conv = spec.ResumeID
		out.Emit(agent.Event{Type: agent.TypeConversation, ConversationID: spec.ResumeID})
		st.delivered = transcriptUserUUIDs(path)
		st.todos = loadTasks(configDir, spec.ResumeID)
		args = append(args, "--resume", spec.ResumeID)
	} else {
		args = append(args, "--session-id", uuid.NewString())
	}
	bin := a.opts.Binary
	if bin == "" {
		var err error
		if bin, err = lookPath("claude", env); err != nil {
			return err
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = spec.Workdir, env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
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
	a.exited = make(chan struct{})
	a.stdin = stdin
	a.mu.Unlock()
	servers := map[string]Server{}
	if a.opts.Servers != nil {
		for _, s := range a.opts.Servers(spec, out) {
			servers[s.Name()] = s
		}
	}
	a.mu.Lock()
	a.servers = servers
	st.a = a
	a.mu.Unlock()
	if err := spawn(cmd); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}
	go a.read(stdout)
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
		a.kill()
		return fmt.Errorf("claude did not start: %w%s", err, stderr.suffix())
	}
	a.mu.Lock()
	st.readInfo(raw)
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
// stream-json both ways, every tool allowed, the CLI's own question tool off,
// the user's settings and connectors left out, the replay echo on as the
// delivery receipt.
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
	if p := strings.TrimSpace(opts.AppendSystemPrompt); p != "" {
		args = append(args, "--append-system-prompt", p)
	}
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
	_, err = a.stdin.Write(append(raw, '\n'))
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

// read is the one reader of the CLI's stdout, and then its reaper.
func (a *Adapter) read(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if f, derr := decodeFrame(line); derr == nil {
				a.frame(f)
			}
		}
		if err != nil {
			break
		}
	}
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	werr := cmd.Wait()
	a.mu.Lock()
	ending := a.ending
	a.st.stopTails()
	if !ending {
		a.st.closeLive()
	}
	close(a.exited)
	out := a.out
	tail := a.stderr.suffix()
	a.mu.Unlock()
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
			ch <- *f.Response
		}
		return
	case "control_request":
		a.controlRequest(f.RequestID, f.Request)
		return
	case "control_cancel_request":
		a.mu.Lock()
		a.st.cancelPlan(f.RequestID)
		a.mu.Unlock()
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
		go func() {
			resp, err := s.Handle(context.Background(), req.Message)
			if err != nil {
				a.refuse(id, err.Error())
				return
			}
			if resp == nil {
				resp = json.RawMessage(`{"jsonrpc":"2.0","result":{}}`)
			}
			a.respond(id, map[string]any{"mcp_response": resp})
		}()
	case "hook_callback":
		a.respond(id, map[string]any{"continue": true})
	case "elicitation":
		a.respond(id, map[string]any{"action": "cancel"})
	default:
		a.refuse(id, "not supported by Socrates: "+req.Subtype)
	}
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

// Interrupt ends the running turn gracefully: what was written stays.
func (a *Adapter) Interrupt(ctx context.Context) error {
	_, err := a.request(ctx, map[string]any{"subtype": "interrupt"})
	return err
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
func (a *Adapter) Park(ctx context.Context) error {
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
	wait := time.NewTimer(stopWait)
	defer wait.Stop()
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
	case <-wait.C:
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

// Stop ends the CLI now.
func (a *Adapter) Stop() error {
	a.mu.Lock()
	a.ending = true
	a.mu.Unlock()
	a.kill()
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
