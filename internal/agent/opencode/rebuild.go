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
	return historyEvents(msgs, child)
}

// historyEvents is history without the HTTP: child reads a subagent's
// session.
func historyEvents(msgs []ocMessageWithParts, child func(session string) []ocMessageWithParts) []agent.Event {
	var events []agent.Event
	var turn *rebuiltTurn
	end := func() {
		if turn == nil {
			return
		}
		events = append(events, turn.end())
		turn = nil
	}
	for _, m := range msgs {
		switch m.Info.Role {
		case "user":
			end()
			turn = &rebuiltTurn{id: "t_" + m.Info.ID, outcome: agent.OutcomeCompleted, usage: &agent.Usage{}}
			events = append(events,
				agent.Event{Type: agent.TypeMessage, MessageID: m.Info.ID, Role: agent.RoleUser, State: agent.MessageDelivered,
					Text: userText(m.Parts), Attachments: userAttachments(m.Parts)},
				agent.Event{Type: agent.TypeTurnStarted, TurnID: turn.id, MessageID: m.Info.ID})
		case "assistant":
			if turn == nil {
				turn = &rebuiltTurn{id: "t_" + m.Info.ID, outcome: agent.OutcomeCompleted, usage: &agent.Usage{}}
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
}

func (t *rebuiltTurn) end() agent.Event {
	return agent.Event{Type: agent.TypeTurnEnded, TurnID: t.id, Outcome: t.outcome, Usage: t.usage,
		CostUSD: t.cost, Text: t.failure}
}

func (t *rebuiltTurn) assistant(m ocMessageWithParts, child func(string) []ocMessageWithParts) []agent.Event {
	complete := m.Info.Time.Completed > 0
	events := []agent.Event{{Type: agent.TypeMessage, MessageID: m.Info.ID, Role: agent.RoleAssistant,
		State: agent.MessageStreaming, TurnID: t.id, Model: m.Info.model()}}
	events = append(events, finalParts(m.Info.ID, "", m.Parts, complete, child, 0)...)
	state := agent.MessageDone
	switch {
	case !complete:
		state, t.outcome = agent.MessageInterrupted, agent.OutcomeInterrupted
	case m.Info.Error != nil && m.Info.Error.Name == errAborted:
		state, t.outcome = agent.MessageInterrupted, agent.OutcomeInterrupted
	case m.Info.Error != nil:
		state, t.outcome, t.failure = agent.MessageInterrupted, agent.OutcomeFailed, m.Info.Error.text()
	}
	tok := m.Info.Tokens
	t.usage.Input += tok.Input
	t.usage.Output += tok.Output
	t.usage.Reasoning += tok.Reasoning
	t.usage.CacheRead += tok.Cache.Read
	t.usage.CacheWrite += tok.Cache.Write
	if c := tok.context(); c > 0 {
		t.usage.Context = c
	}
	t.cost += m.Info.Cost
	return append(events, agent.Event{Type: agent.TypeMessageState, MessageID: m.Info.ID, State: state})
}

// maxNesting bounds subagents inside subagents in a rebuild.
const maxNesting = 4

// finalParts are a message's parts as they ended, nested under parent, with
// each task part followed by its subagent's parts inside it.
func finalParts(msg, parent string, parts []ocPart, complete bool, child func(string) []ocMessageWithParts, depth int) []agent.Event {
	var events []agent.Event
	for _, p := range parts {
		c, ok := convertPart(p)
		if !ok {
			continue
		}
		state := c.state
		if state == agent.PartStreaming && complete {
			state = agent.PartDone
		}
		if state == agent.PartStreaming || state == agent.PartRunning {
			state = agent.PartInterrupted
		}
		events = append(events, agent.Event{Type: agent.TypePart, MessageID: msg, PartID: p.ID, ParentPartID: parent,
			Kind: c.kind, State: state, Text: c.text, Content: c.content})
		if c.diff != nil {
			events = append(events, agent.Event{Type: agent.TypePart, MessageID: msg, PartID: p.ID + diffSuffix,
				ParentPartID: parent, Kind: c.diff.kind, State: c.diff.state, Content: c.diff.content})
		}
		if c.child != "" && depth < maxNesting && child != nil {
			for _, cm := range child(c.child) {
				if cm.Info.Role != "assistant" {
					continue
				}
				events = append(events, finalParts(msg, p.ID, cm.Parts, cm.Info.Time.Completed > 0, child, depth+1)...)
			}
		}
	}
	return events
}
