// Package fakemodel is a scripted stand-in for the OpenAI Responses API, for
// driving the real `codex app-server` without a model or an account. Codex is
// pointed at it as a custom model provider (Config), and it answers each
// request from a directive in the latest user message:
//
//	SAY <text>      a reasoning item, then <text> (default: "pong")
//	SLOW <n> [ms]   n text deltas "tickI ", ms apart (default 20 × 100 ms)
//	RUN: <command>  an exec_command call (inside `exec` for code-mode models)
//	PATCH: <file>   an apply_patch that adds <file>
//	SPAWN: <task>   a collaboration.spawn_agent call whose agent is told "SAY sub"
//	LIMIT           a 429 usage_limit_reached
//	FAIL            a 500
//
// After a tool's output the answer is "done. tool said: <output>".
//
// Requests to /acct/<name>/v1/responses belong to the account <name>: the
// encrypted reasoning it hands out is bound to it, and a request that replays
// another account's is refused with 400 invalid_encrypted_content, the way
// openai/codex #25290 describes. /acct/<name>/strict/v1/responses also refuses
// any replayed assistant message, so that stripping the reasoning is not
// enough.
//
// Every request is appended to the log as one JSON line, with the account and
// the body, so a test can see what codex sent.
package fakemodel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is the fake model. It is an http.Handler.
type Server struct {
	mu  sync.Mutex
	n   int
	log io.Writer
}

// New is a fake model that logs every request to log (nil: no log).
func New(log io.Writer) *Server { return &Server{log: log} }

// Config is the config.toml that makes codex use the fake model at baseURL
// (e.g. http://127.0.0.1:18080/v1, or …/acct/a/v1), with model as its default.
func Config(baseURL, model string) string {
	return fmt.Sprintf(`model = %q
model_provider = "fake"
model_reasoning_effort = "low"
check_for_update_on_startup = false

[model_providers.fake]
name = "fake"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, model, baseURL)
}

type request struct {
	Model string           `json:"model"`
	Input []map[string]any `json:"input"`
	Tools []map[string]any `json:"tools"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.n++
	n := s.n
	if s.log != nil {
		line, _ := json.Marshal(map[string]any{"n": n, "path": r.URL.Path, "body": json.RawMessage(validJSON(raw))})
		_, _ = s.log.Write(append(line, '\n'))
	}
	s.mu.Unlock()
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	account, strict := accountOf(r.URL.Path)
	if account != "" {
		if bad := foreign(req.Input, account, strict); bad != "" {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_encrypted_content",
				"The encrypted content for item "+bad+" could not be verified.")
			return
		}
	}
	text := lastUserText(req.Input)
	if tailToolOutput(req.Input) == nil {
		switch {
		case strings.Contains(text, "LIMIT"):
			writeError(w, http.StatusTooManyRequests, "usage_limit_reached", "usage_limit_reached",
				"The usage limit has been reached")
			return
		case strings.Contains(text, "FAIL"):
			writeError(w, http.StatusInternalServerError, "server_error", "server_error", "The fake model failed.")
			return
		}
	}
	t := &turn{w: w, n: n, account: account, req: req, hangup: r.Context().Done()}
	t.begin()
	t.answer(text)
	t.end()
}

func validJSON(raw []byte) []byte {
	if json.Valid(raw) {
		return raw
	}
	quoted, _ := json.Marshal(string(raw))
	return quoted
}

// accountOf reads /acct/<name>[/strict]/v1/responses.
func accountOf(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/acct/")
	if !ok {
		return "", false
	}
	name, rest, _ := strings.Cut(rest, "/")
	return name, strings.HasPrefix(rest, "strict/")
}

// foreign is the id of the first replayed item the account refuses.
func foreign(input []map[string]any, account string, strict bool) string {
	for _, it := range input {
		if enc, _ := it["encrypted_content"].(string); enc != "" && !strings.HasPrefix(enc, "enc:"+account+":") {
			return fmt.Sprint(it["id"])
		}
		if strict && it["type"] == "message" && it["role"] == "assistant" {
			return fmt.Sprint(it["id"])
		}
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, typ, code, msg string) {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": typ, "param": nil, "code": code}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func lastUserText(input []map[string]any) string {
	for i := len(input) - 1; i >= 0; i-- {
		it := input[i]
		if it["type"] != "message" || it["role"] != "user" {
			continue
		}
		var parts []string
		content, _ := it["content"].([]any)
		for _, c := range content {
			if m, ok := c.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		txt := strings.Join(parts, " ")
		if strings.Contains(txt, "<environment_context>") || strings.HasPrefix(txt, "# AGENTS.md") ||
			strings.Contains(txt, "<user_instructions>") {
			continue
		}
		return txt
	}
	return ""
}

func tailToolOutput(input []map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	last := input[len(input)-1]
	switch last["type"] {
	case "function_call_output", "custom_tool_call_output":
		return last
	}
	return nil
}

// turn is one streamed response.
type turn struct {
	w       http.ResponseWriter
	n       int
	account string
	req     request
	index   int
	// done is closed when codex hangs up, which ends a slow stream.
	hangup <-chan struct{}
}

func (t *turn) sse(ev map[string]any) {
	data, _ := json.Marshal(ev)
	fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", ev["type"], data)
	if f, ok := t.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *turn) begin() {
	h := t.w.Header()
	h.Set("Content-Type", "text/event-stream")
	now := time.Now().Unix()
	// The rate-limit headers the ChatGPT backend sends with every response.
	h.Set("x-codex-primary-used-percent", strconv.Itoa(10+t.n%80))
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-primary-reset-at", strconv.FormatInt(now+3600, 10))
	h.Set("x-codex-secondary-used-percent", "42")
	h.Set("x-codex-secondary-window-minutes", "10080")
	h.Set("x-codex-secondary-reset-at", strconv.FormatInt(now+86400, 10))
	t.w.WriteHeader(http.StatusOK)
	t.sse(map[string]any{"type": "response.created", "response": map[string]any{"id": t.respID()}})
}

func (t *turn) respID() string { return fmt.Sprintf("resp_%d", t.n) }

func (t *turn) end() {
	t.sse(map[string]any{"type": "response.completed", "response": map[string]any{"id": t.respID(), "usage": map[string]any{
		"input_tokens": 1200, "input_tokens_details": map[string]any{"cached_tokens": 1000},
		"output_tokens": 30, "output_tokens_details": map[string]any{"reasoning_tokens": 12}, "total_tokens": 1230}}})
}

var cellID = regexp.MustCompile(`cell ID (\S+?)[.\s]`)

func (t *turn) answer(text string) {
	if out := tailToolOutput(t.req.Input); out != nil {
		o := outputText(out["output"])
		if m := cellID.FindStringSubmatch(o); m != nil && strings.Contains(o, "Script running") {
			t.call("wait", map[string]any{"cell_id": m[1], "yield_time_ms": 30000})
			return
		}
		if len(o) > 300 {
			o = o[:300]
		}
		t.message("done. tool said: " + o)
		return
	}
	switch {
	case strings.Contains(text, "SLOW"):
		count, every := 20, 100
		if f := strings.Fields(after(text, "SLOW")); len(f) > 0 {
			count = atoi(f[0], count)
			if len(f) > 1 {
				every = atoi(f[1], every)
			}
		}
		id := fmt.Sprintf("msg_%d", t.n)
		t.added(map[string]any{"type": "message", "id": id, "role": "assistant", "content": []any{}})
		var all strings.Builder
		for i := range count {
			d := fmt.Sprintf("tick%d ", i)
			all.WriteString(d)
			t.sse(map[string]any{"type": "response.output_text.delta", "delta": d, "item_id": id, "output_index": t.index, "content_index": 0})
			select {
			case <-time.After(time.Duration(every) * time.Millisecond):
			case <-t.hangup:
				return
			}
		}
		t.done(map[string]any{"type": "message", "id": id, "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": all.String(), "annotations": []any{}}}})
	case strings.Contains(text, "RUN:"):
		cmd := strings.TrimSpace(after(text, "RUN:"))
		t.call("exec_command", map[string]any{"cmd": cmd})
	case strings.Contains(text, "PATCH:"):
		name := strings.Fields(after(text, "PATCH:") + " x")[0]
		t.custom("apply_patch", "*** Begin Patch\n*** Add File: "+name+"\n+hello from the fake model\n*** End Patch\n")
	case strings.Contains(text, "SPAWN:"):
		task := strings.Fields(after(text, "SPAWN:") + " helper")[0]
		t.call("collaboration.spawn_agent", map[string]any{"task_name": task, "message": "SAY sub done", "fork_turns": "none"})
	case strings.Contains(text, "PROPOSE"):
		t.message("Here is my plan.\n<proposed_plan>\n# Rename the button\n- Find the usages\n- Rename them\n</proposed_plan>\n")
	default:
		t.reasoning()
		say := "pong"
		if strings.Contains(text, "SAY ") {
			say = strings.TrimSpace(after(text, "SAY "))
		}
		t.message(say)
	}
}

func after(s, marker string) string {
	_, rest, _ := strings.Cut(s, marker)
	return rest
}

func atoi(s string, fallback int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return n
	}
	return fallback
}

func outputText(o any) string {
	switch v := o.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, x := range v {
			if m, ok := x.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	raw, _ := json.Marshal(o)
	return string(raw)
}

func (t *turn) added(item map[string]any) {
	t.sse(map[string]any{"type": "response.output_item.added", "output_index": t.index, "item": item})
}

func (t *turn) done(item map[string]any) {
	t.sse(map[string]any{"type": "response.output_item.done", "output_index": t.index, "item": item})
	t.index++
}

func (t *turn) reasoning() {
	id := fmt.Sprintf("rs_%d", t.n)
	t.added(map[string]any{"type": "reasoning", "id": id, "summary": []any{}})
	t.sse(map[string]any{"type": "response.reasoning_summary_part.added", "item_id": id, "output_index": t.index, "summary_index": 0,
		"part": map[string]any{"type": "summary_text", "text": ""}})
	for _, d := range []string{"**Thinking** ", "about ", "it"} {
		t.sse(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": id, "output_index": t.index, "summary_index": 0, "delta": d})
	}
	account := t.account
	if account == "" {
		account = "none"
	}
	t.done(map[string]any{"type": "reasoning", "id": id,
		"summary":           []any{map[string]any{"type": "summary_text", "text": "**Thinking** about it"}},
		"encrypted_content": fmt.Sprintf("enc:%s:%d", account, t.n)})
}

func (t *turn) message(text string) {
	id := fmt.Sprintf("msg_%d", t.n)
	t.added(map[string]any{"type": "message", "id": id, "role": "assistant", "content": []any{}})
	runes := []rune(text)
	for i := 0; i < len(runes); i += 5 {
		t.sse(map[string]any{"type": "response.output_text.delta", "item_id": id, "output_index": t.index, "content_index": 0,
			"delta": string(runes[i:min(i+5, len(runes))])})
	}
	t.done(map[string]any{"type": "message", "id": id, "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}})
}

// tools is every tool name the request offers, namespaced ones as "ns.name".
func (t *turn) tools() map[string]bool {
	names := map[string]bool{}
	add := func(list []any) {
		for _, x := range list {
			m, _ := x.(map[string]any)
			name, _ := m["name"].(string)
			names[name] = true
			if sub, ok := m["tools"].([]any); ok {
				for _, y := range sub {
					if sm, ok := y.(map[string]any); ok {
						if n, ok := sm["name"].(string); ok {
							names[name+"."+n] = true
							names[n] = true
						}
					}
				}
			}
		}
	}
	tools := make([]any, 0, len(t.req.Tools))
	for _, x := range t.req.Tools {
		tools = append(tools, x)
	}
	add(tools)
	for _, it := range t.req.Input {
		if it["type"] == "additional_tools" {
			if list, ok := it["tools"].([]any); ok {
				add(list)
			}
		}
	}
	return names
}

// call is a function call, wrapped in a code-mode `exec` script when the
// model only has that.
func (t *turn) call(name string, args map[string]any) {
	names := t.tools()
	if !names[name] && names["exec"] && !strings.Contains(name, ".") {
		arg, _ := json.Marshal(args)
		t.custom("exec", fmt.Sprintf("const r = await tools.%s(%s);\ntext(typeof r === 'string' ? r : JSON.stringify(r));", name, arg))
		return
	}
	raw, _ := json.Marshal(args)
	item := map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", t.n), "call_id": fmt.Sprintf("call_%d", t.n),
		"name": name, "arguments": string(raw)}
	if ns, tool, ok := strings.Cut(name, "."); ok {
		item["namespace"], item["name"] = ns, tool
	}
	t.added(item)
	t.done(item)
}

func (t *turn) custom(name, input string) {
	item := map[string]any{"type": "custom_tool_call", "id": fmt.Sprintf("ctc_%d", t.n), "call_id": fmt.Sprintf("call_%d", t.n),
		"name": name, "input": input, "status": "completed"}
	t.added(item)
	t.done(item)
}
