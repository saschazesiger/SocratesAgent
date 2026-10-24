//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// historyPage is how many turns or items one history request asks for.
const historyPage = 100

type historyTurn struct {
	ID     string     `json:"id"`
	Status string     `json:"status"`
	Error  *turnError `json:"error"`
}

// turnsOf is every turn of a thread, oldest first.
func (a *adapter) turnsOf(ctx context.Context, thread string) ([]historyTurn, error) {
	var out []historyTurn
	var cursor *string
	for {
		var page struct {
			Data       []historyTurn `json:"data"`
			NextCursor *string       `json:"nextCursor"`
		}
		params := map[string]any{"threadId": thread, "limit": historyPage, "sortDirection": "asc", "itemsView": "notLoaded"}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		if err := a.rpc.call(ctx, "thread/turns/list", params, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" || len(page.Data) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// itemsOf is every item of one turn, in order.
func (a *adapter) itemsOf(ctx context.Context, thread, turnID string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	var cursor json.RawMessage
	for {
		var page struct {
			Data []struct {
				Item json.RawMessage `json:"item"`
			} `json:"data"`
			NextCursor json.RawMessage `json:"nextCursor"`
		}
		params := map[string]any{"threadId": thread, "turnId": turnID, "limit": historyPage, "sortDirection": "asc"}
		if len(cursor) > 0 {
			params["cursor"] = cursor
		}
		if err := a.rpc.call(ctx, "thread/items/list", params, &page); err != nil {
			return nil, err
		}
		for _, d := range page.Data {
			out = append(out, d.Item)
		}
		if len(page.NextCursor) == 0 || string(page.NextCursor) == "null" || len(page.Data) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// rebuild writes a thread's history into the journal, for a chat whose
// journal was lost: every turn with its user messages and its items as
// parts. A turn that never finished (codex died in it) is interrupted.
func (a *adapter) rebuild(ctx context.Context, thread string) error {
	turns, err := a.turnsOf(ctx, thread)
	if err != nil {
		return err
	}
	for _, ht := range turns {
		items, err := a.itemsOf(ctx, thread, ht.ID)
		if err != nil {
			return err
		}
		a.out.Emit(historyEvents(ht, items)...)
	}
	// Always said, as StartSpec.Rebuild has it, even when there was no turn
	// to read back.
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeRebuilt,
		Text: fmt.Sprintf("The history of this chat was read back from Codex (%d turns).", len(turns))})
	return nil
}

// historyEvents is one stored turn as journal events.
func historyEvents(ht historyTurn, items []json.RawMessage) []agent.Event {
	turnID := "t_" + ht.ID
	msgID := turnID + "_a"
	outcome, state, msgState := agent.OutcomeCompleted, agent.PartDone, agent.MessageDone
	why := ""
	switch ht.Status {
	case "completed":
	case "failed":
		outcome, state, msgState = agent.OutcomeFailed, agent.PartInterrupted, agent.MessageInterrupted
		why = ht.Error.text()
	default:
		outcome, state, msgState = agent.OutcomeInterrupted, agent.PartInterrupted, agent.MessageInterrupted
	}
	var users, parts []agent.Event
	for i, raw := range items {
		var it item
		if json.Unmarshal(raw, &it) != nil || it.Type == "" {
			continue
		}
		if it.Type == "userMessage" {
			users = append(users, userEvent(it, fmt.Sprintf("m_%s_%d", ht.ID, i)))
			continue
		}
		m := mapItem(it, raw, true)
		if m.skip {
			continue
		}
		st := m.state
		if outcome != agent.OutcomeCompleted && (st == agent.PartRunning || st == agent.PartStreaming) {
			st = state
		}
		ev := agent.Event{Type: agent.TypePart, MessageID: msgID, PartID: fmt.Sprintf("%s_p%d", msgID, len(parts)+1),
			Kind: m.kind, State: st, Content: m.content}
		if m.text != nil {
			ev.Text = *m.text
		}
		parts = append(parts, ev)
	}
	first := ""
	if len(users) > 0 {
		first = users[0].MessageID
	}
	events := append([]agent.Event{}, users...)
	events = append(events,
		agent.Event{Type: agent.TypeTurnStarted, TurnID: turnID, MessageID: first},
		agent.Event{Type: agent.TypeMessage, MessageID: msgID, Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: turnID})
	events = append(events, parts...)
	return append(events,
		agent.Event{Type: agent.TypeMessageState, MessageID: msgID, State: msgState},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: turnID, Outcome: outcome, Text: why})
}

// userEvent is a stored user message, delivered: its text and its images.
func userEvent(it item, fallbackID string) agent.Event {
	id := it.ClientID
	if id == "" {
		id = fallbackID
	}
	var texts []string
	var atts []agent.Attachment
	for _, raw := range it.Content {
		var c struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Path string `json:"path"`
			URL  string `json:"url"`
		}
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		switch c.Type {
		case "text":
			texts = append(texts, c.Text)
		case "localImage":
			atts = append(atts, agent.Attachment{Name: baseName(c.Path), Path: c.Path, Mime: "image/*"})
		}
	}
	return agent.Event{Type: agent.TypeMessage, MessageID: id, Role: agent.RoleUser, State: agent.MessageDelivered,
		Text: strings.Join(texts, "\n"), Attachments: atts}
}

func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
