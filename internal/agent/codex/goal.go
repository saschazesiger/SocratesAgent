//go:build !windows

package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// What codex does by itself, said in the chat (AGENT-HOST.md §5.2, "What
// codex does by itself"): its goal and the turns it starts toward it, the
// hooks that block or fail, the MCP servers that do not start, and what a
// turn writes to (or waits on) a background terminal. Verified against the
// schema of codex-cli 0.165 (thread/goal/updated {threadId, turnId?, goal:
// ThreadGoal}, thread/goal/cleared {threadId}, hook/completed {threadId,
// turnId?, run: HookRunSummary}, mcpServer/startupStatus/updated {name,
// status, error?, failureReason?}, item/commandExecution/terminalInteraction
// {threadId, turnId, itemId, processId, stdin}).

// threadGoal is codex's ThreadGoal: what the thread works toward across
// turns (the goals feature, the model's create_goal and update_goal).
type threadGoal struct {
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokenBudget     *int64 `json:"tokenBudget"`
	TokensUsed      int64  `json:"tokensUsed"`
	TimeUsedSeconds int64  `json:"timeUsedSeconds"`
}

// goalUpdated is a goal set, changed or accounted for. Codex says it again
// each time it counts the goal's tokens; only a new objective or status is
// a notice. The first a resumed thread hears is codex's snapshot of what it
// had, which the journal said before.
func (a *adapter) goalUpdated(params json.RawMessage) {
	var p struct {
		ThreadID string     `json:"threadId"`
		Goal     threadGoal `json:"goal"`
	}
	if json.Unmarshal(params, &p) != nil || p.Goal.Status == "" {
		return
	}
	if a.thread != "" && p.ThreadID != a.thread {
		return
	}
	prev := a.goal
	g := p.Goal
	a.goal = &g
	if prev == nil && (a.thread == "" || a.spec.ResumeID != "" && a.active == nil && len(a.turns) == 0) {
		return
	}
	if prev != nil && prev.Objective == g.Objective && prev.Status == g.Status {
		return
	}
	a.noticeLocked(noticeGoal, goalWords(prev, g), goalDetail(g))
}

func (a *adapter) goalCleared(p notification) {
	if p.ThreadID != a.thread || a.goal == nil {
		return
	}
	g := *a.goal
	a.goal = nil
	a.noticeLocked(noticeGoal, withFirstLine("Goal cleared", g.Objective), strings.TrimSpace(g.Objective))
}

// goalStatusWords leads the line of a goal that came to a status.
var goalStatusWords = map[string]string{
	"active":        "Goal resumed",
	"paused":        "Goal paused",
	"blocked":       "Goal blocked",
	"usageLimited":  "Goal stopped at the usage limit",
	"budgetLimited": "Goal stopped at its token budget",
	"complete":      "Goal complete",
}

// goalWords is the line for a goal that was set, or that changed from prev.
func goalWords(prev *threadGoal, g threadGoal) string {
	lead := goalStatusWords[g.Status]
	switch {
	case prev == nil:
		lead = "Goal set"
		if g.Status != "active" && goalStatusWords[g.Status] != "" {
			lead = goalStatusWords[g.Status]
		}
	case prev.Objective != g.Objective && prev.Status == g.Status:
		lead = "Goal changed"
	case lead == "":
		lead = "Goal " + g.Status
	}
	if used := goalUse(g); used != "" && g.Status != "active" && g.Status != "paused" {
		lead += " (" + used + ")"
	}
	return withFirstLine(lead, g.Objective)
}

// goalDetail is all of a goal: its objective and what it used.
func goalDetail(g threadGoal) string {
	detail := strings.TrimSpace(g.Objective)
	if used := goalUse(g); used != "" {
		detail += "\n\nUsed: " + used
	}
	return detail
}

// goalUse is what a goal used so far: "12,345 of 50,000 tokens, 4 min".
func goalUse(g threadGoal) string {
	var parts []string
	if g.TokensUsed > 0 {
		tokens := thousands(g.TokensUsed) + " tokens"
		if g.TokenBudget != nil && *g.TokenBudget > 0 {
			tokens = thousands(g.TokensUsed) + " of " + thousands(*g.TokenBudget) + " tokens"
		}
		parts = append(parts, tokens)
	}
	if g.TimeUsedSeconds > 0 {
		parts = append(parts, spoken(time.Duration(g.TimeUsedSeconds)*time.Second))
	}
	return strings.Join(parts, ", ")
}

// thousands is n with its thousands apart: 12,345.
func thousands(n int64) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 && r != '-' && s[i-1] != '-' {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// openerLocked opens a turn codex started by itself with why: toward the
// goal, when one is active (or wraps up at its budget), else only that it
// did. Called with mu held.
func (a *adapter) openerLocked(t *turn) {
	code, text, detail := noticeSelf, "Codex started this turn by itself.", ""
	if g := a.goal; g != nil && (g.Status == "active" || g.Status == "budgetLimited") {
		code, detail = noticeGoal, strings.TrimSpace(g.Objective)
		text = withFirstLine("Continuing toward the goal", g.Objective)
		if g.Status == "budgetLimited" {
			text = withFirstLine("Wrapping up the goal at its token budget", g.Objective)
		}
	}
	a.noticePartLocked(t, code, text, detail)
}

// noticeLocked says something codex did: a notice part in the running turn,
// where it happened, else a line of its own. Called with mu held.
func (a *adapter) noticeLocked(code, text, detail string) {
	if t := a.active; t != nil && !t.ended {
		a.noticePartLocked(t, code, text, detail)
		return
	}
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: code, Text: text})
}

func (a *adapter) noticePartLocked(t *turn, code, text, detail string) {
	p := t.newPart(newID("notice/"), agent.PartNotice, "")
	c := map[string]any{"code": code}
	if detail != "" {
		c["detail"] = detail
	}
	p.content = jsonOf(c)
	p.text.WriteString(text)
	a.emitPart(t, p, agent.PartDone)
}

// --------------------------------------------------------------- hooks

// hookRun is the part of codex's HookRunSummary the adapter reads.
type hookRun struct {
	EventName     string  `json:"eventName"`
	HandlerType   string  `json:"handlerType"`
	Status        string  `json:"status"`
	StatusMessage *string `json:"statusMessage"`
	SourcePath    string  `json:"sourcePath"`
	Entries       []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"entries"`
}

// hookWords leads the line of a hook that ended this way.
var hookWords = map[string]string{
	"blocked": "blocked",
	"failed":  "failed",
	"stopped": "stopped the turn",
}

// hookCompleted says a hook that blocked, failed or stopped, with what it
// said, and one that finished with a warning or an error in its output. A
// hook that ran as it should is not a line.
func (a *adapter) hookCompleted(params json.RawMessage) {
	var p struct {
		ThreadID string  `json:"threadId"`
		TurnID   *string `json:"turnId"`
		Run      hookRun `json:"run"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	run := p.Run
	var said []string
	warned := false
	for _, e := range run.Entries {
		if t := strings.TrimSpace(e.Text); t != "" {
			said = append(said, t)
			warned = warned || e.Kind == "warning" || e.Kind == "error" || e.Kind == "stop"
		}
	}
	if run.StatusMessage != nil && strings.TrimSpace(*run.StatusMessage) != "" {
		said = append([]string{strings.TrimSpace(*run.StatusMessage)}, said...)
	}
	did, ok := hookWords[run.Status]
	if !ok {
		if !warned {
			return
		}
		did = "said"
	}
	name := "A hook"
	if run.EventName != "" {
		name = "The " + run.EventName + " hook"
	}
	detail := strings.Join(said, "\n\n")
	if run.SourcePath != "" {
		detail = strings.TrimSpace(detail + "\n\n" + run.SourcePath)
	}
	text := withFirstLine(name+" "+did, strings.Join(said, "\n"))
	// In the turn it ran for, under its subagent; a hook of no turn (or of
	// a thread the adapter does not know) is a line.
	var t *turn
	var ag *subagent
	if p.ThreadID == a.thread || a.agents[p.ThreadID] != nil {
		t, ag = a.turnFor(p.ThreadID, deref(p.TurnID))
	}
	if t != nil && !t.ended {
		parent := ""
		if ag != nil {
			parent = ag.group
		}
		pt := t.newPart(newID("hook/"), agent.PartNotice, parent)
		if ag != nil {
			pt.agent = ag.thread
		}
		pt.content = jsonOf(map[string]any{"code": noticeHook, "detail": detail})
		pt.text.WriteString(text)
		a.emitPart(t, pt, agent.PartDone)
		return
	}
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: noticeHook, Text: text})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ----------------------------------------------------------- MCP servers

// mcpStatus says an MCP server that could not start, or that needs its
// sign-in again, once for each way it failed: every thread (a subagent's
// too) starts the servers anew.
func (a *adapter) mcpStatus(params json.RawMessage) {
	var p struct {
		Name          string  `json:"name"`
		Status        string  `json:"status"`
		Error         *string `json:"error"`
		FailureReason *string `json:"failureReason"`
	}
	if json.Unmarshal(params, &p) != nil || p.Name == "" {
		return
	}
	if p.Status != "failed" {
		if p.Status == "ready" {
			delete(a.mcpFailed, p.Name)
		}
		return
	}
	why := strings.TrimSpace(deref(p.Error))
	text := "The MCP server “" + p.Name + "” could not start"
	if deref(p.FailureReason) == "reauthenticationRequired" {
		text = "The MCP server “" + p.Name + "” needs its sign-in again"
	}
	if why != "" {
		text += ": " + why
	} else {
		text += "."
	}
	if a.mcpFailed[p.Name] == text {
		return
	}
	a.mcpFailed[p.Name] = text
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeMCP, Text: text})
}

// ------------------------------------------------- background terminals

// terminalInteraction is a turn writing to a background terminal, or
// waiting on it (nothing written): a row of its own, done at once, named by
// the command the terminal runs when the adapter saw it start.
func (a *adapter) terminalInteraction(p notification) {
	t, ag := a.turnFor(p.ThreadID, p.TurnID)
	if t == nil || t.ended && ag == nil {
		return
	}
	command := a.commands[terminalKey(p.ThreadID, p.ProcessID)]
	if command == "" {
		if cmd := t.parts[p.ThreadID+"/"+p.ItemID]; cmd != nil {
			var c struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(cmd.content, &c)
			command = commandName(c.Command)
		}
	}
	what := "the background terminal"
	if command != "" {
		what = command
	}
	summary := "waited for " + what
	if p.Stdin != "" {
		summary = "wrote to " + what
	}
	pt := t.newPart(newID(p.ThreadID+"/terminal/"), agent.PartTool, "")
	if ag != nil {
		pt.parent, pt.agent = ag.group, ag.thread
	}
	pt.content = jsonOf(map[string]any{"tool": "terminal", "summary": summary, "process_id": p.ProcessID,
		"stdin": p.Stdin, "terminal_command": command})
	pt.text.WriteString(p.Stdin)
	a.emitPart(t, pt, agent.PartDone)
}

// terminalKey is a terminal among the adapter's commands: process ids are a
// thread's own.
func terminalKey(thread, processID string) string { return thread + "\x00" + processID }

// commandName is a command as a terminal's row names it: its first line,
// without the shell around it, cut to a line's length.
func commandName(command string) string { return firstLine(unwrapShell(command)) }

// terminalCommandLocked keeps the name of the command a thread's process
// runs, from its commandExecution item, until the process has ended (the
// item completed with an exit code). One that goes on in the background
// after its item completed (no exit code) stays until watchTerminals no
// longer lists it. Called with mu held.
func (a *adapter) terminalCommandLocked(thread, processID, command string, ended bool) {
	key := terminalKey(thread, processID)
	switch {
	case ended:
		delete(a.commands, key)
	case command != "":
		a.commands[key] = commandName(command)
	}
}

// forgetTerminalsLocked drops the names of a thread's terminals, but those
// of the background terminals codex still lists for it (nil: all of them),
// with the command codex names. Called with mu held.
func (a *adapter) forgetTerminalsLocked(thread string, live []backgroundTerminal) {
	keep := map[string]bool{}
	for _, bt := range live {
		key := terminalKey(thread, bt.ProcessID)
		keep[key] = true
		if bt.Command != "" {
			a.commands[key] = commandName(bt.Command)
		}
	}
	prefix := thread + "\x00"
	for key := range a.commands {
		if strings.HasPrefix(key, prefix) && !keep[key] {
			delete(a.commands, key)
		}
	}
}

// backgroundTerminal is the part of codex's ThreadBackgroundTerminal the
// adapter reads.
type backgroundTerminal struct {
	ProcessID string `json:"processId"`
	Command   string `json:"command"`
}

// unwrapShell is a command without the shell codex wraps it in:
// /bin/bash -lc 'npm run dev' → npm run dev.
func unwrapShell(command string) string {
	for _, shell := range []string{"/bin/bash -lc ", "/bin/zsh -lc ", "/bin/sh -c ", "bash -lc ", "zsh -lc ", "sh -c "} {
		if rest, ok := strings.CutPrefix(command, shell); ok {
			if len(rest) >= 2 && (rest[0] == '\'' || rest[0] == '"') && rest[len(rest)-1] == rest[0] {
				rest = rest[1 : len(rest)-1]
			}
			return rest
		}
	}
	return command
}

// pick is the first of values that is not empty.
func pick(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
