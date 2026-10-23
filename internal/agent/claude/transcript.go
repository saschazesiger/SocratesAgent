//go:build !windows

package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// transcriptLine is one line of a Claude Code transcript,
// <config>/projects/<escaped cwd>/<session>.jsonl.
type transcriptLine struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	UUID             string          `json:"uuid"`
	IsSidechain      bool            `json:"isSidechain"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	Message          json.RawMessage `json:"message"`
	ToolUseResult    json.RawMessage `json:"toolUseResult"`
	Timestamp        string          `json:"timestamp"`
	Content          string          `json:"content"`
}

// findTranscript is the transcript of a conversation, empty when there is
// none. The project directory is the working directory with every
// non-alphanumeric character replaced, which a glob over all projects finds
// without reimplementing the escaping (the id is unique across them).
func findTranscript(configDir, id string) string {
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(configDir, "projects", "*", id+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func readTranscript(path string) ([]transcriptLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []transcriptLine
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 {
			var l transcriptLine
			if json.Unmarshal(line, &l) == nil {
				out = append(out, l)
			}
		}
		if err != nil {
			return out, nil
		}
	}
}

// transcriptUserUUIDs is the uuid of every user line: the messages the CLI
// has, which a resumed chat must not send again.
func transcriptUserUUIDs(path string) map[string]bool {
	lines, _ := readTranscript(path)
	out := map[string]bool{}
	for _, l := range lines {
		if l.Type == "user" && l.UUID != "" {
			out[l.UUID] = true
		}
	}
	return out
}

// loadTasks is the task list of a conversation, <config>/tasks/<id>/<n>.json,
// so that a resumed chat's todo part starts from it.
func loadTasks(configDir, id string) []todoItem {
	files, _ := filepath.Glob(filepath.Join(configDir, "tasks", id, "*.json"))
	sort.Slice(files, func(i, j int) bool { return taskNumber(files[i]) < taskNumber(files[j]) })
	var out []todoItem
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var t struct {
			ID          string `json:"id"`
			Subject     string `json:"subject"`
			Description string `json:"description"`
			ActiveForm  string `json:"activeForm"`
			Status      string `json:"status"`
		}
		if json.Unmarshal(raw, &t) == nil && t.ID != "" && t.Status != "deleted" {
			out = append(out, todoItem{ID: t.ID, Subject: t.Subject, Description: t.Description,
				ActiveForm: t.ActiveForm, Status: t.Status})
		}
	}
	return out
}

func taskNumber(path string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(path), ".json"))
	return n
}

// Rebuild is a chat's history from the CLI's transcript, for a chat whose
// journal is gone (REBUILD.md §2.2); Start writes it when StartSpec.Rebuild
// says so.
func (a *Adapter) Rebuild(spec agent.StartSpec) ([]agent.Event, error) {
	path := findTranscript(configDirOf(cliEnv(spec.Env)), spec.ResumeID)
	if path == "" {
		return nil, fmt.Errorf("%w: no Claude Code transcript for %s", agent.ErrConversationNotFound, spec.ResumeID)
	}
	return RebuildTranscript(path, spec.ResumeID)
}

// RebuildTranscript reads a transcript into chat events, in file order: a
// compaction restarts the parentUuid chain, so following the chain would lose
// everything before it. Each user message opens a turn; the CLI's own lines
// (slash-command wrappers, task notifications, the compaction summary,
// synthetic answers after a cut-off turn) become what the live stream makes
// of them.
func RebuildTranscript(path, conversation string) ([]agent.Event, error) {
	lines, err := readTranscript(path)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errors.New("the transcript is empty")
	}
	r := &rebuild{dir: strings.TrimSuffix(path, ".jsonl"), tools: map[string]int{}, started: map[string]time.Time{}}
	r.events = append(r.events, agent.Event{Type: agent.TypeConversation, ConversationID: conversation})
	for _, l := range lines {
		if l.IsSidechain {
			continue
		}
		switch l.Type {
		case "user":
			r.user(l)
		case "assistant":
			r.assistant(l, "", "")
		case "system":
			if l.Subtype == "compact_boundary" {
				r.events = append(r.events, agent.Event{Type: agent.TypeNotice, Code: "compacted",
					Text: "The conversation was compacted."})
			}
		}
	}
	r.endTurn()
	return r.events, nil
}

type rebuild struct {
	dir    string
	events []agent.Event
	turn   string
	msg    string
	cut    bool
	n      int
	// tools is the index of each tool call's part event, completed in place
	// when its result comes.
	tools   map[string]int
	started map[string]time.Time
}

func (r *rebuild) id(prefix string) string {
	r.n++
	return fmt.Sprintf("%s%d", prefix, r.n)
}

func (r *rebuild) startTurn(userMsg string) {
	r.endTurn()
	r.turn, r.msg, r.cut = r.id("t_"), r.id("a_"), false
	r.events = append(r.events,
		agent.Event{Type: agent.TypeTurnStarted, TurnID: r.turn, MessageID: userMsg},
		agent.Event{Type: agent.TypeMessage, MessageID: r.msg, Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: r.turn})
}

func (r *rebuild) endTurn() {
	if r.turn == "" {
		return
	}
	outcome, state := agent.OutcomeCompleted, agent.MessageDone
	if r.cut {
		outcome, state = agent.OutcomeInterrupted, agent.MessageInterrupted
	}
	r.events = append(r.events,
		agent.Event{Type: agent.TypeMessageState, MessageID: r.msg, State: state},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: r.turn, Outcome: outcome})
	r.turn, r.msg = "", ""
}

func (r *rebuild) part(ev agent.Event) int {
	ev.Type = agent.TypePart
	if ev.MessageID == "" {
		ev.MessageID = r.msg
	}
	r.events = append(r.events, ev)
	return len(r.events) - 1
}

func (r *rebuild) notice(text, code string) {
	if r.turn == "" {
		r.startTurn("")
	}
	content, _ := json.Marshal(map[string]any{"code": code})
	r.part(agent.Event{PartID: r.id("n_"), Kind: agent.PartNotice, State: agent.PartDone, Text: text, Content: content})
}

func (r *rebuild) user(l transcriptLine) {
	if l.IsMeta {
		return
	}
	if l.IsCompactSummary {
		r.endTurn()
		return
	}
	var msg message
	if json.Unmarshal(l.Message, &msg) != nil {
		return
	}
	if text, ok := msg.text(); ok {
		switch {
		case strings.HasPrefix(text, "<task-notification>"):
			// A background task reporting back starts a turn of the CLI's
			// own.
			r.startTurn("")
		case strings.Contains(text, "<command-name>"):
			name, _ := between(text, "<command-name>", "</command-name>")
			args, _ := between(text, "<command-args>", "</command-args>")
			r.userMessage(l, strings.TrimSpace(name+" "+args))
		case strings.Contains(text, "<local-command-stdout>"):
			out, _ := between(text, "<local-command-stdout>", "</local-command-stdout>")
			if out = strings.TrimSpace(out); out != "" && out != "Compacted" {
				r.notice(out, "command_output")
			}
		case strings.HasPrefix(text, "<local-command-caveat>"):
		case strings.HasPrefix(text, "[Request interrupted by user"):
			r.cut = true
		default:
			r.userMessage(l, text)
		}
		return
	}
	for _, b := range msg.blocks() {
		if b.Type == "tool_result" {
			r.toolResult(b, l.ToolUseResult, l.Timestamp)
		}
	}
}

func (r *rebuild) userMessage(l transcriptLine, text string) {
	r.endTurn()
	id := "m_" + l.UUID
	r.events = append(r.events, agent.Event{Type: agent.TypeMessage, MessageID: id, Role: agent.RoleUser,
		State: agent.MessageDelivered, Text: text})
	r.startTurn(id)
}

// assistant maps one assistant line, of the main thread or (with parent) of
// a subagent whose parts go inside its group.
func (r *rebuild) assistant(l transcriptLine, parent, msgID string) {
	var msg message
	if json.Unmarshal(l.Message, &msg) != nil {
		return
	}
	if r.turn == "" && parent == "" {
		r.startTurn("")
	}
	if msg.Model == "<synthetic>" {
		text, _ := msg.text()
		switch text = strings.TrimSpace(text); {
		case text == noResponse:
			r.cut = true
		case text != "" && parent == "":
			r.notice(text, "command_output")
		}
		return
	}
	for i, b := range msg.blocks() {
		base := agent.Event{MessageID: msgID, ParentPartID: parent}
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			base.PartID, base.Kind, base.State, base.Text = fmt.Sprintf("t_%s_%d", l.UUID, i), agent.PartText, agent.PartDone, b.Text
			r.part(base)
		case "thinking":
			if strings.TrimSpace(b.Thinking) == "" {
				continue
			}
			base.PartID, base.Kind, base.State, base.Text = fmt.Sprintf("r_%s_%d", l.UUID, i), agent.PartReasoning, agent.PartDone, b.Thinking
			r.part(base)
		case "tool_use":
			if hiddenTool(b.Name) {
				continue
			}
			kind := agent.PartTool
			switch {
			case diffTool(b.Name):
				kind = agent.PartDiff
			case b.Name == "ExitPlanMode":
				kind = agent.PartPlan
			}
			c := &toolContent{Tool: b.Name, ToolUseID: b.ID, Input: b.Input}
			c.Summary, c.Command, c.Path = summarize(b.Name, b.Input)
			base.PartID, base.Kind, base.State = b.ID, kind, agent.PartInterrupted
			base.Content, _ = json.Marshal(c)
			r.tools[b.ID] = r.part(base)
			if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
				r.started[b.ID] = t
			}
		}
	}
}

// toolResult completes the part of a call, in place.
func (r *rebuild) toolResult(b block, structured json.RawMessage, at string) {
	i, ok := r.tools[b.ToolUseID]
	if !ok {
		return
	}
	ev := &r.events[i]
	var c toolContent
	_ = json.Unmarshal(ev.Content, &c)
	text, _ := resultText(b.Content)
	ev.Text, c.IsError = text, b.IsError
	ev.State = agent.PartDone
	switch {
	case strings.HasPrefix(text, interruptedResult):
		ev.State = agent.PartInterrupted
	case b.IsError && c.Tool != "ExitPlanMode":
		ev.State = agent.PartError
	}
	if t, err := time.Parse(time.RFC3339Nano, at); err == nil && !r.started[b.ToolUseID].IsZero() {
		c.DurationMS = t.Sub(r.started[b.ToolUseID]).Milliseconds()
	}
	switch {
	case diffTool(c.Tool):
		c.Diff, c.Added, c.Removed = patchDiff(c.Path, structured)
	case c.Tool == "ExitPlanMode":
		c.Plan = &planInfo{Decision: "approved"}
		if b.IsError {
			c.Plan.Decision, c.Plan.Note = "keep_planning", text
		}
		ev.Text = planText(ev.Text, structured)
	case agentTool(c.Tool):
		var res struct {
			AgentID string `json:"agentId"`
			Status  string `json:"status"`
		}
		_ = json.Unmarshal(structured, &res)
		c.Agent = &subagentInfo{ID: res.AgentID, Status: "completed"}
		ev.Content, _ = json.Marshal(c)
		if res.AgentID != "" {
			r.children(filepath.Join(r.dir, "subagents", "agent-"+res.AgentID+".jsonl"), b.ToolUseID, ev.MessageID)
		}
		return
	case c.Tool == "Workflow":
		var res struct {
			RunID         string `json:"runId"`
			TranscriptDir string `json:"transcriptDir"`
		}
		_ = json.Unmarshal(structured, &res)
		c.Workflow = &workflowInfo{RunID: res.RunID, Status: "completed"}
		ev.Content, _ = json.Marshal(c)
		if res.TranscriptDir != "" {
			r.workflowAgents(res.TranscriptDir, b.ToolUseID, ev.MessageID)
		}
		return
	}
	ev.Content, _ = json.Marshal(c)
}

// children adds a subagent's calls and answer, nested in its group.
func (r *rebuild) children(path, parent, msgID string) {
	lines, err := readTranscript(path)
	if err != nil {
		return
	}
	for _, l := range lines {
		switch l.Type {
		case "assistant":
			r.assistant(l, parent, msgID)
		case "user":
			var msg message
			if json.Unmarshal(l.Message, &msg) != nil {
				continue
			}
			for _, b := range msg.blocks() {
				if b.Type == "tool_result" {
					r.toolResult(b, l.ToolUseResult, l.Timestamp)
				}
			}
		}
	}
}

// workflowAgents adds a workflow's agents from its run journal, each with
// its calls nested inside.
func (r *rebuild) workflowAgents(dir, parent, msgID string) {
	f, err := os.Open(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	type agentRun struct{ label, phase, result string }
	runs := map[string]*agentRun{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e struct {
			Type    string          `json:"type"`
			AgentID string          `json:"agentId"`
			Label   string          `json:"label"`
			Phase   string          `json:"phase"`
			Result  json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.AgentID == "" {
			continue
		}
		run := runs[e.AgentID]
		if run == nil {
			run = &agentRun{}
			runs[e.AgentID] = run
			order = append(order, e.AgentID)
		}
		switch e.Type {
		case "started":
			run.label, run.phase = e.Label, e.Phase
		case "result":
			var s string
			if json.Unmarshal(e.Result, &s) != nil {
				s = string(e.Result)
			}
			run.result = firstLine(s)
		}
	}
	for i, id := range order {
		run := runs[id]
		partID := fmt.Sprintf("%s_a%d", parent, i+1)
		c := &toolContent{Tool: "WorkflowAgent", ToolUseID: partID, Summary: run.label,
			Agent: &subagentInfo{ID: id, Label: run.label, Phase: run.phase, Status: "completed", Summary: run.result}}
		content, _ := json.Marshal(c)
		r.part(agent.Event{MessageID: msgID, PartID: partID, ParentPartID: parent, Kind: agent.PartTool,
			State: agent.PartDone, Content: content})
		r.children(filepath.Join(dir, "agent-"+id+".jsonl"), partID, msgID)
	}
}
