package backends

import (
	"fmt"
	"strings"
)

// ------------------------------------------------------------ claude code

// claudeParser understands `claude -p --output-format stream-json --verbose`.
type claudeParser struct {
	final      string
	meta       map[string]any
	toolName   map[string]string
	toolBody   map[string]string
	toolDetail map[string]map[string]any
	seq        int
}

func newClaudeParser() *claudeParser {
	return &claudeParser{
		meta:       map[string]any{},
		toolName:   map[string]string{},
		toolBody:   map[string]string{},
		toolDetail: map[string]map[string]any{},
	}
}

func (p *claudeParser) Final() string        { return p.final }
func (p *claudeParser) Meta() map[string]any { return p.meta }

func (p *claudeParser) Line(line string, emit Emitter) {
	m, ok := jsonLine(line)
	if !ok {
		if s := strings.TrimSpace(stripANSI(line)); s != "" {
			p.seq++
			emit(Event{ID: fmt.Sprintf("log-%d", p.seq), Kind: EventLog, Body: truncate(s, 2000), Status: "done"})
		}
		return
	}
	switch str(m, "type") {
	case "system":
		if str(m, "subtype") == "init" {
			p.meta["model"] = str(m, "model")
			p.meta["session_id"] = str(m, "session_id")
			emit(Event{ID: "session", Kind: EventStatus, Title: "Session started",
				Body: str(m, "model"), Status: "done"})
		}
	case "assistant":
		msg := obj(m, "message")
		msgID := str(msg, "id")
		for i, raw := range arr(msg, "content") {
			block, _ := raw.(map[string]any)
			if block == nil {
				continue
			}
			id := fmt.Sprintf("%s-%d", msgID, i)
			switch str(block, "type") {
			case "text":
				if s := strings.TrimSpace(str(block, "text")); s != "" {
					emit(Event{ID: id, Kind: EventText, Body: s, Status: "done"})
				}
			case "thinking":
				if s := strings.TrimSpace(str(block, "thinking")); s != "" {
					emit(Event{ID: id, Kind: EventThinking, Title: "Thinking", Body: truncate(s, 4000), Status: "done"})
				}
			case "tool_use":
				toolID := str(block, "id")
				name := str(block, "name")
				input := block["input"]
				body := summarizeToolInput(name, input)
				p.toolName[toolID] = name
				p.toolBody[toolID] = body
				p.toolDetail[toolID] = map[string]any{"input": input}
				emit(Event{ID: toolID, Kind: EventTool, Title: name, Body: body,
					Status: "running", Detail: map[string]any{"input": input}})
			}
		}
	case "user":
		msg := obj(m, "message")
		for _, raw := range arr(msg, "content") {
			block, _ := raw.(map[string]any)
			if block == nil || str(block, "type") != "tool_result" {
				continue
			}
			toolID := str(block, "tool_use_id")
			isErr, _ := block["is_error"].(bool)
			status := "done"
			if isErr {
				status = "failed"
			}
			detail := p.toolDetail[toolID]
			if detail == nil {
				detail = map[string]any{}
			}
			detail["result"] = truncate(textOf(block["content"]), 4000)
			name := p.toolName[toolID]
			if name == "" {
				name = "tool"
			}
			emit(Event{ID: toolID, Kind: EventTool, Title: name, Body: p.toolBody[toolID],
				Status: status, Detail: detail})
		}
	case "result":
		if s, ok := m["result"].(string); ok && strings.TrimSpace(s) != "" {
			p.final = s
		}
		if v, ok := m["total_cost_usd"].(float64); ok {
			p.meta["cost_usd"] = v
		}
		if v, ok := m["num_turns"].(float64); ok {
			p.meta["turns"] = int(v)
		}
		if v, ok := m["duration_ms"].(float64); ok {
			p.meta["duration_ms"] = int(v)
		}
		if isErr, _ := m["is_error"].(bool); isErr {
			p.seq++
			emit(Event{ID: fmt.Sprintf("err-%d", p.seq), Kind: EventError,
				Title: "Agent reported an error", Body: truncate(str(m, "result"), 2000), Status: "failed"})
		}
	}
}

// ------------------------------------------------------------------ codex

// codexParser understands `codex exec --json`.
type codexParser struct {
	final string
	meta  map[string]any
	seq   int
}

func newCodexParser() *codexParser { return &codexParser{meta: map[string]any{}} }

func (p *codexParser) Final() string        { return p.final }
func (p *codexParser) Meta() map[string]any { return p.meta }

func (p *codexParser) Line(line string, emit Emitter) {
	m, ok := jsonLine(line)
	if !ok {
		if s := strings.TrimSpace(stripANSI(line)); s != "" {
			p.seq++
			emit(Event{ID: fmt.Sprintf("log-%d", p.seq), Kind: EventLog, Body: truncate(s, 2000), Status: "done"})
		}
		return
	}
	typ := str(m, "type")
	// Older codex builds wrap everything in {"id":..,"msg":{"type":..}}.
	if inner := obj(m, "msg"); inner != nil && typ == "" {
		p.item(str(m, "id"), str(inner, "type"), inner, "completed", emit)
		return
	}
	switch {
	case typ == "thread.started":
		p.meta["thread_id"] = str(m, "thread_id")
		emit(Event{ID: "session", Kind: EventStatus, Title: "Session started", Status: "done"})
	case typ == "turn.started":
		// nothing worth showing
	case strings.HasPrefix(typ, "item."):
		item := obj(m, "item")
		if item == nil {
			return
		}
		state := "running"
		if typ == "item.completed" {
			state = "done"
		}
		itemType := str(item, "item_type")
		if itemType == "" {
			itemType = str(item, "type")
		}
		p.item(str(item, "id"), itemType, item, state, emit)
	case typ == "turn.completed":
		if u := obj(m, "usage"); u != nil {
			p.meta["usage"] = u
		}
	case typ == "turn.failed":
		msg := "the turn failed"
		if e := obj(m, "error"); e != nil {
			if s := str(e, "message"); s != "" {
				msg = s
			}
		}
		p.seq++
		emit(Event{ID: fmt.Sprintf("err-%d", p.seq), Kind: EventError, Title: "Turn failed",
			Body: truncate(msg, 2000), Status: "failed"})
	case typ == "error":
		p.seq++
		emit(Event{ID: fmt.Sprintf("err-%d", p.seq), Kind: EventError, Title: "Error",
			Body: truncate(str(m, "message"), 2000), Status: "failed"})
	}
}

func (p *codexParser) item(id, itemType string, item map[string]any, state string, emit Emitter) {
	if id == "" {
		p.seq++
		id = fmt.Sprintf("item-%d", p.seq)
	}
	switch itemType {
	case "agent_message", "assistant_message":
		text := str(item, "text")
		if text == "" {
			text = str(item, "message")
		}
		if strings.TrimSpace(text) == "" {
			return
		}
		if state == "done" {
			p.final = text
		}
		emit(Event{ID: id, Kind: EventText, Body: text, Status: state})
	case "reasoning", "agent_reasoning":
		text := str(item, "text")
		if text == "" {
			text = str(item, "summary")
		}
		if strings.TrimSpace(text) == "" {
			return
		}
		emit(Event{ID: id, Kind: EventThinking, Title: "Thinking", Body: truncate(text, 4000), Status: state})
	case "command_execution", "exec_command":
		cmd := str(item, "command")
		out := str(item, "aggregated_output")
		if out == "" {
			out = str(item, "output")
		}
		status := state
		if s := str(item, "status"); s == "failed" {
			status = "failed"
		} else if s == "completed" {
			status = "done"
		}
		if code, ok := item["exit_code"].(float64); ok && code != 0 {
			status = "failed"
		}
		emit(Event{ID: id, Kind: EventTool, Title: "Shell", Body: truncate(cmd, 500), Status: status,
			Detail: map[string]any{"command": cmd, "result": truncate(out, 4000), "exit_code": item["exit_code"]}})
	case "file_change", "patch_apply":
		files := []string{}
		for _, raw := range arr(item, "changes") {
			ch, _ := raw.(map[string]any)
			if ch == nil {
				continue
			}
			path := str(ch, "path")
			kind := str(ch, "kind")
			if path != "" {
				files = append(files, strings.TrimSpace(kind+" "+path))
			}
		}
		body := strings.Join(files, "\n")
		if body == "" {
			body = compactJSON(item["changes"])
		}
		emit(Event{ID: id, Kind: EventTool, Title: "File changes", Body: truncate(body, 2000), Status: state,
			Detail: map[string]any{"changes": item["changes"]}})
	case "mcp_tool_call":
		title := strings.TrimSpace(str(item, "server") + " · " + str(item, "tool"))
		emit(Event{ID: id, Kind: EventTool, Title: "MCP " + title,
			Body: truncate(compactJSON(item["arguments"]), 500), Status: state,
			Detail: map[string]any{"result": truncate(compactJSON(item["result"]), 4000)}})
	case "web_search":
		emit(Event{ID: id, Kind: EventTool, Title: "Web search", Body: str(item, "query"), Status: state})
	case "todo_list", "plan_update":
		items := []string{}
		for _, raw := range arr(item, "items") {
			it, _ := raw.(map[string]any)
			if it == nil {
				continue
			}
			mark := "○"
			if done, _ := it["completed"].(bool); done {
				mark = "●"
			}
			if s := str(it, "status"); s == "completed" {
				mark = "●"
			}
			items = append(items, mark+" "+str(it, "text"))
		}
		emit(Event{ID: id, Kind: EventStatus, Title: "Plan", Body: strings.Join(items, "\n"), Status: state})
	case "error":
		emit(Event{ID: id, Kind: EventError, Title: "Error",
			Body: truncate(str(item, "message"), 2000), Status: "failed"})
	default:
		if text := str(item, "text"); text != "" {
			emit(Event{ID: id, Kind: EventStatus, Title: itemType, Body: truncate(text, 1000), Status: state})
		}
	}
}

// --------------------------------------------------------------- opencode

// openCodeParser understands `opencode run --format json`.
type openCodeParser struct {
	final string
	meta  map[string]any
	seq   int
}

func newOpenCodeParser() *openCodeParser { return &openCodeParser{meta: map[string]any{}} }

func (p *openCodeParser) Final() string        { return p.final }
func (p *openCodeParser) Meta() map[string]any { return p.meta }

func (p *openCodeParser) Line(line string, emit Emitter) {
	m, ok := jsonLine(line)
	if !ok {
		if s := strings.TrimSpace(stripANSI(line)); s != "" {
			p.seq++
			emit(Event{ID: fmt.Sprintf("log-%d", p.seq), Kind: EventLog, Body: truncate(s, 2000), Status: "done"})
		}
		return
	}
	part := obj(m, "part")
	id := str(part, "id")
	if id == "" {
		p.seq++
		id = fmt.Sprintf("part-%d", p.seq)
	}
	switch str(m, "type") {
	case "text":
		text := str(part, "text")
		if strings.TrimSpace(text) == "" {
			return
		}
		p.final = text
		emit(Event{ID: id, Kind: EventText, Body: text, Status: "done"})
	case "reasoning":
		text := str(part, "text")
		if strings.TrimSpace(text) == "" {
			return
		}
		emit(Event{ID: id, Kind: EventThinking, Title: "Thinking", Body: truncate(text, 4000), Status: "done"})
	case "tool_use", "tool":
		state := obj(part, "state")
		name := str(part, "tool")
		status := "running"
		switch str(state, "status") {
		case "completed":
			status = "done"
		case "error":
			status = "failed"
		}
		title := str(state, "title")
		body := title
		if body == "" {
			body = summarizeToolInput(name, state["input"])
		}
		emit(Event{ID: id, Kind: EventTool, Title: name, Body: truncate(body, 500), Status: status,
			Detail: map[string]any{
				"input":  state["input"],
				"result": truncate(str(state, "output"), 4000),
			}})
	case "step_finish":
		if tok := obj(part, "tokens"); tok != nil {
			p.meta["tokens"] = tok
		}
		if c, ok := part["cost"].(float64); ok && c > 0 {
			p.meta["cost_usd"] = c
		}
	case "error":
		body := str(m, "error")
		if body == "" {
			body = compactJSON(m["error"])
		}
		emit(Event{ID: id, Kind: EventError, Title: "Error", Body: truncate(body, 2000), Status: "failed"})
	case "session_error":
		emit(Event{ID: id, Kind: EventError, Title: "Session error",
			Body: truncate(compactJSON(m), 2000), Status: "failed"})
	}
}

// ------------------------------------------------------------------ plain

// textParser is the fallback for custom commands: every line becomes a log
// entry and the whole output is the answer.
type textParser struct {
	lines []string
	seq   int
}

func newTextParser() *textParser { return &textParser{} }

func (p *textParser) Final() string        { return strings.Join(p.lines, "\n") }
func (p *textParser) Meta() map[string]any { return map[string]any{} }

func (p *textParser) Line(line string, emit Emitter) {
	s := stripANSI(line)
	p.lines = append(p.lines, s)
	if strings.TrimSpace(s) == "" {
		return
	}
	p.seq++
	emit(Event{ID: fmt.Sprintf("out-%d", p.seq), Kind: EventLog, Body: truncate(s, 2000), Status: "done"})
}

// summarizeToolInput renders the most relevant argument of a tool call.
func summarizeToolInput(name string, input any) string {
	m, _ := input.(map[string]any)
	if m == nil {
		return truncate(compactJSON(input), 300)
	}
	for _, key := range []string{"command", "file_path", "filePath", "path", "pattern", "url", "query", "description", "prompt"} {
		if v := str(m, key); v != "" {
			return truncate(v, 300)
		}
	}
	return truncate(compactJSON(m), 300)
}
