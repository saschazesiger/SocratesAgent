package agent

import (
	"encoding/json"
	"strings"
)

// LiteFrom is the size of a tool part (its text and content) from which a
// replay sends it light. A smaller one costs less whole than a request for
// it would.
const LiteFrom = 2 << 10

// liteLine is how long a string a light part keeps may be: the first line
// of a command, a path, a pattern.
const liteLine = 200

// The keys of a tool's content a light part keeps: what a collapsed call
// shows - its name and summary, how long it took, how it ended, whether it
// runs in the background - and what an agent or a workflow says of itself in
// its row (chatparts.js toolOf, toolHtml, backgroundJobs).
var liteKeys = map[string]bool{
	"tool": true, "name": true, "tool_use_id": true, "summary": true, "path": true, "duration_ms": true,
	"exit_code": true, "is_error": true, "background": true, "added": true, "removed": true, "task": true,
	"cwd": true, "status": true,
}

// The keys of a call's input (or Codex's arguments) a light part keeps: the
// ones its summary is read from.
var liteInputKeys = map[string]bool{
	"command": true, "file_path": true, "notebook_path": true, "path": true, "pattern": true,
	"description": true, "url": true, "query": true, "run_in_background": true, "subagent_type": true,
}

// What an agent's or a workflow's own words a light part leaves out: what it
// was told and what it reported, which only its opened body shows.
var liteDrop = map[string]map[string]bool{
	"agent":    {"prompt": true, "summary": true},
	"workflow": {"summary": true},
}

// Lighten is a replay's events - compacted, after the seq since - made light
// for a browser that asks for what it does not draw when it needs it (GET
// /api/chats/{id}/parts/{part}). p is the chat's projection, as of the batch
// or later.
//
//   - A tool part of LiteFrom or more goes without its text (the output) and
//     with only the content its row shows (liteContent), marked lite.
//   - A part nested in another one, finished, and made after since - which
//     the browser has therefore never had - is held back, with everything
//     nested in it: its parent says how many of its own it holds back
//     (held), and its group, opened, asks for them. Held back is never a
//     part that still runs or streams, nor one a question names, nor any
//     part above one of those: what is running is what Home, the jobs and
//     the fold draw, and a question is drawn at its part (chatfold.js
//     cutAtAsk, chatparts.js askPlaces).
//
// What it sends folds into the projection's messages, asks, turns and every
// part a collapsed chat draws; the rest is a request away.
func Lighten(events []Event, since int64, p *Projection) []Event {
	parent := map[string]string{}
	var walk func(list []*Part, up string)
	walk = func(list []*Part, up string) {
		for _, part := range list {
			if up != "" {
				parent[part.ID] = up
			}
			walk(part.Children, part.ID)
		}
	}
	for _, m := range p.Messages {
		walk(m.Parts, "")
	}
	keep := map[string]bool{}
	protect := func(id string) {
		for id != "" && !keep[id] {
			keep[id] = true
			id = parent[id]
		}
	}
	for id, part := range p.parts {
		if part.State == PartRunning || part.State == PartStreaming {
			protect(id)
		}
	}
	for _, a := range p.Asks {
		protect(askPartID(a.Payload))
	}
	sent := map[string]bool{}
	held := map[string]bool{}
	count := map[string]int{}
	for _, ev := range events {
		if ev.Type != TypePart || held[ev.PartID] {
			continue
		}
		up, nested := parent[ev.PartID]
		part := p.parts[ev.PartID]
		if nested && part != nil && part.seq > since && !keep[ev.PartID] && finished(part.State) && (held[up] || sent[up]) {
			held[ev.PartID] = true
			if !held[up] {
				count[up]++
			}
			continue
		}
		sent[ev.PartID] = true
	}
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if (ev.Type == TypePart || ev.Type == TypePartDelta) && held[ev.PartID] {
			continue
		}
		if ev.Type == TypePart {
			ev.Held = count[ev.PartID]
			ev = Light(ev)
		}
		out = append(out, ev)
	}
	return out
}

// Detail is a part whole, as the part event that would make it as it stands,
// and the parts nested directly in it as Lighten sends them, each saying how
// many are nested in it in turn (held): what a browser that holds the part
// light, or holds its group back, asks for when it is opened.
func (p *Projection) Detail(id string) (Event, []Event, bool) {
	part := p.parts[id]
	if part == nil {
		return Event{}, nil, false
	}
	children := make([]Event, 0, len(part.Children))
	for _, child := range part.Children {
		ev := Light(partEvent(child))
		ev.Held = len(child.Children)
		children = append(children, ev)
	}
	return partEvent(part), children, true
}

// partEvent is the part event that makes a part as it stands.
func partEvent(part *Part) Event {
	return Event{
		Seq: part.seq, At: part.At, UpdatedAt: part.UpdatedAt, Type: TypePart, MessageID: part.MessageID,
		PartID: part.ID, ParentPartID: part.ParentID, Kind: part.Kind, State: part.State, Text: part.Text,
		Content: part.Content,
	}
}

// Light is a part event made light, when it is a tool part of LiteFrom or
// more whose content can be cut to what its row shows; any other is itself.
func Light(ev Event) Event {
	if ev.Kind != PartTool || len(ev.Text)+len(ev.Content) < LiteFrom {
		return ev
	}
	content, ok := liteContent(ev.Content)
	if !ok {
		return ev
	}
	ev.Text, ev.Content, ev.Lite = "", content, true
	return ev
}

// liteContent is a tool's content cut to the keys a collapsed row reads.
func liteContent(raw json.RawMessage) (json.RawMessage, bool) {
	var c map[string]json.RawMessage
	if json.Unmarshal(raw, &c) != nil {
		return nil, false
	}
	out := map[string]any{}
	for key, value := range c {
		switch {
		case liteKeys[key]:
			out[key] = value
		case key == "command":
			out[key] = firstLine(value)
		case key == "input" || key == "arguments":
			var input map[string]json.RawMessage
			if json.Unmarshal(value, &input) != nil {
				continue
			}
			kept := map[string]json.RawMessage{}
			for k, v := range input {
				if liteInputKeys[k] {
					kept[k] = firstLine(v)
				}
			}
			out[key] = kept
		case liteDrop[key] != nil:
			var info map[string]json.RawMessage
			if json.Unmarshal(value, &info) != nil {
				continue
			}
			for k := range liteDrop[key] {
				delete(info, k)
			}
			out[key] = info
		}
	}
	lite, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return lite, true
}

// firstLine is a JSON string cut to its first line and liteLine characters;
// any other value is itself.
func firstLine(value json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(value, &s) != nil {
		return value
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > liteLine {
		s = string(r[:liteLine])
	}
	out, _ := json.Marshal(s)
	return out
}

// finished is whether a part's state says it is done changing on its own.
func finished(state string) bool {
	return state != PartRunning && state != PartStreaming
}

// askPartID is the part a question's payload names, or "".
func askPartID(payload json.RawMessage) string {
	var p struct {
		PartID string `json:"part_id"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	return p.PartID
}
