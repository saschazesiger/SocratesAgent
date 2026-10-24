package server

// What the rest of Socrates reads about a chat: Home's line and question, the
// words of a push, the badge, and the turn the namer, the spoken Status and
// the outcome judge are given. A terminal has to be read off its screen by a
// model for all of it; a chat says it in its journal, in the one shape every
// adapter maps onto (AGENT-HOST.md §2), so everything here is a reading of the
// projection and asks nobody. A chat's line on Home is current the moment the
// event that changed it is.

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// questionAsk is the source of a chat's question: an ask in its journal,
// answered in the chat's own dialog. The terminals' sources (a menu on a
// screen, a question in prose) went with the screen digest (WP10).
const questionAsk = "ask"

// What a chat's turn is cut to when it is handed to a model.
const (
	chatPromptRunes = 2000
	chatAnswerRunes = 3000
	chatToolLines   = 20
	chatToolRunes   = 160
	chatTodos       = 20
)

// waitingAsk is the open question that keeps the chat waiting, by the rule
// of agent.Projection.Summary: one of the running turn, one of no turn, or
// any one while no turn runs. An open question of an earlier turn under a
// turn of the agent's own does not: the agent went on.
func waitingAsk(p *agent.Projection) *agent.Ask {
	for _, a := range p.OpenAsks() {
		if p.Turn == nil || a.TurnID == "" || a.TurnID == p.Turn.ID {
			return a
		}
	}
	return nil
}

// planPayload is Claude's plan card, the one ask that is not an ask_user
// call (AGENT-HOST.md §5.1).
type planPayload struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Plan  string `json:"plan"`
}

// chatQuestion is an ask as Home and a push read a question: its kind, and
// one item per question with its options, held to the limits a terminal's
// question is held to.
func chatQuestion(a *agent.Ask) *digestQuestion {
	if a == nil {
		return nil
	}
	var plan planPayload
	if json.Unmarshal(a.Payload, &plan) == nil && plan.Kind == "plan" {
		text := strings.TrimSpace(plan.Title)
		if text == "" {
			text = "Approve the plan?"
		}
		return &digestQuestion{Source: questionAsk, Kind: "plan", Items: []digestItem{{
			Header: "Plan", Text: clipRunes(text, digestTextRunes),
			Options: []digestOption{{Label: "Approve"}, {Label: "Keep planning"}},
		}}}
	}
	payload := askuser.Decode(a.Payload)
	if payload == nil {
		return nil
	}
	out := &digestQuestion{Source: questionAsk, Kind: digestKind(payload.Questions[0].Kind),
		Detail: clipRunes(strings.TrimSpace(payload.Title), digestTextRunes)}
	for _, q := range payload.Questions {
		if len(out.Items) == digestItemsMax {
			break
		}
		item := digestItem{
			Header:   oneLine(q.Header, digestLabelRunes),
			Text:     clipRunes(strings.TrimSpace(q.Question), digestTextRunes),
			Multi:    q.Kind == "multi",
			FreeText: q.AllowOther || q.Kind == "text" || q.Kind == "number",
		}
		for _, o := range q.Options {
			if len(item.Options) == digestOptionsMax {
				break
			}
			item.Options = append(item.Options, digestOption{Label: oneLine(o.Label, digestLabelRunes),
				Description: oneLine(o.Description, digestDescriptionRunes), Recommended: o.Recommended})
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// digestKind is an ask_user kind in the words a digest's question uses.
func digestKind(kind string) string {
	switch kind {
	case "single":
		return "choice"
	case "multi", "confirm":
		return kind
	}
	return "text"
}

// firstQuestion is what a question asks, in one line: its first question.
func firstQuestion(q *digestQuestion) string {
	if q == nil || len(q.Items) == 0 {
		return ""
	}
	return oneLine(q.Items[0].Text, digestSummaryRunes)
}

// chatLine is the one line Home shows under a chat: what it asks while it
// waits; while a turn runs, the tool that runs, else what it is writing, else
// what it was asked; and between turns what its last turn came to.
func chatLine(p *agent.Projection) string {
	if q := chatQuestion(waitingAsk(p)); q != nil {
		return firstQuestion(q)
	}
	if t := p.Turn; t != nil {
		if tool := runningTool(p, t.ID); tool != nil {
			return oneLine(toolLine(tool), digestSummaryRunes)
		}
		if text := latestText(p, t.ID); text != "" {
			return firstLine(text)
		}
		if m := p.Message(t.MessageID); m != nil {
			return firstLine(m.Text)
		}
		return ""
	}
	return turnLine(p, p.LastTurn)
}

// turnLine is what an ended turn came to, in one line: the first line of its
// answer, and for a turn that did not complete, how it ended.
func turnLine(p *agent.Projection, t *agent.Turn) string {
	if t == nil {
		return ""
	}
	answer := firstLine(latestText(p, t.ID))
	why := oneLine(t.Error, digestSummaryRunes)
	switch t.Outcome {
	case agent.OutcomeFailed:
		if why == "" {
			why = answer
		}
		return oneLine(joinLine("Failed", why), digestSummaryRunes)
	case agent.OutcomeLimited:
		return oneLine(joinLine("Limit reached", why), digestSummaryRunes)
	case agent.OutcomeInterrupted:
		if answer == "" {
			return "Stopped"
		}
	}
	return answer
}

func joinLine(label, rest string) string {
	if rest = strings.TrimSpace(rest); rest == "" {
		return label
	}
	return label + ": " + rest
}

// turnMessages is every assistant message of a turn, in order.
func turnMessages(p *agent.Projection, turnID string) []*agent.Message {
	var out []*agent.Message
	for _, m := range p.Messages {
		if m.Role == agent.RoleAssistant && m.TurnID == turnID && turnID != "" {
			out = append(out, m)
		}
	}
	return out
}

// latestText is the newest text the agent wrote in a turn, outside its
// subagents: a subagent's words are its own, and nested in its call.
func latestText(p *agent.Projection, turnID string) string {
	messages := turnMessages(p, turnID)
	for i := len(messages) - 1; i >= 0; i-- {
		parts := messages[i].Parts
		for j := len(parts) - 1; j >= 0; j-- {
			if parts[j].Kind == agent.PartText && strings.TrimSpace(parts[j].Text) != "" {
				return parts[j].Text
			}
		}
	}
	return ""
}

// turnAnswer is everything the agent wrote in a turn, outside its subagents.
func turnAnswer(p *agent.Projection, turnID string) string {
	var texts []string
	for _, m := range turnMessages(p, turnID) {
		for _, part := range m.Parts {
			if part.Kind == agent.PartText && strings.TrimSpace(part.Text) != "" {
				texts = append(texts, strings.TrimSpace(part.Text))
			}
		}
	}
	return strings.Join(texts, "\n\n")
}

// runningTool is the newest call of a turn that is still running, outside
// the subagents: a subagent's own call is the subagent's business, and the
// call that started it says what it is for.
func runningTool(p *agent.Projection, turnID string) *agent.Part {
	var newest *agent.Part
	for _, m := range turnMessages(p, turnID) {
		for _, part := range m.Parts {
			if (part.Kind == agent.PartTool || part.Kind == agent.PartDiff) && part.State == agent.PartRunning &&
				(newest == nil || part.At >= newest.At) {
				newest = part
			}
		}
	}
	return newest
}

// toolContent is what a tool part says about itself, in the fields the
// three adapters use for it (AGENT-HOST.md §5): Claude's summary, command
// and path; Codex's command and changes; OpenCode's title and input.
type toolContent struct {
	Tool    string          `json:"tool"`
	Summary string          `json:"summary"`
	Title   string          `json:"title"`
	Command json.RawMessage `json:"command"`
	Path    string          `json:"path"`
	Name    string          `json:"name"`
	Input   struct {
		Command     string `json:"command"`
		FilePath    string `json:"file_path"`
		FilePathOC  string `json:"filePath"`
		Pattern     string `json:"pattern"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Query       string `json:"query"`
	} `json:"input"`
	Changes []struct {
		Path string `json:"path"`
	} `json:"changes"`
	Files []struct {
		Path string `json:"path"`
	} `json:"files"`
	IsError bool   `json:"is_error"`
	Error   string `json:"error"`
}

func readToolContent(part *agent.Part) toolContent {
	var c toolContent
	_ = json.Unmarshal(part.Content, &c)
	return c
}

// command is the shell command a call runs, whichever field it is in.
func (c toolContent) command() string {
	var s string
	if json.Unmarshal(c.Command, &s) == nil && s != "" {
		return s
	}
	var argv []string
	if json.Unmarshal(c.Command, &argv) == nil && len(argv) > 0 {
		return strings.Join(argv, " ")
	}
	return c.Input.Command
}

// path is the file a call works on.
func (c toolContent) path() string {
	for _, p := range []string{c.Path, c.Input.FilePath, c.Input.FilePathOC} {
		if p != "" {
			return p
		}
	}
	if len(c.Changes) > 0 {
		return c.Changes[0].Path
	}
	if len(c.Files) > 0 {
		return c.Files[0].Path
	}
	return ""
}

// toolLine is a call as a person says it: "Running go test ./...",
// "Editing main.go".
func toolLine(part *agent.Part) string {
	c := readToolContent(part)
	name := strings.ToLower(firstNonEmpty(c.Tool, c.Name))
	about := strings.TrimSpace(c.Summary)
	if about == "" {
		about = strings.TrimSpace(c.Title)
	}
	base := func(path string) string {
		if path == "" {
			return about
		}
		return filepath.Base(path)
	}
	switch {
	case name == "bash" || name == "command" || name == "shell":
		if cmd := firstLine(c.command()); cmd != "" {
			return "Running " + cmd
		}
	case name == "read":
		return "Reading " + base(c.path())
	case part.Kind == agent.PartDiff || name == "edit" || name == "write" || name == "multiedit" ||
		name == "file_change" || name == "apply_patch" || name == "patch" || name == "notebookedit":
		if path := c.path(); path != "" {
			return "Editing " + filepath.Base(path)
		}
		return "Editing files"
	case name == "grep" || name == "glob":
		if c.Input.Pattern != "" {
			return "Searching for " + c.Input.Pattern
		}
	case name == "webfetch":
		if c.Input.URL != "" {
			return "Fetching " + c.Input.URL
		}
	case name == "websearch" || name == "web_search":
		if c.Input.Query != "" {
			return "Searching the web for " + c.Input.Query
		}
	case name == "task" || name == "agent" || name == "subagent":
		if d := firstNonEmpty(c.Input.Description, about); d != "" {
			return "Subagent: " + d
		}
		return "A subagent is working"
	case name == "workflow":
		if d := firstNonEmpty(about, c.Name); d != "" {
			return "Workflow: " + d
		}
		return "A workflow is running"
	}
	label := firstNonEmpty(c.Tool, c.Name)
	if label == "" || label == "mcp" || label == "dynamic" {
		label = firstNonEmpty(c.Name, label, "a tool")
	}
	if about != "" {
		return "Using " + label + ": " + about
	}
	return "Using " + label
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// firstLine is the first line of a text that says something, without the
// Markdown that would be read out as punctuation: a heading's hashes, a
// list's bullet, a quote's mark, emphasis and code ticks. A code block is
// skipped, not summarised.
func firstLine(text string) string {
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#> ")
		for _, bullet := range []string{"- ", "* ", "+ "} {
			line = strings.TrimPrefix(line, bullet)
		}
		line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
		if line = strings.TrimSpace(line); line == "" || strings.Trim(line, "-=*_|: ") == "" {
			continue
		}
		return oneLine(line, digestSummaryRunes)
	}
	return ""
}

// The words a push uses for how a turn ended.
var turnWords = map[string]string{
	agent.OutcomeCompleted:   "Finished",
	agent.OutcomeInterrupted: "Stopped",
	agent.OutcomeFailed:      "Failed",
	agent.OutcomeLimited:     "Limit reached",
}

// turnNews is what the push of an ended turn says: how it ended, and a line
// of its answer (or of why it failed). It is false for a turn the person
// stopped themselves - they know - and for one that is not the chat's last.
func turnNews(p *agent.Projection) (string, bool) {
	t := p.LastTurn
	if t == nil || p.Turn != nil {
		return "", false
	}
	if t.Outcome == agent.OutcomeInterrupted && t.Stopped {
		return "", false
	}
	word := turnWords[t.Outcome]
	if word == "" {
		word = "Finished"
	}
	line := firstLine(latestText(p, t.ID))
	if t.Outcome == agent.OutcomeFailed || t.Outcome == agent.OutcomeLimited {
		if why := oneLine(t.Error, pushBodyRunes); why != "" {
			line = why
		}
	}
	if line == "" {
		return word, true
	}
	return oneLine(word+" · "+line, pushBodyRunes), true
}

// chatTurn is one turn as a model is told it: what the person asked, what
// the agent answered, the calls it made and how they went, and its task
// list as the turn left it.
type chatTurn struct {
	Prompt  string
	Answer  string
	Tasks   []chatTask
	Tools   []string
	Outcome string
	Error   string
}

// chatTask is one entry of a chat's task list: what it is, and pending,
// in_progress or completed.
type chatTask struct {
	Subject string
	Status  string
}

// readChatTurn is the turn t of a chat, read from its projection.
func readChatTurn(p *agent.Projection, t *agent.Turn) chatTurn {
	var out chatTurn
	if t == nil {
		return out
	}
	out.Outcome, out.Error = t.Outcome, t.Error
	out.Prompt = clipRunes(strings.TrimSpace(turnPromptOf(p, t)), chatPromptRunes)
	if answer := turnAnswer(p, t.ID); answer != "" {
		runes := []rune(answer)
		if len(runes) > chatAnswerRunes {
			answer = "…" + strings.TrimSpace(string(runes[len(runes)-chatAnswerRunes+1:]))
		}
		out.Answer = answer
	}
	for _, m := range turnMessages(p, t.ID) {
		for _, part := range m.Parts {
			if part.Kind != agent.PartTool && part.Kind != agent.PartDiff {
				continue
			}
			out.Tools = append(out.Tools, toolOutcome(part))
		}
	}
	if len(out.Tools) > chatToolLines {
		out.Tools = out.Tools[len(out.Tools)-chatToolLines:]
	}
	out.Tasks = todoState(p)
	return out
}

// turnPromptOf is the person's message a turn took up, or for a turn the
// agent started itself (a background task reporting back), the last one
// before it.
func turnPromptOf(p *agent.Projection, t *agent.Turn) string {
	if m := p.Message(t.MessageID); m != nil && m.Role == agent.RoleUser {
		return messageWords(m)
	}
	prompt := ""
	for _, m := range p.Messages {
		if m.At > t.StartedAt {
			break
		}
		if m.Role == agent.RoleUser && m.State == agent.MessageDelivered {
			prompt = messageWords(m)
		}
	}
	return prompt
}

// messageWords is a user message as text, its attachments named.
func messageWords(m *agent.Message) string {
	text := strings.TrimSpace(m.Text)
	for _, a := range m.Attachments {
		text += "\n[attached: " + a.Name + "]"
	}
	return strings.TrimSpace(text)
}

// toolOutcome is one call in a line: what it did, and whether it failed.
func toolOutcome(part *agent.Part) string {
	c := readToolContent(part)
	line := oneLine(toolLine(part), chatToolRunes)
	switch {
	case part.State == agent.PartRunning:
		return line + " (still running)"
	case part.State == agent.PartError || c.IsError || c.Error != "":
		why := firstNonEmpty(c.Error, lastLine(part.Text))
		if why == "" {
			return line + " (failed)"
		}
		return line + " (failed: " + oneLine(why, chatToolRunes) + ")"
	case part.State == agent.PartInterrupted:
		return line + " (interrupted)"
	}
	return line
}

// lastLine is the last line of a text that is not blank.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// todoState is the chat's task list as its newest todo part has it, in the
// three adapters' shapes: Claude's items, Codex's plan steps and OpenCode's
// todos, with their statuses in Claude's words.
func todoState(p *agent.Projection) []chatTask {
	var newest *agent.Part
	var walk func(parts []*agent.Part)
	walk = func(parts []*agent.Part) {
		for _, part := range parts {
			if part.Kind == agent.PartTodo && (newest == nil || part.UpdatedAt >= newest.UpdatedAt) {
				newest = part
			}
			walk(part.Children)
		}
	}
	for _, m := range p.Messages {
		walk(m.Parts)
	}
	if newest == nil {
		return nil
	}
	var c struct {
		Items []struct {
			Subject string `json:"subject"`
			Status  string `json:"status"`
		} `json:"items"`
		Steps []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"steps"`
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal(newest.Content, &c) != nil {
		return nil
	}
	var out []chatTask
	add := func(subject, status string) {
		if subject = oneLine(subject, turnTaskRunes); subject != "" && status != "deleted" && status != "cancelled" {
			out = append(out, chatTask{Subject: subject, Status: taskStatus(status)})
		}
	}
	for _, it := range c.Items {
		add(it.Subject, it.Status)
	}
	for _, it := range c.Steps {
		add(it.Step, it.Status)
	}
	for _, it := range c.Todos {
		add(it.Content, it.Status)
	}
	if len(out) > chatTodos {
		out = out[len(out)-chatTodos:]
	}
	return out
}

// taskStatus is a task's status in the words chatTurnPrompt marks.
func taskStatus(status string) string {
	switch strings.ToLower(strings.ReplaceAll(status, "_", "")) {
	case "completed", "done":
		return "completed"
	case "inprogress", "active":
		return "in_progress"
	}
	return "pending"
}

// chatTurnPrompt is a chat's turn handed to a model: the instruction, the
// answer and the task list, with the calls and how the turn ended.
func chatTurnPrompt(t chatTurn) string {
	var b strings.Builder
	if t.Prompt != "" {
		b.WriteString("The last instruction the person gave the program:\n" + t.Prompt + "\n")
	}
	if t.Answer != "" {
		b.WriteString("The program's answer to it, as it recorded it:\n" + t.Answer + "\n")
	}
	if len(t.Tasks) > 0 {
		b.WriteString("The program's task list:\n")
		for _, task := range t.Tasks {
			mark := "[ ]"
			switch task.Status {
			case "completed":
				mark = "[x]"
			case "in_progress":
				mark = "[~]"
			}
			b.WriteString(mark + " " + task.Subject + "\n")
		}
	}
	if len(t.Tools) > 0 {
		b.WriteString("The calls the program made, in order:\n")
		for _, line := range t.Tools {
			b.WriteString("- " + line + "\n")
		}
	}
	switch t.Outcome {
	case agent.OutcomeFailed:
		b.WriteString("The turn failed" + suffix(": ", oneLine(t.Error, chatToolRunes)) + ".\n")
	case agent.OutcomeLimited:
		b.WriteString("The turn was stopped by a usage limit" + suffix(": ", oneLine(t.Error, chatToolRunes)) + ".\n")
	case agent.OutcomeInterrupted:
		b.WriteString("The turn was interrupted before it finished.\n")
	}
	return b.String()
}

func suffix(sep, text string) string {
	if text == "" {
		return ""
	}
	return sep + text
}

// chatNow is a chat as the spoken Status is told it: its last turn (what the
// person asked, the answer, the calls and the task list), what runs now, and
// what it asks.
func chatNow(p *agent.Projection) string {
	var b strings.Builder
	t := p.Turn
	if t == nil {
		t = p.LastTurn
	}
	if t != nil {
		b.WriteString(chatTurnPrompt(readChatTurn(p, t)))
	}
	if p.Turn != nil {
		if tool := runningTool(p, p.Turn.ID); tool != nil {
			b.WriteString("Running now: " + oneLine(toolLine(tool), chatToolRunes) + "\n")
		} else {
			b.WriteString("The turn is still running.\n")
		}
	}
	if q := chatQuestion(waitingAsk(p)); q != nil {
		b.WriteString("It waits for the person to answer:\n")
		for _, item := range q.Items {
			b.WriteString("- " + item.Text)
			var labels []string
			for _, o := range item.Options {
				labels = append(labels, o.Label)
			}
			if len(labels) > 0 {
				b.WriteString(" (" + strings.Join(labels, ", ") + ")")
			}
			b.WriteString("\n")
		}
	}
	if p.Tasks > 0 {
		b.WriteString("Background tasks still running: " + strconv.Itoa(p.Tasks) + "\n")
	}
	return strings.TrimSpace(b.String())
}
