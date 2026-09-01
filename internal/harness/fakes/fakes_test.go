package fakes

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests freeze the raw wire output of the three fakes. Every FK item of
// DESIGN.md §10.1 has at least one test here that fails if the pin is broken,
// because the adapters in WP2/3/4 are written against these bytes.

func TestMain(m *testing.M) { Main(m) }

const readTimeout = 20 * time.Second

// ---------------------------------------------------------------- plumbing

// proc is a fake started as a subprocess, with its stdout lines on a channel.
type proc struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan string
	stderr *bytes.Buffer

	mu     sync.Mutex
	waited bool
	code   int
}

func start(t *testing.T, name string, env []string, args ...string) *proc {
	t.Helper()
	dir := Build(t)
	cmd := exec.Command(filepath.Join(dir, name), args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Dir = t.TempDir()

	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errbuf bytes.Buffer
	cmd.Stderr = &errbuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{t: t, cmd: cmd, in: in, lines: make(chan string, 4096), stderr: &errbuf}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	t.Cleanup(func() {
		_ = in.Close()
		_ = cmd.Process.Kill()
		_, _ = p.wait()
	})
	return p
}

func (p *proc) send(v string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, v+"\n"); err != nil {
		p.t.Fatalf("write: %v", err)
	}
}

func (p *proc) sendJSON(v any) {
	p.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	p.send(string(b))
}

func (p *proc) nextRaw() string {
	p.t.Helper()
	select {
	case l, ok := <-p.lines:
		if !ok {
			p.t.Fatalf("stdout closed while waiting for a line (stderr: %q)", p.stderr.String())
		}
		return l
	case <-time.After(readTimeout):
		p.t.Fatalf("timed out waiting for a line (stderr: %q)", p.stderr.String())
		return ""
	}
}

func (p *proc) next() map[string]any {
	p.t.Helper()
	return decode(p.t, p.nextRaw())
}

// nextOf reads until a line whose "type" (claude) or "method" (codex) matches,
// returning it and everything skipped.
func (p *proc) until(match func(map[string]any) bool) map[string]any {
	p.t.Helper()
	for i := 0; i < 500; i++ {
		m := p.next()
		if match(m) {
			return m
		}
	}
	p.t.Fatal("never saw the frame we were waiting for")
	return nil
}

// rest drains everything until stdout closes.
func (p *proc) rest() []map[string]any {
	p.t.Helper()
	var out []map[string]any
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				return out
			}
			out = append(out, decode(p.t, l))
		case <-time.After(readTimeout):
			p.t.Fatal("timed out draining stdout")
		}
	}
}

func (p *proc) closeStdin() { _ = p.in.Close() }

// silentFor fails unless the process writes nothing for d.
func (p *proc) silentFor(d time.Duration) {
	p.t.Helper()
	select {
	case l, ok := <-p.lines:
		if ok {
			p.t.Fatalf("expected silence, got %s", l)
		}
		p.t.Fatal("expected silence, stdout closed")
	case <-time.After(d):
	}
}

func (p *proc) wait() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waited {
		return p.code, nil
	}
	p.waited = true
	err := p.cmd.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
		p.code = 0
	case errors.As(err, &ee):
		p.code = ee.ExitCode()
	default:
		return 0, err
	}
	return p.code, nil
}

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("not JSON: %s: %v", line, err)
	}
	return m
}

// dig walks a decoded JSON value by key/index.
func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func str(v any, path ...any) string {
	s, _ := dig(v, path...).(string)
	return s
}

func num(v any, path ...any) float64 {
	f, _ := dig(v, path...).(float64)
	return f
}

func boolean(v any, path ...any) bool {
	b, _ := dig(v, path...).(bool)
	return b
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func argvFile(t *testing.T) (string, func() [][]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "argv.jsonl")
	return path, func() [][]string {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var out [][]string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == "" {
				continue
			}
			var rec []string
			if err := json.Unmarshal([]byte(l), &rec); err != nil {
				t.Fatalf("argv record %q: %v", l, err)
			}
			out = append(out, rec)
		}
		return out
	}
}

func scriptEnv(steps string) []string { return []string{"FAKE_SCRIPT=" + steps} }

// ------------------------------------------------------------------- FK-3

func TestBuildCompilesTheThreeFakesOncePerTestBinary(t *testing.T) {
	dir := Build(t)
	if again := Build(t); again != dir {
		t.Fatalf("Build returned two directories: %q and %q", dir, again)
	}
	for name := range Binaries {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not built: %v", name, err)
		}
	}
	if got := PathWith(dir); !strings.HasPrefix(got, dir+string(os.PathListSeparator)) {
		t.Errorf("PathWith did not prepend the directory: %q", got)
	}
}

// --------------------------------------------------------------- fakeclaude

const claudeArgs = "-p --output-format stream-json --input-format stream-json --verbose --include-partial-messages"

func claudeArgv(extra ...string) []string {
	return append(strings.Fields(claudeArgs), extra...)
}

func startClaude(t *testing.T, steps string, extra ...string) *proc {
	t.Helper()
	return start(t, "claude", scriptEnv(steps), claudeArgv(extra...)...)
}

func startClaudeEnv(t *testing.T, steps string, env []string, extra ...string) *proc {
	t.Helper()
	return start(t, "claude", append(scriptEnv(steps), env...), claudeArgv(extra...)...)
}

// firstTurn sends the first user line and consumes the system/init line that
// opens it. By default the fake writes nothing until it is prompted and init
// is the first line of the first turn (E-1), so every test that wants to read
// a turn starts here.
func (p *proc) firstTurn(text string) map[string]any {
	p.t.Helper()
	p.send(userLine(text))
	m := p.next()
	if str(m, "type") != "system" || str(m, "subtype") != "init" {
		p.t.Fatalf("the first turn must open with system/init, got %v", m)
	}
	return m
}

func userLine(text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	return string(b)
}

func TestClaudeRejectsAnIncompleteArgvAndStillRecordsIt(t *testing.T) {
	path, read := argvFile(t)
	p := start(t, "claude", []string{"FAKE_ARGV_FILE=" + path}, "-p", "--model", "sonnet")
	code, err := p.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "exit code", code, 2)
	recs := read()
	if len(recs) != 1 {
		t.Fatalf("argv records = %v", recs)
	}
	if !strings.Contains(strings.Join(recs[0], " "), "--model sonnet") {
		t.Errorf("argv record does not carry the flags: %v", recs[0])
	}
}

func TestClaudeRecordsItsFullArgv(t *testing.T) {
	path, read := argvFile(t)
	p := start(t, "claude",
		[]string{"FAKE_ARGV_FILE=" + path, `FAKE_SCRIPT=[{"do":"end","outcome":"ok"}]`},
		claudeArgv("--model", "sonnet", "--effort", "medium", "--permission-mode",
			"bypassPermissions", "--setting-sources", "project,local", "--name", "chat title",
			"--session-id", "e2e")...)
	p.closeStdin()
	if _, err := p.wait(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(read()[0], " ")
	for _, want := range []string{"--model sonnet", "--effort medium",
		"--permission-mode bypassPermissions", "--setting-sources project,local",
		"--name chat title", "--session-id e2e"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv record missing %q: %s", want, joined)
		}
	}
}

func TestClaudeInitLineIsExact(t *testing.T) {
	p := startClaude(t, `[{"do":"end","outcome":"ok"}]`, "--model", "sonnet")
	p.send(userLine("hi"))
	raw := p.nextRaw()
	m := decode(t, raw)

	eq(t, "type", str(m, "type"), "system")
	eq(t, "subtype", str(m, "subtype"), "init")
	eq(t, "model", str(m, "model"), "sonnet")
	eq(t, "permissionMode", str(m, "permissionMode"), "bypassPermissions")
	eq(t, "claude_code_version", str(m, "claude_code_version"), "2.1.252-fake")
	// FK-9: no --session-id, no --resume, so the fixed default id.
	eq(t, "session_id", str(m, "session_id"), "11111111-1111-4111-8111-111111111111")
	if str(m, "cwd") == "" || str(m, "uuid") == "" {
		t.Errorf("cwd/uuid missing: %s", raw)
	}
	want := []string{"interrupt_receipt_v1", "interrupt_cancel_queued_v1", "msg_lifecycle_v1"}
	for i, c := range want {
		eq(t, fmt.Sprintf("capabilities[%d]", i), str(m, "capabilities", i), c)
	}
	for i, tool := range []string{"Bash", "Read", "Task"} {
		eq(t, fmt.Sprintf("tools[%d]", i), str(m, "tools", i), tool)
	}
}

func TestClaudeInitEchoesTheSessionIDItWasGiven(t *testing.T) {
	// FK-9: the fake remembers nothing across processes; a resume test may
	// assert only that init.session_id echoes the id it was passed.
	for _, flag := range []string{"--session-id", "--resume"} {
		t.Run(flag, func(t *testing.T) {
			p := startClaude(t, `[{"do":"end","outcome":"ok"}]`, flag, "sess-abc")
			eq(t, "session_id", str(p.firstTurn("hi"), "session_id"), "sess-abc")
		})
	}
}

func TestClaudeReplaysUserMessagesOnlyWhenAsked(t *testing.T) {
	// FK-8.
	t.Run("with the flag", func(t *testing.T) {
		p := startClaude(t, `[{"do":"end","outcome":"ok"}]`, "--replay-user-messages")
		p.firstTurn("hello")
		echo := p.next()
		eq(t, "type", str(echo, "type"), "user")
		eq(t, "isReplay", boolean(echo, "isReplay"), true)
		eq(t, "text", str(echo, "message", "content", 0, "text"), "hello")
		eq(t, "next line", str(p.next(), "type"), "result")
	})
	t.Run("without the flag", func(t *testing.T) {
		p := startClaude(t, `[{"do":"end","outcome":"ok"}]`)
		p.firstTurn("hello")
		eq(t, "next line", str(p.next(), "type"), "result")
	})
}

func TestClaudeStreamsATextBlockInThreeChunksThenCompletesIt(t *testing.T) {
	// FK-4 / FK-5.
	p := startClaude(t, `[{"do":"text","text":"abcdef"},{"do":"end","outcome":"ok"}]`, "--model", "sonnet")
	p.firstTurn("hi")

	ms := p.next()
	eq(t, "message_start", str(ms, "event", "type"), "message_start")
	msgID := str(ms, "event", "message", "id")
	eq(t, "message id", msgID, "msg_1")
	eq(t, "role", str(ms, "event", "message", "role"), "assistant")
	eq(t, "model", str(ms, "event", "message", "model"), "sonnet")

	cbs := p.next()
	eq(t, "content_block_start", str(cbs, "event", "type"), "content_block_start")
	eq(t, "index", num(cbs, "event", "index"), 0)
	eq(t, "block type", str(cbs, "event", "content_block", "type"), "text")
	eq(t, "block text", str(cbs, "event", "content_block", "text"), "")

	var text string
	for i := 0; i < 3; i++ {
		d := p.next()
		eq(t, "content_block_delta", str(d, "event", "type"), "content_block_delta")
		eq(t, "index", num(d, "event", "index"), 0)
		eq(t, "delta type", str(d, "event", "delta", "type"), "text_delta")
		text += str(d, "event", "delta", "text")
	}
	eq(t, "assembled deltas", text, "abcdef")

	stop := p.next()
	eq(t, "content_block_stop", str(stop, "event", "type"), "content_block_stop")

	a := p.next()
	raw, _ := json.Marshal(a)
	eq(t, "type", str(a, "type"), "assistant")
	eq(t, "assistant message id", str(a, "message", "id"), msgID)
	eq(t, "one block", len(dig(a, "message", "content").([]any)), 1)
	eq(t, "block text", str(a, "message", "content", 0, "text"), "abcdef")
	// FK-5: the assistant line carries no index field anywhere.
	if bytes.Contains(raw, []byte(`"index"`)) {
		t.Errorf("assistant line must carry no index field: %s", raw)
	}
}

func TestClaudeTextToolTextYieldsTwoMessagesWithIndexZero(t *testing.T) {
	// FK-5 / S-1: the index restarts at 0 in each API message, so a turn with
	// a tool call in the middle yields msg_1:0 and msg_2:0 — the adapter's two
	// text ids must be distinct.
	p := startClaude(t, `[{"do":"text","text":"one"},
		{"do":"tool","name":"Bash","input":"go test ./...","output":"ok\n","exit":0},
		{"do":"text","text":"two"},{"do":"end","outcome":"ok"}]`)
	p.firstTurn("hi")

	type block struct {
		msg   string
		index float64
		text  string
	}
	var blocks []block
	var starts []block
	var msgs []string
	for {
		m := p.next()
		switch str(m, "type") {
		case "stream_event":
			switch str(m, "event", "type") {
			case "message_start":
				msgs = append(msgs, str(m, "event", "message", "id"))
			case "content_block_start":
				if str(m, "event", "content_block", "type") != "text" {
					continue
				}
				starts = append(starts, block{
					msg:   msgs[len(msgs)-1],
					index: num(m, "event", "index"),
				})
			}
		case "assistant":
			if str(m, "message", "content", 0, "type") == "text" {
				blocks = append(blocks, block{
					msg:  str(m, "message", "id"),
					text: str(m, "message", "content", 0, "text"),
				})
			}
		case "result":
			goto done
		}
	}
done:
	if len(blocks) != 2 {
		t.Fatalf("expected two completed text blocks, got %v", blocks)
	}
	eq(t, "first text", blocks[0].text, "one")
	eq(t, "second text", blocks[1].text, "two")
	if blocks[0].msg == blocks[1].msg {
		t.Errorf("both text blocks landed in the same message %q", blocks[0].msg)
	}
	eq(t, "messages opened", len(msgs), 2)
	if len(starts) != 2 {
		t.Fatalf("expected two content_block_start events, got %v", starts)
	}
	for i, s := range starts {
		eq(t, fmt.Sprintf("start[%d].index", i), s.index, 0)
		eq(t, fmt.Sprintf("start[%d].msg", i), s.msg, blocks[i].msg)
	}
}

func TestClaudeToolCallShape(t *testing.T) {
	// FK-6 and F-2: is_error comes from exit != 0 and there is no exit code
	// anywhere in the payload.
	for _, tc := range []struct {
		name    string
		exit    int
		isError bool
	}{{"ok", 0, false}, {"failure", 3, true}} {
		t.Run(tc.name, func(t *testing.T) {
			p := startClaude(t, fmt.Sprintf(
				`[{"do":"tool","name":"Bash","input":"go test ./...","output":"boom\n","exit":%d},{"do":"end","outcome":"ok"}]`, tc.exit))
			p.firstTurn("hi")

			a := p.until(func(m map[string]any) bool { return str(m, "type") == "assistant" })
			eq(t, "block type", str(a, "message", "content", 0, "type"), "tool_use")
			id := str(a, "message", "content", 0, "id")
			eq(t, "tool id", id, "toolu_1")
			eq(t, "tool name", str(a, "message", "content", 0, "name"), "Bash")
			eq(t, "tool input", str(a, "message", "content", 0, "input", "command"), "go test ./...")

			u := p.until(func(m map[string]any) bool { return str(m, "type") == "user" })
			rawUser := mustJSON(t, u)
			eq(t, "type", str(u, "type"), "user")
			eq(t, "result type", str(u, "message", "content", 0, "type"), "tool_result")
			eq(t, "tool_use_id", str(u, "message", "content", 0, "tool_use_id"), id)
			eq(t, "content", str(u, "message", "content", 0, "content"), "boom\n")
			eq(t, "is_error", boolean(u, "message", "content", 0, "is_error"), tc.isError)
			eq(t, "stdout", str(u, "tool_use_result", "stdout"), "boom\n")
			eq(t, "stderr", str(u, "tool_use_result", "stderr"), "")
			eq(t, "interrupted", boolean(u, "tool_use_result", "interrupted"), false)
			eq(t, "isImage", boolean(u, "tool_use_result", "isImage"), false)
			eq(t, "noOutputExpected", boolean(u, "tool_use_result", "noOutputExpected"), false)
			if strings.Contains(rawUser, "exit") {
				t.Errorf("F-2: no exit code may appear in the tool_result payload: %s", rawUser)
			}
		})
	}
}

func TestClaudeToolUseHasTheSameStreamCadenceAsText(t *testing.T) {
	// A tool_use block streams like any other block, and the message it ends
	// is closed with message_delta + message_stop before the tool result.
	p := startClaude(t, `[{"do":"text","text":"one"},
		{"do":"tool","name":"Bash","input":"echo hi","output":"hi\n","exit":0},
		{"do":"end","outcome":"ok"}]`)
	p.firstTurn("hi")

	var seq []string
	var toolStart map[string]any
	for {
		m := p.next()
		if str(m, "type") == "stream_event" {
			seq = append(seq, str(m, "event", "type"))
			if str(m, "event", "type") == "content_block_start" &&
				str(m, "event", "content_block", "type") == "tool_use" {
				toolStart = m
			}
		}
		if str(m, "type") == "result" {
			break
		}
	}
	want := []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Errorf("stream_event cadence:\n got %v\nwant %v", seq, want)
	}
	if toolStart == nil {
		t.Fatal("no content_block_start for the tool_use block")
	}
	// The tool_use block is index 1 of the same message, and its input starts
	// empty and arrives as input_json_delta.
	eq(t, "tool block index", num(toolStart, "event", "index"), 1)
	if in, ok := dig(toolStart, "event", "content_block", "input").(map[string]any); !ok || len(in) != 0 {
		t.Errorf("content_block_start must carry an empty input: %v", toolStart)
	}
}

func TestClaudeSubagentUsesTheTaskTool(t *testing.T) {
	p := startClaude(t, `[{"do":"subagent","input":"find the caller","output":"found it"},{"do":"end","outcome":"ok"}]`)
	p.firstTurn("hi")
	a := p.until(func(m map[string]any) bool { return str(m, "type") == "assistant" })
	eq(t, "name", str(a, "message", "content", 0, "name"), "Task")
	eq(t, "subagent_type", str(a, "message", "content", 0, "input", "subagent_type"), "general-purpose")
	eq(t, "description", str(a, "message", "content", 0, "input", "description"), "find the caller")
	p.until(func(m map[string]any) bool { return str(m, "type") == "user" })
	r := p.until(func(m map[string]any) bool { return str(m, "type") == "result" })
	eq(t, "spawned", num(r, "subagent_stats", "spawned"), 1)
}

func TestClaudeSubagentCountCanBeOverridden(t *testing.T) {
	p := startClaude(t, `[{"do":"text","text":"done"},{"do":"end","outcome":"ok","subagents":4}]`)
	p.firstTurn("hi")
	r := p.until(func(m map[string]any) bool { return str(m, "type") == "result" })
	eq(t, "spawned", num(r, "subagent_stats", "spawned"), 4)
}

func TestClaudeSuccessResultShape(t *testing.T) {
	// FK-7.
	p := startClaude(t, `[{"do":"text","text":"All tests pass."},{"do":"end","outcome":"ok"}]`)
	p.firstTurn("hi")
	r := p.until(func(m map[string]any) bool { return str(m, "type") == "result" })

	eq(t, "subtype", str(r, "subtype"), "success")
	eq(t, "is_error", boolean(r, "is_error"), false)
	eq(t, "terminal_reason", str(r, "terminal_reason"), "completed")
	eq(t, "num_turns", num(r, "num_turns"), 1)
	eq(t, "result", str(r, "result"), "All tests pass.")
	eq(t, "total_cost_usd", num(r, "total_cost_usd"), 0.0017)
	eq(t, "usage.input_tokens", num(r, "usage", "input_tokens"), 10)
	eq(t, "usage.output_tokens", num(r, "usage", "output_tokens"), 60)
	eq(t, "usage.cache_read_input_tokens", num(r, "usage", "cache_read_input_tokens"), 13615)
	eq(t, "usage.thinking_tokens", num(r, "usage", "output_tokens_details", "thinking_tokens"), 32)
	eq(t, "subagent_stats.spawned", num(r, "subagent_stats", "spawned"), 0)
	if _, ok := dig(r, "api_error_status").(float64); ok {
		t.Errorf("a successful result must not carry api_error_status")
	}
}

func TestClaudeErrorResultKeepsSubtypeSuccess(t *testing.T) {
	// FK-7: the bad-model trap. An adapter that keys off subtype rather than
	// is_error reads this as a success.
	p := startClaude(t, `[{"do":"end","outcome":"error","error":"model_not_found: nope"}]`)
	p.firstTurn("hi")
	r := p.until(func(m map[string]any) bool { return str(m, "type") == "result" })

	eq(t, "subtype", str(r, "subtype"), "success")
	eq(t, "is_error", boolean(r, "is_error"), true)
	eq(t, "terminal_reason", str(r, "terminal_reason"), "api_error")
	eq(t, "api_error_status", num(r, "api_error_status"), 404)
	eq(t, "result", str(r, "result"), "model_not_found: nope")
}

func TestClaudeInterruptDuringAnOpenTurn(t *testing.T) {
	// FK-10 (and FK-2's hang: the control channel stays served).
	p := startClaude(t, `[{"do":"text","text":"working"},{"do":"hang"}]`)
	p.firstTurn("hi")
	p.until(func(m map[string]any) bool {
		return str(m, "type") == "assistant"
	})

	p.send(`{"type":"control_request","request_id":"x1","request":{"subtype":"interrupt"}}`)
	resp := p.next()
	eq(t, "type", str(resp, "type"), "control_response")
	eq(t, "subtype", str(resp, "response", "subtype"), "success")
	eq(t, "request_id", str(resp, "response", "request_id"), "x1")
	if q, ok := dig(resp, "response", "response", "still_queued").([]any); !ok || len(q) != 0 {
		t.Errorf("still_queued must be an empty array, got %#v", dig(resp, "response", "response"))
	}

	r := p.next()
	eq(t, "type", str(r, "type"), "result")
	eq(t, "subtype", str(r, "subtype"), "error_during_execution")
	eq(t, "terminal_reason", str(r, "terminal_reason"), "aborted_tools")
	eq(t, "is_error", boolean(r, "is_error"), true)
}

func TestClaudeInterruptWhileIdleEmitsNothingFurther(t *testing.T) {
	// FK-10, second half.
	p := startClaude(t, `[{"do":"text","text":"done"},{"do":"end","outcome":"ok"}]`)
	p.firstTurn("hi")
	p.until(func(m map[string]any) bool { return str(m, "type") == "result" })

	p.send(`{"type":"control_request","request_id":"x9","request":{"subtype":"interrupt"}}`)
	resp := p.next()
	eq(t, "type", str(resp, "type"), "control_response")
	p.closeStdin()
	if extra := p.rest(); len(extra) != 0 {
		t.Errorf("an idle interrupt emitted %d further lines: %v", len(extra), extra)
	}
}

func TestClaudeRunsTheScriptOnEveryTurn(t *testing.T) {
	// FK-1.
	p := startClaude(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
	var shapes [][]string
	for turn := 0; turn < 2; turn++ {
		if turn == 0 {
			// init opens the first turn and only the first turn.
			p.firstTurn("go")
		} else {
			p.send(userLine("go"))
		}
		var seq []string
		for {
			m := p.next()
			seq = append(seq, str(m, "type")+"/"+str(m, "event", "type"))
			if str(m, "type") == "result" {
				break
			}
		}
		shapes = append(shapes, seq)
	}
	if strings.Join(shapes[0], ",") != strings.Join(shapes[1], ",") {
		t.Errorf("the second turn differs:\n%v\n%v", shapes[0], shapes[1])
	}
}

func TestClaudeDieFlushesEverythingWrittenSoFar(t *testing.T) {
	// FK-2.
	p := startClaude(t, `[{"do":"text","text":"partial"},{"do":"die","code":7}]`)
	p.firstTurn("hi")
	seen := false
	for _, m := range p.rest() {
		if str(m, "type") == "assistant" && str(m, "message", "content", 0, "text") == "partial" {
			seen = true
		}
		if str(m, "type") == "result" {
			t.Errorf("die must not end the turn")
		}
	}
	if !seen {
		t.Error("die did not flush the text block written before it")
	}
	code, err := p.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "exit code", code, 7)
}

func TestClaudeExitsZeroOnStdinEOFWithEmptyStderr(t *testing.T) {
	p := startClaude(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
	p.firstTurn("hi")
	p.until(func(m map[string]any) bool { return str(m, "type") == "result" })
	p.closeStdin()
	code, err := p.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "exit code", code, 0)
	eq(t, "stderr", p.stderr.String(), "")
}

func TestClaudeReasoningBlock(t *testing.T) {
	p := startClaude(t, `[{"do":"reason","text":"thinking hard"},{"do":"end","outcome":"ok"}]`)
	p.firstTurn("hi")
	p.until(func(m map[string]any) bool {
		return str(m, "event", "type") == "content_block_start"
	})
	a := p.until(func(m map[string]any) bool { return str(m, "type") == "assistant" })
	eq(t, "block type", str(a, "message", "content", 0, "type"), "thinking")
	eq(t, "thinking", str(a, "message", "content", 0, "thinking"), "thinking hard")
}

func TestClaudeGhostResumeReproducesTheMeasuredDeath(t *testing.T) {
	// E-2: `claude --resume <uuid>` for a session the CLI cannot read writes
	// no system/init at all — after a moment it writes one unprompted result
	// carrying num_turns:0 and exits 1 with the message on stderr.
	const ghost = "d48f102b-2683-47be-9c3d-86aeb9f4fc7b"
	state := t.TempDir()
	begun := time.Now()
	p := startClaudeEnv(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		[]string{"FAKE_GHOST_RESUME=1", "FAKE_STATE_DIR=" + state},
		"--resume", ghost, "--model", "sonnet")

	// Nothing comes out while the CLI is looking for the transcript.
	p.silentFor(100 * time.Millisecond)

	raw := p.nextRaw()
	if d := time.Since(begun); d < 200*time.Millisecond {
		t.Errorf("the ghost result arrived after %v, sooner than the pinned delay", d)
	}
	m := decode(t, raw)
	eq(t, "type", str(m, "type"), "result")
	eq(t, "subtype", str(m, "subtype"), "error_during_execution")
	eq(t, "is_error", boolean(m, "is_error"), true)
	// num_turns:0 plus the missing init is the discriminator: a real turn
	// always has init ahead of it and num_turns >= 1.
	eq(t, "num_turns", num(m, "num_turns"), 0)
	eq(t, "terminal_reason", str(m, "terminal_reason"), "api_error")
	eq(t, "result", str(m, "result"), "No conversation found with session ID: "+ghost)
	eq(t, "session_id", str(m, "session_id"), ghost)

	for _, extra := range p.rest() {
		t.Errorf("nothing may follow the ghost result, got %v", extra)
	}
	code, err := p.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "exit code", code, 1)
	eq(t, "stderr", strings.TrimSpace(p.stderr.String()),
		"No conversation found with session ID: "+ghost)
}

func TestClaudeGhostResumeIsConsumedOnFirstUse(t *testing.T) {
	// The flag is consumed via a marker in $FAKE_STATE_DIR, so the adapter's
	// one-shot relaunch succeeds and the whole R-3 path is testable.
	const ghost = "e5b11969-e49b-4fac-b5bc-c49dc759e7ff"
	state := t.TempDir()
	env := []string{"FAKE_GHOST_RESUME=1", "FAKE_STATE_DIR=" + state}

	first := startClaudeEnv(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		env, "--resume", ghost)
	eq(t, "first launch dies", str(first.next(), "subtype"), "error_during_execution")
	code, err := first.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "first exit code", code, 1)

	// The relaunch the adapter makes without --resume is unaffected anyway,
	// and even a second --resume of the same id now succeeds.
	second := startClaudeEnv(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		env, "--resume", ghost)
	eq(t, "session_id", str(second.firstTurn("hi"), "session_id"), ghost)
	second.until(func(m map[string]any) bool { return str(m, "type") == "result" })

	// A different session id is still armed.
	other := startClaudeEnv(t, `[{"do":"end","outcome":"ok"}]`, env, "--resume", "another-id")
	eq(t, "another id still dies", str(other.next(), "subtype"), "error_during_execution")
}

func TestClaudeGhostResumeOnlyFiresForResume(t *testing.T) {
	// It is a --resume behaviour: a fresh --session-id starts a new session
	// and must be unaffected, and without the flag --resume always works.
	t.Run("--session-id is unaffected", func(t *testing.T) {
		p := startClaudeEnv(t, `[{"do":"end","outcome":"ok"}]`,
			[]string{"FAKE_GHOST_RESUME=1", "FAKE_STATE_DIR=" + t.TempDir()},
			"--session-id", "brand-new")
		eq(t, "session_id", str(p.firstTurn("hi"), "session_id"), "brand-new")
	})
	t.Run("the flag is off by default", func(t *testing.T) {
		p := startClaude(t, `[{"do":"end","outcome":"ok"}]`, "--resume", "anything-at-all")
		eq(t, "session_id", str(p.firstTurn("hi"), "session_id"), "anything-at-all")
	})
}

func TestClaudeInitOpensTheFirstTurnAndOnlyTheFirst(t *testing.T) {
	// E-1: with --input-format stream-json the real CLI writes nothing until
	// it is prompted, and init arrives inside the first turn, ahead of the
	// --replay-user-messages echo.
	p := startClaude(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		"--replay-user-messages", "--session-id", "sess-1")
	p.silentFor(300 * time.Millisecond)

	initLine := p.firstTurn("go")
	eq(t, "session_id", str(initLine, "session_id"), "sess-1")
	echo := p.next()
	eq(t, "the echo follows init", str(echo, "type"), "user")
	eq(t, "isReplay", boolean(echo, "isReplay"), true)
	p.until(func(m map[string]any) bool { return str(m, "type") == "result" })

	// init is written once, not once per turn.
	p.send(userLine("again"))
	second := p.next()
	eq(t, "the second turn starts with the echo", str(second, "type"), "user")
	p.until(func(m map[string]any) bool { return str(m, "type") == "result" })
}

func TestClaudeInitAtStartIsTheOptIn(t *testing.T) {
	// The opt-in exists so a test can prove an adapter tolerates both orders.
	p := startClaudeEnv(t, `[{"do":"end","outcome":"ok"}]`,
		[]string{"FAKE_INIT_AT_START=1"}, "--session-id", "sess-2")
	m := p.next()
	eq(t, "subtype", str(m, "subtype"), "init")
	eq(t, "session_id", str(m, "session_id"), "sess-2")
	p.send(userLine("hi"))
	eq(t, "the turn does not repeat init", str(p.next(), "type"), "result")
}

// ---------------------------------------------------------------- fakecodex

func startCodex(t *testing.T, steps string, env ...string) *proc {
	t.Helper()
	return start(t, "codex", append(scriptEnv(steps), env...), "app-server", "--listen", "stdio://")
}

func codexInit(t *testing.T, p *proc) {
	t.Helper()
	p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]any{"name": "socrates", "version": "test"}}})
	res := p.next()
	eq(t, "userAgent", str(res, "result", "userAgent"), "fake/0.152.0")
	eq(t, "codexHome", str(res, "result", "codexHome"), "/tmp/fake-codex")
	eq(t, "platformFamily", str(res, "result", "platformFamily"), "unix")
	eq(t, "platformOs", str(res, "result", "platformOs"), "linux")
	// Unsolicited notification, immediately after the reply.
	n := p.next()
	eq(t, "unsolicited notification", str(n, "method"), "remoteControl/status/changed")
	p.sendJSON(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
}

func codexStartThread(t *testing.T, p *proc) string {
	t.Helper()
	p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "thread/start",
		"params": map[string]any{"cwd": "/tmp", "model": "gpt-5.4-mini",
			"config":         map[string]any{"model_reasoning_effort": "low"},
			"approvalPolicy": "never", "sandbox": "danger-full-access"}})
	// The notification arrives before the RPC result.
	n := p.next()
	eq(t, "notification first", str(n, "method"), "thread/started")
	res := p.next()
	eq(t, "result id", num(res, "id"), 2)
	return str(res, "result", "thread", "id")
}

func codexStartTurn(t *testing.T, p *proc, thread string, id int) string {
	t.Helper()
	p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": id, "method": "turn/start",
		"params": map[string]any{"threadId": thread,
			"input":  []any{map[string]any{"type": "text", "text": "go"}},
			"model":  "gpt-5.4-mini",
			"effort": "medium"}})
	res := p.next()
	turnID := str(res, "result", "turn", "id")
	eq(t, "status", str(res, "result", "turn", "status"), "inProgress")
	started := p.next()
	eq(t, "method", str(started, "method"), "turn/started")
	// FK-11: the notification's turn id equals the result's.
	eq(t, "turn/started id", str(started, "params", "turn", "id"), turnID)
	return turnID
}

// codexSkipUserMessage consumes the userMessage item every turn opens with.
func codexSkipUserMessage(t *testing.T, p *proc) {
	t.Helper()
	for i := 0; i < 2; i++ {
		m := p.next()
		if str(m, "params", "item", "type") != "userMessage" {
			t.Fatalf("expected the opening userMessage item, got %v", m)
		}
	}
}

func TestCodexHasNoGenerateJSONSchemaMode(t *testing.T) {
	// FK-15.
	p := start(t, "codex", scriptEnv(`[{"do":"end","outcome":"ok"}]`), "generate-json-schema")
	code, err := p.wait()
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Error("generate-json-schema must not be supported")
	}
}

func TestCodexModelListUsesTheObjectEffortForm(t *testing.T) {
	// FK-16.
	p := startCodex(t, `[{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "model/list", "params": map[string]any{}})
	res := p.next()
	data, ok := dig(res, "result", "data").([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("model/list data = %#v", dig(res, "result", "data"))
	}
	eq(t, "first isDefault", boolean(data[0], "isDefault"), true)
	eq(t, "second isDefault", boolean(data[1], "isDefault"), false)
	for i, m := range data {
		eq(t, fmt.Sprintf("model[%d].defaultReasoningEffort", i), str(m, "defaultReasoningEffort"), "medium")
		efforts, ok := dig(m, "supportedReasoningEfforts").([]any)
		if !ok {
			t.Fatalf("model[%d] supportedReasoningEfforts = %#v", i, dig(m, "supportedReasoningEfforts"))
		}
		want := []string{"low", "medium", "high", "xhigh"}
		if len(efforts) != len(want) {
			t.Fatalf("model[%d] efforts = %#v", i, efforts)
		}
		for j, e := range efforts {
			// The object form, not a bare string.
			if _, isString := e.(string); isString {
				t.Fatalf("FK-16: efforts must use the object form, got %#v", e)
			}
			eq(t, "reasoningEffort", str(e, "reasoningEffort"), want[j])
		}
	}
}

func TestCodexThreadStartAndResume(t *testing.T) {
	// FK-17.
	p := startCodex(t, `[{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	id := codexStartThread(t, p)
	if id == "" {
		t.Fatal("thread/start returned no thread id")
	}

	p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "thread/resume",
		"params": map[string]any{"threadId": "abc-123", "cwd": "/tmp", "excludeTurns": true}})
	res := p.next()
	eq(t, "echoed thread id", str(res, "result", "thread", "id"), "abc-123")
	eq(t, "sessionId", str(res, "result", "thread", "sessionId"), "abc-123")
	turns, ok := dig(res, "result", "thread", "turns").([]any)
	if !ok || len(turns) != 0 {
		t.Errorf("excludeTurns:true must yield an empty turns array, got %#v", dig(res, "result", "thread", "turns"))
	}
	eq(t, "sandbox", str(res, "result", "sandbox", "type"), "dangerFullAccess")
}

func TestCodexTurnStartRecordsModelAndEffort(t *testing.T) {
	// FK-11 / F-5.
	path, read := argvFile(t)
	p := startCodex(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`, "FAKE_ARGV_FILE="+path)
	codexInit(t, p)
	thread := codexStartThread(t, p)
	codexStartTurn(t, p, thread, 3)
	p.until(func(m map[string]any) bool { return str(m, "method") == "turn/completed" })

	var found bool
	for _, rec := range read() {
		if len(rec) == 3 && rec[0] == "turn/start" {
			eq(t, "model", rec[1], "model=gpt-5.4-mini")
			eq(t, "effort", rec[2], "effort=medium")
			found = true
		}
	}
	if !found {
		t.Errorf("no turn/start record in %v", read())
	}
}

func TestCodexCommandExecutionKeepsAggregatedOutputNull(t *testing.T) {
	// FK-12: an adapter that ignores the deltas and reads only the final
	// aggregatedOutput gets nothing.
	p := startCodex(t, `[{"do":"tool","input":"seq 2","output":"line-1\nline-2\n","exit":3},{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	thread := codexStartThread(t, p)
	turnID := codexStartTurn(t, p, thread, 3)
	codexSkipUserMessage(t, p)

	started := p.until(func(m map[string]any) bool { return str(m, "method") == "item/started" })
	eq(t, "item type", str(started, "params", "item", "type"), "commandExecution")
	itemID := str(started, "params", "item", "id")
	eq(t, "turnId", str(started, "params", "turnId"), turnID)
	eq(t, "command", str(started, "params", "item", "command"), "seq 2")
	eq(t, "status", str(started, "params", "item", "status"), "inProgress")
	if str(started, "params", "item", "cwd") == "" || str(started, "params", "item", "processId") == "" {
		t.Errorf("the started item must carry cwd and processId: %v", started)
	}
	if dig(started, "params", "item", "aggregatedOutput") != nil {
		t.Errorf("aggregatedOutput must be null on item/started")
	}
	if dig(started, "params", "item", "exitCode") != nil {
		t.Errorf("exitCode must be null on item/started")
	}

	var deltas []string
	for i := 0; i < 2; i++ {
		d := p.next()
		eq(t, "method", str(d, "method"), "item/commandExecution/outputDelta")
		eq(t, "itemId", str(d, "params", "itemId"), itemID)
		deltas = append(deltas, str(d, "params", "delta"))
	}
	eq(t, "assembled output", strings.Join(deltas, ""), "line-1\nline-2\n")

	done := p.next()
	eq(t, "method", str(done, "method"), "item/completed")
	eq(t, "status", str(done, "params", "item", "status"), "completed")
	eq(t, "exitCode", num(done, "params", "item", "exitCode"), 3)
	eq(t, "durationMs", num(done, "params", "item", "durationMs"), 12)
	if dig(done, "params", "item", "aggregatedOutput") != nil {
		t.Errorf("FK-12: aggregatedOutput must stay null on item/completed")
	}
}

func TestCodexAgentMessagePhases(t *testing.T) {
	p := startCodex(t, `[{"do":"text","text":"working"},
		{"do":"tool","input":"true","output":"","exit":0},
		{"do":"text","text":"DONE"},{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	thread := codexStartThread(t, p)
	codexStartTurn(t, p, thread, 3)

	var phases []string
	var deltas int
	var ids []string
	for {
		m := p.next()
		switch str(m, "method") {
		case "item/agentMessage/delta":
			deltas++
		case "item/completed":
			if str(m, "params", "item", "type") == "agentMessage" {
				phases = append(phases, str(m, "params", "item", "phase"))
				ids = append(ids, str(m, "params", "item", "id"))
			}
		case "turn/completed":
			goto done
		}
	}
done:
	if len(phases) != 2 {
		t.Fatalf("phases = %v", phases)
	}
	eq(t, "first phase", phases[0], "commentary")
	eq(t, "last phase", phases[1], "final_answer")
	eq(t, "delta count", deltas, 6)
	if ids[0] == ids[1] {
		t.Errorf("codex item ids must be unique within a turn: %v", ids)
	}
}

func TestCodexReasoningItem(t *testing.T) {
	p := startCodex(t, `[{"do":"reason","text":"pondering"},{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	thread := codexStartThread(t, p)
	codexStartTurn(t, p, thread, 3)
	codexSkipUserMessage(t, p)
	started := p.next()
	eq(t, "method", str(started, "method"), "item/started")
	eq(t, "type", str(started, "params", "item", "type"), "reasoning")
	done := p.next()
	eq(t, "method", str(done, "method"), "item/completed")
	eq(t, "summary text", str(done, "params", "item", "summary", 0, "text"), "pondering")
	// ReasoningItemReasoningSummary's only variant is summary_text; an adapter
	// switching on the tag must see the schema's spelling.
	eq(t, "summary type", str(done, "params", "item", "summary", 0, "type"), "summary_text")
	if c, ok := dig(done, "params", "item", "content").([]any); !ok || len(c) != 0 {
		t.Errorf("content must be an empty array, got %#v", dig(done, "params", "item", "content"))
	}
}

func TestCodexSubagentItemsMatchTheSchema(t *testing.T) {
	// SubAgentActivityThreadItem requires agentPath, agentThreadId, id, kind
	// and type — and carries nothing else.
	p := startCodex(t, `[{"do":"subagent","input":"general-purpose: find the caller","output":"found"},{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	thread := codexStartThread(t, p)
	codexStartTurn(t, p, thread, 3)
	codexSkipUserMessage(t, p)

	started := p.next()
	eq(t, "method", str(started, "method"), "item/started")
	eq(t, "type", str(started, "params", "item", "type"), "subAgentActivity")
	eq(t, "kind", str(started, "params", "item", "kind"), "started")
	eq(t, "agentPath", str(started, "params", "item", "agentPath"), "general-purpose: find the caller")
	id := str(started, "params", "item", "id")
	agentThread := str(started, "params", "item", "agentThreadId")
	if id == "" || agentThread == "" {
		t.Fatalf("id/agentThreadId missing: %v", started)
	}
	item, _ := dig(started, "params", "item").(map[string]any)
	for k := range item {
		switch k {
		case "type", "id", "kind", "agentPath", "agentThreadId":
		default:
			t.Errorf("subAgentActivity must carry no %q field", k)
		}
	}

	done := p.next()
	eq(t, "method", str(done, "method"), "item/completed")
	eq(t, "kind", str(done, "params", "item", "kind"), "completed")
	eq(t, "id", str(done, "params", "item", "id"), id)
	eq(t, "agentThreadId", str(done, "params", "item", "agentThreadId"), agentThread)
}

func TestCodexEveryTurnOpensWithAUserMessageItem(t *testing.T) {
	// Research codex.md 3: item/started + item/completed of a userMessage
	// echoing the turn's input precede everything else.
	p := startCodex(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	thread := codexStartThread(t, p)
	p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "turn/start",
		"params": map[string]any{"threadId": thread,
			"input":  []any{map[string]any{"type": "text", "text": "count to three"}},
			"model":  "gpt-5.4-mini",
			"effort": "medium"}})
	p.next() // the RPC result
	p.next() // turn/started

	started := p.next()
	eq(t, "method", str(started, "method"), "item/started")
	eq(t, "type", str(started, "params", "item", "type"), "userMessage")
	eq(t, "content", str(started, "params", "item", "content", 0, "text"), "count to three")
	eq(t, "content type", str(started, "params", "item", "content", 0, "type"), "text")
	done := p.next()
	eq(t, "method", str(done, "method"), "item/completed")
	eq(t, "type", str(done, "params", "item", "type"), "userMessage")
	eq(t, "id", str(done, "params", "item", "id"), str(started, "params", "item", "id"))
}

func TestCodexTurnEndsAndTheirTraps(t *testing.T) {
	// FK-13.
	t.Run("ok", func(t *testing.T) {
		p := startCodex(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
		codexInit(t, p)
		thread := codexStartThread(t, p)
		turnID := codexStartTurn(t, p, thread, 3)
		usage := p.until(func(m map[string]any) bool {
			return str(m, "method") == "thread/tokenUsage/updated"
		})
		eq(t, "totalTokens", num(usage, "params", "tokenUsage", "total", "totalTokens"), 11341)
		eq(t, "lastTokens", num(usage, "params", "tokenUsage", "last", "totalTokens"), 11341)
		eq(t, "modelContextWindow", num(usage, "params", "tokenUsage", "modelContextWindow"), 258400)
		done := p.next()
		eq(t, "method", str(done, "method"), "turn/completed")
		eq(t, "turn id", str(done, "params", "turn", "id"), turnID)
		eq(t, "status", str(done, "params", "turn", "status"), "completed")
		limits := p.next()
		eq(t, "rate limits after the end", str(limits, "method"), "account/rateLimits/updated")
	})

	t.Run("error has no turn/completed", func(t *testing.T) {
		p := startCodex(t, `[{"do":"end","outcome":"error","error":"the model is not supported"}]`)
		codexInit(t, p)
		thread := codexStartThread(t, p)
		codexStartTurn(t, p, thread, 3)
		w := p.until(func(m map[string]any) bool { return str(m, "method") == "warning" })
		eq(t, "warning message", str(w, "params", "message"),
			"Model metadata for `gpt-5.4-mini` not found. Defaulting to fallback "+
				"metadata; this can degrade performance and cause issues.")
		st := p.next()
		eq(t, "method", str(st, "method"), "thread/status/changed")
		eq(t, "status type", str(st, "params", "status", "type"), "systemError")
		e := p.next()
		eq(t, "method", str(e, "method"), "error")
		eq(t, "willRetry", boolean(e, "params", "willRetry"), false)
		eq(t, "message", str(e, "params", "error", "message"), "the model is not supported")

		// FK-13: the turn is closed, so a cancel arriving inside the
		// adapter's 30 s post-error window cannot resurrect it.
		p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "turn/interrupt",
			"params": map[string]any{"threadId": thread, "turnId": "turn_1"}})
		reply := p.until(func(m map[string]any) bool { return str(m, "method") == "" })
		eq(t, "interrupt reply id", num(reply, "id"), 9)
		p.closeStdin()
		for _, m := range p.rest() {
			if str(m, "method") == "turn/completed" {
				t.Errorf("FK-13: an error end must never be followed by turn/completed")
			}
		}
	})

	t.Run("retry is remembered but does not end the turn", func(t *testing.T) {
		p := startCodex(t, `[{"do":"end","outcome":"retry","error":"transient"}]`)
		codexInit(t, p)
		thread := codexStartThread(t, p)
		codexStartTurn(t, p, thread, 3)
		e := p.until(func(m map[string]any) bool { return str(m, "method") == "error" })
		eq(t, "willRetry", boolean(e, "params", "willRetry"), true)
		done := p.next()
		eq(t, "method", str(done, "method"), "turn/completed")
		eq(t, "status", str(done, "params", "turn", "status"), "completed")
	})

	t.Run("twice emits turn/completed twice", func(t *testing.T) {
		p := startCodex(t, `[{"do":"end","outcome":"ok","twice":true}]`)
		codexInit(t, p)
		thread := codexStartThread(t, p)
		codexStartTurn(t, p, thread, 3)
		p.until(func(m map[string]any) bool { return str(m, "method") == "turn/completed" })
		second := p.next()
		eq(t, "second end", str(second, "method"), "turn/completed")
		eq(t, "status", str(second, "params", "turn", "status"), "completed")
	})
}

func TestCodexApprovalRequestAcceptsAResultOrAnError(t *testing.T) {
	// FK-14: F-8's error reply must be valid.
	for _, reply := range []string{"result", "error"} {
		t.Run(reply, func(t *testing.T) {
			p := startCodex(t, `[{"do":"ask","input":"whoami"},{"do":"text","text":"after"},{"do":"end","outcome":"ok"}]`)
			codexInit(t, p)
			thread := codexStartThread(t, p)
			codexStartTurn(t, p, thread, 3)
			codexSkipUserMessage(t, p)

			req := p.next()
			eq(t, "method", str(req, "method"), "item/commandExecution/requestApproval")
			eq(t, "command", str(req, "params", "command"), "whoami")
			id := dig(req, "id")
			if id == nil {
				t.Fatal("the approval request must carry an id")
			}
			if reply == "result" {
				p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"decision": "accept"}})
			} else {
				p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": id,
					"error": map[string]any{"code": -32601, "message": "socrates runs unattended"}})
			}
			// The script continues either way.
			done := p.until(func(m map[string]any) bool { return str(m, "method") == "turn/completed" })
			eq(t, "status", str(done, "params", "turn", "status"), "completed")
		})
	}
}

func TestCodexInterrupt(t *testing.T) {
	p := startCodex(t, `[{"do":"text","text":"working"},{"do":"hang"}]`)
	codexInit(t, p)
	thread := codexStartThread(t, p)
	turnID := codexStartTurn(t, p, thread, 3)
	codexSkipUserMessage(t, p)
	p.until(func(m map[string]any) bool {
		return str(m, "method") == "item/completed" &&
			str(m, "params", "item", "type") == "agentMessage"
	})

	p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "turn/interrupt",
		"params": map[string]any{"threadId": thread, "turnId": turnID}})
	res := p.next()
	eq(t, "id", num(res, "id"), 9)
	if m, ok := dig(res, "result").(map[string]any); !ok || len(m) != 0 {
		t.Errorf("turn/interrupt must reply with an empty object, got %#v", dig(res, "result"))
	}
	st := p.next()
	eq(t, "method", str(st, "method"), "thread/status/changed")
	eq(t, "status", str(st, "params", "status", "type"), "idle")
	done := p.next()
	eq(t, "method", str(done, "method"), "turn/completed")
	eq(t, "status", str(done, "params", "turn", "status"), "interrupted")
	eq(t, "turn id", str(done, "params", "turn", "id"), turnID)
}

func TestCodexEmitsNoTurnContentAfterTheTerminalFrame(t *testing.T) {
	// SHOULD FIX 4: the write lock is the arbiter, so an interrupt landing
	// mid-item cannot be overtaken by the frames already in flight.
	for i := 0; i < 5; i++ {
		p := startCodex(t, `[{"do":"text","text":"one"},{"do":"tool","input":"seq 3","output":"a\nb\nc\n","exit":0},
			{"do":"text","text":"two"},{"do":"hang"}]`)
		codexInit(t, p)
		thread := codexStartThread(t, p)
		turnID := codexStartTurn(t, p, thread, 3)
		p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "turn/interrupt",
			"params": map[string]any{"threadId": thread, "turnId": turnID}})
		p.closeStdin()

		seenEnd := false
		for _, m := range p.rest() {
			method := str(m, "method")
			if seenEnd && method != "" && method != "account/rateLimits/updated" {
				t.Fatalf("%s arrived after turn/completed", method)
			}
			if method == "turn/completed" {
				seenEnd = true
			}
		}
		if !seenEnd {
			t.Fatal("the interrupt produced no turn/completed")
		}
	}
}

func TestClaudeEmitsNoBlockAfterTheResultLine(t *testing.T) {
	// SHOULD FIX 4, the claude half.
	for i := 0; i < 5; i++ {
		p := startClaude(t, `[{"do":"text","text":"one"},
			{"do":"tool","name":"Bash","input":"seq 3","output":"a\nb\nc\n","exit":0},
			{"do":"text","text":"two"},{"do":"hang"}]`)
		p.send(userLine("hi"))
		p.send(`{"type":"control_request","request_id":"x1","request":{"subtype":"interrupt"}}`)
		p.closeStdin()

		seenResult := false
		for _, m := range p.rest() {
			if seenResult {
				t.Fatalf("%s arrived after the result line", str(m, "type"))
			}
			if str(m, "type") == "result" {
				seenResult = true
			}
		}
		if !seenResult {
			t.Fatal("the interrupt produced no result")
		}
	}
}

func TestClaudeAskStepEmitsNothing(t *testing.T) {
	// There is no approval path under --permission-mode bypassPermissions.
	p := startClaude(t, `[{"do":"ask","input":"whoami"},{"do":"end","outcome":"ok"}]`)
	p.firstTurn("hi")
	eq(t, "next line", str(p.next(), "type"), "result")
}

func TestCodexUnknownMethodGetsAJSONRPCError(t *testing.T) {
	p := startCodex(t, `[{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	p.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 42, "method": "thread/nonsense", "params": map[string]any{}})
	res := p.next()
	eq(t, "id", num(res, "id"), 42)
	eq(t, "code", num(res, "error", "code"), -32601)
}

func TestCodexRunsTheScriptOnEveryTurnAndDiesOnCommand(t *testing.T) {
	// FK-1 and FK-2 for fakecodex.
	p := startCodex(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	thread := codexStartThread(t, p)
	var shapes [][]string
	for i := 0; i < 2; i++ {
		codexStartTurn(t, p, thread, 3+i)
		var seq []string
		for {
			m := p.next()
			seq = append(seq, str(m, "method"))
			// account/rateLimits/updated is the last frame of a turn: it comes
			// after turn/completed.
			if str(m, "method") == "account/rateLimits/updated" {
				break
			}
		}
		shapes = append(shapes, seq)
	}
	if strings.Join(shapes[0], ",") != strings.Join(shapes[1], ",") {
		t.Errorf("the second turn differs:\n%v\n%v", shapes[0], shapes[1])
	}

	d := startCodex(t, `[{"do":"text","text":"partial"},{"do":"die","code":9}]`)
	codexInit(t, d)
	dt := codexStartThread(t, d)
	codexStartTurn(t, d, dt, 3)
	var sawItem bool
	for _, m := range d.rest() {
		if str(m, "method") == "item/completed" {
			sawItem = true
		}
	}
	if !sawItem {
		t.Error("die did not flush the item written before it")
	}
	code, err := d.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "exit code", code, 9)
}

func TestCodexExitsZeroOnStdinEOFWithEmptyStderr(t *testing.T) {
	p := startCodex(t, `[{"do":"end","outcome":"ok"}]`)
	codexInit(t, p)
	p.closeStdin()
	code, err := p.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "exit code", code, 0)
	eq(t, "stderr", p.stderr.String(), "")
}

// ------------------------------------------------------------- fakeopencode

const ocPassword = "0123456789abcdef0123456789abcdef"

type oc struct {
	t    *testing.T
	p    *proc
	base string
}

func startOpenCode(t *testing.T, steps string, env ...string) *oc {
	t.Helper()
	e := append(scriptEnv(steps),
		"OPENCODE_SERVER_PASSWORD="+ocPassword,
		"OPENCODE_SERVER_USERNAME=socrates",
		`OPENCODE_PERMISSION="allow"`)
	p := start(t, "opencode", append(e, env...), "serve", "--port", "0", "--hostname", "127.0.0.1")
	line := p.nextRaw()
	const prefix = "opencode server listening on "
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("startup line = %q", line)
	}
	return &oc{t: t, p: p, base: strings.TrimSpace(strings.TrimPrefix(line, prefix))}
}

func (o *oc) do(method, path, body string, auth bool) (int, string) {
	o.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, o.base+path, r)
	if err != nil {
		o.t.Fatal(err)
	}
	if auth {
		req.SetBasicAuth("socrates", ocPassword)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		o.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (o *oc) json(method, path, body string) map[string]any {
	o.t.Helper()
	code, b := o.do(method, path, body, true)
	if code != 200 {
		o.t.Fatalf("%s %s = %d %s", method, path, code, b)
	}
	return decode(o.t, b)
}

// sse opens the session-scoped event stream and returns a channel of decoded
// frames plus the raw lines, so both the payloads and the framing can be
// asserted.
func (o *oc) sse(session string) (<-chan map[string]any, <-chan string) {
	o.t.Helper()
	return o.stream("/api/session/" + session + "/event")
}

// globalSSE opens GET /api/event, the stream every *.delta frame lives on.
func (o *oc) globalSSE() (<-chan map[string]any, <-chan string) {
	o.t.Helper()
	return o.stream("/api/event")
}

func (o *oc) stream(path string) (<-chan map[string]any, <-chan string) {
	o.t.Helper()
	req, err := http.NewRequest("GET", o.base+path, nil)
	if err != nil {
		o.t.Fatal(err)
	}
	req.SetBasicAuth("socrates", ocPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		o.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		o.t.Fatalf("SSE status %d", resp.StatusCode)
	}
	o.t.Cleanup(func() { resp.Body.Close() })

	frames := make(chan map[string]any, 1024)
	raws := make(chan string, 1024)
	go func() {
		defer close(frames)
		defer close(raws)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				continue
			}
			raws <- line
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var m map[string]any
				if json.Unmarshal([]byte(data), &m) == nil {
					frames <- m
				}
			}
		}
	}()
	return frames, raws
}

func nextFrame(t *testing.T, ch <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatal("SSE stream closed")
		}
		return m
	case <-time.After(readTimeout):
		t.Fatal("timed out waiting for an SSE frame")
		return nil
	}
}

func untilFrame(t *testing.T, ch <-chan map[string]any, typ string) map[string]any {
	t.Helper()
	for i := 0; i < 500; i++ {
		m := nextFrame(t, ch)
		if str(m, "type") == typ {
			return m
		}
	}
	t.Fatalf("never saw %s", typ)
	return nil
}

func (o *oc) newSession(dir string) string {
	o.t.Helper()
	res := o.json("POST", "/api/session", fmt.Sprintf(`{"location":{"directory":%q}}`, dir))
	return str(res, "data", "id")
}

func (o *oc) activeIDs() []string {
	o.t.Helper()
	res := o.json("GET", "/api/session/active", "")
	m, _ := dig(res, "data").(map[string]any)
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}

func (o *oc) waitIdle() {
	o.t.Helper()
	deadline := time.Now().Add(readTimeout)
	for time.Now().Before(deadline) {
		if len(o.activeIDs()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	o.t.Fatal("the session never left /api/session/active")
}

func TestOpenCodeEveryRouteIncludingHealthIsBehindBasicAuth(t *testing.T) {
	// FK-24.
	o := startOpenCode(t, `[{"do":"end","outcome":"ok"}]`)
	for _, path := range []string{"/api/health", "/api/model", "/api/session/active"} {
		code, body := o.do("GET", path, "", false)
		eq(t, path+" status", code, 401)
		m := decode(t, body)
		eq(t, "_tag", str(m, "_tag"), "UnauthorizedError")
		eq(t, "message", str(m, "message"), "Authentication required")
	}
	code, body := o.do("GET", "/api/health", "", true)
	eq(t, "authenticated health status", code, 200)
	eq(t, "healthy", boolean(decode(t, body), "healthy"), true)
}

func TestOpenCodeRecordsItsArgvAndPermissionEnv(t *testing.T) {
	// F-10.
	path, read := argvFile(t)
	startOpenCode(t, `[{"do":"end","outcome":"ok"}]`, "FAKE_ARGV_FILE="+path)
	recs := read()
	if len(recs) == 0 {
		t.Fatal("no argv record")
	}
	joined := strings.Join(recs[0], " ")
	if !strings.Contains(joined, `OPENCODE_PERMISSION="allow"`) {
		t.Errorf("argv record does not carry OPENCODE_PERMISSION: %s", joined)
	}
	if !strings.Contains(joined, "serve") || !strings.Contains(joined, "--hostname 127.0.0.1") {
		t.Errorf("argv record does not carry the serve arguments: %s", joined)
	}
}

func TestOpenCodeModelListHasAVariantsMapAndAModelWithout(t *testing.T) {
	// F-13: variants is a map, not an array.
	o := startOpenCode(t, `[{"do":"end","outcome":"ok"}]`)
	res := o.json("GET", "/api/model", "")
	data, _ := dig(res, "data").([]any)
	if len(data) < 2 {
		t.Fatalf("data = %#v", data)
	}
	withVariants, _ := dig(data[0], "variants").(map[string]any)
	if len(withVariants) != 3 {
		t.Fatalf("the first model must carry a low/medium/high variants map, got %#v", dig(data[0], "variants"))
	}
	for _, k := range []string{"low", "medium", "high"} {
		if _, ok := withVariants[k]; !ok {
			t.Errorf("variants map missing %q", k)
		}
	}
	// F-13: "no variants" is an empty array on GET /api/model, not an empty
	// map. A decoder that only accepts the map shape must fail here.
	none, ok := dig(data[1], "variants").([]any)
	if !ok || len(none) != 0 {
		t.Errorf("the second model must report variants as [], got %#v", dig(data[1], "variants"))
	}
}

func TestOpenCodeSessionCreationUsesLocationDirectory(t *testing.T) {
	o := startOpenCode(t, `[{"do":"end","outcome":"ok"}]`)
	res := o.json("POST", "/api/session", `{"location":{"directory":"/tmp/workdir"},"directory":"/ignored"}`)
	eq(t, "id", str(res, "data", "id"), "ses_fake0001")
	eq(t, "directory", str(res, "data", "location", "directory"), "/tmp/workdir")
}

func TestOpenCodeSetModelIsAlways204AndRecordsTheBody(t *testing.T) {
	// FK-23: an unknown variant is accepted too — the real server only fails
	// on it later.
	path, read := argvFile(t)
	o := startOpenCode(t, `[{"do":"end","outcome":"ok"}]`, "FAKE_ARGV_FILE="+path)
	id := o.newSession(t.TempDir())
	body := `{"model":{"id":"anthropic/claude-haiku-4.5","providerID":"openrouter","variant":"nonsense"}}`
	code, _ := o.do("POST", "/api/session/"+id+"/model", body, true)
	eq(t, "status", code, 204)

	var found bool
	for _, rec := range read() {
		if len(rec) == 2 && rec[0] == "POST /api/session/"+id+"/model" {
			eq(t, "recorded body", rec[1], body)
			found = true
		}
	}
	if !found {
		t.Errorf("the model body was not recorded verbatim: %v", read())
	}
}

func TestOpenCodePromptDeliveryDefaultsToSteer(t *testing.T) {
	o := startOpenCode(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	res := o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"}}`)
	eq(t, "echoed delivery", str(res, "data", "delivery"), "steer")
	adm := untilFrame(t, frames, "session.next.prompt.admitted")
	eq(t, "event delivery", str(adm, "data", "delivery"), "steer")
}

func TestOpenCodeWaitIsA503Stub(t *testing.T) {
	o := startOpenCode(t, `[{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	code, body := o.do("POST", "/api/session/"+id+"/wait", "", true)
	eq(t, "status", code, 503)
	m := decode(t, body)
	eq(t, "_tag", str(m, "_tag"), "ServiceUnavailableError")
	eq(t, "message", str(m, "message"), "Session wait is not available yet")
	eq(t, "service", str(m, "service"), "session.wait")
}

func TestOpenCodeSSEFramingAndAdmittedSeq(t *testing.T) {
	// FK-18 and F-9.
	o := startOpenCode(t, `[{"do":"text","text":"hello"},{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	frames, raws := o.sse(id)

	first := nextFrame(t, frames)
	eq(t, "first frame", str(first, "type"), "server.connected")
	if dig(first, "durable") != nil {
		t.Errorf("server.connected must carry no durable key")
	}

	res := o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
	admitted := num(res, "data", "admittedSeq")

	adm := untilFrame(t, frames, "session.next.prompt.admitted")
	eq(t, "aggregateID", str(adm, "durable", "aggregateID"), id)
	eq(t, "version", num(adm, "durable", "version"), 1)
	versions := map[string]float64{}
	// F-9: admittedSeq is exactly the prompt.admitted event's durable.seq.
	eq(t, "admittedSeq == durable.seq", num(adm, "durable", "seq"), admitted)

	var seqs []float64
	for {
		m := nextFrame(t, frames)
		if d := dig(m, "durable"); d != nil {
			seqs = append(seqs, num(m, "durable", "seq"))
			versions[str(m, "type")] = num(m, "durable", "version")
		} else if strings.HasSuffix(str(m, "type"), ".delta") {
			t.Errorf("a session stream must never carry a *.delta frame: %v", m)
		}
		if str(m, "type") == "session.next.step.ended" && str(m, "data", "finish") == "stop" {
			break
		}
	}
	for i, s := range seqs {
		if s != admitted+float64(i)+1 {
			t.Fatalf("durable seq must count up per session: %v (admittedSeq %v)", seqs, admitted)
		}
	}
	// The research observed version 2 on step.ended and 1 everywhere else.
	eq(t, "step.ended version", versions["session.next.step.ended"], 2)
	eq(t, "text.ended version", versions["session.next.text.ended"], 1)
	eq(t, "step.started version", versions["session.next.step.started"], 1)

	// The heartbeat comment lines.
	deadline := time.After(3 * heartbeatWait)
	for {
		select {
		case line := <-raws:
			if line == ": heartbeat" {
				return
			}
		case <-deadline:
			t.Fatal("no heartbeat comment line within three intervals")
		}
	}
}

const heartbeatWait = 2 * time.Second

func TestOpenCodeFullStepStructure(t *testing.T) {
	// FK-19 / FK-20 / F-12.
	o := startOpenCode(t, `[{"do":"tool","name":"bash","input":"echo hi","output":"hi\n","exit":0},
		{"do":"text","text":"done"},{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	globals, _ := o.globalSSE()
	untilFrame(t, globals, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)

	var types []string
	var stepMsgs []string
	var textIDs []string
	var finishes []string
	var success map[string]any
	for {
		m := untilAny(t, frames)
		typ := strings.TrimPrefix(str(m, "type"), "session.next.")
		types = append(types, typ)
		switch typ {
		case "step.started":
			stepMsgs = append(stepMsgs, str(m, "data", "assistantMessageID"))
		case "text.started":
			textIDs = append(textIDs, str(m, "data", "textID"))
		case "tool.success":
			success = m
		case "step.ended":
			finishes = append(finishes, str(m, "data", "finish"))
			if str(m, "data", "finish") == "stop" {
				goto done
			}
		}
	}
done:
	// FK-19's order, minus the *.delta frames: the session stream carries the
	// durable events only.
	want := []string{
		"prompt.admitted", "prompted", "step.started",
		"tool.input.started", "tool.input.ended",
		"tool.called", "tool.success", "step.ended",
		"step.started", "text.started", "text.ended", "step.ended",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Errorf("event order:\n got %v\nwant %v", types, want)
	}

	// The chunks the session stream did not carry are all on /api/event, and
	// so is traffic for the server's own internal session.
	var globalTypes []string
	var text string
	sawInternal := false
	for len(globalTypes) < 5 {
		m := nextFrame(t, globals)
		globalTypes = append(globalTypes, strings.TrimPrefix(str(m, "type"), "session.next."))
		if dig(m, "durable") != nil {
			t.Errorf("the global stream carries the ephemeral chunks, not durables: %v", m)
		}
		switch str(m, "data", "sessionID") {
		case id:
			if str(m, "type") == "session.next.text.delta" {
				text += str(m, "data", "delta")
			}
		case "ses_internal0001":
			sawInternal = true
		default:
			t.Errorf("unexpected sessionID on the global stream: %v", m)
		}
	}
	wantGlobal := []string{"text.delta", "tool.input.delta", "text.delta", "text.delta", "text.delta"}
	if strings.Join(globalTypes, ",") != strings.Join(wantGlobal, ",") {
		t.Errorf("global stream order:\n got %v\nwant %v", globalTypes, wantGlobal)
	}
	if !sawInternal {
		t.Error("the global stream must also carry the server's internal session")
	}
	eq(t, "the assembled text deltas", text, "done")
	if len(stepMsgs) != 2 || stepMsgs[0] == stepMsgs[1] {
		t.Errorf("each step needs its own assistantMessageID: %v", stepMsgs)
	}
	eq(t, "finishes", strings.Join(finishes, ","), "tool-calls,stop")
	// F-12: textID restarts at text-0 in every step.
	for _, id := range textIDs {
		eq(t, "textID", id, "text-0")
	}
	// FK-20's success shape.
	eq(t, "exit", num(success, "data", "structured", "exit"), 0)
	eq(t, "truncated", boolean(success, "data", "structured", "truncated"), false)
	eq(t, "content[0]", str(success, "data", "content", 0, "text"), "hi\n")
	eq(t, "content[1]", str(success, "data", "content", 1, "text"), "Command exited with code 0.")
	if _, ok := dig(success, "data", "outputPaths").([]any); !ok {
		t.Errorf("outputPaths must be an array")
	}
	eq(t, "provider.executed", boolean(success, "data", "provider", "executed"), false)
}

func untilAny(t *testing.T, ch <-chan map[string]any) map[string]any {
	t.Helper()
	return nextFrame(t, ch)
}

func TestOpenCodeToolFailedShape(t *testing.T) {
	// FK-20's failure event, per EventSessionNextToolFailed in the OpenAPI
	// document: the name is session.next.tool.failed and error is a
	// SessionErrorUnknown object.
	o := startOpenCode(t, `[{"do":"tool","name":"bash","input":"false","output":"boom","exit":2},{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)

	var e map[string]any
	for e == nil {
		m := nextFrame(t, frames)
		if str(m, "type") == "session.next.tool.success" {
			t.Fatal("a failing tool call must not report tool.success")
		}
		if str(m, "type") == "session.next.tool.failed" {
			e = m
		}
	}
	eq(t, "sessionID", str(e, "data", "sessionID"), id)
	eq(t, "error type", str(e, "data", "error", "type"), "unknown")
	eq(t, "error message", str(e, "data", "error", "message"), "boom")
	eq(t, "provider.executed", boolean(e, "data", "provider", "executed"), false)
	if str(e, "data", "assistantMessageID") == "" || str(e, "data", "callID") == "" {
		t.Errorf("tool.failed must carry assistantMessageID and callID: %v", e)
	}
	if dig(e, "durable") == nil {
		t.Errorf("tool.failed is durable")
	}
}

func TestOpenCodePermissionAskBlocksUntilItIsAnswered(t *testing.T) {
	// The permission.v2.asked / .replied shapes are observed verbatim in the
	// research; an unanswered ask blocks the turn forever.
	path, read := argvFile(t)
	o := startOpenCode(t, `[{"do":"ask","input":"echo permcheck > perm.txt"},
		{"do":"text","text":"done"},{"do":"end","outcome":"ok"}]`, "FAKE_ARGV_FILE="+path)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)

	asked := untilFrame(t, frames, "permission.v2.asked")
	if dig(asked, "durable") != nil {
		t.Errorf("permission.v2.asked is ephemeral and must carry no durable key")
	}
	reqID := str(asked, "data", "id")
	if !strings.HasPrefix(reqID, "per_") {
		t.Fatalf("permission id = %q", reqID)
	}
	eq(t, "sessionID", str(asked, "data", "sessionID"), id)
	eq(t, "action", str(asked, "data", "action"), "bash")
	eq(t, "resources[0]", str(asked, "data", "resources", 0), "echo permcheck > perm.txt")
	eq(t, "save[0]", str(asked, "data", "save", 0), "echo permcheck > perm.txt")
	eq(t, "source.type", str(asked, "data", "source", "type"), "tool")
	if str(asked, "data", "source", "messageID") == "" || str(asked, "data", "source", "callID") == "" {
		t.Errorf("source must carry messageID and callID: %v", asked)
	}

	// The turn is blocked until the reply route is hit.
	select {
	case m := <-frames:
		t.Fatalf("the script continued before the ask was answered: %v", m)
	case <-time.After(300 * time.Millisecond):
	}

	code, _ := o.do("POST", "/api/session/"+id+"/permission/"+reqID+"/reply", `{"reply":"always"}`, true)
	eq(t, "reply status", code, 204)
	replied := untilFrame(t, frames, "permission.v2.replied")
	eq(t, "requestID", str(replied, "data", "requestID"), reqID)
	eq(t, "reply", str(replied, "data", "reply"), "always")
	untilFrame(t, frames, "session.next.text.ended")

	var found bool
	for _, rec := range read() {
		if len(rec) == 2 && strings.Contains(rec[0], "/permission/"+reqID+"/reply") {
			eq(t, "recorded reply body", rec[1], `{"reply":"always"}`)
			found = true
		}
	}
	if !found {
		t.Errorf("the permission reply was not recorded: %v", read())
	}

	// An unknown request id is a 404, not a silent success.
	code, _ = o.do("POST", "/api/session/"+id+"/permission/per_nope/reply", `{"reply":"always"}`, true)
	eq(t, "unknown request status", code, 404)
}

func TestOpenCodeSubagentStepIsAPlainTaskToolCall(t *testing.T) {
	// OpenCode reports no subagents on its stream, so the fake never invents
	// one.
	o := startOpenCode(t, `[{"do":"subagent","input":"find the caller","output":"found"},{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
	called := untilFrame(t, frames, "session.next.tool.called")
	eq(t, "tool", str(called, "data", "tool"), "task")
	eq(t, "input", str(called, "data", "input", "command"), "find the caller")
}

func TestOpenCodeSessionErrorEndsTheTurnWithoutAStepEnded(t *testing.T) {
	// FK-21 / F-11.
	o := startOpenCode(t, `[{"do":"text","text":"trying"},{"do":"end","outcome":"error","error":"Unsupported API for openrouter/x"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)

	var sawStepEnded bool
	var e map[string]any
	for e == nil {
		m := nextFrame(t, frames)
		if str(m, "type") == "session.next.step.ended" {
			sawStepEnded = true
		}
		if str(m, "type") == "session.error" {
			e = m
		}
	}
	if sawStepEnded {
		t.Error("FK-21: a session.error end must not be preceded by a step.ended")
	}
	eq(t, "sessionID", str(e, "data", "sessionID"), id)
	eq(t, "error name", str(e, "data", "error", "name"), "UnsupportedApiError")
	eq(t, "error message", str(e, "data", "error", "data", "message"), "Unsupported API for openrouter/x")

	// The session was still active when the error arrived, and empties after.
	o.waitIdle()
}

func TestOpenCodeInterruptEmptiesActiveWithoutAStepEnded(t *testing.T) {
	// FK-22.
	o := startOpenCode(t, `[{"do":"text","text":"working"},{"do":"hang"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
	untilFrame(t, frames, "session.next.text.ended")

	if ids := o.activeIDs(); len(ids) != 1 || ids[0] != id {
		t.Fatalf("active while the turn is open = %v", ids)
	}
	code, _ := o.do("POST", "/api/session/"+id+"/interrupt", "", true)
	eq(t, "status", code, 204)
	if ids := o.activeIDs(); len(ids) != 0 {
		t.Errorf("interrupt must empty active immediately, got %v", ids)
	}
	select {
	case m := <-frames:
		if str(m, "type") == "session.next.step.ended" {
			t.Error("FK-22: an interrupt must not produce a step.ended")
		}
	case <-time.After(500 * time.Millisecond):
	}
}

func TestOpenCodeDoubleEndArrivesAfterActiveHasEmptied(t *testing.T) {
	// FK-22's variant: the C-4 double-end guard.
	o := startOpenCode(t, `[{"do":"text","text":"done"},{"do":"end","outcome":"ok","twice":true}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
	untilFrame(t, frames, "session.next.text.ended")

	// active empties before either step.ended lands.
	o.waitIdle()
	first := nextFrame(t, frames)
	eq(t, "first late frame", str(first, "type"), "session.next.step.ended")
	eq(t, "finish", str(first, "data", "finish"), "stop")
	second := nextFrame(t, frames)
	// No third step is started between the two ends: both name the one step
	// that was open.
	eq(t, "second late frame", str(second, "type"), "session.next.step.ended")
	eq(t, "finish", str(second, "data", "finish"), "stop")
	eq(t, "same step", str(second, "data", "assistantMessageID"),
		str(first, "data", "assistantMessageID"))
}

func TestOpenCodeUnknownSessionIsAnExistingEmptySession(t *testing.T) {
	// S-2.
	o := startOpenCode(t, `[{"do":"text","text":"resumed"},{"do":"end","outcome":"ok"}]`)
	frames, _ := o.sse("ses_neverseen0001")
	eq(t, "first frame", str(nextFrame(t, frames), "type"), "server.connected")
	select {
	case m := <-frames:
		t.Fatalf("an unknown session must replay nothing, got %v", m)
	case <-time.After(300 * time.Millisecond):
	}
	// And it can be prompted straight away.
	res := o.json("POST", "/api/session/ses_neverseen0001/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
	eq(t, "admittedSeq", num(res, "data", "admittedSeq"), 1)
	untilFrame(t, frames, "session.next.text.ended")
}

func TestOpenCodeReplaysTheWholeDurableHistoryOnEveryConnect(t *testing.T) {
	// FK-18's replay rule, which is what makes the F-9 baseline meaningful.
	o := startOpenCode(t, `[{"do":"text","text":"one"},{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
	untilFrame(t, frames, "session.next.step.ended")
	o.waitIdle()

	again, _ := o.sse(id)
	eq(t, "first frame", str(nextFrame(t, again), "type"), "server.connected")
	var replayed []string
	for {
		m := nextFrame(t, again)
		replayed = append(replayed, str(m, "type"))
		if dig(m, "durable") == nil {
			t.Errorf("only durable events are replayed, got %v", m)
		}
		if str(m, "type") == "session.next.step.ended" {
			break
		}
	}
	want := "session.next.prompt.admitted,session.next.prompted,session.next.step.started," +
		"session.next.text.started,session.next.text.ended,session.next.step.ended"
	if strings.Join(replayed, ",") != want {
		t.Errorf("replay:\n got %v\nwant %v", strings.Join(replayed, ","), want)
	}

	// The second turn's seq continues counting across the server's whole life.
	res := o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"again"},"delivery":"queue"}`)
	eq(t, "second admittedSeq", num(res, "data", "admittedSeq"), 7)
}

func TestOpenCodeReasoningEvents(t *testing.T) {
	// S-2's design-chosen reasoning shape.
	o := startOpenCode(t, `[{"do":"reason","text":"pondering"},{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	globals, _ := o.globalSSE()
	untilFrame(t, globals, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)

	started := untilFrame(t, frames, "session.next.reasoning.started")
	eq(t, "reasoningID", str(started, "data", "reasoningID"), "reasoning-0")
	if str(started, "data", "assistantMessageID") == "" {
		t.Error("reasoning.started must carry an assistantMessageID")
	}
	// The chunks are on the global stream, never on the session one.
	delta := untilFrame(t, globals, "session.next.reasoning.delta")
	if dig(delta, "durable") != nil {
		t.Error("reasoning.delta is ephemeral and must carry no durable key")
	}
	eq(t, "reasoning delta sessionID", str(delta, "data", "sessionID"), id)
	ended := untilFrame(t, frames, "session.next.reasoning.ended")
	eq(t, "text", str(ended, "data", "text"), "pondering")
}

func TestOpenCodeGlobalStreamIsBehindAuthAndNeverReplays(t *testing.T) {
	// GET /api/event is authenticated like every other route and, unlike the
	// session stream, replays nothing on connect.
	o := startOpenCode(t, `[{"do":"text","text":"done"},{"do":"end","outcome":"ok"}]`)
	code, body := o.do("GET", "/api/event", "", false)
	eq(t, "unauthenticated status", code, 401)
	eq(t, "_tag", str(decode(t, body), "_tag"), "UnauthorizedError")

	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	globals, raws := o.globalSSE()
	eq(t, "first frame", str(nextFrame(t, globals), "type"), "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
	untilFrame(t, frames, "session.next.step.ended")
	o.waitIdle()

	// A second connection sees none of what has already happened.
	again, _ := o.globalSSE()
	eq(t, "first frame", str(nextFrame(t, again), "type"), "server.connected")
	select {
	case m := <-again:
		t.Fatalf("the global stream must not replay, got %v", m)
	case <-time.After(300 * time.Millisecond):
	}

	// Same framing as the session stream, heartbeat comments included.
	deadline := time.After(3 * heartbeatWait)
	for {
		select {
		case line := <-raws:
			if line == ": heartbeat" {
				return
			}
			if !strings.HasPrefix(line, "data: ") {
				t.Fatalf("unexpected SSE line %q", line)
			}
		case <-deadline:
			t.Fatal("no heartbeat comment line on the global stream")
		}
	}
}

func TestOpenCodeRunsTheScriptOnEveryTurn(t *testing.T) {
	// FK-1 for fakeopencode.
	o := startOpenCode(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")

	var shapes [][]string
	for i := 0; i < 2; i++ {
		o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
		var seq []string
		for {
			m := nextFrame(t, frames)
			seq = append(seq, str(m, "type"))
			if str(m, "type") == "session.next.step.ended" {
				break
			}
		}
		shapes = append(shapes, seq)
		o.waitIdle()
	}
	if strings.Join(shapes[0], ",") != strings.Join(shapes[1], ",") {
		t.Errorf("the second turn differs:\n%v\n%v", shapes[0], shapes[1])
	}
}

func TestOpenCodeSigtermExitsImmediately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on windows")
	}
	o := startOpenCode(t, `[{"do":"end","outcome":"ok"}]`)
	if err := o.p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	code, err := o.p.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "exit code", code, 0)
	eq(t, "stderr", o.p.stderr.String(), "")
}

func TestOpenCodeDieExitsWithTheScriptedCode(t *testing.T) {
	// FK-2 for fakeopencode.
	o := startOpenCode(t, `[{"do":"text","text":"partial"},{"do":"die","code":5}]`)
	id := o.newSession(t.TempDir())
	frames, _ := o.sse(id)
	untilFrame(t, frames, "server.connected")
	o.json("POST", "/api/session/"+id+"/prompt", `{"prompt":{"text":"hi"},"delivery":"queue"}`)
	untilFrame(t, frames, "session.next.text.ended")
	code, err := o.p.wait()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "exit code", code, 5)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
