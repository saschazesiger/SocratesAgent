//go:build !windows

package opencode

import (
	"context"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// history is a session's whole conversation as journal events, for a chat
// whose journal does not have it (StartSpec.Rebuild): every user message and
// the turn it started, the assistant messages with their parts in their
// final state, and what subagents did inside the task parts that ran them.
// A message that never completed is interrupted, and so is anything in it
// that was still running.
func (a *Adapter) history(ctx context.Context, cl *client, msgs []ocMessageWithParts) []agent.Event {
	child := func(session string) []ocMessageWithParts {
		if !validID(session, "ses") {
			return nil
		}
		out, err := a.messages(ctx, cl, session)
		if err != nil {
			return nil
		}
		return out
	}
	return historyEvents(msgs, a.spec.Workdir, child)
}

// historyEvents is history without the HTTP, for the chat's folder
// workdir: child reads a subagent's session.
func historyEvents(msgs []ocMessageWithParts, workdir string, child func(session string) []ocMessageWithParts) []agent.Event {
	var events []agent.Event
	var turn *rebuiltTurn
	end := func() {
		if turn == nil {
			return
		}
		events = append(events, turn.end()...)
		turn = nil
	}
	for _, m := range msgs {
		switch m.Info.Role {
		case "user":
			end()
			turn = &rebuiltTurn{id: "t_" + m.Info.ID, outcome: agent.OutcomeCompleted, usage: &agent.Usage{}}
			turn.changes.workdir = workdir
			if diffs, ok := summaryDiffs(m.Info.Summary); ok {
				turn.changes.summary(diffs)
			}
			events = append(events,
				agent.Event{Type: agent.TypeMessage, MessageID: m.Info.ID, Role: agent.RoleUser, State: agent.MessageDelivered,
					Text: userText(m.Parts), Attachments: userAttachments(m.Parts)},
				agent.Event{Type: agent.TypeTurnStarted, TurnID: turn.id, MessageID: m.Info.ID})
		case "assistant":
			if turn == nil {
				turn = &rebuiltTurn{id: "t_" + m.Info.ID, outcome: agent.OutcomeCompleted, usage: &agent.Usage{}}
				turn.changes.workdir = workdir
				events = append(events, agent.Event{Type: agent.TypeTurnStarted, TurnID: turn.id})
			}
			events = append(events, turn.assistant(m, child)...)
		}
	}
	end()
	return events
}

type rebuiltTurn struct {
	id      string
	outcome string
	failure string
	usage   *agent.Usage
	cost    float64
	// changes has the turn's summary and what its edit calls showed, for
	// its steps' changes.
	changes turnChanges
	// answer is the message the turn's steps go into, as the live mapping
	// has them (answers): the first step's, until a step answers another
	// user message; parent is the one they answer, and state how the
	// answer ended so far.
	answer string
	parent string
	state  string
}

func (t *rebuiltTurn) end() []agent.Event {
	return append(t.endAnswer(), agent.Event{Type: agent.TypeTurnEnded, TurnID: t.id, Outcome: t.outcome, Usage: t.usage,
		CostUSD: t.cost, Text: t.failure})
}

// endAnswer ends the answer the steps so far went into.
func (t *rebuiltTurn) endAnswer() []agent.Event {
	if t.answer == "" {
		return nil
	}
	ev := agent.Event{Type: agent.TypeMessageState, MessageID: t.answer, State: t.state}
	t.answer = ""
	return []agent.Event{ev}
}

func (t *rebuiltTurn) assistant(m ocMessageWithParts, child func(string) []ocMessageWithParts) []agent.Event {
	complete := m.Info.Time.Completed > 0
	t.changes.place(m.Info.Path)
	var events []agent.Event
	if t.answer == "" || t.parent != m.Info.ParentID {
		events = append(t.endAnswer(), agent.Event{Type: agent.TypeMessage, MessageID: m.Info.ID, Role: agent.RoleAssistant,
			State: agent.MessageStreaming, TurnID: t.id, Model: m.Info.model()})
		t.answer, t.parent, t.state = m.Info.ID, m.Info.ParentID, agent.MessageDone
	}
	events = append(events, t.finalParts(t.answer, "", m.Parts, complete, child, 0)...)
	switch {
	case !complete:
		t.state, t.outcome = agent.MessageInterrupted, agent.OutcomeInterrupted
	case m.Info.Error != nil && m.Info.Error.Name == errAborted:
		t.state, t.outcome = agent.MessageInterrupted, agent.OutcomeInterrupted
	case m.Info.Error != nil:
		t.state, t.outcome, t.failure = agent.MessageInterrupted, agent.OutcomeFailed, m.Info.Error.text()
	}
	t.count(m.Info)
	if c := m.Info.Tokens.context(); c > 0 {
		t.usage.Context = c
	}
	return events
}

// count adds what one model call, the chat's or a subagent's, used and cost.
func (t *rebuiltTurn) count(m ocMessage) {
	tok := m.Tokens
	t.usage.Input += tok.Input
	// The journal's output counts the reasoning in, as events.go has it.
	t.usage.Output += tok.Output + tok.Reasoning
	t.usage.Reasoning += tok.Reasoning
	t.usage.CacheRead += tok.Cache.Read
	t.usage.CacheWrite += tok.Cache.Write
	t.cost += m.Cost
}

// maxNesting bounds subagents inside subagents in a rebuild.
const maxNesting = 4

// finalParts are a message's parts as they ended, nested under parent, with
// each task part followed by its subagent's parts inside it, whose usage and
// cost count into the turn. A step of the chat's own that changed files no
// edit call showed is followed by those changes (changes.go).
func (t *rebuiltTurn) finalParts(msg, parent string, parts []ocPart, complete bool, child func(string) []ocMessageWithParts, depth int) []agent.Event {
	var events []agent.Event
	for _, p := range parts {
		if p.Type == partPatch {
			if files, _ := t.changes.files(p.Files); depth == 0 && len(files) > 0 {
				c := stepDiff(files)
				events = append(events, agent.Event{Type: agent.TypePart, MessageID: msg, PartID: p.ID, Kind: c.kind, State: c.state, Content: c.content})
			}
			continue
		}
		c, ok := convertPart(p)
		if !ok {
			continue
		}
		t.changes.edit(c.paths)
		state := c.state
		if state == agent.PartStreaming && complete {
			state = agent.PartDone
		}
		if state == agent.PartStreaming || state == agent.PartRunning {
			state = agent.PartInterrupted
		}
		events = append(events, agent.Event{Type: agent.TypePart, MessageID: msg, PartID: partID(p, c, msg, parent), ParentPartID: parent,
			Kind: c.kind, State: state, Text: c.text, Content: c.content})
		if c.child != "" && depth < maxNesting && child != nil {
			for _, cm := range child(c.child) {
				if cm.Info.Role != "assistant" || !ranFor(p, cm.Info) {
					continue
				}
				t.count(cm.Info)
				events = append(events, t.finalParts(msg, p.ID, cm.Parts, cm.Info.Time.Completed > 0, child, depth+1)...)
			}
		}
	}
	return events
}

// ranFor reports whether a subagent's message was made while the task part
// ran it: a task can resume an earlier task's subagent (task_id), and each
// task part shows what the subagent did for it.
func ranFor(task ocPart, m ocMessage) bool {
	if task.State == nil {
		return true
	}
	start, end := task.State.Time.Start, task.State.Time.End
	return m.Time.Created >= start && (end == 0 || m.Time.Created <= end)
}
