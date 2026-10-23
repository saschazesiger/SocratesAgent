//go:build !windows

package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// turnKind is what a turn can take while it runs.
type turnKind int

const (
	// turnNormal takes steered messages.
	turnNormal turnKind = iota
	// turnShell is a "!" command; turnOther a compaction or a review. Neither
	// can be steered, so messages wait for them to end.
	turnShell
	turnOther
)

// turn is one journal turn: a root turn of the chat's thread (or, after a
// recovery, the root turns that retried it), with its assistant message and
// the parts its items became.
type turn struct {
	codexID string
	id      string
	msgID   string
	kind    turnKind
	// input is every message the turn was given, for a recovery to send again.
	input []agent.Outgoing
	parts map[string]*part
	order []*part
	n     int
	usage agent.Usage
	err   *turnError
	ended bool
	// grace ends a turn whose turn/completed does not come.
	grace *time.Timer
	diff  diffState
}

type diffState struct {
	id      string
	text    string
	last    time.Time
	pending *time.Timer
}

// part is one item as a part, with the text its deltas built.
type part struct {
	id     string
	kind   string
	parent string
	// agent is the subagent thread the part came from, empty for the root.
	agent   string
	text    strings.Builder
	content json.RawMessage
	done    bool
}

// subagent is a thread a collaboration tool spawned: one group part in the
// turn that spawned it, which its own items nest under.
type subagent struct {
	thread string
	path   string
	model  string
	effort string
	turn   *turn
	group  string
	parent string
	busy   bool
	status string
}

// turnError is a turn's failure as codex reported it.
type turnError struct {
	Message        string          `json:"message"`
	CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
}

func (e *turnError) code() string {
	if e == nil {
		return ""
	}
	var s string
	if json.Unmarshal(e.CodexErrorInfo, &s) == nil {
		return s
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(e.CodexErrorInfo, &m) == nil {
		for k := range m {
			return k
		}
	}
	return ""
}

// encryptedContent is the refusal of replayed reasoning from another account
// (openai/codex #25290, #36704).
func (e *turnError) encryptedContent() bool {
	return e != nil && strings.Contains(e.Message, "invalid_encrypted_content")
}

// text is the sentence to show: codex wraps the model's JSON error body in
// the message, and the body's own message is the readable part.
func (e *turnError) text() string {
	if e == nil {
		return ""
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(e.Message), &body) == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return e.Message
}

// --------------------------------------------------------------- turns

// openTurnLocked is the journal turn for a root codex turn, opened with its
// events the first time it is seen. A turn a recovery started continues the
// journal turn it retries.
func (a *adapter) openTurnLocked(codexID, userMsg string, kind turnKind) *turn {
	if t := a.turns[codexID]; t != nil {
		return t
	}
	if a.retry != nil {
		t := a.retry
		a.retry = nil
		t.codexID, t.ended, t.err = codexID, false, nil
		a.turns[codexID] = t
		a.active = t
		return t
	}
	t := &turn{codexID: codexID, id: newID("t_"), kind: kind, parts: map[string]*part{}}
	t.msgID = t.id + "_a"
	a.turns[codexID] = t
	a.active = t
	model := a.model
	if model == "" {
		model = a.threadModel
	}
	var events []agent.Event
	if userMsg != "" {
		events = append(events, agent.Event{Type: agent.TypeMessageState, MessageID: userMsg, State: agent.MessageDelivered})
	}
	events = append(events,
		agent.Event{Type: agent.TypeTurnStarted, TurnID: t.id, MessageID: userMsg},
		agent.Event{Type: agent.TypeMessage, MessageID: t.msgID, Role: agent.RoleAssistant, State: agent.MessageStreaming,
			TurnID: t.id, Model: model})
	a.out.Emit(events...)
	return t
}

// closeTurnLocked ends a journal turn: every part still open is closed in the
// state the outcome gives it, and the turn's usage goes with its end.
func (a *adapter) closeTurnLocked(t *turn, outcome, why string) {
	if t.ended {
		return
	}
	t.ended = true
	if t.grace != nil {
		t.grace.Stop()
	}
	if t.diff.pending != nil {
		t.diff.pending.Stop()
		t.diff.pending = nil
	}
	if a.active == t {
		a.active = nil
	}
	state, msgState := agent.PartDone, agent.MessageDone
	if outcome != agent.OutcomeCompleted {
		state, msgState = agent.PartInterrupted, agent.MessageInterrupted
	}
	var events []agent.Event
	if t.diff.id != "" {
		events = append(events, a.diffEvent(t, agent.PartDone))
	}
	for _, p := range t.order {
		if p.done || a.agentRunningLocked(p.agent) {
			continue
		}
		p.done = true
		events = append(events, partEvent(t.msgID, p, state))
	}
	for _, c := range a.calls {
		if c.turn == t {
			c.cancel()
		}
	}
	usage := t.usage
	usage.Context, usage.Window = a.context, a.window
	if outcome == agent.OutcomeLimited {
		events = append(events, agent.Event{Type: agent.TypeError, Code: "rate_limit", Text: why})
	}
	events = append(events,
		agent.Event{Type: agent.TypeMessageState, MessageID: t.msgID, State: msgState},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: t.id, Outcome: outcome, Usage: &usage, Text: why})
	a.out.Emit(events...)
	if len(a.queue) > 0 {
		go a.flush()
	}
}

// newPart adds a part to the turn under key.
func (t *turn) newPart(key, kind, parent string) *part {
	t.n++
	p := &part{id: fmt.Sprintf("%s_p%d", t.msgID, t.n), kind: kind, parent: parent}
	t.parts[key] = p
	t.order = append(t.order, p)
	return p
}

// ------------------------------------------------------------- dispatch

// handle is every frame that is not an answer to one of ours, on the reading
// goroutine, in order.
func (a *adapter) handle(fr *frame) {
	if len(fr.ID) > 0 {
		a.serverRequest(fr)
		return
	}
	var p notification
	_ = json.Unmarshal(fr.Params, &p)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch fr.Method {
	case "turn/started":
		a.turnStarted(p)
	case "turn/completed":
		a.turnCompleted(p)
	case "item/started", "item/completed":
		a.item(p, fr.Method == "item/completed")
	case "item/agentMessage/delta", "item/plan/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta",
		"item/commandExecution/outputDelta", "item/fileChange/outputDelta":
		a.delta(p, p.Delta)
	case "item/reasoning/summaryPartAdded":
		if p.SummaryIndex > 0 {
			a.delta(p, "\n\n")
		}
	case "turn/diff/updated":
		a.turnDiff(p)
	case "turn/plan/updated":
		a.turnPlan(p)
	case "thread/tokenUsage/updated":
		a.tokenUsage(p)
	case "account/rateLimits/updated":
		a.rateLimits(fr.Params)
	case "error":
		a.turnErrorNote(p)
	case "warning", "configWarning":
		if p.Message != "" {
			a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: "warning", Text: p.Message})
		}
	case "model/rerouted":
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: "model_rerouted",
			Text: fmt.Sprintf("Codex moved this turn from %s to %s.", p.FromModel, p.ToModel)})
	case "serverRequest/resolved":
		if c := a.calls[string(p.RequestID)]; c != nil {
			c.cancel()
		}
	}
}

// notification is the union of the notification params the adapter reads.
type notification struct {
	ThreadID     string          `json:"threadId"`
	TurnID       string          `json:"turnId"`
	ItemID       string          `json:"itemId"`
	Delta        string          `json:"delta"`
	SummaryIndex int             `json:"summaryIndex"`
	Item         json.RawMessage `json:"item"`
	Turn         struct {
		ID         string     `json:"id"`
		RootTurnID string     `json:"rootTurnId"`
		Status     string     `json:"status"`
		Error      *turnError `json:"error"`
	} `json:"turn"`
	Diff        string          `json:"diff"`
	Explanation *string         `json:"explanation"`
	Plan        json.RawMessage `json:"plan"`
	TokenUsage  struct {
		Last               tokens `json:"last"`
		ModelContextWindow int64  `json:"modelContextWindow"`
	} `json:"tokenUsage"`
	Error     *turnError      `json:"error"`
	WillRetry bool            `json:"willRetry"`
	Message   string          `json:"message"`
	FromModel string          `json:"fromModel"`
	ToModel   string          `json:"toModel"`
	RequestID json.RawMessage `json:"requestId"`
}

type tokens struct {
	TotalTokens           int64 `json:"totalTokens"`
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
}

func (a *adapter) turnStarted(p notification) {
	if p.ThreadID != a.thread {
		if ag := a.agents[p.ThreadID]; ag != nil {
			a.agentBusy(ag, true)
		}
		return
	}
	userMsg, kind := "", a.expect
	if a.starting != nil {
		userMsg = a.starting.MessageID
		kind = turnNormal
		if isShellLine(a.starting.Text) {
			kind = turnShell
		}
	}
	a.expect = turnNormal
	t := a.openTurnLocked(p.Turn.ID, userMsg, kind)
	if a.starting != nil && len(t.input) == 0 {
		t.input = append(t.input, *a.starting)
	}
}

func (a *adapter) turnCompleted(p notification) {
	if p.ThreadID != a.thread {
		if ag := a.agents[p.ThreadID]; ag != nil {
			a.agentBusy(ag, false)
		}
		return
	}
	t := a.turns[p.Turn.ID]
	if t == nil || t.ended {
		return
	}
	terr := p.Turn.Error
	if terr == nil {
		terr = t.err
	}
	switch p.Turn.Status {
	case "completed":
		a.closeTurnLocked(t, agent.OutcomeCompleted, "")
	case "interrupted":
		a.closeTurnLocked(t, agent.OutcomeInterrupted, "")
	default:
		if terr.encryptedContent() && a.recovered < 2 && len(t.input) > 0 {
			a.beginRecoveryLocked(t, terr)
			return
		}
		outcome := agent.OutcomeFailed
		if c := terr.code(); c == "usageLimitExceeded" {
			outcome = agent.OutcomeLimited
		}
		why := terr.text()
		if why == "" {
			why = "The turn failed."
		}
		a.closeTurnLocked(t, outcome, why)
	}
}

// turnErrorNote is codex's `error` notification: a retry is said in a line of
// its own; a final failure is kept for the turn's end, which should follow
// at once and is waited for only so long.
func (a *adapter) turnErrorNote(p notification) {
	if p.Error == nil {
		return
	}
	if p.WillRetry {
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: "retrying", Text: "Codex is retrying: " + p.Error.text()})
		return
	}
	t := a.turns[p.TurnID]
	if t == nil || t.ended || p.ThreadID != a.thread {
		return
	}
	t.err = p.Error
	if t.grace != nil {
		t.grace.Stop()
	}
	codexID := t.codexID
	t.grace = time.AfterFunc(errorGrace, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if t.ended || t.codexID != codexID {
			return
		}
		a.closeTurnLocked(t, agent.OutcomeFailed, t.err.text())
	})
}

// turnFor is the journal turn a thread's event belongs to: the root turn for
// the chat's thread, the spawning turn for a subagent's.
func (a *adapter) turnFor(threadID, turnID string) (*turn, *subagent) {
	if threadID == a.thread || threadID == "" {
		if t := a.turns[turnID]; t != nil {
			return t, nil
		}
		return a.active, nil
	}
	ag := a.agents[threadID]
	if ag == nil {
		// A thread nothing announced: it can only be a subagent of the
		// running turn.
		if a.active == nil {
			return nil, nil
		}
		ag = a.newAgentLocked(threadID, "", "", "", a.active, "")
	}
	return ag.turn, ag
}

// ---------------------------------------------------------------- parts

// partFor is the part an item of a thread is, created on first sight.
func (a *adapter) partFor(t *turn, ag *subagent, threadID, itemID, kind string) *part {
	key := threadID + "/" + itemID
	if p := t.parts[key]; p != nil {
		return p
	}
	if ag == nil {
		return t.newPart(key, kind, "")
	}
	p := t.newPart(key, kind, ag.group)
	p.agent = ag.thread
	return p
}

func (a *adapter) emitPart(t *turn, p *part, state string) {
	p.done = state != agent.PartStreaming && state != agent.PartRunning
	a.out.Emit(partEvent(t.msgID, p, state))
}

func partEvent(msgID string, p *part, state string) agent.Event {
	return agent.Event{Type: agent.TypePart, MessageID: msgID, PartID: p.id, ParentPartID: p.parent, Kind: p.kind,
		State: state, Text: p.text.String(), Content: p.content}
}

func (a *adapter) delta(p notification, text string) {
	if text == "" {
		return
	}
	t, _ := a.turnFor(p.ThreadID, p.TurnID)
	if t == nil {
		return
	}
	pt := t.parts[p.ThreadID+"/"+p.ItemID]
	if pt == nil || pt.done {
		return
	}
	pt.text.WriteString(text)
	a.out.Delta(t.msgID, pt.id, text)
}

// item maps item/started and item/completed.
func (a *adapter) item(p notification, completed bool) {
	var it item
	if json.Unmarshal(p.Item, &it) != nil || it.Type == "" {
		return
	}
	if p.ThreadID == a.thread && it.Type == "userMessage" {
		if _, ok := a.steered[it.ClientID]; ok {
			delete(a.steered, it.ClientID)
			a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: it.ClientID, State: agent.MessageDelivered})
		}
		return
	}
	t, ag := a.turnFor(p.ThreadID, p.TurnID)
	if t == nil {
		return
	}
	m := mapItem(it, p.Item, completed)
	if m.skip {
		if it.Type == "subAgentActivity" {
			a.subAgentActivity(t, ag, it)
		}
		return
	}
	pt := a.partFor(t, ag, p.ThreadID, it.ID, m.kind)
	pt.content = m.content
	if m.text != nil {
		pt.text.Reset()
		pt.text.WriteString(*m.text)
	}
	a.emitPart(t, pt, m.state)
}

// subAgentActivity opens and closes a subagent's group.
func (a *adapter) subAgentActivity(t *turn, parent *subagent, it item) {
	switch it.Kind {
	case "started":
		if a.agents[it.AgentThreadID] != nil {
			return
		}
		var parentGroup string
		if parent != nil {
			parentGroup = parent.group
		}
		a.newAgentLocked(it.AgentThreadID, it.AgentPath, it.Model, it.ReasoningEffort, t, parentGroup)
	case "completed", "interrupted":
		if ag := a.agents[it.AgentThreadID]; ag != nil {
			ag.status = it.Kind
			a.agentBusy(ag, false)
		}
	}
}

func (a *adapter) newAgentLocked(thread, path, model, effort string, t *turn, parentGroup string) *subagent {
	ag := &subagent{thread: thread, path: path, model: model, effort: effort, turn: t, parent: parentGroup, status: "started"}
	group := t.newPart("agent/"+thread, agent.PartTool, parentGroup)
	group.agent = thread
	ag.group = group.id
	a.agents[thread] = ag
	a.emitAgent(ag)
	return ag
}

func (a *adapter) agentBusy(ag *subagent, busy bool) {
	changed := ag.busy != busy
	if changed {
		ag.busy = busy
		if busy {
			a.running++
			ag.status = "running"
		} else {
			a.running--
			if ag.status == "running" {
				ag.status = "idle"
			}
			a.closeAgentPartsLocked(ag)
		}
	}
	a.emitAgent(ag)
	if changed {
		a.out.Emit(agent.Event{Type: agent.TypeTasks, Count: a.running})
	}
}

func (a *adapter) emitAgent(ag *subagent) {
	p := ag.turn.parts["agent/"+ag.thread]
	p.content, _ = json.Marshal(map[string]any{"tool": "subagent", "thread_id": ag.thread, "path": ag.path,
		"name": agentName(ag.path), "model": ag.model, "effort": ag.effort, "status": ag.status})
	state := agent.PartRunning
	switch ag.status {
	case "completed", "idle":
		state = agent.PartDone
	case "interrupted":
		state = agent.PartInterrupted
	}
	p.done = false
	a.out.Emit(partEvent(ag.turn.msgID, p, state))
	p.done = state != agent.PartRunning
}

// agentRunningLocked is whether a subagent thread still works.
func (a *adapter) agentRunningLocked(thread string) bool {
	ag := a.agents[thread]
	return ag != nil && ag.busy
}

// closeAgentPartsLocked ends what a subagent left open when it stopped: its
// parts outlive the spawning turn while it runs.
func (a *adapter) closeAgentPartsLocked(ag *subagent) {
	t := ag.turn
	for _, p := range t.order {
		if p.done || p.agent != ag.thread || p.id == ag.group {
			continue
		}
		a.emitPart(t, p, agent.PartDone)
	}
}

// agentName is the last element of a task path: /root/helper → helper.
func agentName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// turnDiff keeps the turn's aggregated diff as one part, journaled at most
// every diffEvery while it changes and once more at the turn's end.
func (a *adapter) turnDiff(p notification) {
	t, _ := a.turnFor(p.ThreadID, p.TurnID)
	if t == nil || t.ended || p.ThreadID != a.thread {
		return
	}
	if t.diff.id == "" {
		t.n++
		t.diff.id = fmt.Sprintf("%s_p%d", t.msgID, t.n)
	}
	t.diff.text = p.Diff
	if wait := diffEvery - time.Since(t.diff.last); wait > 0 {
		if t.diff.pending == nil {
			t.diff.pending = time.AfterFunc(wait, func() {
				a.mu.Lock()
				defer a.mu.Unlock()
				t.diff.pending = nil
				if !t.ended {
					t.diff.last = time.Now()
					a.out.Emit(a.diffEvent(t, agent.PartRunning))
				}
			})
		}
		return
	}
	t.diff.last = time.Now()
	a.out.Emit(a.diffEvent(t, agent.PartRunning))
}

func (a *adapter) diffEvent(t *turn, state string) agent.Event {
	content, _ := json.Marshal(map[string]any{"tool": "turn_diff", "diff": t.diff.text})
	return agent.Event{Type: agent.TypePart, MessageID: t.msgID, PartID: t.diff.id, Kind: agent.PartDiff, State: state, Content: content}
}

// turnPlan is the legacy update_plan list, as the turn's todo part.
func (a *adapter) turnPlan(p notification) {
	t, ag := a.turnFor(p.ThreadID, p.TurnID)
	if t == nil {
		return
	}
	pt := a.partFor(t, ag, p.ThreadID, "todo", agent.PartTodo)
	var steps []map[string]any
	_ = json.Unmarshal(p.Plan, &steps)
	explanation := ""
	if p.Explanation != nil {
		explanation = *p.Explanation
	}
	pt.content, _ = json.Marshal(map[string]any{"explanation": explanation, "steps": steps})
	a.emitPart(t, pt, agent.PartRunning)
}

// tokenUsage adds each model call's tokens to its turn, and reports the
// context in use. Codex counts cached input inside the input; the journal
// counts it apart.
func (a *adapter) tokenUsage(p notification) {
	t, ag := a.turnFor(p.ThreadID, p.TurnID)
	last := p.TokenUsage.Last
	if t != nil && !t.ended {
		t.usage.Input += last.InputTokens - last.CachedInputTokens
		t.usage.CacheRead += last.CachedInputTokens
		t.usage.CacheWrite += last.CacheWriteInputTokens
		t.usage.Output += last.OutputTokens
		t.usage.Reasoning += last.ReasoningOutputTokens
	}
	if ag != nil || p.ThreadID != a.thread {
		return
	}
	if w := p.TokenUsage.ModelContextWindow; w > 0 {
		a.window = w
	}
	ctx := last.InputTokens + last.OutputTokens
	if ctx > 0 && ctx != a.context {
		a.context = ctx
		a.out.Emit(agent.Event{Type: agent.TypeUsage, Usage: &agent.Usage{Context: ctx, Window: a.window}})
	}
}

// rateLimits maps the account's windows onto rate_limit events, one per
// window whose reading changed.
func (a *adapter) rateLimits(params json.RawMessage) {
	var p struct {
		RateLimits struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
		} `json:"rateLimits"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	var events []agent.Event
	for _, w := range []*window{p.RateLimits.Primary, p.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		ev := agent.Event{Type: agent.TypeRateLimit, Window: w.name(), Used: w.UsedPercent / 100}
		if w.ResetsAt != nil {
			ev.ResetsAt = *w.ResetsAt
		}
		if prev, ok := a.limits[ev.Window]; ok && prev.Used == ev.Used && prev.ResetsAt == ev.ResetsAt {
			continue
		}
		a.limits[ev.Window] = ev
		events = append(events, ev)
	}
	if len(events) > 0 {
		a.out.Emit(events...)
	}
}

type window struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

// name is the window as the usage strip knows it: five_hour, weekly, or its
// length.
func (w *window) name() string {
	if w.WindowDurationMins == nil {
		return "unknown"
	}
	switch m := *w.WindowDurationMins; m {
	case 300:
		return "five_hour"
	case 10080:
		return "weekly"
	default:
		return fmt.Sprintf("%dm", m)
	}
}
