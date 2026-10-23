//go:build !windows

package claude

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// toolContent is the content of a tool, diff or plan part (AGENT-HOST.md §5,
// "Claude content shapes"). A tool's output is the part's text.
type toolContent struct {
	Tool       string          `json:"tool"`
	ToolUseID  string          `json:"tool_use_id"`
	Input      json.RawMessage `json:"input,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Command    string          `json:"command,omitempty"`
	Path       string          `json:"path,omitempty"`
	Diff       string          `json:"diff,omitempty"`
	Added      int             `json:"added,omitempty"`
	Removed    int             `json:"removed,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	// Result is the tool's structured result where it is small and says
	// more than the text.
	Result json.RawMessage `json:"result,omitempty"`
	// Task is a background shell the call left running.
	Task     *taskInfo     `json:"task,omitempty"`
	Agent    *subagentInfo `json:"agent,omitempty"`
	Workflow *workflowInfo `json:"workflow,omitempty"`
	Plan     *planInfo     `json:"plan,omitempty"`
}

type taskInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// subagentInfo is a subagent: a Task/Agent call, or one agent of a workflow.
type subagentInfo struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Model       string `json:"model,omitempty"`
	Phase       string `json:"phase,omitempty"`
	// Status is running, completed, failed or killed.
	Status     string `json:"status"`
	Background bool   `json:"background,omitempty"`
	Tokens     int64  `json:"tokens,omitempty"`
	ToolUses   int    `json:"tool_uses,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	LastTool   string `json:"last_tool,omitempty"`
	Activity   string `json:"activity,omitempty"`
	Summary    string `json:"summary,omitempty"`
}

// workflowInfo is a dynamic workflow (the Workflow tool): its phases, and how
// far its agents are. The agents themselves are parts nested in it.
type workflowInfo struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	TaskID      string          `json:"task_id,omitempty"`
	RunID       string          `json:"run_id,omitempty"`
	Status      string          `json:"status"`
	Phases      []workflowPhase `json:"phases,omitempty"`
	Agents      int             `json:"agents,omitempty"`
	AgentsDone  int             `json:"agents_done,omitempty"`
	Tokens      int64           `json:"tokens,omitempty"`
	DurationMS  int64           `json:"duration_ms,omitempty"`
	Summary     string          `json:"summary,omitempty"`
}

type workflowPhase struct {
	Index int    `json:"index"`
	Title string `json:"title"`
}

type planInfo struct {
	File  string `json:"file,omitempty"`
	AskID string `json:"ask_id,omitempty"`
	// Decision is pending, approved or keep_planning.
	Decision string `json:"decision,omitempty"`
	Note     string `json:"note,omitempty"`
}

// The tools whose calls are not rows of their own.
func hiddenTool(name string) bool { return name == "TaskCreate" || name == "TaskUpdate" }

func diffTool(name string) bool { return name == "Edit" || name == "Write" || name == "MultiEdit" }

func agentTool(name string) bool { return name == "Task" || name == "Agent" }

// toolUse creates or completes the part of a tool call. A task-list call
// becomes the turn's todo part instead.
func (s *state) toolUse(b block, parent string) *part {
	if hiddenTool(b.Name) {
		if len(b.Input) > 0 {
			s.taskInputs[b.ID] = b.Input
		} else if _, ok := s.taskInputs[b.ID]; !ok {
			s.taskInputs[b.ID] = nil
		}
		return nil
	}
	p := s.parts[b.ID]
	if p == nil {
		kind := agent.PartTool
		switch {
		case diffTool(b.Name):
			kind = agent.PartDiff
		case b.Name == "ExitPlanMode":
			kind = agent.PartPlan
		}
		p = &part{id: b.ID, kind: kind, parent: parent, state: agent.PartRunning, started: time.Now(),
			tool: &toolContent{Tool: b.Name, ToolUseID: b.ID}, group: agentTool(b.Name) || b.Name == "Workflow"}
		p.msgID, p.turnID = s.messageFor(parent)
		s.parts[b.ID] = p
	}
	if len(b.Input) > 0 && string(b.Input) != "{}" {
		p.tool.Input = b.Input
		p.tool.Summary, p.tool.Command, p.tool.Path = summarize(b.Name, b.Input)
		if b.Name == "ExitPlanMode" {
			var in struct {
				Plan string `json:"plan"`
			}
			if json.Unmarshal(b.Input, &in) == nil && in.Plan != "" {
				p.text = in.Plan
			}
		}
		if agentTool(b.Name) {
			var in struct {
				Description  string `json:"description"`
				SubagentType string `json:"subagent_type"`
				Prompt       string `json:"prompt"`
				Model        string `json:"model"`
			}
			_ = json.Unmarshal(b.Input, &in)
			if p.tool.Agent == nil {
				p.tool.Agent = &subagentInfo{Status: "running"}
			}
			p.tool.Agent.Description, p.tool.Agent.Type = in.Description, in.SubagentType
			p.tool.Agent.Prompt, p.tool.Agent.Model = in.Prompt, in.Model
		}
	}
	s.emitPart(p)
	return p
}

// summarize is a call's one line: the command, the path, the pattern.
func summarize(tool string, input json.RawMessage) (summary, command, path string) {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return "", "", ""
	}
	str := func(k string) string { v, _ := in[k].(string); return v }
	switch tool {
	case "Bash":
		command = str("command")
		summary = firstLine(command)
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit":
		path = str("file_path")
		if path == "" {
			path = str("notebook_path")
		}
		summary = path
	case "Grep", "Glob":
		summary = str("pattern")
		if p := str("path"); p != "" {
			summary += " in " + p
		}
	case "WebFetch":
		summary = str("url")
	case "WebSearch", "ToolSearch":
		summary = str("query")
	case "Task", "Agent", "Workflow":
		summary = str("description")
		if summary == "" {
			summary = str("name")
		}
	default:
		for _, k := range []string{"description", "subject", "query", "path", "url", "name"} {
			if v := str(k); v != "" {
				summary = v
				break
			}
		}
	}
	return firstLine(summary), command, path
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len([]rune(s)) > 200 {
		s = string([]rune(s)[:200]) + "…"
	}
	return s
}

// interruptedResult starts the result Claude Code writes for a call a dead
// process left without one, on resume.
const interruptedResult = "[Tool call interrupted"

// toolResult completes a call with its result.
func (s *state) toolResult(b block, structured json.RawMessage, parent string) {
	if _, ok := s.taskInputs[b.ToolUseID]; ok {
		s.taskResult(b.ToolUseID, structured, b.IsError)
		return
	}
	p := s.parts[b.ToolUseID]
	if p == nil {
		p = s.toolUse(block{Type: "tool_use", ID: b.ToolUseID, Name: "tool"}, parent)
	}
	c := p.tool
	text, images := resultText(b.Content)
	p.text = text
	c.IsError = b.IsError
	c.DurationMS = time.Since(p.started).Milliseconds()
	p.state = agent.PartDone
	switch {
	case strings.HasPrefix(text, interruptedResult):
		p.state = agent.PartInterrupted
	case b.IsError && c.Tool != "ExitPlanMode":
		p.state = agent.PartError
	}
	switch {
	case diffTool(c.Tool):
		c.Diff, c.Added, c.Removed = patchDiff(c.Path, structured)
	case c.Tool == "Bash":
		var r struct {
			Stdout     string `json:"stdout"`
			Stderr     string `json:"stderr"`
			Background string `json:"backgroundTaskId"`
		}
		if json.Unmarshal(structured, &r) == nil {
			if r.Background != "" {
				c.Task = &taskInfo{ID: r.Background, Status: "running"}
				s.tasks[r.Background] = p.id
				p.group = true
			}
			if r.Stderr != "" && !strings.Contains(text, r.Stderr) {
				p.text = strings.TrimRight(text+"\n"+r.Stderr, "\n")
			}
		}
	case agentTool(c.Tool):
		var r struct {
			Status  string `json:"status"`
			AgentID string `json:"agentId"`
			Model   string `json:"resolvedModel"`
		}
		_ = json.Unmarshal(structured, &r)
		if c.Agent == nil {
			c.Agent = &subagentInfo{}
		}
		if r.AgentID != "" {
			c.Agent.ID = r.AgentID
		}
		if r.Model != "" {
			c.Agent.Model = r.Model
		}
		if r.Status == "async_launched" && p.state == agent.PartDone {
			// The call returned at once; the agent works on in the
			// background, and its group stays open until it reports back.
			c.Agent.Background, c.Agent.Status, p.state = true, "running", agent.PartRunning
		} else if p.state == agent.PartDone {
			c.Agent.Status = "completed"
		}
	case c.Tool == "Workflow":
		var r struct {
			Status        string `json:"status"`
			TaskID        string `json:"taskId"`
			RunID         string `json:"runId"`
			Name          string `json:"workflowName"`
			Summary       string `json:"summary"`
			TranscriptDir string `json:"transcriptDir"`
		}
		_ = json.Unmarshal(structured, &r)
		if c.Workflow == nil {
			c.Workflow = &workflowInfo{}
		}
		w := c.Workflow
		w.RunID, w.TaskID = pick(r.RunID, w.RunID), pick(r.TaskID, w.TaskID)
		w.Name, w.Description = pick(r.Name, w.Name), pick(r.Summary, w.Description)
		if r.Status == "async_launched" && p.state == agent.PartDone {
			w.Status, p.state = "running", agent.PartRunning
			if r.TaskID != "" {
				s.tasks[r.TaskID] = p.id
			}
			if r.TranscriptDir != "" {
				s.startTail(p.id, r.TranscriptDir)
			}
		}
	case c.Tool == "ExitPlanMode":
		if c.Plan == nil {
			c.Plan = &planInfo{}
		}
		if c.Plan.Decision == "" || c.Plan.Decision == "pending" {
			c.Plan.Decision = "approved"
			if b.IsError {
				c.Plan.Decision, c.Plan.Note = "keep_planning", text
			}
		}
		p.text = planText(p.text, structured)
	default:
		if len(structured) > 0 && len(structured) <= 4096 && structured[0] == '{' {
			c.Result = structured
		}
	}
	s.emitPart(p)
	for i, img := range images {
		s.imagePart(fmt.Sprintf("%s_img%d", p.id, i), p.id, img)
	}
	if !p.live() {
		delete(s.parts, p.id)
	}
}

func pick(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func planText(current string, structured json.RawMessage) string {
	var r struct {
		Plan string `json:"plan"`
	}
	if json.Unmarshal(structured, &r) == nil && r.Plan != "" {
		return r.Plan
	}
	return current
}

// patchDiff is an Edit/Write/MultiEdit result as a unified diff: the hunks of
// its structuredPatch, or a new file's lines.
func patchDiff(path string, structured json.RawMessage) (string, int, int) {
	var r struct {
		Type     string  `json:"type"`
		FilePath string  `json:"filePath"`
		Content  string  `json:"content"`
		Original *string `json:"originalFile"`
		Patch    []struct {
			OldStart int      `json:"oldStart"`
			OldLines int      `json:"oldLines"`
			NewStart int      `json:"newStart"`
			NewLines int      `json:"newLines"`
			Lines    []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	if json.Unmarshal(structured, &r) != nil {
		return "", 0, 0
	}
	if r.FilePath != "" {
		path = r.FilePath
	}
	var b strings.Builder
	added, removed := 0, 0
	if len(r.Patch) == 0 && r.Type == "create" {
		lines := strings.Split(strings.TrimSuffix(r.Content, "\n"), "\n")
		if r.Content == "" {
			lines = nil
		}
		fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", strings.TrimPrefix(path, "/"), len(lines))
		for _, l := range lines {
			b.WriteString("+" + l + "\n")
		}
		return b.String(), len(lines), 0
	}
	if len(r.Patch) == 0 {
		return "", 0, 0
	}
	name := strings.TrimPrefix(path, "/")
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", name, name)
	for _, h := range r.Patch {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
			b.WriteString(l + "\n")
		}
	}
	return b.String(), added, removed
}

// imagePart is an image a tool returned, kept as a file beside the journal
// rather than inside it.
func (s *state) imagePart(id, parent string, img block) {
	if img.Source == nil || img.Source.Data == "" {
		return
	}
	data, err := base64.StdEncoding.DecodeString(img.Source.Data)
	if err != nil {
		return
	}
	sum := sha256.Sum256(data)
	ext := ".png"
	switch img.Source.MediaType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	dir := filepath.Join(s.spec.StateDir, "images")
	path := filepath.Join(dir, hex.EncodeToString(sum[:12])+ext)
	if err := os.MkdirAll(dir, 0o700); err != nil || os.WriteFile(path, data, 0o600) != nil {
		return
	}
	p := s.newPart(id, agent.PartImage, parent, agent.PartDone)
	p.extra = map[string]any{"mime": img.Source.MediaType, "path": path, "size": len(data)}
	s.emitPart(p)
	delete(s.parts, id)
}

func encodeBase64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// ---------------------------------------------------------------- the task list

// todoItem is one entry of the model's task list (TaskCreate/TaskUpdate,
// with CLAUDE_CODE_ENABLE_TODO_TOOLS=1).
type todoItem struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	ActiveForm  string `json:"active_form,omitempty"`
	// Status is pending, in_progress or completed.
	Status string `json:"status"`
}

func (s *state) taskResult(toolUseID string, structured json.RawMessage, isError bool) {
	input := s.taskInputs[toolUseID]
	delete(s.taskInputs, toolUseID)
	if isError {
		return
	}
	var in struct {
		Subject     string `json:"subject"`
		Description string `json:"description"`
		ActiveForm  string `json:"activeForm"`
		TaskID      string `json:"taskId"`
		Status      string `json:"status"`
	}
	_ = json.Unmarshal(input, &in)
	var r struct {
		Task *struct {
			ID      string `json:"id"`
			Subject string `json:"subject"`
		} `json:"task"`
		Success *bool  `json:"success"`
		TaskID  string `json:"taskId"`
	}
	_ = json.Unmarshal(structured, &r)
	switch {
	case r.Task != nil:
		s.todos = append(s.todos, todoItem{ID: r.Task.ID, Subject: pick(r.Task.Subject, in.Subject),
			Description: in.Description, ActiveForm: in.ActiveForm, Status: "pending"})
	case r.Success != nil && *r.Success:
		id := pick(r.TaskID, in.TaskID)
		for i := range s.todos {
			if s.todos[i].ID != id {
				continue
			}
			if in.Status == "deleted" {
				s.todos = append(s.todos[:i], s.todos[i+1:]...)
				break
			}
			t := &s.todos[i]
			t.Status, t.Subject = pick(in.Status, t.Status), pick(in.Subject, t.Subject)
			t.Description, t.ActiveForm = pick(in.Description, t.Description), pick(in.ActiveForm, t.ActiveForm)
			break
		}
	default:
		return
	}
	s.emitTodos()
}

// emitTodos writes the whole list as the running turn's todo part: the part
// of the newest turn is the list as it stands.
func (s *state) emitTodos() {
	msgID, turnID := s.messageFor("")
	id := "todo_" + msgID
	items := s.todos
	if items == nil {
		items = []todoItem{}
	}
	p := &part{id: id, msgID: msgID, turnID: turnID, kind: agent.PartTodo, state: agent.PartDone,
		extra: map[string]any{"items": items}}
	s.emitPart(p)
}

// ---------------------------------------------------------------- background tasks

func (s *state) taskStarted(f *frame) {
	p := s.parts[f.ToolUseID]
	if p == nil || p.tool == nil {
		return
	}
	s.tasks[f.TaskID] = p.id
	switch f.TaskType {
	case "local_agent":
		if p.tool.Agent == nil {
			p.tool.Agent = &subagentInfo{}
		}
		a := p.tool.Agent
		a.ID, a.Status = pick(a.ID, f.TaskID), "running"
		a.Type, a.Description = pick(f.SubagentType, a.Type), pick(f.Description, a.Description)
		a.Prompt, a.Background = pick(f.Prompt, a.Prompt), f.Backgrounded
	case "local_workflow":
		if p.tool.Workflow == nil {
			p.tool.Workflow = &workflowInfo{}
		}
		w := p.tool.Workflow
		w.TaskID, w.Status = f.TaskID, "running"
		w.Name, w.Description = pick(f.WorkflowName, w.Name), pick(f.Description, w.Description)
	default:
		return
	}
	p.group = true
	s.emitPart(p)
}

func (s *state) taskProgress(f *frame) {
	p := s.parts[f.ToolUseID]
	if p == nil || p.tool == nil {
		return
	}
	if a := p.tool.Agent; a != nil {
		if u := f.TaskUsage; u != nil {
			a.Tokens, a.ToolUses, a.DurationMS = u.TotalTokens, u.ToolUses, u.DurationMS
		}
		a.LastTool, a.Activity = pick(f.LastToolName, a.LastTool), pick(f.Description, a.Activity)
	}
	if w := p.tool.Workflow; w != nil {
		if u := f.TaskUsage; u != nil {
			w.Tokens, w.DurationMS = u.TotalTokens, u.DurationMS
		}
		if len(f.WorkflowProgress) > 0 {
			s.workflowProgress(p, f.WorkflowProgress)
		}
	}
	s.emitPart(p)
}

// taskStatus is a background task reporting a new status; a final one closes
// its group.
func (s *state) taskStatus(taskID, status, summary string, usage *taskUsage) {
	p := s.parts[s.tasks[taskID]]
	if p == nil || p.tool == nil {
		return
	}
	final := status == "completed" || status == "failed" || status == "killed" || status == "stopped"
	state := agent.PartDone
	switch status {
	case "failed":
		state = agent.PartError
	case "killed", "stopped":
		state = agent.PartInterrupted
	}
	c := p.tool
	switch {
	case c.Agent != nil:
		c.Agent.Status, c.Agent.Summary = status, pick(summary, c.Agent.Summary)
		if usage != nil {
			c.Agent.Tokens, c.Agent.ToolUses, c.Agent.DurationMS = usage.TotalTokens, usage.ToolUses, usage.DurationMS
		}
	case c.Workflow != nil:
		c.Workflow.Status, c.Workflow.Summary = status, pick(summary, c.Workflow.Summary)
		if usage != nil {
			c.Workflow.Tokens, c.Workflow.DurationMS = usage.TotalTokens, usage.DurationMS
		}
		if final {
			s.stopTail(p.id)
		}
	case c.Task != nil:
		c.Task.Status = status
	}
	if !final {
		s.emitPart(p)
		return
	}
	if p.state == agent.PartRunning {
		p.state = state
	}
	s.emitPart(p)
	delete(s.parts, p.id)
	delete(s.tasks, taskID)
}

// wfEntry is one entry of a task_progress's workflow_progress.
type wfEntry struct {
	Type            string `json:"type"`
	Index           int    `json:"index"`
	Title           string `json:"title"`
	Label           string `json:"label"`
	PhaseTitle      string `json:"phaseTitle"`
	AgentID         string `json:"agentId"`
	Model           string `json:"model"`
	State           string `json:"state"`
	LastToolName    string `json:"lastToolName"`
	LastToolSummary string `json:"lastToolSummary"`
	Tokens          int64  `json:"tokens"`
	ToolCalls       int    `json:"toolCalls"`
	DurationMS      int64  `json:"durationMs"`
	ResultPreview   string `json:"resultPreview"`
	PromptPreview   string `json:"promptPreview"`
}

// workflowProgress maps a workflow's progress list: its phases onto the
// workflow part, every agent onto a part of its own nested in it.
func (s *state) workflowProgress(p *part, entries []json.RawMessage) {
	w := p.tool.Workflow
	w.Phases, w.Agents, w.AgentsDone = nil, 0, 0
	for _, raw := range entries {
		var e wfEntry
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		switch e.Type {
		case "workflow_phase":
			w.Phases = append(w.Phases, workflowPhase{Index: e.Index, Title: e.Title})
		case "workflow_agent":
			w.Agents++
			if s.workflowAgent(p, e) {
				w.AgentsDone++
			}
		}
	}
}

// workflowAgent updates one workflow agent's part and reports whether it is
// finished.
func (s *state) workflowAgent(wf *part, e wfEntry) bool {
	id := fmt.Sprintf("%s_a%d", wf.id, e.Index)
	if e.AgentID != "" {
		s.wfAgents[e.AgentID] = id
	}
	st, status := agent.PartRunning, "running"
	switch e.State {
	case "done":
		st, status = agent.PartDone, "completed"
	case "error", "failed":
		st, status = agent.PartError, "failed"
	case "skipped", "cancelled", "killed":
		st, status = agent.PartInterrupted, "killed"
	}
	p := s.parts[id]
	if p == nil {
		p = &part{id: id, msgID: wf.msgID, turnID: wf.turnID, kind: agent.PartTool, parent: wf.id,
			started: time.Now(), group: true, tool: &toolContent{Tool: "WorkflowAgent", ToolUseID: id}}
		s.parts[id] = p
	}
	before, _ := json.Marshal(p.tool)
	beforeState := p.state
	p.state = st
	p.tool.Summary = e.Label
	p.tool.Agent = &subagentInfo{ID: e.AgentID, Label: e.Label, Phase: e.PhaseTitle, Model: e.Model, Status: status,
		Prompt: e.PromptPreview, Tokens: e.Tokens, ToolUses: e.ToolCalls, DurationMS: e.DurationMS,
		LastTool: e.LastToolName, Activity: e.LastToolSummary, Summary: e.ResultPreview}
	after, _ := json.Marshal(p.tool)
	if beforeState != p.state || string(before) != string(after) {
		s.emitPart(p)
	}
	return st != agent.PartRunning
}
