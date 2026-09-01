package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// read is the one goroutine that consumes the app-server's stdout. Every
// event this adapter emits for a turn is produced here, in arrival order,
// which is why the per-turn accumulators need no locking.
func (a *adapter) read(stdout io.Reader) {
	sc := scanLines(stdout)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var fr frame
		if err := json.Unmarshal(line, &fr); err != nil {
			// Not JSON at all: the app-server does not do this, and guessing
			// at it would be worse than ignoring it.
			continue
		}
		switch {
		case fr.Method != "" && len(fr.ID) > 0:
			a.serverRequest(fr)
		case fr.Method != "":
			a.notification(fr)
		case len(fr.ID) > 0:
			a.rpc.deliver(&fr)
		}
	}
	if err := sc.Err(); err != nil {
		// The scan stopped on a frame this adapter cannot hold (or a broken
		// pipe read). The process is very likely alive and blocked writing
		// the rest of that frame, so waiting on it would hang until the
		// silence watchdog fired an hour later: kill it and say why (F6).
		a.mu.Lock()
		a.scanErr = err
		a.mu.Unlock()
		a.kill()
	}
	a.eof()
}

// eof is the death of the process, however it came about.
func (a *adapter) eof() {
	err := a.cmd.Wait()
	close(a.exited)
	a.rpc.shutdown(errClosed)
	a.markDead()

	a.mu.Lock()
	closing, started, scanErr := a.closing, a.started, a.scanErr
	a.mu.Unlock()
	if closing {
		// A shutdown we asked for. Close is waiting on a.exited and closes
		// Events itself.
		return
	}
	if !started {
		// The handshake never got through, so Start is about to return the
		// whole story. A fatal here would only be a second, vaguer copy of
		// it in the journal (F4).
		return
	}

	what := "codex exited"
	if err != nil {
		what = "codex exited: " + err.Error()
	}
	if scanErr != nil {
		what = "codex sent a frame this adapter cannot read: " + scanErr.Error()
	}
	if tail := a.stderr.String(); tail != "" {
		what += ": " + lastLine(tail)
	}
	if t := a.current(); t != nil {
		a.closeTurn(t, harness.OutcomeError, what+" before the turn finished")
	}
	a.emit(harness.Event{Kind: harness.KindFatal, Error: what})
	// Invariant 6: after a fatal, Events is closed and nothing else arrives.
	a.finish()
}

// serverRequest answers anything the server asks of us with a JSON-RPC error
// (F-8). With approvalPolicy "never" this never fires; when it does, an
// unanswered request would hang the turn forever, and an error response is
// valid for every request method whereas a decision object is only the shape
// one of them expects. No approval UI is involved: Socrates runs unattended.
func (a *adapter) serverRequest(fr frame) {
	t := a.current()
	if t != nil {
		t.touch()
	}
	_ = a.rpc.respondError(fr.ID, -32601, "socrates runs unattended")
	a.notice(t, "codex asked for a decision ("+fr.Method+"); socrates runs unattended and declined it")
}

// notification is the mapping table of §2.5.2.
func (a *adapter) notification(fr frame) {
	t := a.current()
	if t != nil {
		t.touch()
	}
	switch fr.Method {
	case "turn/started":
		if t == nil {
			return
		}
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		t.setNative(p.Turn.ID)
		a.startTurn(t)

	case "item/started":
		a.itemStarted(t, fr.Params)
	case "item/completed":
		a.itemCompleted(t, fr.Params)

	case "item/agentMessage/delta":
		if t == nil {
			return
		}
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		a.textDelta(t, p.ItemID, p.Delta)

	case "item/commandExecution/outputDelta":
		if t == nil {
			return
		}
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		if p.Delta == "" {
			return
		}
		buf, ok := t.output[p.ItemID]
		if !ok {
			buf = &strings.Builder{}
			t.output[p.ItemID] = buf
		}
		if buf.Len() >= harness.ToolOutputLimit {
			// The card cannot show more than this and the journal should not
			// carry more, so a chatty build stops here.
			return
		}
		buf.WriteString(p.Delta)
		a.emit(harness.Event{Kind: harness.KindToolOutput, TurnID: t.id, ID: p.ItemID,
			Tool: &harness.Tool{Name: "shell", Output: p.Delta}})

	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		// Reasoning never streams as text_delta: it is buffered and emitted
		// as one reasoning event when the item completes.
		if t == nil {
			return
		}
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		buf, ok := t.reason[p.ItemID]
		if !ok {
			buf = &strings.Builder{}
			t.reason[p.ItemID] = buf
		}
		buf.WriteString(p.Delta)

	case "thread/tokenUsage/updated":
		if t == nil {
			return
		}
		var p struct {
			TokenUsage struct {
				Total struct {
					TotalTokens           int64 `json:"totalTokens"`
					InputTokens           int64 `json:"inputTokens"`
					CachedInputTokens     int64 `json:"cachedInputTokens"`
					OutputTokens          int64 `json:"outputTokens"`
					ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
				} `json:"total"`
				ModelContextWindow int64 `json:"modelContextWindow"`
			} `json:"tokenUsage"`
		}
		if err := json.Unmarshal(fr.Params, &p); err != nil {
			return
		}
		total := usage{
			Total: p.TokenUsage.Total.TotalTokens, Input: p.TokenUsage.Total.InputTokens,
			Cached: p.TokenUsage.Total.CachedInputTokens, Output: p.TokenUsage.Total.OutputTokens,
			Reasoning: p.TokenUsage.Total.ReasoningOutputTokens,
		}
		// tokenUsage.total counts the whole thread, not this turn, so turn two
		// would otherwise open by reporting turn one's tokens. What is emitted
		// is what this turn added on top of the thread total it started from -
		// still a running total of the turn, as invariant 5 requires (F3).
		a.mu.Lock()
		a.lastTotal = total
		a.mu.Unlock()
		u := total.since(t.base)
		if u.empty() {
			return
		}
		a.emit(harness.Event{Kind: harness.KindUsage, TurnID: t.id, Usage: &harness.Usage{
			Input:     u.Input,
			Output:    u.Output,
			Cached:    u.Cached,
			Reasoning: u.Reasoning,
			Total:     u.Total,
			// The context window is a property of the model, not of the turn.
			Context: p.TokenUsage.ModelContextWindow,
		}})

	case "turn/completed":
		if t == nil {
			return
		}
		var p struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		if native := t.nativeID(); native != "" && p.Turn.ID != "" && p.Turn.ID != native {
			return
		}
		switch p.Turn.Status {
		case "completed":
			a.closeTurn(t, harness.OutcomeOK, "")
		case "interrupted":
			a.closeTurn(t, harness.OutcomeInterrupted, "")
		default:
			msg := ""
			if p.Turn.Error != nil {
				msg = p.Turn.Error.Message
			}
			if msg == "" {
				t.mu.Lock()
				msg = t.lastErr
				t.mu.Unlock()
			}
			if msg == "" {
				msg = "the turn ended with status " + p.Turn.Status
			}
			a.closeTurn(t, harness.OutcomeError, msg)
		}

	case "thread/status/changed":
		var p struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		if p.Status.Type != "systemError" || t == nil {
			return
		}
		t.mu.Lock()
		if t.lastErr == "" {
			t.lastErr = "codex reported a system error"
		}
		t.mu.Unlock()
		a.armErrorGrace(t)

	case "error":
		var p struct {
			WillRetry bool `json:"willRetry"`
			Error     struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		if t == nil {
			// Nothing to end, but a silent error is worse than a dim line.
			if p.Error.Message != "" {
				a.notice(nil, p.Error.Message)
			}
			return
		}
		if p.Error.Message != "" {
			t.mu.Lock()
			t.lastErr = p.Error.Message
			t.mu.Unlock()
		}
		if p.WillRetry {
			// The agent is about to try again: remember it, do not end the
			// turn.
			return
		}
		a.armErrorGrace(t)

	case "warning":
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(fr.Params, &p)
		if p.Message != "" {
			a.notice(t, p.Message)
		}

	default:
		// thread/started (the RPC result already carried the id),
		// turn/diff/updated (the fileChange items already carry it),
		// account/rateLimits/updated, mcpServer/startupStatus/updated,
		// remoteControl/status/changed, deprecationNotice, configWarning and
		// everything a later release adds: dropped, never guessed at.
	}
}

// ------------------------------------------------------------------- items

// item is the part of a ThreadItem this adapter reads. Fields the schema does
// not define for a type are simply absent from that type's JSON.
type item struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	// agentMessage
	Text  string `json:"text"`
	Phase string `json:"phase"`

	// reasoning: both arrays hold either bare strings (ThreadItem) or objects
	// with a text field (summary_text / reasoning_text), so both are decoded
	// permissively.
	Summary []json.RawMessage `json:"summary"`
	Content []json.RawMessage `json:"content"`

	// commandExecution
	Command          string  `json:"command"`
	Status           string  `json:"status"`
	ExitCode         *int    `json:"exitCode"`
	AggregatedOutput *string `json:"aggregatedOutput"`

	// fileChange
	Changes []struct {
		Path string `json:"path"`
		Diff string `json:"diff"`
	} `json:"changes"`

	// subAgentActivity
	Kind          string `json:"kind"`
	AgentPath     string `json:"agentPath"`
	AgentThreadID string `json:"agentThreadId"`

	// collabAgentToolCall
	Tool   string `json:"tool"`
	Prompt string `json:"prompt"`
}

// itemPayload is the envelope of item/started and item/completed.
type itemPayload struct {
	Item json.RawMessage `json:"item"`
}

func decodeItem(params json.RawMessage) (item, json.RawMessage, bool) {
	var env itemPayload
	if err := json.Unmarshal(params, &env); err != nil || len(env.Item) == 0 {
		return item{}, nil, false
	}
	var it item
	if err := json.Unmarshal(env.Item, &it); err != nil {
		return item{}, nil, false
	}
	if it.Type == "" || it.ID == "" {
		return item{}, nil, false
	}
	return it, env.Item, true
}

func (a *adapter) itemStarted(t *turn, params json.RawMessage) {
	if t == nil {
		return
	}
	it, raw, ok := decodeItem(params)
	if !ok {
		return
	}
	switch it.Type {
	case "userMessage":
		// E5: every real turn opens with the input that was just sent coming
		// back as an item. It is the harness's own text and must never become
		// a card.
		return
	case "agentMessage", "reasoning":
		// Nothing yet: wait for the content.
		return
	case "commandExecution":
		a.emit(harness.Event{Kind: harness.KindToolStarted, TurnID: t.id, ID: it.ID,
			Tool: &harness.Tool{Name: "shell", Title: "Ran a command",
				Input: it.Command, InputJSON: string(raw)}})
	case "fileChange":
		a.emit(harness.Event{Kind: harness.KindToolStarted, TurnID: t.id, ID: it.ID,
			Tool: &harness.Tool{Name: "edit", Title: a.fileTitle(it), Input: a.filePaths(it),
				InputJSON: string(raw)}})
	case "subAgentActivity":
		a.subAgent(t, it, raw)
	case "collabAgentToolCall":
		a.collab(t, it, raw)
	default:
		a.emit(harness.Event{Kind: harness.KindToolStarted, TurnID: t.id, ID: it.ID,
			Tool: &harness.Tool{Name: it.Type, Title: humanise(it.Type), InputJSON: string(raw)}})
	}
}

func (a *adapter) itemCompleted(t *turn, params json.RawMessage) {
	if t == nil {
		return
	}
	it, raw, ok := decodeItem(params)
	if !ok {
		return
	}
	switch it.Type {
	case "userMessage":
		return

	case "agentMessage":
		// Both phases - commentary and final_answer - are real assistant
		// text. Codex item ids are unique within a turn, so they are the
		// block ids verbatim (F-12).
		text := it.Text
		if b, ok := t.texts[it.ID]; ok {
			a.flushText(t, it.ID, b)
			if text == "" {
				text = b.full.String()
			}
			delete(t.texts, it.ID)
		}
		if text == "" {
			return
		}
		a.emit(harness.Event{Kind: harness.KindText, TurnID: t.id, ID: it.ID, Text: text})

	case "reasoning":
		text := joinTexts(it.Summary)
		if body := joinTexts(it.Content); body != "" {
			if text != "" {
				text += "\n\n"
			}
			text += body
		}
		if buf, ok := t.reason[it.ID]; ok {
			if text == "" {
				text = buf.String()
			}
			delete(t.reason, it.ID)
		}
		if strings.TrimSpace(text) == "" {
			return
		}
		a.emit(harness.Event{Kind: harness.KindReasoning, TurnID: t.id, ID: it.ID, Text: text})

	case "commandExecution":
		// FK-12: aggregatedOutput is null on a real short command, so the
		// accumulated deltas are the reliable source.
		out := ""
		if it.AggregatedOutput != nil {
			out = *it.AggregatedOutput
		}
		if buf, ok := t.output[it.ID]; ok {
			if out == "" {
				out = buf.String()
			}
			delete(t.output, it.ID)
		}
		exit := 0
		if it.ExitCode != nil {
			exit = *it.ExitCode
		}
		a.emit(harness.Event{Kind: harness.KindToolFinished, TurnID: t.id, ID: it.ID,
			Tool: &harness.Tool{Name: "shell", Title: "Ran a command", Input: it.Command,
				Output: harness.TruncateOutput(out), OK: exit == 0 && statusOK(it.Status),
				ExitCode: exit}})

	case "fileChange":
		var b strings.Builder
		for _, c := range it.Changes {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(c.Diff)
		}
		a.emit(harness.Event{Kind: harness.KindToolFinished, TurnID: t.id, ID: it.ID,
			Tool: &harness.Tool{Name: "edit", Title: a.fileTitle(it), Input: a.filePaths(it),
				Output: harness.TruncateOutput(b.String()), OK: statusOK(it.Status)}})

	case "subAgentActivity":
		a.subAgent(t, it, raw)
	case "collabAgentToolCall":
		a.collab(t, it, raw)

	default:
		a.emit(harness.Event{Kind: harness.KindToolFinished, TurnID: t.id, ID: it.ID,
			Tool: &harness.Tool{Name: it.Type, Title: humanise(it.Type),
				InputJSON: string(raw), OK: statusOK(it.Status)}})
	}
}

// subAgent maps a SubAgentActivityThreadItem. Its only fields besides the id
// and the kind are agentPath and agentThreadId, both optional, so nothing
// else is read off it.
func (a *adapter) subAgent(t *turn, it item, raw json.RawMessage) {
	title := "Subagent"
	if it.AgentPath != "" {
		title += " " + filepath.Base(it.AgentPath)
	}
	tool := &harness.Tool{Name: "subagent", Title: title, Input: it.AgentThreadID,
		InputJSON: string(raw)}
	switch it.Kind {
	case "started":
		a.emit(harness.Event{Kind: harness.KindSubagentStarted, TurnID: t.id, ID: it.ID, Tool: tool})
	case "completed", "interrupted":
		tool.OK = it.Kind == "completed"
		a.emit(harness.Event{Kind: harness.KindSubagentFinished, TurnID: t.id, ID: it.ID, Tool: tool})
	default:
		// "interacted" is neither a start nor an end.
	}
}

// collab maps a CollabAgentToolCallThreadItem by its status.
func (a *adapter) collab(t *turn, it item, raw json.RawMessage) {
	name := it.Tool
	if name == "" {
		name = "collabAgentToolCall"
	}
	tool := &harness.Tool{Name: name, Title: "Subagent " + humanise(name), Input: it.Prompt,
		InputJSON: string(raw)}
	if it.Status == "inProgress" {
		a.emit(harness.Event{Kind: harness.KindSubagentStarted, TurnID: t.id, ID: it.ID, Tool: tool})
		return
	}
	tool.OK = statusOK(it.Status)
	a.emit(harness.Event{Kind: harness.KindSubagentFinished, TurnID: t.id, ID: it.ID, Tool: tool})
}

// ------------------------------------------------------------------- text

// textBlock accumulates one agentMessage item's deltas.
type textBlock struct {
	full    strings.Builder
	pending strings.Builder
	last    time.Time
	started bool
}

// textDelta streams one agentMessage delta. The first delta of a block goes
// out at once so the draft starts filling immediately; the rest are coalesced
// to one event per TextDeltaFlush, and whatever is left is flushed when the
// item completes.
func (a *adapter) textDelta(t *turn, itemID, delta string) {
	if itemID == "" || delta == "" {
		return
	}
	b, ok := t.texts[itemID]
	if !ok {
		b = &textBlock{}
		t.texts[itemID] = b
	}
	b.full.WriteString(delta)
	b.pending.WriteString(delta)
	if !b.started || time.Since(b.last) >= harness.TextDeltaFlush {
		a.flushText(t, itemID, b)
	}
}

func (a *adapter) flushText(t *turn, itemID string, b *textBlock) {
	if b.pending.Len() == 0 {
		return
	}
	text := b.pending.String()
	b.pending.Reset()
	b.started = true
	b.last = time.Now()
	a.emit(harness.Event{Kind: harness.KindTextDelta, TurnID: t.id, ID: itemID, Text: text})
}

// ------------------------------------------------------------------ helpers

// joinTexts reads a reasoning summary or content array. Its elements are bare
// strings in the ThreadItem schema and objects carrying a text field in the
// response-item form, and both have been observed, so both are accepted.
func joinTexts(parts []json.RawMessage) string {
	var out []string
	for _, p := range parts {
		if s := textOf(p); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}

func textOf(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
		return ""
	}
	var obj struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &obj); err == nil {
		return obj.Text
	}
	return ""
}

// statusOK reads the item status enums, which all share the same three bad
// values.
func statusOK(status string) bool {
	switch status {
	case "failed", "declined":
		return false
	}
	return true
}

// fileTitle names a fileChange card for a human.
func (a *adapter) fileTitle(it item) string {
	if len(it.Changes) == 0 {
		return "Edited files"
	}
	title := "Edited " + a.shortPath(it.Changes[0].Path)
	if n := len(it.Changes) - 1; n > 0 {
		title += fmt.Sprintf(" and %d more", n)
	}
	return title
}

func (a *adapter) filePaths(it item) string {
	paths := make([]string, 0, len(it.Changes))
	for _, c := range it.Changes {
		paths = append(paths, a.shortPath(c.Path))
	}
	return strings.Join(paths, ", ")
}

// shortPath makes a path relative to the chat's working directory, which is
// what a person reading the card has in mind.
func (a *adapter) shortPath(path string) string {
	if a.spec.Cwd == "" || !filepath.IsAbs(path) {
		return path
	}
	rel, err := filepath.Rel(a.spec.Cwd, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

// humanise turns an item type or tool name into a card title:
// "mcpToolCall" -> "Mcp tool call".
func humanise(s string) string {
	if s == "" {
		return "Ran a tool"
	}
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteRune(' ')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		if i == 0 {
			b.WriteRune(unicode.ToUpper(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// lastLine keeps the most recent line of a captured stderr, which is where a
// dying process says why.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
