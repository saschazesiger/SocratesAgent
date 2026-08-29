package backends

import (
	"strings"
	"testing"
)

func collect(p parser, lines []string) []Event {
	var events []Event
	for _, line := range lines {
		p.Line(line, func(e Event) { events = append(events, e) })
	}
	return events
}

func find(events []Event, kind string) *Event {
	for i := range events {
		if events[i].Kind == kind {
			return &events[i]
		}
	}
	return nil
}

func TestClaudeParser(t *testing.T) {
	p := newClaudeParser()
	events := collect(p, []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`,
		`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking","thinking":"let me look"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"cat a.txt"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":[{"type":"text","text":"hi"}]}]}}`,
		`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"It says hi."}]}}`,
		`{"type":"rate_limit_event","rate_limit_info":{}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"It says hi.","total_cost_usd":0.01,"num_turns":2,"duration_ms":1200}`,
	})

	if p.Final() != "It says hi." {
		t.Fatalf("final = %q", p.Final())
	}
	if p.Meta()["model"] != "claude-opus-5" {
		t.Errorf("model meta missing: %#v", p.Meta())
	}
	if p.Meta()["turns"] != 2 {
		t.Errorf("turns meta = %v", p.Meta()["turns"])
	}
	if think := find(events, EventThinking); think == nil || think.Body != "let me look" {
		t.Errorf("thinking event missing: %#v", events)
	}
	var tool *Event
	for i := range events {
		if events[i].Kind == EventTool && events[i].Status == "done" {
			tool = &events[i]
		}
	}
	if tool == nil {
		t.Fatalf("no completed tool event in %#v", events)
	}
	if tool.Title != "Bash" || tool.Body != "cat a.txt" {
		t.Errorf("tool event = %#v", tool)
	}
	if got, _ := tool.Detail["result"].(string); got != "hi" {
		t.Errorf("tool result = %v", tool.Detail["result"])
	}
}

func TestCodexParser(t *testing.T) {
	p := newCodexParser()
	events := collect(p, []string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"i1","item_type":"command_execution","command":"ls","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"i1","item_type":"command_execution","command":"ls","aggregated_output":"a.txt","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"i2","item_type":"reasoning","text":"thinking about it"}}`,
		`{"type":"item.completed","item":{"id":"i3","item_type":"agent_message","text":"Content is hi."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10}}`,
	})
	if p.Final() != "Content is hi." {
		t.Fatalf("final = %q", p.Final())
	}
	if p.Meta()["thread_id"] != "t1" {
		t.Errorf("thread meta = %v", p.Meta()["thread_id"])
	}
	var shell *Event
	for i := range events {
		if events[i].Kind == EventTool {
			shell = &events[i]
		}
	}
	if shell == nil || shell.Status != "done" || shell.Body != "ls" {
		t.Errorf("shell event = %#v", shell)
	}
	if find(events, EventThinking) == nil {
		t.Errorf("reasoning event missing")
	}
}

func TestCodexParserTurnFailed(t *testing.T) {
	p := newCodexParser()
	events := collect(p, []string{`{"type":"turn.failed","error":{"message":"boom"}}`})
	e := find(events, EventError)
	if e == nil || e.Body != "boom" {
		t.Fatalf("expected error event, got %#v", events)
	}
}

func TestOpenCodeParser(t *testing.T) {
	p := newOpenCodeParser()
	events := collect(p, []string{
		`[multi-auth] refreshing token`,
		`{"type":"step_start","part":{"id":"p1","type":"step-start"}}`,
		`{"type":"tool_use","part":{"id":"p2","type":"tool","tool":"read","callID":"c1","state":{"status":"completed","input":{"filePath":"/tmp/a.txt"},"output":"hi","title":"a.txt"}}}`,
		`{"type":"text","part":{"id":"p3","type":"text","text":"It says hi."}}`,
		`{"type":"step_finish","part":{"id":"p4","tokens":{"total":10},"cost":0.002}}`,
	})
	if p.Final() != "It says hi." {
		t.Fatalf("final = %q", p.Final())
	}
	if p.Meta()["cost_usd"] != 0.002 {
		t.Errorf("cost meta = %v", p.Meta()["cost_usd"])
	}
	if log := find(events, EventLog); log == nil || !strings.Contains(log.Body, "multi-auth") {
		t.Errorf("non JSON line should surface as a log event: %#v", events)
	}
	tool := find(events, EventTool)
	if tool == nil || tool.Title != "read" || tool.Status != "done" {
		t.Errorf("tool event = %#v", tool)
	}
}

func TestStripANSI(t *testing.T) {
	got := stripANSI("\x1b[31mred\x1b[0m text")
	if got != "red text" {
		t.Fatalf("got %q", got)
	}
}

func TestSummarizeToolInput(t *testing.T) {
	if got := summarizeToolInput("Bash", map[string]any{"command": "ls -la"}); got != "ls -la" {
		t.Errorf("got %q", got)
	}
	if got := summarizeToolInput("Read", map[string]any{"file_path": "/tmp/x"}); got != "/tmp/x" {
		t.Errorf("got %q", got)
	}
}
