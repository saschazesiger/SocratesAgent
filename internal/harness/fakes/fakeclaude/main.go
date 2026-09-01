// Command fakeclaude imitates `claude -p --output-format stream-json
// --input-format stream-json` closely enough to drive the Claude adapter.
//
// It is installed on PATH under the name "claude" by fakes.Build. Its
// behaviour is driven entirely by FAKE_SCRIPT; see the fakes package doc.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness/fakes/script"
)

// defaultSessionID is what init reports when neither --session-id nor
// --resume was passed (FK-9: the fake remembers nothing across processes).
const defaultSessionID = "11111111-1111-4111-8111-111111111111"

// Two env vars reproduce behaviour of the real CLI that the fake would
// otherwise have to guess at (DESIGN rev 10, E-1 and E-2).
const (
	// envGhostResume=1 makes a --resume launch reproduce the measured failure
	// of resuming a session that does not exist. It is consumed on first use,
	// so the adapter's one-shot relaunch without --resume succeeds.
	envGhostResume = "FAKE_GHOST_RESUME"
	// envStateDir is where the consumed-ghost marker is written. With it
	// unset the flag is never consumed and every --resume dies.
	envStateDir = "FAKE_STATE_DIR"
	// envInitAtStart=1 restores writing system/init at process start. By
	// default init is the first line of the first turn, which is what the
	// real CLI does under --input-format stream-json.
	envInitAtStart = "FAKE_INIT_AT_START"
)

// ghostResumeDelay is how long the ghost death takes. The real CLI takes
// 1.3-1.5 s; the design pins ~200 ms so a hermetic test stays quick while the
// asynchronous shape of the failure is preserved.
const ghostResumeDelay = 200 * time.Millisecond

// claudeVersion is what `claude --version` prints, in the real format
// ("2.1.252 (Claude Code)"), carrying the same version init reports.
const claudeVersion = "2.1.252-fake (Claude Code)"

// noConversation is the real CLI's message for an unknown --resume id. It goes
// on stderr and, per the design, into the ghost result's result field.
const noConversation = "No conversation found with session ID: "

type fake struct {
	wmu sync.Mutex
	out *bufio.Writer

	session string
	model   string
	replay  bool

	steps []script.Step

	initOnce sync.Once
	initial  initLine
	deferred bool

	uuidN atomic.Int64
	msgN  atomic.Int64
	toolN atomic.Int64

	runMu sync.Mutex // serialises turns

	turnMu sync.Mutex
	cur    *turn
}

type turn struct {
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc

	lastText  string
	subagents int

	curMsg string // "" when no API message is open
	curIdx int
}

func main() {
	args := os.Args[1:]
	script.Record(os.Args)

	// The catalogue runs `claude --version` to find out whether the binary is
	// there; it must not look like a usage error.
	if script.VersionAsked(args) {
		fmt.Println(claudeVersion)
		os.Exit(0)
	}

	if !hasFlag(args, "-p") ||
		value(args, "--output-format") != "stream-json" ||
		value(args, "--input-format") != "stream-json" {
		fmt.Fprintln(os.Stderr, "fakeclaude: expected -p --output-format stream-json --input-format stream-json")
		os.Exit(2)
	}

	f := &fake{out: bufio.NewWriter(os.Stdout), steps: script.MustLoad()}
	f.model = value(args, "--model")
	f.replay = hasFlag(args, "--replay-user-messages")
	switch {
	case value(args, "--session-id") != "":
		f.session = value(args, "--session-id")
	case value(args, "--resume") != "":
		f.session = value(args, "--resume")
	default:
		f.session = defaultSessionID
	}

	// A --resume that the CLI cannot satisfy never reaches init.
	if resume := value(args, "--resume"); resume != "" && ghostArmed(resume) {
		f.ghostDeath(resume)
	}

	cwd, _ := os.Getwd()
	f.initial = initLine{
		Type:           "system",
		Subtype:        "init",
		Cwd:            cwd,
		SessionID:      f.session,
		Tools:          []string{"Bash", "Read", "Task"},
		Model:          f.model,
		PermissionMode: "bypassPermissions",
		Version:        "2.1.252-fake",
		Capabilities:   []string{"interrupt_receipt_v1", "interrupt_cancel_queued_v1", "msg_lifecycle_v1"},
		UUID:           f.uuid(),
	}
	// E-1: by default nothing at all is written until the first user line.
	f.deferred = os.Getenv(envInitAtStart) != "1"
	if !f.deferred {
		f.sayInit()
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		switch env["type"] {
		case "control_request":
			f.control(env)
		case "user":
			// In deferred mode init is the first thing the turn produces,
			// ahead of the replay echo, exactly as observed live.
			f.sayInit()
			if f.replay {
				env["isReplay"] = true
				env["session_id"] = f.session
				env["uuid"] = f.uuid()
				f.emit(env)
			}
			// The turn is registered here, on the reader, so an interrupt
			// that arrives before the turn goroutine is scheduled still
			// finds it.
			t := f.openTurn()
			wg.Add(1)
			go func() { defer wg.Done(); f.runTurn(t) }()
		}
	}
	wg.Wait()
	f.flush()
	os.Exit(0)
}

// sayInit writes the system/init line, once.
func (f *fake) sayInit() {
	f.initOnce.Do(func() { f.emit(f.initial) })
}

// ghostArmed reports whether this --resume should die, and consumes the flag
// so the next launch for the same session succeeds. The marker lives in
// $FAKE_STATE_DIR; with that unset the flag is never consumed.
func ghostArmed(id string) bool {
	if os.Getenv(envGhostResume) != "1" {
		return false
	}
	dir := os.Getenv(envStateDir)
	if dir == "" {
		return true
	}
	marker := filepath.Join(dir, "ghost-resume-"+markerName(id))
	if _, err := os.Stat(marker); err == nil {
		return false
	}
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(marker, []byte(id+"\n"), 0o600)
	return true
}

// markerName keeps a session id usable as a file name.
func markerName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ghostDeath reproduces `claude --resume <uuid>` for a session that does not
// exist: silence, then one unprompted result carrying num_turns:0 with no
// system/init before it, then exit 1 with the CLI's own message on stderr. The
// missing init plus num_turns:0 is the discriminator — a real turn always has
// init ahead of it and num_turns >= 1.
func (f *fake) ghostDeath(id string) {
	time.Sleep(ghostResumeDelay)
	f.emit(ghostResult{
		Type:           "result",
		Subtype:        "error_during_execution",
		IsError:        true,
		NumTurns:       0,
		TerminalReason: "api_error",
		Result:         noConversation + id,
		SessionID:      id,
		UUID:           f.uuid(),
	})
	f.flush()
	fmt.Fprintln(os.Stderr, noConversation+id)
	os.Exit(1)
}

// ---------------------------------------------------------------- the turn

// openTurn registers a turn as open before its goroutine runs.
func (f *fake) openTurn() *turn {
	ctx, cancel := context.WithCancel(context.Background())
	t := &turn{ctx: ctx, cancel: cancel}
	f.turnMu.Lock()
	f.cur = t
	f.turnMu.Unlock()
	return t
}

func (f *fake) runTurn(t *turn) {
	f.runMu.Lock()
	defer f.runMu.Unlock()

	ctx := t.ctx
	for _, s := range f.steps {
		if ctx.Err() != nil {
			return
		}
		switch s.Do {
		case script.DoText:
			f.text(t, s.Text)
		case script.DoReason:
			f.reason(t, s.Text)
		case script.DoTool:
			f.tool(t, s.Name, map[string]any{"command": s.Input}, s.Output, s.Exit)
		case script.DoSubagent:
			t.subagents++
			f.tool(t, "Task", map[string]any{
				"subagent_type": "general-purpose",
				"description":   s.Input,
			}, s.Output, 0)
		case script.DoAsk:
			// Claude has no approval path under --permission-mode
			// bypassPermissions, so an ask step emits nothing at all.
		case script.DoSleep:
			script.Sleep(ctx, s.MS)
		case script.DoDie:
			f.flush()
			os.Exit(s.Code)
		case script.DoHang:
			// Keep the turn open forever; the reader loop keeps serving
			// control requests and stdin EOF still exits.
			return
		case script.DoEnd:
			f.end(t, s)
			return
		}
	}
}

func (f *fake) end(t *turn, s script.Step) {
	f.closeMsg(t, "end_turn")
	spawned := t.subagents
	if s.Subagents != nil {
		spawned = *s.Subagents
	}
	r := resultLine{
		Type:         "result",
		Subtype:      "success",
		NumTurns:     1,
		Result:       t.lastText,
		TotalCostUSD: 0.0017,
		Usage:        defaultUsage(),
		SessionID:    f.session,
		UUID:         f.uuid(),
	}
	r.SubagentStats.Spawned = spawned
	if s.Outcome == script.OutcomeError {
		// FK-7: the bad-model shape. subtype stays "success" on purpose.
		status := 404
		r.IsError = true
		r.TerminalReason = "api_error"
		r.APIErrorStatus = &status
		r.Result = s.Error
	} else {
		r.TerminalReason = "completed"
	}
	f.finish(t, r)
}

// finish emits the turn's single result line. Every trigger goes through it.
func (f *fake) finish(t *turn, r resultLine) {
	t.once.Do(func() {
		// The turn is closed before the result reaches the reader, so a
		// control_request that arrives right after it finds an idle process.
		f.turnMu.Lock()
		if f.cur == t {
			f.cur = nil
		}
		f.turnMu.Unlock()

		b, err := json.Marshal(r)
		if err != nil {
			return
		}
		// The write lock is the arbiter: the turn is cancelled while it is
		// held, so a block already on its way to emitTurn is dropped instead
		// of landing after the result line.
		f.wmu.Lock()
		defer f.wmu.Unlock()
		t.cancel()
		f.write(b)
	})
}

// control answers a control_request. FK-10.
func (f *fake) control(env map[string]any) {
	id, _ := env["request_id"].(string)
	f.emit(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": id,
			"response":   map[string]any{"still_queued": []any{}},
		},
	})

	f.turnMu.Lock()
	t := f.cur
	f.turnMu.Unlock()
	if t == nil {
		// Idle: nothing further is emitted.
		return
	}
	r := resultLine{
		Type:           "result",
		Subtype:        "error_during_execution",
		IsError:        true,
		TerminalReason: "aborted_tools",
		NumTurns:       1,
		Result:         t.lastText,
		TotalCostUSD:   0.0017,
		Usage:          defaultUsage(),
		SessionID:      f.session,
		UUID:           f.uuid(),
	}
	r.SubagentStats.Spawned = t.subagents
	f.finish(t, r)
}

// ------------------------------------------------------------ block emission

// openMsg starts a new API message when none is open. The block index
// restarts at 0 in every message (FK-5).
func (f *fake) openMsg(t *turn) {
	if t.curMsg != "" {
		return
	}
	t.curMsg = fmt.Sprintf("msg_%d", f.msgN.Add(1))
	t.curIdx = 0
	f.emitTurn(t, streamEvent{
		Type: "stream_event",
		Event: map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":    t.curMsg,
				"role":  "assistant",
				"model": f.model,
			},
		},
		SessionID: f.session,
		UUID:      f.uuid(),
	})
}

// closeMsg ends the open API message the way the real CLI does, with a
// message_delta carrying the stop reason and a message_stop.
func (f *fake) closeMsg(t *turn, stopReason string) {
	if t.curMsg == "" {
		return
	}
	f.emitTurn(t, streamEvent{
		Type: "stream_event",
		Event: map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 60},
		},
		SessionID: f.session,
		UUID:      f.uuid(),
	})
	f.emitTurn(t, streamEvent{
		Type:      "stream_event",
		Event:     map[string]any{"type": "message_stop"},
		SessionID: f.session,
		UUID:      f.uuid(),
	})
	t.curMsg = ""
}

func (f *fake) text(t *turn, text string) {
	f.openMsg(t)
	i := t.curIdx
	t.curIdx++
	f.blockStart(t, i, map[string]any{"type": "text", "text": ""})
	for _, c := range script.Chunks(text, 3) {
		f.blockDelta(t, i, map[string]any{"type": "text_delta", "text": c})
	}
	f.blockStop(t, i)
	f.assistant(t, map[string]any{"type": "text", "text": text})
	t.lastText = text
}

func (f *fake) reason(t *turn, text string) {
	f.openMsg(t)
	i := t.curIdx
	t.curIdx++
	f.blockStart(t, i, map[string]any{"type": "thinking", "thinking": ""})
	for _, c := range script.Chunks(text, 3) {
		f.blockDelta(t, i, map[string]any{"type": "thinking_delta", "thinking": c})
	}
	f.blockStop(t, i)
	f.assistant(t, map[string]any{"type": "thinking", "thinking": text, "signature": "sig_fake"})
}

// tool emits the tool_use assistant line and the tool_result user line (FK-6),
// then closes the API message so the next text block starts a new one.
func (f *fake) tool(t *turn, name string, input map[string]any, output string, exit int) {
	if name == "" {
		name = "Bash"
	}
	f.openMsg(t)
	i := t.curIdx
	t.curIdx++
	id := fmt.Sprintf("toolu_%d", f.toolN.Add(1))
	args, _ := json.Marshal(input)
	f.blockStart(t, i, map[string]any{
		"type": "tool_use", "id": id, "name": name, "input": map[string]any{},
	})
	f.blockDelta(t, i, map[string]any{
		"type": "input_json_delta", "partial_json": string(args),
	})
	f.blockStop(t, i)
	f.assistant(t, map[string]any{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": input,
	})
	// The message ends with the tool call; the tool result opens the next one.
	f.closeMsg(t, "tool_use")
	f.emitTurn(t, userLine{
		Type: "user",
		Message: map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     output,
				"is_error":    exit != 0,
			}},
		},
		SessionID: f.session,
		UUID:      f.uuid(),
		ToolUseResult: map[string]any{
			"stdout":           output,
			"stderr":           "",
			"interrupted":      false,
			"isImage":          false,
			"noOutputExpected": false,
		},
	})
}

// assistant emits one assistant line holding exactly one completed content
// block. It deliberately carries no index field (FK-5).
func (f *fake) assistant(t *turn, block map[string]any) {
	f.emitTurn(t, assistantLine{
		Type: "assistant",
		Message: map[string]any{
			"model":       f.model,
			"id":          t.curMsg,
			"type":        "message",
			"role":        "assistant",
			"content":     []any{block},
			"stop_reason": nil,
			"usage": map[string]any{
				"input_tokens":            10,
				"cache_read_input_tokens": 13615,
				"output_tokens":           60,
			},
		},
		SessionID: f.session,
		UUID:      f.uuid(),
	})
}

func (f *fake) blockStart(t *turn, i int, block map[string]any) {
	f.emitTurn(t, streamEvent{
		Type:      "stream_event",
		Event:     map[string]any{"type": "content_block_start", "index": i, "content_block": block},
		SessionID: f.session,
		UUID:      f.uuid(),
	})
}

func (f *fake) blockDelta(t *turn, i int, delta map[string]any) {
	f.emitTurn(t, streamEvent{
		Type:      "stream_event",
		Event:     map[string]any{"type": "content_block_delta", "index": i, "delta": delta},
		SessionID: f.session,
		UUID:      f.uuid(),
	})
}

func (f *fake) blockStop(t *turn, i int) {
	f.emitTurn(t, streamEvent{
		Type:      "stream_event",
		Event:     map[string]any{"type": "content_block_stop", "index": i},
		SessionID: f.session,
		UUID:      f.uuid(),
	})
}

// ------------------------------------------------------------------- wire

type initLine struct {
	Type           string   `json:"type"`
	Subtype        string   `json:"subtype"`
	Cwd            string   `json:"cwd"`
	SessionID      string   `json:"session_id"`
	Tools          []string `json:"tools"`
	Model          string   `json:"model"`
	PermissionMode string   `json:"permissionMode"`
	Version        string   `json:"claude_code_version"`
	Capabilities   []string `json:"capabilities"`
	UUID           string   `json:"uuid"`
}

type streamEvent struct {
	Type      string         `json:"type"`
	Event     map[string]any `json:"event"`
	SessionID string         `json:"session_id"`
	Parent    *string        `json:"parent_tool_use_id"`
	UUID      string         `json:"uuid"`
}

type assistantLine struct {
	Type      string         `json:"type"`
	Message   map[string]any `json:"message"`
	Parent    *string        `json:"parent_tool_use_id"`
	SessionID string         `json:"session_id"`
	UUID      string         `json:"uuid"`
}

type userLine struct {
	Type          string         `json:"type"`
	Message       map[string]any `json:"message"`
	Parent        *string        `json:"parent_tool_use_id"`
	SessionID     string         `json:"session_id"`
	UUID          string         `json:"uuid"`
	ToolUseResult map[string]any `json:"tool_use_result"`
}

type usageBlock struct {
	InputTokens          int            `json:"input_tokens"`
	OutputTokens         int            `json:"output_tokens"`
	CacheReadInputTokens int            `json:"cache_read_input_tokens"`
	OutputTokensDetails  map[string]int `json:"output_tokens_details"`
}

func defaultUsage() usageBlock {
	return usageBlock{
		InputTokens:          10,
		OutputTokens:         60,
		CacheReadInputTokens: 13615,
		OutputTokensDetails:  map[string]int{"thinking_tokens": 32},
	}
}

// ghostResult is the result line a --resume of an unknown session writes.
type ghostResult struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	NumTurns       int    `json:"num_turns"`
	TerminalReason string `json:"terminal_reason"`
	Result         string `json:"result"`
	SessionID      string `json:"session_id"`
	UUID           string `json:"uuid"`
}

type resultLine struct {
	Type           string     `json:"type"`
	Subtype        string     `json:"subtype"`
	IsError        bool       `json:"is_error"`
	TerminalReason string     `json:"terminal_reason"`
	APIErrorStatus *int       `json:"api_error_status,omitempty"`
	NumTurns       int        `json:"num_turns"`
	Result         string     `json:"result"`
	TotalCostUSD   float64    `json:"total_cost_usd"`
	Usage          usageBlock `json:"usage"`
	SubagentStats  struct {
		Spawned int `json:"spawned"`
	} `json:"subagent_stats"`
	SessionID string `json:"session_id"`
	UUID      string `json:"uuid"`
}

func (f *fake) emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.write(b)
}

// emitTurn writes a frame that belongs to a turn. It checks the turn under the
// write lock, so nothing can be written once finish has decided on the
// terminal result line.
func (f *fake) emitTurn(t *turn, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if t.ctx.Err() != nil {
		return
	}
	f.write(b)
}

// write appends one line. The write lock must be held.
func (f *fake) write(b []byte) {
	f.out.Write(b)
	f.out.WriteByte('\n')
	f.out.Flush()
}

func (f *fake) flush() {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.out.Flush()
}

func (f *fake) uuid() string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", f.uuidN.Add(1))
}

// ------------------------------------------------------------------- argv

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func value(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return a[len(name)+1:]
		}
	}
	return ""
}
