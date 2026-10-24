//go:build !windows

package claude

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// state is the stream as the adapter has read it so far: the conversation,
// the running turn, the parts still open, and what is waiting on the person.
// Everything in it is touched under Adapter.mu.
type state struct {
	a         *Adapter
	spec      agent.StartSpec
	configDir string

	conv          string
	model, effort string
	mode          string

	// pending maps the CLI-side uuid of every message sent to its message
	// id, until the CLI takes it up; delivered is the uuids the transcript
	// already holds (a resume), and deliveredMsgs the messages reported
	// delivered.
	pending       map[string]string
	delivered     map[string]bool
	deliveredMsgs map[string]bool
	// next is the message the CLI said it started, for the turn its next
	// system/init opens; hasNext says there is one (a command has none).
	next    string
	hasNext bool

	turn *turn
	// seen is the uuids of assistant and user frames already mapped: after
	// a compaction the CLI repeats the preserved ones.
	seen  map[string]bool
	parts map[string]*part
	// tasks maps a background task to the part of the tool that started it.
	tasks      map[string]string
	background int

	todos      []todoItem
	taskInputs map[string]json.RawMessage
	todoPart   string

	plans     map[string]*planAsk
	planByReq map[string]string

	// workflow agents: the part of each agent, by agent id, and the tails
	// reading their transcripts.
	wfAgents map[string]string
	tails    map[string]*tail
	// homes is the message and turn of every workflow agent's part, which
	// its calls go in after the part itself is no longer kept, and wfDone
	// the agents whose parts are final.
	homes  map[string]home
	wfDone map[string]bool

	info         agentInfo
	terminalOnly map[string]bool
	lastCost     float64
}

func newState(spec agent.StartSpec, configDir string) *state {
	return &state{
		spec: spec, configDir: configDir, effort: effortLevel(spec.Effort),
		pending: map[string]string{}, delivered: map[string]bool{}, deliveredMsgs: map[string]bool{},
		seen: map[string]bool{}, parts: map[string]*part{}, tasks: map[string]string{},
		taskInputs: map[string]json.RawMessage{}, plans: map[string]*planAsk{}, planByReq: map[string]string{},
		wfAgents: map[string]string{}, tails: map[string]*tail{}, homes: map[string]home{}, wfDone: map[string]bool{},
		terminalOnly: map[string]bool{"doctor": true, "color": true, "focus": true, "heapdump": true},
	}
}

// home is where a part lives: its message and turn.
type home struct{ msgID, turnID string }

// turn is the turn the CLI is on.
type turn struct {
	id, userMsg, msgID string
	// apiMsg is the API message being streamed, and open its content blocks
	// by stream index, last the one most recently started.
	apiMsg      string
	open        map[int]string
	last        int
	limited     bool
	interrupted bool
	limitText   string
}

// part is one part as the adapter last emitted it.
type part struct {
	id, msgID, turnID, kind, parent, state string
	text                                   string
	tool                                   *toolContent
	extra                                  any
	started                                time.Time
	// group is a subagent or workflow that may outlive its turn.
	group bool
	// resulted is whether the call's tool_result has come: a foreground
	// subagent reports its end before its call returns.
	resulted bool
}

// live is a group whose agent, workflow or background shell still runs: it
// outlives the turn and is closed by its task's notification.
func (p *part) live() bool {
	if !p.group {
		return false
	}
	return p.state == agent.PartRunning || (p.tool != nil && p.tool.Task != nil && p.tool.Task.Status == "running")
}

// inLiveGroup is whether a part is nested in a group that still runs: a
// background subagent's call outlives the turn as its group does.
func (s *state) inLiveGroup(p *part) bool {
	for seen := 0; p.parent != "" && seen < 64; seen++ {
		g := s.parts[p.parent]
		if g == nil {
			return false
		}
		if g.live() {
			return true
		}
		p = g
	}
	return false
}

// closeLive ends every group still running when the CLI is gone: what it was
// doing died with it.
func (s *state) closeLive() {
	for id, p := range s.parts {
		if !p.live() {
			continue
		}
		p.state = agent.PartInterrupted
		if c := p.tool; c != nil {
			switch {
			case c.Agent != nil:
				c.Agent.Status = "killed"
			case c.Workflow != nil:
				c.Workflow.Status = "killed"
			case c.Task != nil:
				c.Task.Status = "killed"
			}
		}
		s.emitPart(p)
		delete(s.parts, id)
	}
}

type planAsk struct {
	reqID  string
	input  json.RawMessage
	partID string
}

func (s *state) emit(events ...agent.Event) { s.a.out.Emit(events...) }

func (s *state) emitPart(p *part) {
	ev := agent.Event{Type: agent.TypePart, MessageID: p.msgID, PartID: p.id, Kind: p.kind, State: p.state,
		Text: p.text, ParentPartID: p.parent}
	switch {
	case p.tool != nil:
		ev.Content, _ = json.Marshal(p.tool)
	case p.extra != nil:
		ev.Content, _ = json.Marshal(p.extra)
	}
	s.emit(ev)
}

func (s *state) frame(f *frame) {
	switch f.Type {
	case "system":
		s.system(f)
	case "stream_event":
		s.stream(f)
	case "assistant":
		s.assistant(f)
	case "user":
		s.user(f)
	case "result":
		s.result(f)
	case "rate_limit_event":
		s.rateLimit(f)
	case "command_lifecycle":
		s.lifecycle(f)
	}
}

// ---------------------------------------------------------------- turns

// lifecycle follows the CLI's queue: "started" is the CLI taking a message
// up, which is its delivery as much as the replay echo is.
func (s *state) lifecycle(f *frame) {
	msgID, ours := s.pending[f.CommandUUID]
	switch f.State {
	case "started":
		if s.turn == nil {
			s.next, s.hasNext = msgID, true
		}
		if ours {
			s.markDelivered(msgID)
		}
	case "cancelled":
		if ours && !s.deliveredMsgs[msgID] {
			delete(s.pending, f.CommandUUID)
			s.emit(agent.Event{Type: agent.TypeMessageState, MessageID: msgID, State: agent.MessageCancelled},
				agent.Event{Type: agent.TypeError, Code: "message_dropped", MessageID: msgID,
					Text: "Claude Code dropped this message before taking it up."})
		}
	}
}

func (s *state) markDelivered(msgID string) {
	if msgID == "" || s.deliveredMsgs[msgID] {
		return
	}
	s.deliveredMsgs[msgID] = true
	s.emit(agent.Event{Type: agent.TypeMessageState, MessageID: msgID, State: agent.MessageDelivered})
}

// ensureTurn is the running turn, opened without a user message when the CLI
// started one by itself.
func (s *state) ensureTurn() *turn {
	if s.turn == nil {
		user := ""
		if s.hasNext {
			user = s.next
		}
		s.next, s.hasNext = "", false
		t := &turn{id: "t_" + randomID(), userMsg: user, msgID: "a_" + randomID(), open: map[int]string{}}
		s.turn = t
		s.emit(
			agent.Event{Type: agent.TypeTurnStarted, TurnID: t.id, MessageID: user},
			agent.Event{Type: agent.TypeMessage, MessageID: t.msgID, Role: agent.RoleAssistant,
				State: agent.MessageStreaming, TurnID: t.id, Model: s.model},
		)
	}
	return s.turn
}

func (s *state) result(f *frame) {
	t := s.turn
	if t == nil {
		if f.IsError {
			s.emit(agent.Event{Type: agent.TypeError, Code: "claude", Text: resultError(f)})
		}
		return
	}
	outcome, why := agent.OutcomeCompleted, ""
	switch {
	case t.limited || (f.APIError != nil && *f.APIError == 429):
		outcome, why = agent.OutcomeLimited, t.limitText
		if why == "" {
			why = "The usage limit of this account is reached."
		}
	case f.TerminalRsn == "aborted_streaming" || f.TerminalRsn == "aborted_tools" || (f.IsError && t.interrupted):
		outcome = agent.OutcomeInterrupted
	case f.IsError:
		outcome, why = agent.OutcomeFailed, resultError(f)
	}
	s.endTurn(outcome, resultUsage(f), s.turnCost(f), why)
}

// turnCost is what this turn cost: the result's per-turn model usage when it
// has one, else the growth of total_cost_usd. That total is cumulative per
// conversation, not per process - a resume restores it from the transcript's
// cost-state, which is why lastCost starts there (newState's caller).
func (s *state) turnCost(f *frame) float64 {
	defer func() { s.lastCost = f.TotalCost }()
	if len(f.SinceLast) > 0 {
		sum := 0.0
		for _, m := range f.SinceLast {
			sum += m.CostUSD
		}
		return sum
	}
	if cost := f.TotalCost - s.lastCost; cost >= 0 {
		return cost
	}
	return f.TotalCost
}

func resultError(f *frame) string {
	if len(f.Errors) > 0 {
		return strings.Join(f.Errors, "; ")
	}
	if strings.TrimSpace(f.Result) != "" {
		return f.Result
	}
	return "Claude Code ended the turn with an error (" + f.Subtype + ")."
}

// resultUsage is a turn's tokens, and the context in use after it: the last
// request's whole prompt plus its answer.
func resultUsage(f *frame) *agent.Usage {
	u := f.ResultUsage
	if u == nil {
		return nil
	}
	out := &agent.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheCreate}
	if u.Details != nil {
		out.Reasoning = u.Details.Thinking
	}
	if n := len(u.Iterations); n > 0 {
		it := u.Iterations[n-1]
		out.Context = it.Input + it.CacheRead + it.CacheCreate + it.Output
	}
	for _, m := range f.ModelUsage {
		out.Window = max(out.Window, m.ContextWindow)
	}
	return out
}

// endTurn closes the turn: every part it left open gets its final state,
// except a subagent or workflow still running in the background.
func (s *state) endTurn(outcome string, usage *agent.Usage, cost float64, why string) {
	t := s.turn
	ids := make([]string, 0, len(s.parts))
	for id, p := range s.parts {
		if p.turnID == t.id {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return s.parts[ids[i]].started.Before(s.parts[ids[j]].started) })
	for _, id := range ids {
		p := s.parts[id]
		if p.live() || s.inLiveGroup(p) {
			continue
		}
		if p.state == agent.PartStreaming || p.state == agent.PartRunning {
			p.state = agent.PartInterrupted
			if outcome == agent.OutcomeCompleted && p.kind != agent.PartTool && p.kind != agent.PartDiff {
				p.state = agent.PartDone
			}
			s.emitPart(p)
		}
		delete(s.parts, id)
	}
	state := agent.MessageDone
	if outcome != agent.OutcomeCompleted {
		state = agent.MessageInterrupted
	}
	s.emit(
		agent.Event{Type: agent.TypeMessageState, MessageID: t.msgID, State: state},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: t.id, Outcome: outcome, Usage: usage, CostUSD: cost, Text: why},
	)
	s.turn = nil
}

// ---------------------------------------------------------------- system

func (s *state) system(f *frame) {
	switch f.Subtype {
	case "init":
		s.init(f)
	case "status":
		if f.Mode != "" && f.Mode != s.mode {
			s.mode = f.Mode
			s.info.Mode = f.Mode
			s.emitInfo()
		}
	case "compact_boundary":
		text := "The conversation was compacted."
		if m := f.CompactMetadata; m != nil && m.PreTokens > 0 {
			text = fmt.Sprintf("The conversation was compacted (%s → %s tokens).", kilo(m.PreTokens), kilo(m.PostTokens))
		}
		s.emit(agent.Event{Type: agent.TypeNotice, Code: "compacted", Text: text})
	case "api_retry":
		status := ""
		if f.ErrorStatus != nil {
			status = fmt.Sprintf(" (%d)", *f.ErrorStatus)
		}
		s.emit(agent.Event{Type: agent.TypeNotice, Code: "api_retry", Text: fmt.Sprintf(
			"The model API failed%s; retrying in %.0f s (attempt %d of %d).",
			status, float64(f.RetryDelay)/1000, f.Attempt, f.MaxRetries)})
	case "informational":
		switch content := strings.TrimSpace(f.Content); {
		case strings.Contains(content, "thinking from another organization"):
			// Resumed on an account of another organization, Claude Code
			// sets the earlier thinking aside and keeps it for the way back
			// (research/08): a hint on the account switch, not a warning.
			s.emit(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeForeignThinking,
				Text: "The earlier thinking was written under another organization, so this account's Claude goes on without it. It is kept for a switch back."})
		case f.Level != "info" && content != "":
			s.emit(agent.Event{Type: agent.TypeNotice, Code: "informational", Text: content})
		}
	case "task_started":
		s.taskStarted(f)
	case "task_progress":
		s.taskProgress(f)
	case "task_updated":
		if f.Patch != nil && f.Patch.Status != "" {
			s.taskUpdated(f.TaskID, f.Patch.Status)
		}
	case "task_notification":
		status := "completed"
		if f.Status != nil && *f.Status != "" {
			status = *f.Status
		}
		s.taskStatus(f.TaskID, status, f.Summary, f.TaskUsage)
	case "background_tasks_changed":
		if n := len(f.Tasks); n != s.background {
			s.background = n
			s.emit(agent.Event{Type: agent.TypeTasks, Count: n})
		}
	}
}

func kilo(n int64) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// init is the CLI's system/init, written before every turn: the
// conversation id (a new one after /clear), the model, the commands, and the
// start of the turn.
func (s *state) init(f *frame) {
	if f.Session != "" && f.Session != s.conv {
		s.conv = f.Session
		s.emit(agent.Event{Type: agent.TypeConversation, ConversationID: f.Session})
	}
	if f.Model != "" && f.Model != s.model {
		s.model = f.Model
		s.emit(agent.Event{Type: agent.TypeModelChanged, Model: f.Model, Effort: s.effort})
	}
	changed := false
	for _, name := range f.TermCommands {
		s.terminalOnly[name] = true
	}
	known := map[string]bool{}
	for _, c := range s.info.Commands {
		known[c.Name] = true
	}
	for _, name := range f.SlashCommands {
		if !known[name] && !s.terminalOnly[name] {
			s.info.Commands = append(s.info.Commands, agent.CommandInfo{Name: name})
			changed = true
		}
	}
	if f.Version != "" && f.Version != s.info.Version {
		s.info.Version, changed = f.Version, true
	}
	if len(f.Tools) > 0 && len(f.Tools) != len(s.info.Tools) {
		s.info.Tools, changed = f.Tools, true
	}
	if f.Mode != "" && f.Mode != s.info.Mode {
		s.mode, s.info.Mode, changed = f.Mode, f.Mode, true
	}
	if changed {
		s.emitInfo()
	}
	s.ensureTurn()
}

func (s *state) modelChanged(model, effort string) {
	if model = strings.TrimSpace(model); model != "" {
		s.model = model
	}
	if effort != "" {
		s.effort = effort
	}
	s.emit(agent.Event{Type: agent.TypeModelChanged, Model: s.model, Effort: s.effort})
}

func (s *state) rateLimit(f *frame) {
	info := f.RateLimit
	if info == nil {
		return
	}
	windows := make([]string, 0, len(info.UnifiedWindows))
	for w := range info.UnifiedWindows {
		windows = append(windows, w)
	}
	sort.Strings(windows)
	for _, w := range windows {
		u := info.UnifiedWindows[w]
		s.emit(agent.Event{Type: agent.TypeRateLimit, Window: w, Used: u.Utilization, ResetsAt: u.ResetsAt})
	}
	if len(windows) == 0 && info.RateLimitType != "" {
		s.emit(agent.Event{Type: agent.TypeRateLimit, Window: info.RateLimitType, Used: info.Utilization, ResetsAt: info.ResetsAt})
	}
	if info.Status == "rejected" {
		text := "The usage limit of this account is reached."
		if info.ResetsAt > 0 {
			text = fmt.Sprintf("The usage limit of this account is reached; it renews %s.",
				time.Unix(info.ResetsAt, 0).UTC().Format("2006-01-02 15:04 UTC"))
		}
		if t := s.turn; t != nil {
			t.limited, t.limitText = true, text
		}
		s.emit(agent.Event{Type: agent.TypeError, Code: "rate_limit", Text: text})
	}
}

// ---------------------------------------------------------------- the stream

// stream maps the main thread's partial messages: a part per content block as
// it starts, its text as deltas. Subagents are not streamed; their blocks
// arrive whole.
func (s *state) stream(f *frame) {
	if f.ParentToolUseID != "" || f.Event == nil {
		return
	}
	t := s.ensureTurn()
	ev := f.Event
	switch ev.Type {
	case "message_start":
		t.apiMsg, t.open = f.APIMessageID, map[int]string{}
	case "content_block_start":
		b := ev.ContentBlock
		if b == nil {
			return
		}
		var id string
		switch b.Type {
		case "text":
			id = fmt.Sprintf("t_%s_%d", t.apiMsg, ev.Index)
			s.newPart(id, agent.PartText, "", agent.PartStreaming)
		case "thinking":
			id = fmt.Sprintf("r_%s_%d", t.apiMsg, ev.Index)
			s.newPart(id, agent.PartReasoning, "", agent.PartStreaming)
		case "tool_use":
			id = b.ID
			s.toolUse(*b, "")
		default:
			return
		}
		t.open[ev.Index], t.last = id, ev.Index
	case "content_block_delta":
		d := ev.Delta
		id := t.open[ev.Index]
		p := s.parts[id]
		if d == nil || p == nil {
			return
		}
		text := d.Text
		if d.Type == "thinking_delta" {
			text = d.Thinking
		}
		if (d.Type == "text_delta" || d.Type == "thinking_delta") && text != "" {
			p.text += text
			s.a.out.Delta(p.msgID, p.id, text)
		}
	}
}

// newPart creates and emits a part of the running turn (or of the group
// parent belongs to).
func (s *state) newPart(id, kind, parent, st string) *part {
	if p := s.parts[id]; p != nil {
		return p
	}
	p := &part{id: id, kind: kind, parent: parent, state: st, started: time.Now()}
	p.msgID, p.turnID = s.messageFor(parent)
	s.parts[id] = p
	s.emitPart(p)
	return p
}

// messageFor is the message a part goes in: its group's, or the running
// turn's.
func (s *state) messageFor(parent string) (msgID, turnID string) {
	if g := s.parts[parent]; parent != "" && g != nil {
		return g.msgID, g.turnID
	}
	if h, ok := s.homes[parent]; parent != "" && ok {
		return h.msgID, h.turnID
	}
	t := s.ensureTurn()
	return t.msgID, t.id
}

func (s *state) assistant(f *frame) {
	if f.UUID != "" {
		if s.seen[f.UUID] {
			return
		}
		s.seen[f.UUID] = true
	}
	var msg message
	if json.Unmarshal(f.Message, &msg) != nil {
		return
	}
	if msg.Model == "<synthetic>" {
		s.synthetic(f, &msg)
		return
	}
	if f.Error == "rate_limit" {
		if t := s.ensureTurn(); !t.limited {
			t.limited = true
		}
	}
	parent := f.ParentToolUseID
	for i, b := range msg.blocks() {
		switch b.Type {
		case "text", "thinking":
			kind := agent.PartText
			text := b.Text
			if b.Type == "thinking" {
				kind, text = agent.PartReasoning, b.Thinking
			}
			id := s.blockID(f, &msg, kind, i)
			p := s.newPart(id, kind, parent, agent.PartStreaming)
			if text != "" {
				p.text = text
			}
			if kind == agent.PartReasoning && f.ThinkingMS > 0 {
				p.extra = map[string]any{"duration_ms": f.ThinkingMS}
			}
			if kind == agent.PartReasoning && p.text == "" {
				// Thinking whose text the CLI keeps to itself: there is
				// nothing to show, only that it happened.
				p.extra = map[string]any{"duration_ms": f.ThinkingMS, "redacted": true}
			}
			p.state = agent.PartDone
			s.emitPart(p)
			delete(s.parts, id)
		case "tool_use":
			s.toolUse(b, parent)
		}
	}
}

// blockID is the part id of a whole block: the streamed part it completes,
// for the main thread, or one of its own.
func (s *state) blockID(f *frame, msg *message, kind string, i int) string {
	if t := s.turn; t != nil && f.ParentToolUseID == "" && msg.ID == t.apiMsg {
		if id := t.open[t.last]; id != "" {
			if p := s.parts[id]; p != nil && p.kind == kind {
				return id
			}
		}
	}
	prefix := "t_"
	if kind == agent.PartReasoning {
		prefix = "r_"
	}
	return fmt.Sprintf("%s%s_%d", prefix, f.UUID, i)
}

// synthetic is an assistant message the CLI wrote itself: a local command's
// output (/model), an API error, or the "No response requested." a resume
// puts after a turn that was cut off.
func (s *state) synthetic(f *frame, msg *message) {
	text, _ := msg.text()
	text = strings.TrimSpace(text)
	if text == "" || text == noResponse {
		return
	}
	code := "command_output"
	if f.LocalCommandRun == nil {
		code = "claude"
		if f.Error != "" {
			code = f.Error
		}
		if f.Error == "rate_limit" {
			t := s.ensureTurn()
			t.limited, t.limitText = true, text
		}
	}
	p := s.newPart("n_"+f.UUID, agent.PartNotice, f.ParentToolUseID, agent.PartDone)
	p.text, p.extra = text, map[string]any{"code": code}
	s.emitPart(p)
	delete(s.parts, p.id)
}

// noResponse is the text of the synthetic message a resume writes after a
// turn that never finished.
const noResponse = "No response requested."

func (s *state) user(f *frame) {
	var msg message
	if json.Unmarshal(f.Message, &msg) != nil {
		return
	}
	if f.IsReplay {
		if msgID, ok := s.pending[f.UUID]; ok {
			delete(s.pending, f.UUID)
			s.markDelivered(msgID)
			return
		}
		if text, ok := msg.text(); ok {
			if out, found := between(text, "<local-command-stdout>", "</local-command-stdout>"); found {
				if out = strings.TrimSpace(out); out != "" && out != "Compacted" && s.turn != nil {
					p := s.newPart("n_"+f.UUID, agent.PartNotice, "", agent.PartDone)
					p.text, p.extra = out, map[string]any{"code": "command_output"}
					s.emitPart(p)
					delete(s.parts, p.id)
				}
			}
		}
		return
	}
	if f.UUID != "" {
		if s.seen[f.UUID] {
			return
		}
		s.seen[f.UUID] = true
	}
	if f.IsSynthetic {
		return
	}
	for _, b := range msg.blocks() {
		switch b.Type {
		case "tool_result":
			s.toolResult(b, f.ToolUseResult, f.ParentToolUseID)
		case "text":
			if strings.HasPrefix(b.Text, "[Request interrupted by user") && s.turn != nil {
				s.turn.interrupted = true
			}
		}
	}
}

func between(s, open, close string) (string, bool) {
	i := strings.Index(s, open)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// ---------------------------------------------------------------- the plan card

// askPlan is ExitPlanMode asking to leave plan mode: the plan becomes a plan
// part and an ask that the person answers with Approve or Keep planning.
func (s *state) askPlan(reqID string, req controlRequest) {
	askID := "plan_" + req.ToolUseID
	s.plans[askID] = &planAsk{reqID: reqID, input: req.Input, partID: req.ToolUseID}
	s.planByReq[reqID] = askID
	var in struct {
		Plan     string `json:"plan"`
		PlanFile string `json:"planFilePath"`
	}
	_ = json.Unmarshal(req.Input, &in)
	p := s.parts[req.ToolUseID]
	if p == nil {
		p = s.toolUse(block{Type: "tool_use", ID: req.ToolUseID, Name: "ExitPlanMode", Input: req.Input}, "")
	}
	p.text = in.Plan
	p.tool.Plan = &planInfo{File: in.PlanFile, AskID: askID, Decision: "pending"}
	s.emitPart(p)
	payload, _ := json.Marshal(map[string]any{"kind": "plan", "title": "Plan", "plan": in.Plan,
		"plan_file": in.PlanFile, "part_id": p.id, "options": []string{"approve", "keep_planning"}})
	turnID := ""
	if s.turn != nil {
		turnID = s.turn.id
	}
	s.emit(agent.Event{Type: agent.TypeAsk, AskID: askID, TurnID: turnID, Payload: payload})
}

// cancelPlan is the CLI withdrawing its question, as an interrupt does. The
// ask stays open; an answer to it goes to the agent as a message.
func (s *state) cancelPlan(reqID string) {
	if askID, ok := s.planByReq[reqID]; ok {
		delete(s.planByReq, reqID)
		delete(s.plans, askID)
	}
}

func (s *state) takePlan(askID string) (string, json.RawMessage, bool) {
	pa := s.plans[askID]
	if pa == nil {
		return "", nil, false
	}
	delete(s.plans, askID)
	delete(s.planByReq, pa.reqID)
	input := pa.input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return pa.reqID, input, true
}

func (s *state) planDecided(askID string, approve bool, note string) {
	p := s.parts[strings.TrimPrefix(askID, "plan_")]
	if p == nil || p.tool == nil || p.tool.Plan == nil {
		return
	}
	p.tool.Plan.Decision, p.tool.Plan.Note = "keep_planning", note
	if approve {
		p.tool.Plan.Decision = "approved"
	}
	s.emitPart(p)
}

// ---------------------------------------------------------------- info

// agentInfo is the content of the agent.info event: what the composer, the
// model picker and the account divider need to know about the CLI.
type agentInfo struct {
	Agent    string              `json:"agent"`
	Version  string              `json:"version,omitempty"`
	Account  *account            `json:"account,omitempty"`
	Commands []agent.CommandInfo `json:"commands,omitempty"`
	Models   []model             `json:"models,omitempty"`
	Tools    []string            `json:"tools,omitempty"`
	Mode     string              `json:"permission_mode,omitempty"`
}

type account struct {
	Email        string `json:"email,omitempty"`
	Organization string `json:"organization,omitempty"`
	Plan         string `json:"plan,omitempty"`
	APIProvider  string `json:"api_provider,omitempty"`
	TokenSource  string `json:"token_source,omitempty"`
}

type model struct {
	ID          string   `json:"id"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Efforts     []string `json:"efforts,omitempty"`
}

// readInfo reads the initialize answer.
func (s *state) readInfo(raw json.RawMessage) {
	var r struct {
		Commands []struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			ArgumentHint string `json:"argumentHint"`
		} `json:"commands"`
		Models []struct {
			Value       string   `json:"value"`
			DisplayName string   `json:"displayName"`
			Description string   `json:"description"`
			Efforts     []string `json:"supportedEffortLevels"`
		} `json:"models"`
		Account *struct {
			Email            string `json:"email"`
			Organization     string `json:"organization"`
			SubscriptionType string `json:"subscriptionType"`
			APIProvider      string `json:"apiProvider"`
			TokenSource      string `json:"tokenSource"`
		} `json:"account"`
		Version string `json:"claude_code_version"`
		Mode    string `json:"current_permission_mode"`
	}
	_ = json.Unmarshal(raw, &r)
	s.info = agentInfo{Agent: Harness, Version: r.Version, Mode: r.Mode}
	s.mode = r.Mode
	for _, c := range r.Commands {
		if !s.terminalOnly[c.Name] {
			s.info.Commands = append(s.info.Commands, agent.CommandInfo{Name: c.Name, Description: c.Description, Args: c.ArgumentHint})
		}
	}
	for _, m := range r.Models {
		s.info.Models = append(s.info.Models, model{ID: m.Value, Label: m.DisplayName, Description: m.Description, Efforts: m.Efforts})
	}
	if a := r.Account; a != nil {
		s.info.Account = &account{Email: a.Email, Organization: a.Organization, Plan: a.SubscriptionType,
			APIProvider: a.APIProvider, TokenSource: a.TokenSource}
	}
	s.emitInfo()
}

func (s *state) emitInfo() {
	raw, _ := json.Marshal(s.info)
	s.emit(agent.Event{Type: agent.TypeAgentInfo, Content: raw})
}
