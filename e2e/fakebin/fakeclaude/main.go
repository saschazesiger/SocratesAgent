// fakeclaude is a stand-in for `claude -p --input-format stream-json
// --output-format stream-json`: it speaks the stream-json frames and the
// control protocol of Claude Code 2.1.309 closely enough for the Claude
// adapter's tests and the browser suite, and it writes a transcript in the
// real format under $CLAUDE_CONFIG_DIR/projects, so that resume and history
// rebuild can be tested against it.
//
// What it keeps from the real program (docs/design/research/03-claude-code.md
// and 09-claude-subagents-workflows.md):
//
//   - nothing is written before the first turn; initialize is answered at once;
//   - every turn starts with command_lifecycle started and system/init, and
//     ends with a result whose total_cost_usd counts for the whole process;
//   - user messages are echoed with isReplay (--replay-user-messages);
//   - a message sent during a turn is queued and runs after it;
//   - --resume of an unknown id fails at once with "No conversation found";
//     a resume after a turn that was cut off appends the synthetic lines the
//     real one does ("No response requested.", "[Tool call interrupted…");
//   - interrupt keeps the partial text and ends the turn aborted_streaming.
//
// The script, by message text:
//
//	/stream N [ms]   N text deltas, ms apart (default 20, 10 ms)
//	/slow S          streams for S seconds (interruptible)
//	/tool            a Bash call with output
//	/edit            an Edit of fake.txt in the working directory, with a patch
//	/write           a Write that creates new.txt
//	/todo            TaskCreate twice, then TaskUpdate the first to completed
//	/subagent        a background Agent call with a Bash call inside it
//	/subagent bg S   one whose Bash call still runs when the turn ends (S s)
//	/subagent fail   a foreground one that fails before its call returns
//	/workflow [S]    a Workflow run of two phases and three agents (S seconds),
//	                 which reports back with a turn of the CLI's own
//	/bg S            a background Bash task that reports back after S seconds
//	/plan            ExitPlanMode through can_use_tool; waits for the answer
//	/limit           a rate limit that stops the turn
//	/retry           an API retry, then text
//	/image           a Read that returns an image
//	/crash           exits in the middle of the turn
//	/env             says the CLAUDE_*, ANTHROPIC_* and IS_SANDBOX variables
//	/argv            says its command line
//	/compact         compacts (as the slash command does)
//	/model X         a local command answer, "Set model to X"
//	/mcp             says the in-process MCP servers initialize declared,
//	                 their configs and the tools their tools/list gave
//	/askuser [JSON]  calls the socrates server's ask_user (a call of two
//	                 questions, or the JSON given) through mcp_message, waits
//	                 for the result and says it; an interrupt withdraws the
//	                 call (notifications/cancelled, then control_cancel_request)
//	/askdrop         calls ask_user and withdraws the call at once with
//	                 notifications/cancelled alone, as an abort right after
//	                 the call does, then says "ask_user was withdrawn"
//	/elicit [url]    an MCP elicitation of a form with two fields (or of a
//	                 URL); says the answer
//	anything else    "you said: <text>"
//
// After initialize it does the MCP handshake (initialize,
// notifications/initialized, tools/list) with every server in sdkMcpServers,
// as the real CLI does, and lists their tools as mcp__<server>__<tool>.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const version = "2.1.309"

func main() {
	f := newFake(os.Args[1:])
	os.Exit(f.run())
}

type fake struct {
	args      []string
	session   string
	model     string
	cwd       string
	configDir string

	outMu sync.Mutex

	mu         sync.Mutex
	queue      []job
	busy       bool
	cancel     context.CancelFunc
	wake       chan struct{}
	waiting    map[string]chan json.RawMessage
	mode       string
	totalCost  float64
	bg         map[string]string
	lastUUID   string
	results    int
	interrupts int
	// The in-process MCP servers: their configs as initialize gave them,
	// the tools they listed, the handshake's end (nil before initialize),
	// and the next JSON-RPC id.
	mcpConfigs map[string]json.RawMessage
	mcpServers []string
	mcpTools   []string
	mcpReady   chan struct{}
	rpcID      int
}

// job is one turn to run: a user message, or a background task reporting.
type job struct {
	uuid   string
	text   string
	origin string
}

func newFake(args []string) *fake {
	cwd, _ := os.Getwd()
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	conf := os.Getenv("CLAUDE_CONFIG_DIR")
	if conf == "" {
		home, _ := os.UserHomeDir()
		conf = filepath.Join(home, ".claude")
	}
	return &fake{args: args, cwd: cwd, configDir: conf, model: "claude-fake-1", mode: "bypassPermissions",
		wake: make(chan struct{}, 1), waiting: map[string]chan json.RawMessage{}, bg: map[string]string{}}
}

func (f *fake) flag(name string) string {
	for i, a := range f.args {
		if a == name && i+1 < len(f.args) {
			return f.args[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

func (f *fake) run() int {
	if m := f.flag("--model"); m != "" {
		f.model = resolveModel(m)
	}
	if id := f.flag("--resume"); id != "" {
		if _, err := os.Stat(f.transcriptPath(id)); err != nil {
			fmt.Fprintf(os.Stderr, "No conversation found with session ID: %s\n", id)
			f.out(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true,
				"num_turns": 0, "session_id": id, "total_cost_usd": 0, "errors": []string{"No conversation found with session ID: " + id}})
			return 1
		}
		f.session = id
		f.repairCutOffTurn()
	} else if id := f.flag("--session-id"); id != "" {
		f.session = id
	} else {
		f.session = newUUID()
	}
	go f.work()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		f.input(sc.Bytes())
	}
	// stdin closed: the queue is worked off, then the process leaves.
	f.mu.Lock()
	f.queue = append(f.queue, job{origin: "eof"})
	f.mu.Unlock()
	f.poke()
	select {}
}

func resolveModel(m string) string {
	switch m {
	case "haiku", "sonnet", "opus", "fable":
		return "claude-" + m + "-5-5"
	}
	return m
}

func (f *fake) out(v any) {
	raw, _ := json.Marshal(v)
	f.outMu.Lock()
	defer f.outMu.Unlock()
	os.Stdout.Write(append(raw, '\n'))
}

func (f *fake) poke() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// input is one stdin line.
func (f *fake) input(line []byte) {
	var msg struct {
		Type      string          `json:"type"`
		UUID      string          `json:"uuid"`
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
		Response  struct {
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
		} `json:"response"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &msg) != nil {
		return
	}
	switch msg.Type {
	case "control_request":
		f.control(msg.RequestID, msg.Request)
	case "control_response":
		f.mu.Lock()
		ch := f.waiting[msg.Response.RequestID]
		delete(f.waiting, msg.Response.RequestID)
		f.mu.Unlock()
		if ch != nil {
			ch <- msg.Response.Response
		}
	case "user":
		id := msg.UUID
		if id == "" {
			id = newUUID()
		}
		text := contentText(msg.Message.Content)
		f.out(map[string]any{"type": "command_lifecycle", "command_uuid": id, "state": "queued", "uuid": newUUID(), "session_id": f.session})
		f.mu.Lock()
		f.queue = append(f.queue, job{uuid: id, text: text})
		f.mu.Unlock()
		f.poke()
	}
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	var parts []string
	images := 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			images++
		}
	}
	text := strings.Join(parts, "\n")
	if images > 0 {
		text += fmt.Sprintf(" [%d image]", images)
	}
	return text
}

func (f *fake) control(id string, raw json.RawMessage) {
	var req struct {
		Subtype    string                     `json:"subtype"`
		Model      string                     `json:"model"`
		Mode       string                     `json:"mode"`
		Settings   json.RawMessage            `json:"settings"`
		MCPServers []string                   `json:"sdkMcpServers"`
		MCPConfigs map[string]json.RawMessage `json:"sdkMcpServerConfigs"`
	}
	_ = json.Unmarshal(raw, &req)
	ok := func(resp any) {
		f.out(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": id, "response": resp}})
	}
	switch req.Subtype {
	case "initialize":
		ok(map[string]any{
			"commands": []map[string]any{
				{"name": "compact", "description": "Clear conversation history but keep a summary", "argumentHint": "<optional instructions>"},
				{"name": "model", "description": "Set the AI model", "argumentHint": "[model]"},
				{"name": "shout", "description": "A project command (fake)", "argumentHint": ""},
			},
			"models": []map[string]any{
				{"value": "haiku", "displayName": "Haiku", "description": "Fastest", "supportedEffortLevels": []string{"low", "medium", "high"}},
				{"value": "sonnet", "displayName": "Sonnet", "description": "Balanced", "supportedEffortLevels": []string{"low", "medium", "high", "max"}},
			},
			"account":                 map[string]any{"email": "fake@example.com", "organization": "Fake Org", "subscriptionType": "Claude Fake", "apiProvider": "firstParty"},
			"claude_code_version":     version,
			"current_permission_mode": f.mode,
			"pid":                     os.Getpid(),
		})
		ready := make(chan struct{})
		f.mu.Lock()
		f.mcpServers, f.mcpConfigs, f.mcpReady = req.MCPServers, req.MCPConfigs, ready
		f.mu.Unlock()
		go f.mcpHandshake(ready)
	case "interrupt":
		f.mu.Lock()
		if f.cancel != nil {
			f.cancel()
			f.interrupts++
		}
		f.mu.Unlock()
		ok(map[string]any{"still_queued": []string{}})
	case "set_model":
		f.mu.Lock()
		f.model = resolveModel(req.Model)
		m := f.model
		f.mu.Unlock()
		ok(map[string]any{"model": m})
	case "set_permission_mode":
		f.mu.Lock()
		f.mode = req.Mode
		f.mu.Unlock()
		ok(map[string]any{"mode": req.Mode})
		f.out(map[string]any{"type": "system", "subtype": "status", "status": nil, "permissionMode": req.Mode, "uuid": newUUID(), "session_id": f.session})
	case "apply_flag_settings", "set_max_thinking_tokens":
		ok(map[string]any{})
	case "get_context_usage":
		ok(map[string]any{"totalTokens": 1234, "maxTokens": 200000})
	default:
		f.out(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": id, "error": "unsupported: " + req.Subtype}})
	}
}

// ask sends the host a control request and waits for its answer.
func (f *fake) ask(ctx context.Context, request map[string]any) (json.RawMessage, bool) {
	id := newUUID()
	ch := make(chan json.RawMessage, 1)
	f.mu.Lock()
	f.waiting[id] = ch
	f.mu.Unlock()
	f.out(map[string]any{"type": "control_request", "request_id": id, "request": request})
	select {
	case r := <-ch:
		return r, true
	case <-ctx.Done():
		f.mu.Lock()
		delete(f.waiting, id)
		f.mu.Unlock()
		f.out(map[string]any{"type": "control_cancel_request", "request_id": id})
		return nil, false
	}
}

// mcpRequest sends one JSON-RPC message to an in-process MCP server and
// waits for its response; a notification is answered too, with an empty
// result. When ctx ends first the call is withdrawn, as an aborted MCP client
// does: notifications/cancelled, then the control request is cancelled.
func (f *fake) mcpRequest(ctx context.Context, server, method string, params any, notify bool) (json.RawMessage, bool) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	var rpcID int
	if !notify {
		f.mu.Lock()
		rpcID = f.rpcID
		f.rpcID++
		f.mu.Unlock()
		msg["id"] = rpcID
	}
	id := newUUID()
	ch := make(chan json.RawMessage, 1)
	f.mu.Lock()
	f.waiting[id] = ch
	f.mu.Unlock()
	f.out(map[string]any{"type": "control_request", "request_id": id,
		"request": map[string]any{"subtype": "mcp_message", "server_name": server, "message": msg}})
	select {
	case r := <-ch:
		var resp struct {
			MCPResponse json.RawMessage `json:"mcp_response"`
		}
		_ = json.Unmarshal(r, &resp)
		return resp.MCPResponse, true
	case <-ctx.Done():
		f.mu.Lock()
		delete(f.waiting, id)
		f.mu.Unlock()
		f.mcpRequest(context.Background(), server, "notifications/cancelled",
			map[string]any{"requestId": rpcID, "reason": "AbortError: the operation was aborted"}, true)
		f.out(map[string]any{"type": "control_cancel_request", "request_id": id})
		return nil, false
	}
}

// mcpHandshake connects every in-process server and lists its tools; ready
// closes when it is done, and the first turn waits for that.
func (f *fake) mcpHandshake(ready chan struct{}) {
	defer close(ready)
	f.mu.Lock()
	servers := append([]string(nil), f.mcpServers...)
	f.mu.Unlock()
	ctx := context.Background()
	for _, server := range servers {
		_, _ = f.mcpRequest(ctx, server, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "claude-code", "version": version}}, false)
		_, _ = f.mcpRequest(ctx, server, "notifications/initialized", nil, true)
		raw, _ := f.mcpRequest(ctx, server, "tools/list", map[string]any{}, false)
		var list struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &list)
		f.mu.Lock()
		for _, tool := range list.Result.Tools {
			f.mcpTools = append(f.mcpTools, "mcp__"+server+"__"+tool.Name)
		}
		f.mu.Unlock()
	}
}

// work runs the queue, one turn at a time.
func (f *fake) work() {
	for range f.wake {
		for {
			f.mu.Lock()
			if len(f.queue) == 0 {
				f.busy = false
				f.mu.Unlock()
				break
			}
			j := f.queue[0]
			f.queue = f.queue[1:]
			if j.origin == "eof" {
				pending := len(f.bg)
				f.mu.Unlock()
				if pending == 0 {
					os.Exit(0)
				}
				time.Sleep(50 * time.Millisecond)
				f.mu.Lock()
				f.queue = append(f.queue, j)
				f.mu.Unlock()
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			f.busy, f.cancel = true, cancel
			f.mu.Unlock()
			f.turn(ctx, j)
			cancel()
			f.mu.Lock()
			f.cancel = nil
			f.mu.Unlock()
		}
	}
}

// turn is one turn as the real CLI writes it.
type turnRun struct {
	f       *fake
	ctx     context.Context
	job     job
	apiMsg  string
	index   int
	text    strings.Builder
	cut     bool
	limited bool
	isError bool
	errText string
	turns   int
}

func (f *fake) turn(ctx context.Context, j job) {
	t := &turnRun{f: f, ctx: ctx, job: j}
	if j.uuid != "" {
		f.out(map[string]any{"type": "command_lifecycle", "command_uuid": j.uuid, "state": "started", "uuid": newUUID(), "session_id": f.session})
	}
	f.mu.Lock()
	ready := f.mcpReady
	f.mu.Unlock()
	if ready != nil {
		<-ready
	}
	f.mu.Lock()
	model := f.model
	tools := append([]string{"Task", "Bash", "Edit", "Read", "Write", "TaskCreate", "TaskUpdate", "Workflow", "ExitPlanMode"}, f.mcpTools...)
	f.mu.Unlock()
	f.out(map[string]any{"type": "system", "subtype": "init", "cwd": f.cwd, "session_id": f.session, "model": model,
		"tools":          tools,
		"slash_commands": []string{"compact", "model", "shout", "doctor"}, "terminal_slash_commands": []string{"doctor"},
		"permissionMode": f.mode, "claude_code_version": version, "uuid": newUUID()})
	switch {
	case j.origin != "":
		f.writeLine("user", j.uuid, j.text, nil, false)
	case strings.HasPrefix(j.text, "/compact"):
		t.compact()
		return
	case strings.HasPrefix(j.text, "/model"):
		t.modelCommand(strings.TrimSpace(strings.TrimPrefix(j.text, "/model")))
		return
	default:
		f.out(map[string]any{"type": "user", "message": map[string]any{"role": "user",
			"content": []map[string]any{{"type": "text", "text": j.text}}}, "session_id": f.session,
			"parent_tool_use_id": nil, "uuid": j.uuid, "isReplay": true})
		f.writeLine("user", j.uuid, j.text, nil, false)
	}
	t.script()
	t.finish()
}

func (t *turnRun) script() {
	word, rest, _ := strings.Cut(strings.TrimSpace(t.job.text), " ")
	fields := strings.Fields(rest)
	if t.job.origin != "" {
		t.say("The background task reported back.")
		return
	}
	switch word {
	case "/stream":
		n, every := intArg(fields, 0, 20), time.Duration(intArg(fields, 1, 10))*time.Millisecond
		for i := 1; i <= n && !t.cut; i++ {
			t.stream(fmt.Sprintf("w%d ", i), every)
		}
	case "/slow":
		end := time.Now().Add(time.Duration(floatArg(fields, 0, 1) * float64(time.Second)))
		for i := 1; time.Now().Before(end) && !t.cut; i++ {
			t.stream(fmt.Sprintf("s%d ", i), 50*time.Millisecond)
		}
	case "/tool":
		t.tool("Bash", map[string]any{"command": "printf 'one\\ntwo\\n'", "description": "Print two lines"},
			"one\ntwo", map[string]any{"stdout": "one\ntwo", "stderr": "", "interrupted": false}, false, "")
		t.say("The command printed two lines.")
	case "/edit":
		t.edit()
	case "/write":
		path := filepath.Join(t.f.cwd, "new.txt")
		_ = os.WriteFile(path, []byte("hello\nworld\n"), 0o644)
		t.tool("Write", map[string]any{"file_path": path, "content": "hello\nworld\n"},
			"File created successfully at: "+path, map[string]any{"type": "create", "filePath": path,
				"content": "hello\nworld\n", "structuredPatch": []any{}, "originalFile": nil}, false, "")
		t.say("Wrote new.txt.")
	case "/todo":
		t.todo()
	case "/subagent":
		switch {
		case len(fields) > 0 && fields[0] == "bg":
			t.subagentOutliving(floatArg(fields, 1, 1))
		case len(fields) > 0 && fields[0] == "fail":
			t.subagentFailing()
		default:
			t.subagent()
		}
	case "/workflow":
		t.workflow(floatArg(fields, 0, 1))
	case "/bg":
		t.background(floatArg(fields, 0, 1))
	case "/plan":
		t.plan()
	case "/limit":
		t.limit()
	case "/retry":
		t.f.out(map[string]any{"type": "system", "subtype": "api_retry", "attempt": 1, "max_retries": 10,
			"retry_delay_ms": 500, "error_status": 529, "error": "overloaded", "uuid": newUUID(), "session_id": t.f.session})
		t.say("Answered after a retry.")
	case "/image":
		t.image()
	case "/crash":
		t.stream("about to crash ", 0)
		os.Exit(3)
	case "/env":
		var vars []string
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "ANTHROPIC") || k == "IS_SANDBOX" || strings.HasPrefix(k, "SOCRATES_CLAUDE") {
				vars = append(vars, kv)
			}
		}
		sort.Strings(vars)
		t.say(strings.Join(vars, "\n"))
	case "/argv":
		t.say(strings.Join(t.f.args, " "))
	case "/mcp":
		t.f.mu.Lock()
		configs, _ := json.Marshal(t.f.mcpConfigs)
		text := fmt.Sprintf("servers: %s\nconfigs: %s\ntools: %s", strings.Join(t.f.mcpServers, ","), configs, strings.Join(t.f.mcpTools, ","))
		t.f.mu.Unlock()
		t.say(text)
	case "/askuser":
		t.askUser(rest)
	case "/askdrop":
		t.askDrop()
	case "/elicit":
		t.elicit(strings.TrimSpace(rest) == "url")
	default:
		t.say("you said: " + t.job.text)
	}
}

// ------------------------------------------------------------ frames

func (t *turnRun) msgStart() {
	t.apiMsg, t.index = "msg_fake_"+randHex(8), 0
	t.f.out(t.se(map[string]any{"type": "message_start", "message": map[string]any{"id": t.apiMsg, "model": t.f.model}}))
}

func (t *turnRun) se(ev map[string]any) map[string]any {
	return map[string]any{"type": "stream_event", "event": ev, "session_id": t.f.session, "parent_tool_use_id": nil,
		"uuid": newUUID(), "api_message_id": t.apiMsg}
}

// assistant writes one completed block of the current API message, on the
// stream and in the transcript.
func (t *turnRun) assistant(b map[string]any, parent string) {
	id := newUUID()
	frame := map[string]any{"type": "assistant", "message": map[string]any{"model": t.f.model, "id": t.apiMsg,
		"type": "message", "role": "assistant", "content": []any{b}}, "parent_tool_use_id": nilIfEmpty(parent),
		"session_id": t.f.session, "uuid": id}
	t.f.out(frame)
	if parent == "" {
		t.f.writeRaw(map[string]any{"type": "assistant", "uuid": id, "message": frame["message"]})
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// stream writes text as deltas of one text block, which the next say or the
// turn's end completes.
func (t *turnRun) stream(text string, every time.Duration) {
	if t.cut {
		return
	}
	if every > 0 {
		select {
		case <-t.ctx.Done():
			t.cut = true
			return
		case <-time.After(every):
		}
	}
	if t.apiMsg == "" {
		t.msgStart()
	}
	if t.text.Len() == 0 {
		t.f.out(t.se(map[string]any{"type": "content_block_start", "index": t.index, "content_block": map[string]any{"type": "text", "text": ""}}))
	}
	t.text.WriteString(text)
	t.f.out(t.se(map[string]any{"type": "content_block_delta", "index": t.index, "delta": map[string]any{"type": "text_delta", "text": text}}))
}

// closeText completes the streamed text block.
func (t *turnRun) closeText() {
	if t.text.Len() == 0 {
		return
	}
	t.assistant(map[string]any{"type": "text", "text": t.text.String()}, "")
	t.f.out(t.se(map[string]any{"type": "content_block_stop", "index": t.index}))
	t.index++
	t.text.Reset()
}

func (t *turnRun) say(text string) {
	for _, chunk := range chunks(text, 8) {
		t.stream(chunk, 2*time.Millisecond)
	}
	t.closeText()
}

// tool is one tool call and its result, in the main thread or (with parent)
// a subagent's.
func (t *turnRun) tool(name string, input map[string]any, result string, structured any, isError bool, parent string) string {
	t.closeText()
	id := "toolu_fake_" + randHex(10)
	if parent == "" {
		if t.apiMsg == "" {
			t.msgStart()
		}
		t.f.out(t.se(map[string]any{"type": "content_block_start", "index": t.index,
			"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}}))
	}
	t.assistant(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}, parent)
	if parent == "" {
		t.f.out(t.se(map[string]any{"type": "content_block_stop", "index": t.index}))
		t.index++
	}
	t.result(id, result, structured, isError, parent)
	return id
}

func (t *turnRun) result(id, result string, structured any, isError bool, parent string) {
	uid := newUUID()
	block := map[string]any{"type": "tool_result", "tool_use_id": id, "content": result, "is_error": isError}
	frame := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{block}},
		"parent_tool_use_id": nilIfEmpty(parent), "session_id": t.f.session, "uuid": uid}
	if structured != nil {
		frame["tool_use_result"] = structured
	}
	t.f.out(frame)
	if parent == "" {
		t.f.writeRaw(map[string]any{"type": "user", "uuid": uid, "message": frame["message"], "toolUseResult": structured})
	}
}

func (t *turnRun) finish() {
	t.closeText()
	f := t.f
	f.mu.Lock()
	f.totalCost += 0.001
	cost := f.totalCost
	f.results++
	f.mu.Unlock()
	res := map[string]any{"type": "result", "subtype": "success", "is_error": false, "terminal_reason": "completed",
		"result": "ok", "num_turns": 1, "session_id": f.session, "total_cost_usd": cost, "uuid": newUUID(),
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 20, "cache_read_input_tokens": 100,
			"cache_creation_input_tokens": 5, "output_tokens_details": map[string]any{"thinking_tokens": 3},
			"iterations": []map[string]any{{"input_tokens": 2, "output_tokens": 20, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 5}}},
		"modelUsage": map[string]any{f.model: map[string]any{"contextWindow": 200000, "costUSD": cost}}}
	if t.job.origin != "" {
		res["origin"] = map[string]any{"kind": t.job.origin}
	} else if t.job.uuid != "" {
		res["user_message_uuids"] = []string{t.job.uuid}
	}
	switch {
	case t.cut:
		f.out(map[string]any{"type": "user", "message": map[string]any{"role": "user",
			"content": []map[string]any{{"type": "text", "text": "[Request interrupted by user]"}}}, "session_id": f.session, "uuid": newUUID()})
		f.writeLine("user", newUUID(), "[Request interrupted by user]", nil, false)
		res["subtype"], res["is_error"], res["terminal_reason"] = "error_during_execution", true, "aborted_streaming"
		res["errors"] = []string{"[ede_diagnostic] interrupted"}
	case t.limited:
		res["subtype"], res["is_error"], res["api_error_status"] = "error_during_execution", true, 429
		res["result"] = "Claude AI usage limit reached"
	}
	if os.Getenv("FAKE_NO_SINCE_LAST") == "" {
		res["model_usage_since_last_result"] = map[string]any{f.model: map[string]any{"costUSD": 0.001}}
	}
	f.out(res)
	f.writeRaw(map[string]any{"type": "cost-state", "totalCostUSD": cost})
	if t.job.uuid != "" {
		f.out(map[string]any{"type": "command_lifecycle", "command_uuid": t.job.uuid, "state": "completed", "uuid": newUUID(), "session_id": f.session})
	}
}

// ------------------------------------------------------------ scenarios

func (t *turnRun) edit() {
	path := filepath.Join(t.f.cwd, "fake.txt")
	original := "alpha\nbeta\ngamma\n"
	_ = os.WriteFile(path, []byte("alpha\nBETA\ngamma\n"), 0o644)
	t.tool("Edit", map[string]any{"file_path": path, "old_string": "beta", "new_string": "BETA", "replace_all": false},
		"The file "+path+" has been updated successfully.", map[string]any{"filePath": path, "oldString": "beta",
			"newString": "BETA", "originalFile": original, "structuredPatch": []map[string]any{{"oldStart": 1, "oldLines": 3,
				"newStart": 1, "newLines": 3, "lines": []string{" alpha", "-beta", "+BETA", " gamma"}}}}, false, "")
	t.say("Edited fake.txt.")
}

func (t *turnRun) todo() {
	dir := filepath.Join(t.f.configDir, "tasks", t.f.session)
	_ = os.MkdirAll(dir, 0o755)
	save := func(id, subject, status string) {
		raw, _ := json.MarshalIndent(map[string]any{"id": id, "subject": subject, "description": "", "status": status,
			"blocks": []any{}, "blockedBy": []any{}}, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o644)
	}
	t.tool("TaskCreate", map[string]any{"subject": "first step", "description": "do the first thing", "activeForm": "Doing the first step"},
		"Task #1 created successfully: first step", map[string]any{"task": map[string]any{"id": "1", "subject": "first step"}}, false, "")
	save("1", "first step", "pending")
	t.tool("TaskCreate", map[string]any{"subject": "second step", "description": "then the second"},
		"Task #2 created successfully: second step", map[string]any{"task": map[string]any{"id": "2", "subject": "second step"}}, false, "")
	save("2", "second step", "pending")
	t.tool("TaskUpdate", map[string]any{"taskId": "1", "status": "completed"}, "Updated task #1 status",
		map[string]any{"success": true, "taskId": "1", "updatedFields": []string{"status"},
			"statusChange": map[string]any{"from": "pending", "to": "completed"}}, false, "")
	save("1", "first step", "completed")
	t.say("Tracked two tasks.")
}

func (t *turnRun) system(v map[string]any) {
	v["type"], v["uuid"], v["session_id"] = "system", newUUID(), t.f.session
	t.f.out(v)
}

func (t *turnRun) tasksChanged() {
	t.f.mu.Lock()
	var tasks []map[string]any
	for id, kind := range t.f.bg {
		tasks = append(tasks, map[string]any{"task_id": id, "task_type": kind})
	}
	t.f.mu.Unlock()
	if tasks == nil {
		tasks = []map[string]any{}
	}
	t.system(map[string]any{"subtype": "background_tasks_changed", "tasks": tasks})
}

func (t *turnRun) setBG(id, kind string) {
	t.f.mu.Lock()
	if kind == "" {
		delete(t.f.bg, id)
	} else {
		t.f.bg[id] = kind
	}
	t.f.mu.Unlock()
	t.tasksChanged()
}

// subagent is a background Agent call: its own Bash call and answer arrive
// with parent_tool_use_id, framed by task_* messages, within the turn.
func (t *turnRun) subagent() {
	t.closeText()
	if t.apiMsg == "" {
		t.msgStart()
	}
	id := "toolu_fake_" + randHex(10)
	agentID := "a" + randHex(8)
	input := map[string]any{"description": "Echo a word", "subagent_type": "general-purpose", "prompt": "Run echo sub-ok"}
	t.f.out(t.se(map[string]any{"type": "content_block_start", "index": t.index,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": "Agent", "input": map[string]any{}}}))
	t.assistant(map[string]any{"type": "tool_use", "id": id, "name": "Agent", "input": input}, "")
	t.index++
	t.setBG(agentID, "local_agent")
	t.system(map[string]any{"subtype": "task_started", "task_id": agentID, "tool_use_id": id, "description": "Echo a word",
		"subagent_type": "general-purpose", "is_backgrounded": true, "task_type": "local_agent", "prompt": "Run echo sub-ok"})
	t.result(id, "Async agent launched successfully.\nagentId: "+agentID, map[string]any{"isAsync": true,
		"status": "async_launched", "agentId": agentID, "description": "Echo a word", "resolvedModel": t.f.model}, false, "")
	sub := filepath.Join(t.f.projectDir(), t.f.session, "subagents")
	_ = os.MkdirAll(sub, 0o755)
	inner := t.tool("Bash", map[string]any{"command": "echo sub-ok"}, "sub-ok", nil, false, id)
	t.system(map[string]any{"subtype": "task_progress", "task_id": agentID, "tool_use_id": id, "description": "Running echo sub-ok",
		"usage": map[string]any{"total_tokens": 900, "tool_uses": 1, "duration_ms": 300}, "last_tool_name": "Bash"})
	t.assistant(map[string]any{"type": "text", "text": "sub-ok"}, id)
	writeJSONL(filepath.Join(sub, "agent-"+agentID+".jsonl"),
		map[string]any{"type": "user", "isSidechain": true, "uuid": newUUID(), "message": map[string]any{"role": "user", "content": "Run echo sub-ok"}},
		map[string]any{"type": "assistant", "isSidechain": true, "uuid": newUUID(), "message": map[string]any{"role": "assistant", "model": t.f.model,
			"content": []any{map[string]any{"type": "tool_use", "id": inner, "name": "Bash", "input": map[string]any{"command": "echo sub-ok"}}}}},
		map[string]any{"type": "user", "isSidechain": true, "uuid": newUUID(), "message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": inner, "content": "sub-ok"}}}},
		map[string]any{"type": "assistant", "isSidechain": true, "uuid": newUUID(), "message": map[string]any{"role": "assistant", "model": t.f.model,
			"content": []any{map[string]any{"type": "text", "text": "sub-ok"}}}})
	t.system(map[string]any{"subtype": "task_updated", "task_id": agentID, "patch": map[string]any{"status": "completed"}})
	t.system(map[string]any{"subtype": "task_notification", "task_id": agentID, "tool_use_id": id, "status": "completed",
		"summary": "sub-ok", "usage": map[string]any{"total_tokens": 1000, "tool_uses": 1, "duration_ms": 400}})
	t.setBG(agentID, "")
	t.f.out(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<task-notification>\n<task-id>" +
		agentID + "</task-id>\n<status>completed</status>\n<result>sub-ok</result>\n</task-notification>"},
		"session_id": t.f.session, "uuid": newUUID(), "isReplay": true, "origin": map[string]any{"kind": "task-notification"}})
	t.say("The subagent said sub-ok.")
}

// agentCall starts an Agent call and returns its tool_use id and task id.
func (t *turnRun) agentCall(background bool) (string, string) {
	t.closeText()
	if t.apiMsg == "" {
		t.msgStart()
	}
	id := "toolu_fake_" + randHex(10)
	agentID := "a" + randHex(8)
	t.assistant(map[string]any{"type": "tool_use", "id": id, "name": "Agent",
		"input": map[string]any{"description": "A subagent", "subagent_type": "general-purpose", "prompt": "Work"}}, "")
	t.index++
	t.setBG(agentID, "local_agent")
	t.system(map[string]any{"subtype": "task_started", "task_id": agentID, "tool_use_id": id, "description": "A subagent",
		"subagent_type": "general-purpose", "is_backgrounded": background, "task_type": "local_agent", "prompt": "Work"})
	return id, agentID
}

// subagentOutliving is a background subagent whose Bash call is still
// running when the turn ends; the call, the agent and a turn of the CLI's
// own follow after seconds.
func (t *turnRun) subagentOutliving(seconds float64) {
	f := t.f
	id, agentID := t.agentCall(true)
	t.result(id, "Async agent launched successfully.\nagentId: "+agentID, map[string]any{"isAsync": true,
		"status": "async_launched", "agentId": agentID}, false, "")
	inner := "toolu_fake_" + randHex(10)
	t.assistant(map[string]any{"type": "tool_use", "id": inner, "name": "Bash", "input": map[string]any{"command": "sleep 1; echo late"}}, id)
	t.say("The subagent works on.")
	go func() {
		time.Sleep(time.Duration(seconds * float64(time.Second)))
		bt := &turnRun{f: f, ctx: context.Background(), apiMsg: "msg_fake_" + randHex(8)}
		bt.result(inner, "late", nil, false, id)
		bt.assistant(map[string]any{"type": "text", "text": "late"}, id)
		bt.system(map[string]any{"subtype": "task_updated", "task_id": agentID, "patch": map[string]any{"status": "completed"}})
		bt.system(map[string]any{"subtype": "task_notification", "task_id": agentID, "tool_use_id": id, "status": "completed", "summary": "late"})
		f.mu.Lock()
		delete(f.bg, agentID)
		f.queue = append(f.queue, job{uuid: newUUID(), text: "<task-notification>\n<task-id>" + agentID + "</task-id>\n</task-notification>", origin: "task-notification"})
		f.mu.Unlock()
		bt.tasksChanged()
		f.poke()
	}()
}

// subagentFailing is a foreground subagent that fails: its task reports the
// failure before the call returns its hand-back.
func (t *turnRun) subagentFailing() {
	id, agentID := t.agentCall(false)
	t.system(map[string]any{"subtype": "task_updated", "task_id": agentID, "patch": map[string]any{"status": "failed"}})
	t.system(map[string]any{"subtype": "task_notification", "task_id": agentID, "tool_use_id": id, "status": "failed", "summary": "it broke"})
	t.setBG(agentID, "")
	t.result(id, "[Subagent hand-back] it broke", map[string]any{"status": "completed", "agentId": agentID}, false, "")
	t.say("The subagent failed.")
}

// workflow launches a background workflow of two phases and three agents.
// The turn ends at once; the workflow reports through task_progress, writes
// each agent's transcript, and its end starts a turn of the CLI's own.
func (t *turnRun) workflow(seconds float64) {
	t.closeText()
	if t.apiMsg == "" {
		t.msgStart()
	}
	f := t.f
	id := "toolu_fake_" + randHex(10)
	taskID := "w" + randHex(6)
	runID := "wf_" + randHex(6)
	dir := filepath.Join(f.projectDir(), f.session, "subagents", "workflows", runID)
	_ = os.MkdirAll(dir, 0o755)
	t.f.out(t.se(map[string]any{"type": "content_block_start", "index": t.index,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": "Workflow", "input": map[string]any{}}}))
	t.assistant(map[string]any{"type": "tool_use", "id": id, "name": "Workflow", "input": map[string]any{"script": "export const meta = {name: 'fake-flow'}"}}, "")
	t.index++
	t.setBG(taskID, "local_workflow")
	t.system(map[string]any{"subtype": "task_started", "task_id": taskID, "tool_use_id": id, "description": "A fake workflow",
		"task_type": "local_workflow", "workflow_name": "fake-flow"})
	t.result(id, "Workflow launched in background. Task ID: "+taskID, map[string]any{"status": "async_launched", "taskId": taskID,
		"taskType": "local_workflow", "workflowName": "fake-flow", "runId": runID, "summary": "A fake workflow", "transcriptDir": dir}, false, "")
	t.say("The workflow is running in the background.")
	go f.runWorkflow(id, taskID, dir, time.Duration(seconds*float64(time.Second)))
}

func (f *fake) runWorkflow(toolUseID, taskID, dir string, total time.Duration) {
	t := &turnRun{f: f, ctx: context.Background()}
	type wfAgent struct {
		label, phase string
		phaseIndex   int
		id, state    string
	}
	agents := []*wfAgent{{label: "echo apple", phase: "Gather", phaseIndex: 1}, {label: "echo pear", phase: "Gather", phaseIndex: 1},
		{label: "join", phase: "Combine", phaseIndex: 2}}
	progress := func(desc string) {
		list := []map[string]any{{"type": "workflow_phase", "index": 1, "title": "Gather"}, {"type": "workflow_phase", "index": 2, "title": "Combine"}}
		for i, a := range agents {
			if a.state == "" {
				continue
			}
			e := map[string]any{"type": "workflow_agent", "index": i + 1, "label": a.label, "phaseIndex": a.phaseIndex,
				"phaseTitle": a.phase, "model": f.model, "state": a.state, "promptPreview": "Do " + a.label}
			if a.id != "" {
				e["agentId"] = a.id
			}
			if a.state == "done" {
				e["resultPreview"], e["tokens"], e["toolCalls"], e["durationMs"] = strings.TrimPrefix(a.label, "echo "), 900, 1, 300
			}
			list = append(list, e)
		}
		t.system(map[string]any{"subtype": "task_progress", "task_id": taskID, "tool_use_id": toolUseID, "description": desc,
			"usage": map[string]any{"total_tokens": 1000, "tool_uses": 1, "duration_ms": 500}, "workflow_progress": list})
	}
	journal := filepath.Join(dir, "journal.jsonl")
	writeJSONL(journal, map[string]any{"type": "launched"})
	step := total / 6
	for _, a := range agents {
		a.id, a.state = "a"+randHex(8), "start"
		appendJSONL(journal, map[string]any{"type": "started", "agentId": a.id, "label": a.label, "phase": a.phase})
		progress(a.phase + ": " + a.label)
		time.Sleep(step)
		tool := "toolu_fake_" + randHex(10)
		cmd := "echo " + strings.TrimPrefix(a.label, "echo ")
		path := filepath.Join(dir, "agent-"+a.id+".jsonl")
		writeJSONL(path,
			map[string]any{"type": "user", "isSidechain": true, "uuid": newUUID(), "message": map[string]any{"role": "user", "content": "Do " + a.label}},
			map[string]any{"type": "assistant", "isSidechain": true, "uuid": newUUID(), "message": map[string]any{"role": "assistant", "model": f.model,
				"content": []any{map[string]any{"type": "tool_use", "id": tool, "name": "Bash", "input": map[string]any{"command": cmd}}}}})
		a.state = "progress"
		progress(a.phase + ": " + a.label)
		time.Sleep(step)
		appendJSONL(path,
			map[string]any{"type": "user", "isSidechain": true, "uuid": newUUID(), "message": map[string]any{"role": "user",
				"content": []any{map[string]any{"type": "tool_result", "tool_use_id": tool, "content": strings.TrimPrefix(cmd, "echo ")}}}},
			map[string]any{"type": "assistant", "isSidechain": true, "uuid": newUUID(), "message": map[string]any{"role": "assistant", "model": f.model,
				"content": []any{map[string]any{"type": "text", "text": strings.TrimPrefix(cmd, "echo ")}}}})
		appendJSONL(journal, map[string]any{"type": "result", "agentId": a.id, "result": strings.TrimPrefix(cmd, "echo ")})
		a.state = "done"
		progress(a.phase + ": " + a.label)
	}
	time.Sleep(tailSettle)
	t.system(map[string]any{"subtype": "task_updated", "task_id": taskID, "patch": map[string]any{"status": "completed"}})
	t.system(map[string]any{"subtype": "task_notification", "task_id": taskID, "tool_use_id": toolUseID, "status": "completed",
		"summary": "Dynamic workflow \"A fake workflow\" completed", "usage": map[string]any{"total_tokens": 3000, "tool_uses": 3, "duration_ms": 1500}})
	f.mu.Lock()
	delete(f.bg, taskID)
	f.queue = append(f.queue, job{uuid: newUUID(), text: "<task-notification>\n<task-id>" + taskID + "</task-id>\n<status>completed</status>\n</task-notification>", origin: "task-notification"})
	f.mu.Unlock()
	t.tasksChanged()
	f.poke()
}

// tailSettle lets a reader of the agent transcripts see the last lines
// before the workflow says it is done.
const tailSettle = 700 * time.Millisecond

func (t *turnRun) background(seconds float64) {
	t.closeText()
	f := t.f
	taskID := "b" + randHex(6)
	id := t.tool("Bash", map[string]any{"command": "sleep 1; echo bg-done", "run_in_background": true},
		"Command running in background with ID: "+taskID, map[string]any{"stdout": "", "stderr": "", "backgroundTaskId": taskID}, false, "")
	t.setBG(taskID, "local_bash")
	t.system(map[string]any{"subtype": "task_started", "task_id": taskID, "tool_use_id": id, "description": "sleep then echo", "task_type": "local_bash"})
	t.say("Started a background task.")
	go func() {
		time.Sleep(time.Duration(seconds * float64(time.Second)))
		bt := &turnRun{f: f, ctx: context.Background()}
		bt.system(map[string]any{"subtype": "task_updated", "task_id": taskID, "patch": map[string]any{"status": "completed"}})
		bt.system(map[string]any{"subtype": "task_notification", "task_id": taskID, "tool_use_id": id, "status": "completed",
			"summary": "Background command completed (exit code 0)"})
		f.mu.Lock()
		delete(f.bg, taskID)
		f.queue = append(f.queue, job{uuid: newUUID(), text: "<task-notification>\n<task-id>" + taskID + "</task-id>\n</task-notification>", origin: "task-notification"})
		f.mu.Unlock()
		bt.tasksChanged()
		f.poke()
	}()
}

// plan asks to leave plan mode through can_use_tool, as ExitPlanMode does,
// and waits for the answer.
func (t *turnRun) plan() {
	t.closeText()
	if t.apiMsg == "" {
		t.msgStart()
	}
	f := t.f
	planText := "1. Create hi.txt.\n2. Check it."
	planFile := filepath.Join(f.configDir, "plans", "fake-plan.md")
	_ = os.MkdirAll(filepath.Dir(planFile), 0o755)
	_ = os.WriteFile(planFile, []byte(planText), 0o644)
	id := "toolu_fake_" + randHex(10)
	input := map[string]any{"plan": planText, "planFilePath": planFile}
	f.out(t.se(map[string]any{"type": "content_block_start", "index": t.index,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": "ExitPlanMode", "input": map[string]any{}}}))
	t.assistant(map[string]any{"type": "tool_use", "id": id, "name": "ExitPlanMode", "input": input}, "")
	t.index++
	resp, ok := f.ask(t.ctx, map[string]any{"subtype": "can_use_tool", "tool_name": "ExitPlanMode", "display_name": "ExitPlanMode",
		"input": input, "tool_use_id": id, "requires_user_interaction": true})
	if !ok {
		t.result(id, "The user doesn't want to proceed with this tool use.", nil, true, "")
		t.cut = true
		return
	}
	var r struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Behavior == "allow" {
		f.mu.Lock()
		f.mode = "bypassPermissions"
		f.mu.Unlock()
		t.system(map[string]any{"subtype": "status", "status": nil, "permissionMode": "bypassPermissions"})
		t.result(id, "User has approved your plan. You can now start coding.", map[string]any{"plan": planText, "filePath": planFile}, false, "")
		t.say("The plan was approved.")
		return
	}
	t.result(id, r.Message, "Error: "+r.Message, true, "")
	t.say("Keeping on planning: " + r.Message)
}

// askCall is the ask_user call /askuser makes without arguments.
const askCall = `{"title":"Setup","questions":[` +
	`{"id":"db","header":"Database","question":"Which database?","kind":"single",` +
	`"options":[{"id":"pg","label":"PostgreSQL","recommended":true},{"id":"sqlite","label":"SQLite"}]},` +
	`{"id":"name","question":"What is it called?","kind":"text"}]}`

// askUser is the model calling the socrates server's ask_user: the tool_use,
// then the call over mcp_message, which blocks until the host answers it.
func (t *turnRun) askUser(arg string) {
	f := t.f
	t.closeText()
	if t.apiMsg == "" {
		t.msgStart()
	}
	input := json.RawMessage(askCall)
	if strings.TrimSpace(arg) != "" {
		input = json.RawMessage(arg)
	}
	id := "toolu_fake_" + randHex(10)
	name := "mcp__socrates__ask_user"
	f.out(t.se(map[string]any{"type": "content_block_start", "index": t.index,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}}))
	t.assistant(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}, "")
	f.out(t.se(map[string]any{"type": "content_block_stop", "index": t.index}))
	t.index++
	raw, ok := f.mcpRequest(t.ctx, "socrates", "tools/call", map[string]any{"name": "ask_user", "arguments": input,
		"_meta": map[string]any{"claudecode/toolUseId": id, "progressToken": 7}}, false)
	if !ok {
		t.result(id, "[Tool call interrupted by user]", nil, true, "")
		t.cut = true
		return
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &resp)
	text := ""
	if len(resp.Result.Content) > 0 {
		text = resp.Result.Content[0].Text
	}
	if resp.Error != nil {
		text, resp.Result.IsError = resp.Error.Message, true
	}
	t.result(id, text, nil, resp.Result.IsError, "")
	if resp.Result.IsError {
		t.say("ask_user failed: " + text)
		return
	}
	t.say("ask_user returned: " + text)
}

// askDrop is a call of ask_user withdrawn the moment it is made: the
// tools/call, and right behind it its notifications/cancelled, without a
// control_cancel_request, so that only the JSON-RPC id ties the two.
func (t *turnRun) askDrop() {
	f := t.f
	f.mu.Lock()
	rpcID := f.rpcID
	f.rpcID++
	f.mu.Unlock()
	f.out(map[string]any{"type": "control_request", "request_id": newUUID(),
		"request": map[string]any{"subtype": "mcp_message", "server_name": "socrates", "message": map[string]any{
			"jsonrpc": "2.0", "id": rpcID, "method": "tools/call",
			"params": map[string]any{"name": "ask_user", "arguments": json.RawMessage(askCall)}}}})
	f.mcpRequest(context.Background(), "socrates", "notifications/cancelled",
		map[string]any{"requestId": rpcID, "reason": "AbortError: the operation was aborted"}, true)
	t.say("ask_user was withdrawn")
}

// elicit is an MCP server asking the person through the CLI: a form, or a
// URL to open.
func (t *turnRun) elicit(url bool) {
	mode := "form"
	if url {
		mode = "url"
	}
	resp, ok := t.f.ask(t.ctx, map[string]any{"subtype": "elicitation", "mcp_server_name": "lab", "message": "Pick a colour.",
		"mode": mode, "property_order": []string{"colour", "notes"},
		"requested_schema": map[string]any{"type": "object", "required": []string{"colour"}, "properties": map[string]any{
			"colour": map[string]any{"type": "string", "title": "Colour", "enum": []string{"red", "green", "blue"}},
			"notes":  map[string]any{"type": "string", "title": "Notes"}}}})
	if !ok {
		t.cut = true
		return
	}
	t.say("elicitation: " + string(resp))
}

func (t *turnRun) limit() {
	t.f.out(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected",
		"resetsAt": time.Now().Add(2 * time.Hour).Unix(), "rateLimitType": "five_hour",
		"unifiedWindows": map[string]any{"five_hour": map[string]any{"utilization": 1.0, "resetsAt": time.Now().Add(2 * time.Hour).Unix()}}},
		"uuid": newUUID(), "session_id": t.f.session})
	if t.apiMsg == "" {
		t.msgStart()
	}
	t.f.out(map[string]any{"type": "assistant", "message": map[string]any{"model": "<synthetic>", "id": newUUID(), "role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": "Claude AI usage limit reached"}}}, "error": "rate_limit",
		"session_id": t.f.session, "uuid": newUUID()})
	t.limited = true
}

func (t *turnRun) image() {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	t.closeText()
	if t.apiMsg == "" {
		t.msgStart()
	}
	id := "toolu_fake_" + randHex(10)
	t.assistant(map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{"file_path": "/tmp/red.png"}}, "")
	uid := newUUID()
	t.f.out(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{
		"type": "tool_result", "tool_use_id": id, "content": []any{map[string]any{"type": "image",
			"source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(png)}}}}}},
		"session_id": t.f.session, "uuid": uid})
	t.say("A red square.")
}

func (t *turnRun) compact() {
	f := t.f
	t.system(map[string]any{"subtype": "status", "status": "compacting"})
	time.Sleep(20 * time.Millisecond)
	t.system(map[string]any{"subtype": "status", "status": nil, "compact_result": "success"})
	t.system(map[string]any{"subtype": "compact_boundary", "compact_metadata": map[string]any{"trigger": "manual", "pre_tokens": 21641, "post_tokens": 5092}})
	f.writeRaw(map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": newUUID(), "content": "Conversation compacted"})
	summary := newUUID()
	f.out(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "This session is being continued from a previous conversation."},
		"session_id": f.session, "uuid": summary, "isSynthetic": true})
	f.writeRaw(map[string]any{"type": "user", "uuid": summary, "isCompactSummary": true,
		"message": map[string]any{"role": "user", "content": "This session is being continued from a previous conversation."}})
	f.out(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<local-command-stdout>Compacted </local-command-stdout>"},
		"session_id": f.session, "uuid": newUUID(), "isReplay": true})
	f.out(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args></command-args>"},
		"session_id": f.session, "uuid": t.job.uuid, "isReplay": true})
	t.text.Reset()
	t.finishQuiet()
}

func (t *turnRun) modelCommand(arg string) {
	f := t.f
	f.mu.Lock()
	f.model = resolveModel(arg)
	f.mu.Unlock()
	text := "Set model to " + arg + " for this session only"
	f.out(map[string]any{"type": "assistant", "message": map[string]any{"model": "<synthetic>", "id": newUUID(), "role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": text}}}, "session_id": f.session, "uuid": newUUID(),
		"local_command_run": map[string]any{"command": "model", "args": arg}})
	f.out(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<command-name>/model</command-name>\n<command-args>" + arg + "</command-args>"},
		"session_id": f.session, "uuid": t.job.uuid, "isReplay": true})
	f.writeLine("user", t.job.uuid, "<command-name>/model</command-name>\n<command-args>"+arg+"</command-args>", nil, false)
	f.writeRaw(map[string]any{"type": "assistant", "uuid": newUUID(), "message": map[string]any{"model": "<synthetic>", "role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": text}}}})
	t.finishQuiet()
}

// finishQuiet ends a local command's turn: no text block of its own.
func (t *turnRun) finishQuiet() {
	t.text.Reset()
	t.finish()
}

// ------------------------------------------------------------ the transcript

func (f *fake) projectDir() string {
	return filepath.Join(f.configDir, "projects", escapeCwd(f.cwd))
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

func escapeCwd(cwd string) string { return nonAlnum.ReplaceAllString(cwd, "-") }

func (f *fake) transcriptPath(id string) string {
	matches, _ := filepath.Glob(filepath.Join(f.configDir, "projects", "*", id+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return filepath.Join(f.projectDir(), id+".jsonl")
}

// writeRaw appends one line to the transcript with the fields every message
// line has.
func (f *fake) writeRaw(line map[string]any) {
	f.mu.Lock()
	parent := f.lastUUID
	if id, _ := line["uuid"].(string); id != "" {
		f.lastUUID = id
	}
	f.mu.Unlock()
	line["parentUuid"], line["sessionId"], line["cwd"], line["version"] = nilIfEmpty(parent), f.session, f.cwd, version
	if _, ok := line["isSidechain"]; !ok {
		line["isSidechain"] = false
	}
	line["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	path := f.transcriptPath(f.session)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	appendJSONL(path, line)
}

func (f *fake) writeLine(kind, id, text string, content any, sidechain bool) {
	msg := map[string]any{"role": kind, "content": text}
	if content != nil {
		msg["content"] = content
	}
	f.writeRaw(map[string]any{"type": kind, "uuid": id, "message": msg, "isSidechain": sidechain})
}

// repairCutOffTurn does what a resume does after a process that died mid-turn:
// a call without a result gets "[Tool call interrupted…", and an unanswered
// prompt gets "No response requested.".
func (f *fake) repairCutOffTurn() {
	raw, err := os.ReadFile(f.transcriptPath(f.session))
	if err != nil {
		return
	}
	var last map[string]any
	pendingTools := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var l map[string]any
		if json.Unmarshal([]byte(line), &l) != nil {
			continue
		}
		if id, _ := l["uuid"].(string); id != "" {
			f.lastUUID = id
		}
		typ, _ := l["type"].(string)
		if typ == "cost-state" {
			// The real CLI restores the conversation's cost on resume:
			// total_cost_usd goes on from here, not from zero.
			f.totalCost, _ = l["totalCostUSD"].(float64)
		}
		if typ != "user" && typ != "assistant" {
			continue
		}
		last = l
		msg, _ := l["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			switch bm["type"] {
			case "tool_use":
				pendingTools[bm["id"].(string)] = true
			case "tool_result":
				delete(pendingTools, fmt.Sprint(bm["tool_use_id"]))
			}
		}
	}
	if last == nil {
		return
	}
	for id := range pendingTools {
		f.writeRaw(map[string]any{"type": "user", "uuid": newUUID(), "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": true, "content": "[Tool call interrupted: the session ended before this call's result was recorded, so its outcome is unknown.]"}}},
			"toolDenialKind": "interrupted"})
	}
	if last["type"] == "user" || len(pendingTools) > 0 {
		f.writeRaw(map[string]any{"type": "assistant", "uuid": newUUID(), "message": map[string]any{"model": "<synthetic>", "role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": "No response requested."}}}})
	}
}

func writeJSONL(path string, lines ...map[string]any) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, nil, 0o644)
	appendJSONL(path, lines...)
}

func appendJSONL(path string, lines ...map[string]any) {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		fh.Write(append(raw, '\n'))
	}
}

// ------------------------------------------------------------ helpers

func intArg(fields []string, i, fallback int) int {
	if i < len(fields) {
		if n, err := strconv.Atoi(fields[i]); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

func floatArg(fields []string, i int, fallback float64) float64 {
	if i < len(fields) {
		if n, err := strconv.ParseFloat(fields[i], 64); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

func chunks(s string, n int) []string {
	var out []string
	r := []rune(s)
	for len(r) > 0 {
		k := min(n, len(r))
		out = append(out, string(r[:k]))
		r = r[k:]
	}
	return out
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
