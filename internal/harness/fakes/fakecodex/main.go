// Command fakecodex imitates `codex app-server --listen stdio://`: a
// newline-delimited JSON-RPC 2.0 responder on stdin/stdout.
//
// It is installed on PATH under the name "codex" by fakes.Build. Its
// behaviour is driven entirely by FAKE_SCRIPT; see the fakes package doc.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/saschazesiger/SocratesAgent/internal/harness/fakes/script"
)

// codexVersion is what `codex --version` prints, in the real format
// ("codex-cli 0.152.0").
const codexVersion = "codex-cli 0.152.0-fake"

// defaultThreadID is what thread/start reports. FK-9's rule applies here too:
// the fake remembers nothing across processes, so thread/resume simply echoes
// back the id it was given.
const defaultThreadID = "01a05d4f-cefd-7101-b75a-900101783ce4"

type fake struct {
	wmu sync.Mutex
	out *bufio.Writer

	steps    []script.Step
	lastText int

	itemN atomic.Int64
	turnN atomic.Int64
	reqN  atomic.Int64

	mu       sync.Mutex
	threadID string
	model    string
	pending  map[string]chan struct{} // our ServerRequest id -> answered

	runMu sync.Mutex

	turnMu sync.Mutex
	cur    *turnState
}

type turnState struct {
	id     string
	input  string // the user's text, echoed back as the userMessage item
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func main() {
	script.Record(os.Args)

	// The catalogue runs `codex --version` before anything else.
	if script.VersionAsked(os.Args[1:]) {
		fmt.Println(codexVersion)
		os.Exit(0)
	}

	// FK-15: there is no `generate-json-schema` mode. Discovery goes through
	// model/list, so anything but `app-server` is refused.
	if len(os.Args) < 2 || os.Args[1] != "app-server" {
		fmt.Fprintln(os.Stderr, "fakecodex: expected `app-server`")
		os.Exit(2)
	}

	f := &fake{
		out:      bufio.NewWriter(os.Stdout),
		steps:    script.MustLoad(),
		threadID: defaultThreadID,
		pending:  map[string]chan struct{}{},
	}
	f.lastText = script.LastText(f.steps)

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var fr frame
		if err := json.Unmarshal([]byte(line), &fr); err != nil {
			continue
		}
		switch {
		case fr.Method != "" && len(fr.ID) > 0:
			f.request(&wg, fr)
		case fr.Method != "":
			// A client notification ("initialized"); accepted and ignored.
		case len(fr.ID) > 0:
			// A response to one of our ServerRequests (FK-14): either a
			// JSON-RPC result or a JSON-RPC error is accepted.
			f.answer(string(fr.ID))
		}
	}
	wg.Wait()
	f.flush()
	os.Exit(0)
}

func (f *fake) request(wg *sync.WaitGroup, fr frame) {
	switch fr.Method {
	case "initialize":
		f.reply(fr.ID, map[string]any{
			"userAgent":      "fake/0.152.0",
			"codexHome":      "/tmp/fake-codex",
			"platformFamily": "unix",
			"platformOs":     "linux",
		})
		// Unsolicited, immediately after the reply: the drain-unknown path.
		f.notify("remoteControl/status/changed", map[string]any{
			"status":         "disabled",
			"serverName":     "browserdev",
			"installationId": "b252cc02-bc14-4843-91bb-65f1a0bcd701",
			"environmentId":  nil,
		})

	case "model/list":
		f.reply(fr.ID, map[string]any{"data": modelList()})

	case "thread/start":
		var p struct {
			Cwd            string          `json:"cwd"`
			Model          string          `json:"model"`
			Config         json.RawMessage `json:"config"`
			ApprovalPolicy string          `json:"approvalPolicy"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		f.mu.Lock()
		f.threadID = defaultThreadID
		id := f.threadID
		f.mu.Unlock()
		th := threadObject(id, p.Cwd, nil)
		f.notify("thread/started", map[string]any{"threadId": id, "thread": th})
		f.reply(fr.ID, map[string]any{
			"thread":          th,
			"model":           p.Model,
			"approvalPolicy":  firstNonEmpty(p.ApprovalPolicy, "never"),
			"sandbox":         map[string]any{"type": "dangerFullAccess"},
			"reasoningEffort": effortFromConfig(p.Config),
		})

	case "thread/resume":
		// FK-17: same shape, empty turns, echoing the requested id.
		var p struct {
			ThreadID     string `json:"threadId"`
			Cwd          string `json:"cwd"`
			ExcludeTurns bool   `json:"excludeTurns"`
			Model        string `json:"model"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		f.mu.Lock()
		if p.ThreadID != "" {
			f.threadID = p.ThreadID
		}
		id := f.threadID
		f.mu.Unlock()
		f.reply(fr.ID, map[string]any{
			"thread":          threadObject(id, p.Cwd, []any{}),
			"model":           p.Model,
			"approvalPolicy":  "never",
			"sandbox":         map[string]any{"type": "dangerFullAccess"},
			"reasoningEffort": "medium",
		})

	case "turn/start":
		var p struct {
			ThreadID string `json:"threadId"`
			Model    string `json:"model"`
			Effort   string `json:"effort"`
			Input    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		input := ""
		if len(p.Input) > 0 {
			input = p.Input[0].Text
		}
		f.mu.Lock()
		f.model = p.Model
		f.mu.Unlock()
		// F-5: model and effort must arrive on every turn.
		script.Record([]string{"turn/start", "model=" + p.Model, "effort=" + p.Effort})

		turnID := fmt.Sprintf("turn_%d", f.turnN.Add(1))
		f.reply(fr.ID, map[string]any{
			"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}},
		})
		// FK-11: the notification carries the same id the result did.
		f.notify("turn/started", map[string]any{
			"threadId": f.thread(),
			"turn":     map[string]any{"id": turnID, "status": "inProgress", "items": []any{}},
		})
		// The turn is registered here, on the reader, so a turn/interrupt
		// that arrives before the turn goroutine is scheduled still finds it.
		t := f.openTurn(turnID, input)
		wg.Add(1)
		go func() { defer wg.Done(); f.runTurn(t) }()

	case "turn/interrupt":
		f.reply(fr.ID, map[string]any{})
		f.turnMu.Lock()
		t := f.cur
		f.turnMu.Unlock()
		if t == nil {
			return
		}
		f.finish(t,
			notification("thread/status/changed", map[string]any{
				"threadId": f.thread(), "status": map[string]any{"type": "idle"},
			}),
			notification("turn/completed", turnFrame(f.thread(), t.id, "interrupted")),
		)

	default:
		f.replyError(fr.ID, -32601, "method not found: "+fr.Method)
	}
}

// ---------------------------------------------------------------- the turn

// openTurn registers a turn as open before its goroutine runs.
func (f *fake) openTurn(turnID, input string) *turnState {
	ctx, cancel := context.WithCancel(context.Background())
	t := &turnState{id: turnID, input: input, ctx: ctx, cancel: cancel}
	f.turnMu.Lock()
	f.cur = t
	f.turnMu.Unlock()
	return t
}

func (f *fake) runTurn(t *turnState) {
	f.runMu.Lock()
	defer f.runMu.Unlock()

	// Every real turn starts by echoing the user's own message back as a
	// userMessage item (research codex.md 3).
	f.userMessage(t)

	for i, s := range f.steps {
		if t.ctx.Err() != nil {
			return
		}
		switch s.Do {
		case script.DoText:
			phase := "commentary"
			if i == f.lastText {
				phase = "final_answer"
			}
			f.agentMessage(t, s.Text, phase)
		case script.DoReason:
			f.reasoning(t, s.Text)
		case script.DoTool:
			f.command(t, s.Input, s.Output, s.Exit)
		case script.DoSubagent:
			f.subagent(t, s.Input)
		case script.DoAsk:
			f.ask(t, s.Input)
		case script.DoSleep:
			script.Sleep(t.ctx, s.MS)
		case script.DoDie:
			f.flush()
			os.Exit(s.Code)
		case script.DoHang:
			return
		case script.DoEnd:
			f.end(t, s)
			return
		}
	}
}

// end runs the FK-13 turn endings.
func (f *fake) end(t *turnState, s script.Step) {
	f.tokenUsage(t)
	switch s.Outcome {
	case script.OutcomeError:
		// warning -> systemError -> error, and no turn/completed ever. The
		// turn is closed all the same, so a later turn/interrupt cannot
		// resurrect it into a turn/completed.
		f.finish(t,
			notification("warning", map[string]any{
				"threadId": f.thread(),
				"message": fmt.Sprintf("Model metadata for `%s` not found. Defaulting to "+
					"fallback metadata; this can degrade performance and cause issues.", f.currentModel()),
			}),
			notification("thread/status/changed", map[string]any{
				"threadId": f.thread(), "status": map[string]any{"type": "systemError"},
			}),
			notification("error", errorParams(f.thread(), s.Error, false)),
		)
	case script.OutcomeRetry:
		// willRetry:true must be remembered but must not end the turn.
		f.emitTurn(t, notification("error", errorParams(f.thread(), s.Error, true)))
		f.finish(t, notification("turn/completed", turnFrame(f.thread(), t.id, "completed")))
	default:
		done := notification("turn/completed", turnFrame(f.thread(), t.id, "completed"))
		if s.Twice {
			// C-4: two turn/completed frames for one turn.
			f.finish(t, done, done)
		} else {
			f.finish(t, done)
		}
	}
	f.rateLimits()
}

// finish writes the turn's terminal frames. Every trigger goes through it and
// only the first one gets through; the turn is cancelled while the write lock
// is held, so no per-turn frame can land after them.
func (f *fake) finish(t *turnState, frames ...map[string]any) {
	t.once.Do(func() {
		// Clear before emitting, so an interrupt that races the turn end
		// finds an idle process rather than a half-closed turn.
		f.clear(t)
		var lines [][]byte
		for _, fr := range frames {
			b, err := json.Marshal(fr)
			if err != nil {
				return
			}
			lines = append(lines, b)
		}
		f.wmu.Lock()
		defer f.wmu.Unlock()
		t.cancel()
		for _, b := range lines {
			f.write(b)
		}
	})
}

func notification(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

func turnFrame(thread, turnID, status string) map[string]any {
	return map[string]any{
		"threadId": thread,
		"turn": map[string]any{
			"id":        turnID,
			"items":     []any{},
			"itemsView": "summary",
			"status":    status,
			"error":     nil,
		},
	}
}

func errorParams(thread, msg string, willRetry bool) map[string]any {
	return map[string]any{
		"threadId":  thread,
		"willRetry": willRetry,
		"error": map[string]any{
			"message":           msg,
			"codexErrorInfo":    "other",
			"additionalDetails": nil,
			"misalignment":      nil,
		},
	}
}

func (f *fake) currentModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.model == "" {
		return "gpt-5.4-mini"
	}
	return f.model
}

func (f *fake) clear(t *turnState) {
	f.turnMu.Lock()
	if f.cur == t {
		f.cur = nil
	}
	f.turnMu.Unlock()
}

// ------------------------------------------------------------------- items

// userMessage echoes the turn's input back, which is what a real turn opens
// with. 2.5.2's catch-all row would otherwise turn it into a tool card.
func (f *fake) userMessage(t *turnState) {
	id := f.item("usr")
	item := map[string]any{
		"type": "userMessage", "id": id,
		"content": []any{map[string]any{"type": "text", "text": t.input}},
	}
	f.itemStarted(t, item)
	f.itemCompleted(t, item)
}

func (f *fake) agentMessage(t *turnState, text, phase string) {
	id := f.item("msg")
	f.itemStarted(t, map[string]any{
		"type": "agentMessage", "id": id, "text": "", "phase": phase,
	})
	for _, c := range script.Chunks(text, 3) {
		f.emitTurn(t, notification("item/agentMessage/delta", map[string]any{
			"threadId": f.thread(), "turnId": t.id, "itemId": id, "delta": c,
		}))
	}
	f.itemCompleted(t, map[string]any{
		"type": "agentMessage", "id": id, "text": text, "phase": phase,
	})
}

func (f *fake) reasoning(t *turnState, text string) {
	id := f.item("rs")
	f.itemStarted(t, map[string]any{
		"type": "reasoning", "id": id, "summary": []any{}, "content": []any{},
	})
	f.itemCompleted(t, map[string]any{
		"type": "reasoning", "id": id,
		"summary": []any{map[string]any{"type": "summary_text", "text": text}},
		"content": []any{},
	})
}

// command is FK-12: the full commandExecution item, output only as deltas,
// and aggregatedOutput null on both ends.
func (f *fake) command(t *turnState, cmd, output string, exit int) {
	id := f.item("call")
	cwd, _ := os.Getwd()
	f.itemStarted(t, map[string]any{
		"type":             "commandExecution",
		"id":               id,
		"command":          cmd,
		"cwd":              cwd,
		"processId":        "19421",
		"source":           "unifiedExecStartup",
		"status":           "inProgress",
		"aggregatedOutput": nil,
		"exitCode":         nil,
		"durationMs":       nil,
	})
	for _, line := range splitLines(output) {
		f.emitTurn(t, notification("item/commandExecution/outputDelta", map[string]any{
			"threadId": f.thread(), "turnId": t.id, "itemId": id, "delta": line,
		}))
	}
	f.itemCompleted(t, map[string]any{
		"type":             "commandExecution",
		"id":               id,
		"command":          cmd,
		"cwd":              cwd,
		"processId":        "19421",
		"status":           "completed",
		"aggregatedOutput": nil,
		"exitCode":         exit,
		"durationMs":       12,
	})
}

// subagent emits a SubAgentActivityThreadItem. The schema's required fields
// are agentPath, agentThreadId, id, kind and type, and it carries nothing
// else — no source and no text.
func (f *fake) subagent(t *turnState, input string) {
	id := f.item("sub")
	thread := fmt.Sprintf("01a05d4f-0000-7101-b75a-%012d", f.itemN.Add(1))
	f.itemStarted(t, map[string]any{
		"type": "subAgentActivity", "id": id, "kind": "started",
		"agentPath": input, "agentThreadId": thread,
	})
	f.itemCompleted(t, map[string]any{
		"type": "subAgentActivity", "id": id, "kind": "completed",
		"agentPath": input, "agentThreadId": thread,
	})
}

// ask is FK-14: a ServerRequest the client must answer before the script
// continues. Either a JSON-RPC result or a JSON-RPC error is accepted.
func (f *fake) ask(t *turnState, cmd string) {
	id := f.reqN.Add(1)
	key := fmt.Sprintf("%d", id)
	ch := make(chan struct{})
	f.mu.Lock()
	f.pending[key] = ch
	f.mu.Unlock()

	cwd, _ := os.Getwd()
	f.emitTurn(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "item/commandExecution/requestApproval",
		"params": map[string]any{
			"kind":               "command",
			"threadId":           f.thread(),
			"turnId":             t.id,
			"itemId":             f.item("call"),
			"command":            cmd,
			"cwd":                cwd,
			"availableDecisions": []any{"accept", "cancel"},
		},
	})
	select {
	case <-ch:
	case <-t.ctx.Done():
	}
}

func (f *fake) answer(id string) {
	f.mu.Lock()
	ch, ok := f.pending[strings.TrimSpace(id)]
	delete(f.pending, strings.TrimSpace(id))
	f.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (f *fake) itemStarted(t *turnState, item map[string]any) {
	f.emitTurn(t, notification("item/started", map[string]any{
		"item": item, "threadId": f.thread(), "turnId": t.id,
	}))
}

func (f *fake) itemCompleted(t *turnState, item map[string]any) {
	f.emitTurn(t, notification("item/completed", map[string]any{
		"item": item, "threadId": f.thread(), "turnId": t.id,
	}))
}

func (f *fake) tokenUsage(t *turnState) {
	usage := map[string]any{
		"totalTokens": 11341, "inputTokens": 11143, "cachedInputTokens": 4352,
		"cacheWriteInputTokens": 0, "outputTokens": 198, "reasoningOutputTokens": 21,
	}
	f.emitTurn(t, notification("thread/tokenUsage/updated", map[string]any{
		"threadId": f.thread(), "turnId": t.id,
		"tokenUsage": map[string]any{
			"total": usage, "last": usage, "modelContextWindow": 258400,
		},
	}))
}

func (f *fake) rateLimits() {
	f.notify("account/rateLimits/updated", map[string]any{
		"rateLimits": map[string]any{
			"limitId": "codex",
			"primary": map[string]any{"usedPercent": 0, "windowDurationMins": 300},
		},
	})
}

// ------------------------------------------------------------------- wire

func modelList() []any {
	efforts := []any{
		map[string]any{"reasoningEffort": "low"},
		map[string]any{"reasoningEffort": "medium"},
		map[string]any{"reasoningEffort": "high"},
		map[string]any{"reasoningEffort": "xhigh"},
	}
	return []any{
		map[string]any{
			"id": "gpt-5.4-mini", "displayName": "GPT-5.4-Mini",
			"description":               "Fake model.",
			"defaultReasoningEffort":    "medium",
			"supportedReasoningEfforts": efforts,
			"isDefault":                 true,
			"hidden":                    false,
		},
		map[string]any{
			"id": "gpt-5.4", "displayName": "GPT-5.4",
			"description":               "Fake model.",
			"defaultReasoningEffort":    "medium",
			"supportedReasoningEfforts": efforts,
			"isDefault":                 false,
			"hidden":                    false,
		},
	}
}

func threadObject(id, cwd string, turns []any) map[string]any {
	th := map[string]any{
		"id":         id,
		"sessionId":  id,
		"path":       "/tmp/fake-codex/sessions/rollout-" + id + ".jsonl",
		"cwd":        cwd,
		"cliVersion": "0.152.0-fake",
		"status":     map[string]any{"type": "idle"},
		"turns":      []any{},
	}
	if turns != nil {
		th["turns"] = turns
	}
	return th
}

func effortFromConfig(cfg json.RawMessage) string {
	var m struct {
		Effort string `json:"model_reasoning_effort"`
	}
	if len(cfg) > 0 {
		_ = json.Unmarshal(cfg, &m)
	}
	return firstNonEmpty(m.Effort, "medium")
}

func (f *fake) thread() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.threadID
}

func (f *fake) item(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, f.itemN.Add(1))
}

func (f *fake) reply(id json.RawMessage, result any) {
	f.emit(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (f *fake) replyError(id json.RawMessage, code int, msg string) {
	f.emit(map[string]any{
		"jsonrpc": "2.0", "id": json.RawMessage(id),
		"error": map[string]any{"code": code, "message": msg},
	})
}

func (f *fake) notify(method string, params any) {
	f.emit(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
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

// emitTurn writes a frame that belongs to a turn. The turn is checked under
// the write lock, so nothing can be written once finish has decided on the
// terminal frames.
func (f *fake) emitTurn(t *turnState, v any) {
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

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
