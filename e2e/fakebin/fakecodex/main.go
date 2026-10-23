// fakecodex is the fake `codex` the adapter tests and the e2e suite run
// instead of the real one: `codex app-server` speaking the app-server's stdio
// JSON-RPC (codex-cli 0.164) closely enough for internal/agent/codex, with no
// model and no network.
//
// It keeps its threads as rollout files under $CODEX_HOME/sessions/YYYY/MM/DD/
// in the real line format (session_meta, turn_context, response_item,
// event_msg task_started / item_completed / task_complete / turn_aborted), and
// reads them back for thread/resume, thread/turns/list and thread/items/list,
// so resume, a rebuilt history and a stripped copy work as they do with
// codex. Like codex, it exits the moment stdin closes.
//
// A turn is scripted by the first word of its message:
//
//	/stream N [ms]   N agent message deltas "wI ", ms apart (default 20 × 10 ms)
//	/sleep S         a turn that works for S seconds, then says "slept"
//	/run CMD         a commandExecution of CMD: three output lines, exit 0 (exit 1 for "fail")
//	/patch FILE      a fileChange adding FILE, with its diff and the turn diff
//	/subagent        a subagent thread that answers "sub done", nested in the turn
//	/plan            a plan item and a turn/plan/updated list
//	/limit           the usage limit: rate limits at 100 %, the turn fails as usageLimitExceeded
//	/crash           a delta, then the process dies
//	/approve         a command approval request; says the decision it got
//	/elicit          an MCP elicitation form; says the action it got
//	/bg S            leaves a background terminal for S seconds
//	anything else    reasoning, then "you said: <text>"
//
// A message steered into a running turn is taken up at its next step: its
// userMessage item (with the client id) and "steered: <text>" in the answer.
//
// Accounts: the encrypted reasoning it writes is bound to FAKECODEX_ACCOUNT
// (default "default"). A turn on a thread whose history holds another
// account's fails with invalid_encrypted_content, as the real model does;
// with FAKECODEX_STRICT=1 any assistant message in the history fails it too.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const version = "0.164.0"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-V") {
		fmt.Printf("codex-cli %s\n", version)
		return
	}
	if len(args) == 0 || args[0] != "app-server" {
		fmt.Fprintln(os.Stderr, "fakecodex only speaks `codex app-server`")
		os.Exit(2)
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		fmt.Fprintln(os.Stderr, "fakecodex needs CODEX_HOME")
		os.Exit(2)
	}
	account := os.Getenv("FAKECODEX_ACCOUNT")
	if account == "" {
		account = "default"
	}
	s := &server{home: home, account: account, strict: os.Getenv("FAKECODEX_STRICT") == "1",
		out: bufio.NewWriter(os.Stdout), threads: map[string]*thread{}, pending: map[int64]chan reply{}}
	s.serve()
}

type server struct {
	home    string
	account string
	strict  bool

	outMu sync.Mutex
	out   *bufio.Writer

	mu      sync.Mutex
	threads map[string]*thread
	nextReq int64
	pending map[int64]chan reply
	counter int
}

type reply struct {
	Result json.RawMessage
	Error  json.RawMessage
}

type thread struct {
	id    string
	path  string
	cwd   string
	model string
	turns []*fturn
	// history is the model context the rollout holds, for the account check.
	foreignEnc bool
	assistant  bool
	active     *fturn
	bgUntil    time.Time
}

type fturn struct {
	id      string
	status  string
	items   []json.RawMessage
	steer   chan userInput
	cancel  context.CancelFunc
	errText string
}

type userInput struct {
	clientID string
	text     string
	content  []any
}

type frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func (s *server) serve() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		var fr frame
		if json.Unmarshal(sc.Bytes(), &fr) != nil {
			continue
		}
		switch {
		case fr.Method == "" && len(fr.ID) > 0:
			id, _ := strconv.ParseInt(string(fr.ID), 10, 64)
			s.mu.Lock()
			ch := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if ch != nil {
				ch <- reply{Result: fr.Result, Error: fr.Error}
			}
		case len(fr.ID) > 0:
			s.request(fr)
		}
	}
	// Like codex on stdio: the end of stdin is the end, turns and all.
	os.Exit(0)
}

func (s *server) write(v any) {
	raw, _ := json.Marshal(v)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.out.Write(raw)
	s.out.WriteByte('\n')
	s.out.Flush()
}

func (s *server) notify(method string, params any) {
	raw, _ := json.Marshal(params)
	s.write(frame{Method: method, Params: raw})
}

func (s *server) respond(id json.RawMessage, result any) {
	raw, _ := json.Marshal(result)
	s.write(frame{ID: id, Result: raw})
}

func (s *server) fail(id json.RawMessage, code int, msg string, data any) {
	e := map[string]any{"code": code, "message": msg}
	if data != nil {
		e["data"] = data
	}
	raw, _ := json.Marshal(e)
	s.write(frame{ID: id, Error: raw})
}

// ask sends a server request and waits for its answer.
func (s *server) ask(ctx context.Context, method string, params any) (reply, error) {
	s.mu.Lock()
	id := s.nextReq
	s.nextReq++
	ch := make(chan reply, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	raw, _ := json.Marshal(params)
	s.write(frame{ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: raw})
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return reply{}, ctx.Err()
	}
}

func (s *server) request(fr frame) {
	var p map[string]json.RawMessage
	_ = json.Unmarshal(fr.Params, &p)
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(p[k], &v)
		return v
	}
	switch fr.Method {
	case "initialize":
		s.respond(fr.ID, map[string]any{"userAgent": "fakecodex/" + version, "codexHome": s.home,
			"platformFamily": "unix", "platformOs": "linux"})
	case "thread/start":
		th, err := s.newThread(str("cwd"), str("model"))
		if err != nil {
			s.fail(fr.ID, -32603, err.Error(), nil)
			return
		}
		s.respond(fr.ID, s.threadResult(th))
		s.notify("thread/started", map[string]any{"thread": s.threadJSON(th)})
	case "thread/resume":
		th, err := s.load(str("threadId"))
		if err != nil {
			s.fail(fr.ID, -32600, err.Error(), nil)
			return
		}
		s.respond(fr.ID, s.threadResult(th))
	case "turn/start":
		s.turnStart(fr, str("threadId"), str("clientUserMessageId"), p["input"])
	case "turn/steer":
		s.turnSteer(fr, str("threadId"), str("expectedTurnId"), str("clientUserMessageId"), p["input"])
	case "turn/interrupt":
		th := s.thread(str("threadId"))
		s.mu.Lock()
		var t *fturn
		if th != nil {
			t = th.active
		}
		s.mu.Unlock()
		if t != nil && t.id == str("turnId") {
			t.cancel()
		}
		s.respond(fr.ID, map[string]any{})
	case "thread/compact/start":
		s.special(fr, str("threadId"), "compact", "")
	case "review/start":
		s.special(fr, str("threadId"), "review", string(p["target"]))
	case "thread/shellCommand":
		s.special(fr, str("threadId"), "shell", str("command"))
	case "thread/turns/list":
		s.turnsList(fr, str("threadId"))
	case "thread/items/list":
		s.itemsList(fr, str("threadId"), str("turnId"))
	case "thread/backgroundTerminals/list":
		th := s.thread(str("threadId"))
		var data []any
		s.mu.Lock()
		if th != nil && time.Now().Before(th.bgUntil) {
			data = append(data, map[string]any{"itemId": "bg_1", "processId": "1", "command": "sleep", "cwd": th.cwd})
		}
		s.mu.Unlock()
		s.respond(fr.ID, map[string]any{"data": data, "nextCursor": nil})
	case "model/list":
		s.respond(fr.ID, map[string]any{"data": []any{map[string]any{"id": "gpt-fake", "displayName": "GPT Fake"}}})
	default:
		s.fail(fr.ID, -32601, "fakecodex does not implement "+fr.Method, nil)
	}
}

func (s *server) thread(id string) *thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threads[id]
}

func (s *server) threadJSON(th *thread) map[string]any {
	return map[string]any{"id": th.id, "sessionId": th.id, "path": th.path, "cwd": th.cwd, "modelProvider": "fake",
		"model": th.model, "status": map[string]any{"type": "idle"}, "turns": []any{}, "cliVersion": version}
}

func (s *server) threadResult(th *thread) map[string]any {
	return map[string]any{"thread": s.threadJSON(th), "model": th.model, "modelProvider": "fake",
		"approvalPolicy": "never", "sandbox": map[string]any{"type": "dangerFullAccess"}, "cwd": th.cwd}
}

// ------------------------------------------------------------ rollouts

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(time.Now().UnixMilli()))
	copy(b[:6], ms[2:])
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (s *server) newThread(cwd, model string) (*thread, error) {
	if model == "" {
		model = "gpt-fake"
	}
	now := time.Now().UTC()
	id := newUUID()
	dir := filepath.Join(s.home, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	th := &thread{id: id, cwd: cwd, model: model,
		path: filepath.Join(dir, "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")}
	s.mu.Lock()
	s.threads[id] = th
	s.mu.Unlock()
	s.record(th, "session_meta", map[string]any{"id": id, "session_id": id, "timestamp": now.Format(time.RFC3339Nano),
		"cwd": cwd, "originator": "socrates", "cli_version": version, "model_provider": "fake"})
	return th, nil
}

// record appends one rollout line.
func (s *server) record(th *thread, typ string, payload any) {
	line, _ := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": typ, "payload": payload})
	f, err := os.OpenFile(th.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// load finds a thread's rollout by its id under sessions/, the way codex
// scans for one it has not indexed, and reads it back.
func (s *server) load(id string) (*thread, error) {
	if th := s.thread(id); th != nil {
		return th, nil
	}
	var path string
	_ = filepath.WalkDir(filepath.Join(s.home, "sessions"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, id+".jsonl") {
			path = p
			return fs.SkipAll
		}
		return nil
	})
	if path == "" {
		return nil, fmt.Errorf("no rollout found for thread id %s", id)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	th := &thread{id: id, path: path, model: "gpt-fake"}
	byID := map[string]*fturn{}
	for _, line := range strings.Split(string(raw), "\n") {
		var rec struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		var p map[string]json.RawMessage
		_ = json.Unmarshal(rec.Payload, &p)
		str := func(k string) string {
			var v string
			_ = json.Unmarshal(p[k], &v)
			return v
		}
		switch rec.Type {
		case "session_meta":
			th.cwd = str("cwd")
		case "event_msg":
			switch str("type") {
			case "task_started":
				t := &fturn{id: str("turn_id"), status: "inProgress"}
				byID[t.id] = t
				th.turns = append(th.turns, t)
			case "item_completed":
				if t := byID[str("turn_id")]; t != nil {
					t.items = append(t.items, p["item"])
				}
			case "task_complete":
				if t := byID[str("turn_id")]; t != nil {
					t.status = "completed"
					if st := str("status"); st != "" {
						t.status = st
					}
					t.errText = str("error")
				}
			case "turn_aborted":
				if t := byID[str("turn_id")]; t != nil {
					t.status = "interrupted"
				}
			}
		case "response_item":
			switch str("type") {
			case "reasoning":
				if enc := str("encrypted_content"); enc != "" && !strings.HasPrefix(enc, "enc:"+s.account+":") {
					th.foreignEnc = true
				}
			case "message":
				if str("role") == "assistant" {
					th.assistant = true
				}
			}
		}
	}
	for _, t := range th.turns {
		if t.status == "inProgress" {
			// Codex died during it.
			t.status = "interrupted"
		}
	}
	s.mu.Lock()
	s.threads[id] = th
	s.mu.Unlock()
	return th, nil
}

// --------------------------------------------------------------- turns

func parseInput(raw json.RawMessage, clientID string) userInput {
	var items []map[string]any
	_ = json.Unmarshal(raw, &items)
	in := userInput{clientID: clientID}
	var texts []string
	for _, it := range items {
		c := map[string]any{"type": it["type"]}
		switch it["type"] {
		case "text":
			t, _ := it["text"].(string)
			texts = append(texts, t)
			c["text"], c["text_elements"] = t, []any{}
		case "localImage":
			c["path"], c["detail"] = it["path"], nil
		case "image":
			c["url"] = it["url"]
		}
		in.content = append(in.content, c)
	}
	in.text = strings.Join(texts, "\n")
	return in
}

func (s *server) turnStart(fr frame, threadID, clientID string, input json.RawMessage) {
	th := s.thread(threadID)
	if th == nil {
		s.fail(fr.ID, -32600, "thread not loaded: "+threadID, nil)
		return
	}
	s.mu.Lock()
	if th.active != nil {
		s.mu.Unlock()
		s.fail(fr.ID, -32600, "a turn is already running", nil)
		return
	}
	t := s.openTurnLocked(th, "normal")
	s.mu.Unlock()
	s.respond(fr.ID, map[string]any{"turn": map[string]any{"id": t.id, "items": []any{}, "status": "inProgress"}})
	in := parseInput(input, clientID)
	go s.run(th, t, in)
}

func (s *server) openTurnLocked(th *thread, kind string) *fturn {
	s.counter++
	t := &fturn{id: newUUID(), status: "inProgress", steer: make(chan userInput, 16)}
	if kind != "normal" {
		t.steer = nil
	}
	th.active = t
	th.turns = append(th.turns, t)
	return t
}

func (s *server) turnSteer(fr frame, threadID, expected, clientID string, input json.RawMessage) {
	th := s.thread(threadID)
	s.mu.Lock()
	var t *fturn
	if th != nil {
		t = th.active
	}
	s.mu.Unlock()
	switch {
	case t == nil || t.id != expected:
		s.fail(fr.ID, -32600, "no active turn to steer", nil)
	case t.steer == nil:
		s.fail(fr.ID, -32600, "cannot steer a compact turn", map[string]any{"message": "cannot steer a compact turn",
			"codexErrorInfo": map[string]any{"activeTurnNotSteerable": map[string]any{"turnKind": "compact"}}})
	default:
		t.steer <- parseInput(input, clientID)
		s.respond(fr.ID, map[string]any{"turnId": t.id})
	}
}

// tcx is one turn being run.
type tcx struct {
	s   *server
	th  *thread
	t   *fturn
	ctx context.Context
	n   int
}

func (c *tcx) base() map[string]any {
	return map[string]any{"threadId": c.th.id, "turnId": c.t.id}
}

func (c *tcx) with(extra map[string]any) map[string]any {
	m := c.base()
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func (c *tcx) started(item map[string]any) {
	c.s.notify("item/started", c.with(map[string]any{"item": item}))
}

func (c *tcx) completed(item map[string]any) {
	raw, _ := json.Marshal(item)
	c.s.mu.Lock()
	c.t.items = append(c.t.items, raw)
	c.s.mu.Unlock()
	c.s.notify("item/completed", c.with(map[string]any{"item": item}))
	c.s.record(c.th, "event_msg", map[string]any{"type": "item_completed", "thread_id": c.th.id, "turn_id": c.t.id, "item": item})
}

func (c *tcx) id(prefix string) string {
	c.n++
	return fmt.Sprintf("%s_%s_%d", prefix, c.t.id[len(c.t.id)-6:], c.n)
}

// wait is a pause that an interrupt cuts short; steered input is taken up
// at the end of it.
func (c *tcx) wait(d time.Duration, answer *strings.Builder) bool {
	select {
	case <-time.After(d):
	case <-c.ctx.Done():
		return false
	}
	c.takeSteered(answer)
	return true
}

func (c *tcx) takeSteered(answer *strings.Builder) {
	for {
		select {
		case in := <-c.t.steer:
			c.user(in)
			if answer != nil {
				answer.WriteString(" steered: " + in.text)
			}
		default:
			return
		}
	}
}

func (c *tcx) user(in userInput) {
	item := map[string]any{"type": "userMessage", "id": c.id("user"), "clientId": nilIfEmpty(in.clientID), "content": in.content}
	c.started(item)
	c.completed(item)
	c.s.record(c.th, "response_item", map[string]any{"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": in.text}}})
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *server) run(th *thread, t *fturn, in userInput) {
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	c := &tcx{s: s, th: th, t: t, ctx: ctx}
	s.record(th, "event_msg", map[string]any{"type": "task_started", "turn_id": t.id})
	s.record(th, "turn_context", map[string]any{"turn_id": t.id, "cwd": th.cwd, "model": th.model, "approval_policy": "never"})
	s.notify("thread/status/changed", map[string]any{"threadId": th.id, "status": map[string]any{"type": "active", "activeFlags": []any{}}})
	s.notify("turn/started", map[string]any{"threadId": th.id, "turn": map[string]any{"id": t.id, "rootTurnId": t.id, "items": []any{}, "status": "inProgress"}})
	c.user(in)
	s.mu.Lock()
	foreign, assistant := th.foreignEnc, th.assistant
	s.mu.Unlock()
	if foreign || s.strict && assistant {
		msg := `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content for item rs_1 could not be verified.","param":null,"type":"invalid_request_error"}}`
		c.end("failed", map[string]any{"message": msg, "codexErrorInfo": "other"})
		return
	}
	status, terr := c.script(in.text)
	c.end(status, terr)
}

// end closes the turn the way codex does: the usage, the status, the rollout.
func (c *tcx) end(status string, terr map[string]any) {
	s := c.s
	if status == "completed" {
		s.notify("thread/tokenUsage/updated", c.with(map[string]any{"tokenUsage": map[string]any{
			"total": tokens(), "last": tokens(), "modelContextWindow": 200000}}))
	}
	if terr != nil {
		s.notify("error", c.with(map[string]any{"error": terr, "willRetry": false}))
	}
	switch status {
	case "interrupted":
		s.record(c.th, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": c.t.id, "reason": "interrupted"})
	case "failed":
		msg, _ := terr["message"].(string)
		s.record(c.th, "event_msg", map[string]any{"type": "task_complete", "turn_id": c.t.id, "status": "failed", "error": msg})
	default:
		s.record(c.th, "event_msg", map[string]any{"type": "task_complete", "turn_id": c.t.id})
	}
	s.mu.Lock()
	c.t.status = status
	c.th.active = nil
	s.mu.Unlock()
	s.notify("thread/status/changed", map[string]any{"threadId": c.th.id, "status": map[string]any{"type": "idle"}})
	s.notify("turn/completed", map[string]any{"threadId": c.th.id, "turn": map[string]any{"id": c.t.id, "rootTurnId": c.t.id,
		"items": []any{}, "status": status, "error": terr}})
}

func tokens() map[string]any {
	return map[string]any{"totalTokens": 160, "inputTokens": 100, "cachedInputTokens": 40, "cacheWriteInputTokens": 0,
		"outputTokens": 60, "reasoningOutputTokens": 10}
}

func (c *tcx) rateLimits(used float64) {
	now := time.Now().Unix()
	c.s.notify("account/rateLimits/updated", map[string]any{"rateLimits": map[string]any{"limitId": "codex",
		"primary":   map[string]any{"usedPercent": used, "windowDurationMins": 300, "resetsAt": now + 3600},
		"secondary": map[string]any{"usedPercent": 42, "windowDurationMins": 10080, "resetsAt": now + 86400}}})
}

// reasoning is a reasoning item, and its encrypted content in the rollout.
func (c *tcx) reasoning() {
	id := c.id("rs")
	c.started(map[string]any{"type": "reasoning", "id": id, "summary": []any{}, "content": []any{}})
	c.s.notify("item/reasoning/summaryTextDelta", c.with(map[string]any{"itemId": id, "delta": "Thinking", "summaryIndex": 0}))
	c.completed(map[string]any{"type": "reasoning", "id": id, "summary": []any{"Thinking"}, "content": []any{}})
	c.s.record(c.th, "response_item", map[string]any{"type": "reasoning", "id": id,
		"summary": []any{map[string]any{"type": "summary_text", "text": "Thinking"}}, "encrypted_content": "enc:" + c.s.account + ":" + id})
}

// say streams an agent message in pieces, taking up steered input between
// them, and reports whether it was not interrupted.
func (c *tcx) say(pieces []string, every time.Duration) bool {
	id := c.id("msg")
	c.started(map[string]any{"type": "agentMessage", "id": id, "text": "", "phase": nil, "delivery": nil, "questions": nil})
	var text strings.Builder
	for _, p := range pieces {
		if !c.wait(every, &text) {
			c.s.notify("item/agentMessage/delta", c.with(map[string]any{"itemId": id, "delta": ""}))
			return false
		}
		before := text.Len()
		text.WriteString(p)
		c.s.notify("item/agentMessage/delta", c.with(map[string]any{"itemId": id, "delta": text.String()[before:]}))
	}
	c.takeSteered(&text)
	c.completed(map[string]any{"type": "agentMessage", "id": id, "text": text.String(), "phase": nil, "delivery": nil, "questions": nil})
	c.s.record(c.th, "response_item", map[string]any{"type": "message", "role": "assistant", "id": id,
		"content": []any{map[string]any{"type": "output_text", "text": text.String()}}})
	c.s.mu.Lock()
	c.th.assistant = true
	c.s.mu.Unlock()
	return true
}

func chunks(s string, n int) []string {
	var out []string
	r := []rune(s)
	for len(r) > 0 {
		k := min(n, len(r))
		out = append(out, string(r[:k]))
		r = r[k:]
	}
	return out
}

func atoi(fields []string, i, fallback int) int {
	if i < len(fields) {
		if n, err := strconv.Atoi(fields[i]); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

func atof(fields []string, i int, fallback float64) float64 {
	if i < len(fields) {
		if n, err := strconv.ParseFloat(fields[i], 64); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

// script runs one turn's directive and returns its status and error.
func (c *tcx) script(text string) (string, map[string]any) {
	word, rest, _ := strings.Cut(strings.TrimSpace(text), " ")
	fields := strings.Fields(rest)
	ok := true
	switch word {
	case "/stream":
		n, every := atoi(fields, 0, 20), atoi(fields, 1, 10)
		pieces := make([]string, n)
		for i := range pieces {
			pieces[i] = fmt.Sprintf("w%d ", i+1)
		}
		ok = c.say(pieces, time.Duration(every)*time.Millisecond)
	case "/sleep":
		if !c.wait(time.Duration(atof(fields, 0, 1)*float64(time.Second)), nil) {
			return "interrupted", nil
		}
		ok = c.say([]string{"slept"}, 0)
	case "/run":
		ok = c.command(rest) && c.say([]string{"ran it"}, 0)
	case "/patch":
		name := "file.txt"
		if len(fields) > 0 {
			name = fields[0]
		}
		c.patch(name)
		ok = c.say([]string{"patched"}, 0)
	case "/subagent":
		c.subagent()
		ok = c.say([]string{"spawned"}, 0)
	case "/plan":
		id := c.t.id + "-plan"
		c.started(map[string]any{"type": "plan", "id": id, "text": ""})
		c.s.notify("item/plan/delta", c.with(map[string]any{"itemId": id, "delta": "# Plan\n"}))
		c.completed(map[string]any{"type": "plan", "id": id, "text": "# Plan\n- one\n- two\n"})
		c.s.notify("turn/plan/updated", c.with(map[string]any{"explanation": "the steps", "plan": []any{
			map[string]any{"step": "one", "status": "completed"}, map[string]any{"step": "two", "status": "inProgress"}}}))
		ok = c.say([]string{"planned"}, 0)
	case "/limit":
		c.rateLimits(100)
		return "failed", map[string]any{"message": "You've hit your usage limit.", "codexErrorInfo": "usageLimitExceeded"}
	case "/crash":
		id := c.id("msg")
		c.started(map[string]any{"type": "agentMessage", "id": id, "text": ""})
		c.s.notify("item/agentMessage/delta", c.with(map[string]any{"itemId": id, "delta": "about to crash"}))
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "fakecodex: crashing as asked")
		os.Exit(3)
	case "/approve":
		r, err := c.s.ask(c.ctx, "item/commandExecution/requestApproval", c.with(map[string]any{"itemId": c.id("exec"),
			"command": "touch x", "cwd": c.th.cwd, "availableDecisions": []any{"accept", "cancel"}}))
		if err != nil {
			return "interrupted", nil
		}
		var res struct {
			Decision string `json:"decision"`
		}
		_ = json.Unmarshal(r.Result, &res)
		ok = c.say([]string{"decision: " + res.Decision}, 0)
	case "/elicit":
		r, err := c.s.ask(c.ctx, "mcpServer/elicitation/request", map[string]any{"threadId": c.th.id, "turnId": c.t.id,
			"serverName": "lab", "mode": "form", "_meta": nil, "message": "Deploy now?",
			"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}})
		if err != nil {
			return "interrupted", nil
		}
		var res struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(r.Result, &res)
		ok = c.say([]string{"action: " + res.Action}, 0)
	case "/bg":
		c.s.mu.Lock()
		c.th.bgUntil = time.Now().Add(time.Duration(atof(fields, 0, 1) * float64(time.Second)))
		c.s.mu.Unlock()
		ok = c.say([]string{"started a background terminal"}, 0)
	default:
		c.rateLimits(12)
		c.reasoning()
		ok = c.say(chunks("you said: "+text, 6), 5*time.Millisecond)
	}
	if !ok {
		return "interrupted", nil
	}
	return "completed", nil
}

func (c *tcx) command(cmd string) bool {
	id := c.id("call")
	item := map[string]any{"type": "commandExecution", "id": id, "command": "/bin/bash -lc '" + cmd + "'", "cwd": c.th.cwd,
		"processId": "42", "source": "agent", "status": "inProgress", "commandActions": []any{}, "aggregatedOutput": nil,
		"exitCode": nil, "durationMs": nil}
	c.started(item)
	var out strings.Builder
	for i := 1; i <= 3; i++ {
		if !c.wait(10*time.Millisecond, nil) {
			return false
		}
		line := fmt.Sprintf("line %d\n", i)
		out.WriteString(line)
		c.s.notify("item/commandExecution/outputDelta", c.with(map[string]any{"itemId": id, "delta": line}))
	}
	exit, status := 0, "completed"
	if strings.Contains(cmd, "fail") {
		exit, status = 1, "failed"
	}
	item["status"], item["aggregatedOutput"], item["exitCode"], item["durationMs"] = status, out.String(), exit, 30
	c.completed(item)
	return true
}

func (c *tcx) patch(name string) {
	path := filepath.Join(c.th.cwd, name)
	diff := "+hello from fakecodex\n"
	item := map[string]any{"type": "fileChange", "id": c.id("patch"), "status": "inProgress",
		"changes": []any{map[string]any{"path": path, "kind": map[string]any{"type": "add"}, "diff": diff}}}
	c.started(item)
	item["status"] = "completed"
	c.completed(item)
	c.s.notify("turn/diff/updated", c.with(map[string]any{"diff": "diff --git a/" + name + " b/" + name +
		"\nnew file mode 100644\n--- /dev/null\n+++ b/" + name + "\n@@ -0,0 +1 @@\n" + diff}))
}

// subagent spawns a thread that answers on its own, as codex's
// collaboration tools do: a subAgentActivity item in the turn, the child's
// turn and items on the child thread, and a completion item.
func (c *tcx) subagent() {
	child := newUUID()
	start := map[string]any{"type": "subAgentActivity", "id": c.id("call"), "kind": "started", "agentThreadId": child,
		"agentPath": "/root/helper", "model": "gpt-fake", "reasoningEffort": "low"}
	c.started(start)
	c.completed(start)
	childTurn := newUUID()
	cb := func(extra map[string]any) map[string]any {
		m := map[string]any{"threadId": child, "turnId": childTurn}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	c.s.notify("turn/started", map[string]any{"threadId": child, "turn": map[string]any{"id": childTurn, "rootTurnId": c.t.id, "status": "inProgress"}})
	msg := map[string]any{"type": "agentMessage", "id": "msg_sub", "text": ""}
	c.s.notify("item/started", cb(map[string]any{"item": msg}))
	c.s.notify("item/agentMessage/delta", cb(map[string]any{"itemId": "msg_sub", "delta": "sub done"}))
	msg["text"] = "sub done"
	c.s.notify("item/completed", cb(map[string]any{"item": msg}))
	c.s.notify("turn/completed", map[string]any{"threadId": child, "turn": map[string]any{"id": childTurn, "rootTurnId": c.t.id, "status": "completed"}})
	done := map[string]any{"type": "subAgentActivity", "id": "subagent-completed-" + childTurn, "kind": "completed",
		"agentThreadId": child, "agentPath": "/root/helper"}
	c.started(done)
	c.completed(done)
}

// special runs a turn that a command starts rather than a message: a
// compaction, a review or a shell command. None of them can be steered.
func (s *server) special(fr frame, threadID, kind, arg string) {
	th := s.thread(threadID)
	if th == nil {
		s.fail(fr.ID, -32600, "thread not loaded: "+threadID, nil)
		return
	}
	s.mu.Lock()
	if th.active != nil {
		s.mu.Unlock()
		s.fail(fr.ID, -32600, "a turn is already running", nil)
		return
	}
	t := s.openTurnLocked(th, kind)
	s.mu.Unlock()
	s.respond(fr.ID, map[string]any{})
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		t.cancel = cancel
		c := &tcx{s: s, th: th, t: t, ctx: ctx}
		s.record(th, "event_msg", map[string]any{"type": "task_started", "turn_id": t.id})
		s.notify("turn/started", map[string]any{"threadId": th.id, "turn": map[string]any{"id": t.id, "rootTurnId": t.id, "status": "inProgress"}})
		switch kind {
		case "compact":
			item := map[string]any{"type": "contextCompaction", "id": c.id("compact")}
			c.started(item)
			if !c.wait(50*time.Millisecond, nil) {
				c.end("interrupted", nil)
				return
			}
			c.completed(item)
		case "review":
			c.completed(map[string]any{"type": "enteredReviewMode", "id": c.id("review"), "review": "current changes"})
			c.completed(map[string]any{"type": "exitedReviewMode", "id": c.id("review"), "review": "No issues found."})
		case "shell":
			item := map[string]any{"type": "commandExecution", "id": c.id("shell"), "command": "/bin/bash -lc '" + arg + "'",
				"cwd": th.cwd, "source": "userShell", "status": "inProgress", "aggregatedOutput": nil, "exitCode": nil}
			c.started(item)
			out := "shell: " + arg + "\n"
			s.notify("item/commandExecution/outputDelta", c.with(map[string]any{"itemId": item["id"], "delta": out}))
			item["status"], item["aggregatedOutput"], item["exitCode"], item["durationMs"] = "completed", out, 0, 5
			c.completed(item)
		}
		c.end("completed", nil)
	}()
}

// ------------------------------------------------------------- history

func (s *server) turnsList(fr frame, threadID string) {
	th, err := s.load(threadID)
	if err != nil {
		s.fail(fr.ID, -32600, err.Error(), nil)
		return
	}
	var p struct {
		Cursor *string `json:"cursor"`
		Limit  int     `json:"limit"`
	}
	_ = json.Unmarshal(fr.Params, &p)
	if p.Limit <= 0 {
		p.Limit = 25
	}
	s.mu.Lock()
	turns := append([]*fturn(nil), th.turns...)
	s.mu.Unlock()
	start := 0
	if p.Cursor != nil {
		start, _ = strconv.Atoi(*p.Cursor)
	}
	end := min(start+p.Limit, len(turns))
	data := []any{}
	for _, t := range turns[start:end] {
		var terr any
		if t.errText != "" {
			terr = map[string]any{"message": t.errText}
		}
		data = append(data, map[string]any{"id": t.id, "rootTurnId": t.id, "items": []any{}, "itemsView": "notLoaded",
			"status": t.status, "error": terr})
	}
	var next any
	if end < len(turns) {
		next = strconv.Itoa(end)
	}
	s.respond(fr.ID, map[string]any{"data": data, "nextCursor": next, "backwardsCursor": nil})
}

func (s *server) itemsList(fr frame, threadID, turnID string) {
	th, err := s.load(threadID)
	if err != nil {
		s.fail(fr.ID, -32600, err.Error(), nil)
		return
	}
	var p struct {
		Cursor *string `json:"cursor"`
		Limit  int     `json:"limit"`
	}
	_ = json.Unmarshal(fr.Params, &p)
	if p.Limit <= 0 {
		p.Limit = 25
	}
	var items []json.RawMessage
	s.mu.Lock()
	for _, t := range th.turns {
		if turnID == "" || t.id == turnID {
			items = append(items, t.items...)
		}
	}
	s.mu.Unlock()
	start := 0
	if p.Cursor != nil {
		start, _ = strconv.Atoi(*p.Cursor)
	}
	end := min(start+p.Limit, len(items))
	data := []any{}
	for _, it := range items[start:end] {
		data = append(data, map[string]any{"turnId": turnID, "item": it})
	}
	var next any
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	s.respond(fr.ID, map[string]any{"data": data, "nextCursor": next, "backwardsCursor": nil})
}
