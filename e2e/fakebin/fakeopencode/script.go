//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The turns, by the text of the user's message:
//
//	/stream N [ms]   a text part of N deltas, ms apart (default 20 × 10 ms)
//	/reason          a reasoning part, then text
//	/tool            a bash tool streaming three lines of output, in a step of
//	                 its own, then text in a second step
//	/edit            an edit tool with a unified diff, then text
//	/todo            a todowrite tool with three items
//	/subagent        a task tool running a child session (a text and a read
//	                 tool part in it), then text
//	/limit           retry with a free-tier-limit action, every second, until
//	                 aborted (research/06 §3.7)
//	/retry           one plain retry, then a normal answer
//	/error           an APIError 401 on the message and session.error
//	/perm            permission.asked; the reply goes into the answer
//	/question [unseen]  question.asked; the answers go into the answer.
//	                 unseen: every /event stream is closed first, so that only
//	                 a reader of GET /question learns of it
//	/sleep S         text, then S seconds of work
//	/crash           a running bash tool, and the process exits on the spot
//	/dropstream      text, every /event stream is closed, then more text that
//	                 only a reader of GET …/message sees
//	/whoami          "account: <identity>"
//	/mcp             the local MCP servers of the config: connected or not,
//	                 their tools and the start of their instructions
//	/askuser [JSON]  the model calling socrates_ask_user (a call of two
//	                 questions, or the arguments given) on the "socrates"
//	                 server: a running tool part until the call returns, then
//	                 "ask_user returned: <result>"; an abort withdraws the call
//	                 with notifications/cancelled
//	anything else    "you said: <text> (turn N of <session>)"

var commands = []map[string]any{
	{"name": "init", "description": "guided AGENTS.md setup", "source": "command", "template": "init"},
	{"name": "review", "description": "review changes", "source": "command", "template": "review", "hints": []string{"$ARGUMENTS"}},
	{"name": "hello", "description": "say hello", "source": "command", "template": "hello $ARGUMENTS"},
}

type promptBody struct {
	MessageID string          `json:"messageID"`
	Model     json.RawMessage `json:"model"`
	Agent     string          `json:"agent"`
	Variant   string          `json:"variant"`
	Parts     []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Mime     string `json:"mime"`
		Filename string `json:"filename"`
		URL      string `json:"url"`
	} `json:"parts"`
}

// userMessage stores and announces a user message.
func (s *srv) userMessage(sid, id, agent string, parts []map[string]any) {
	if id == "" {
		id = newID("msg")
	}
	if agent == "" {
		agent = "build"
	}
	info := map[string]any{"id": id, "role": "user", "sessionID": sid, "agent": agent,
		"time":  map[string]any{"created": time.Now().UnixMilli()},
		"model": map[string]any{"providerID": "fake", "modelID": "model"}}
	for _, p := range parts {
		p["id"], p["messageID"], p["sessionID"] = newID("prt"), id, sid
	}
	_ = withStore(func(st *store) error {
		sess := st.Sessions[sid]
		sess.Messages = append(sess.Messages, &message{Info: info, Parts: parts})
		return nil
	})
	s.publish("message.updated", map[string]any{"sessionID": sid, "info": info})
	for _, p := range parts {
		s.publish("message.part.updated", map[string]any{"sessionID": sid, "part": p, "time": time.Now().UnixMilli()})
	}
}

func (s *srv) prompt(sid string, body promptBody) {
	var parts []map[string]any
	for _, p := range body.Parts {
		switch p.Type {
		case "text":
			parts = append(parts, map[string]any{"type": "text", "text": p.Text})
		case "file":
			parts = append(parts, map[string]any{"type": "file", "mime": p.Mime, "filename": p.Filename, "url": p.URL})
		}
	}
	s.userMessage(sid, body.MessageID, body.Agent, parts)
	s.ensureRun(sid)
}

// ensureRun starts the session's loop unless it runs.
func (s *srv) ensureRun(sid string) *run {
	s.mu.Lock()
	if r := s.runs[sid]; r != nil {
		s.mu.Unlock()
		return r
	}
	r := &run{cancel: make(chan struct{})}
	s.runs[sid] = r
	s.mu.Unlock()
	s.publish("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "busy"}})
	go s.loop(sid, r)
	return r
}

// loop answers the newest user message until it has an answer, the way
// OpenCode's session loop does.
func (s *srv) loop(sid string, r *run) {
	defer func() {
		s.mu.Lock()
		delete(s.runs, sid)
		s.mu.Unlock()
		s.publish("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "idle"}})
		s.publish("session.idle", map[string]any{"sessionID": sid})
	}()
	for {
		sess, _ := loadSession(sid)
		if sess == nil {
			return
		}
		var last *message
		turns := 0
		for _, m := range sess.Messages {
			if m.Info["role"] == "user" {
				last = m
				turns++
			}
		}
		if last == nil || answered(sess, last.Info["id"].(string)) {
			return
		}
		t := &turn{s: s, sid: sid, run: r, user: last.Info["id"].(string), n: turns}
		if err := t.play(textOf(last)); err != nil {
			t.aborted()
			return
		}
	}
}

func answered(sess *session, user string) bool {
	for _, m := range sess.Messages {
		if m.Info["role"] == "assistant" && m.Info["parentID"] == user {
			if _, done := m.Info["time"].(map[string]any)["completed"]; done {
				if f, _ := m.Info["finish"].(string); f != "tool-calls" {
					return true
				}
			}
		}
	}
	return false
}

func textOf(m *message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p["type"] == "text" {
			if syn, _ := p["synthetic"].(bool); !syn {
				b.WriteString(p["text"].(string))
			}
		}
	}
	return b.String()
}

// turn is one answer being written.
type turn struct {
	s    *srv
	sid  string
	run  *run
	user string
	n    int
	msg  map[string]any
}

var errAborted = errors.New("aborted")

func (t *turn) wait(d time.Duration) error {
	select {
	case <-t.run.cancel:
		return errAborted
	case <-time.After(d):
		return nil
	}
}

// step opens a new assistant message.
func (t *turn) step() {
	t.msg = map[string]any{"id": newID("msg"), "role": "assistant", "parentID": t.user, "sessionID": t.sid,
		"mode": "build", "agent": "build", "modelID": "model", "providerID": "fake", "cost": 0,
		"tokens": tokens(0, 0), "time": map[string]any{"created": time.Now().UnixMilli()}}
	_ = withStore(func(st *store) error {
		sess := st.Sessions[t.sid]
		sess.Messages = append(sess.Messages, &message{Info: t.msg, Parts: []map[string]any{}})
		return nil
	})
	t.s.publish("message.updated", map[string]any{"sessionID": t.sid, "info": t.msg})
	t.part(map[string]any{"type": "step-start", "snapshot": "0000"})
}

func tokens(in, out int) map[string]any {
	return map[string]any{"total": in + out, "input": in, "output": out, "reasoning": 0,
		"cache": map[string]any{"read": 0, "write": 0}}
}

// part stores a part (new or changed) and announces it.
func (t *turn) part(p map[string]any) map[string]any {
	if p["id"] == nil {
		p["id"] = newID("prt")
	}
	p["messageID"], p["sessionID"] = t.msg["id"], t.sid
	t.save(p)
	t.s.publish("message.part.updated", map[string]any{"sessionID": t.sid, "part": p, "time": time.Now().UnixMilli()})
	return p
}

func (t *turn) save(p map[string]any) {
	raw, _ := json.Marshal(p)
	var cp map[string]any
	_ = json.Unmarshal(raw, &cp)
	_ = withStore(func(st *store) error {
		for _, m := range st.Sessions[t.sid].Messages {
			if m.Info["id"] != t.msg["id"] {
				continue
			}
			for i, q := range m.Parts {
				if q["id"] == cp["id"] {
					m.Parts[i] = cp
					return nil
				}
			}
			m.Parts = append(m.Parts, cp)
		}
		return nil
	})
}

func (t *turn) saveInfo() {
	raw, _ := json.Marshal(t.msg)
	var cp map[string]any
	_ = json.Unmarshal(raw, &cp)
	_ = withStore(func(st *store) error {
		for _, m := range st.Sessions[t.sid].Messages {
			if m.Info["id"] == cp["id"] {
				m.Info = cp
			}
		}
		return nil
	})
	t.s.publish("message.updated", map[string]any{"sessionID": t.sid, "info": t.msg})
}

// say streams text into a new text part, in chunks, wait apart.
func (t *turn) say(chunks []string, wait time.Duration) error {
	p := t.part(map[string]any{"type": "text", "text": "", "time": map[string]any{"start": time.Now().UnixMilli()}})
	text := ""
	for _, c := range chunks {
		if err := t.wait(wait); err != nil {
			p["text"] = text
			t.save(p)
			return err
		}
		text += c
		t.s.publish("message.part.delta", map[string]any{"sessionID": t.sid, "messageID": t.msg["id"],
			"partID": p["id"], "field": "text", "delta": c})
	}
	p["text"] = text
	p["time"] = map[string]any{"start": p["time"].(map[string]any)["start"], "end": time.Now().UnixMilli()}
	t.part(p)
	return nil
}

// finish ends the step's message.
func (t *turn) finish(reason string, in, out int) {
	t.part(map[string]any{"type": "step-finish", "reason": reason, "snapshot": "0000", "cost": 0.001, "tokens": tokens(in, out)})
	t.msg["tokens"], t.msg["cost"], t.msg["finish"] = tokens(in, out), 0.001, reason
	t.msg["time"] = map[string]any{"created": t.msg["time"].(map[string]any)["created"], "completed": time.Now().UnixMilli()}
	t.saveInfo()
}

// aborted closes what the abort cut short, as OpenCode does.
func (t *turn) aborted() {
	if t.msg == nil {
		return
	}
	sess, _ := loadSession(t.sid)
	for _, m := range sess.Messages {
		if m.Info["id"] != t.msg["id"] {
			continue
		}
		for _, p := range m.Parts {
			if p["type"] != "tool" {
				continue
			}
			st := p["state"].(map[string]any)
			if st["status"] == "running" || st["status"] == "pending" {
				st["status"], st["error"] = "error", "Tool execution aborted"
				t.part(p)
			}
		}
	}
	abortErr := map[string]any{"name": "MessageAbortedError", "data": map[string]any{"message": "Aborted"}}
	t.msg["error"] = abortErr
	t.msg["time"] = map[string]any{"created": t.msg["time"].(map[string]any)["created"], "completed": time.Now().UnixMilli()}
	t.saveInfo()
	t.s.publish("session.error", map[string]any{"sessionID": t.sid, "error": abortErr})
}

// tool runs a tool part through pending, running (with output lines) and
// completed.
func (t *turn) tool(name string, input map[string]any, lines []string, meta map[string]any, title string) (map[string]any, error) {
	start := time.Now().UnixMilli()
	p := t.part(map[string]any{"type": "tool", "tool": name, "callID": "call_" + newID("x")[2:12],
		"state": map[string]any{"status": "pending", "input": map[string]any{}, "raw": ""}})
	if err := t.wait(5 * time.Millisecond); err != nil {
		return p, err
	}
	p["state"] = map[string]any{"status": "running", "input": input, "time": map[string]any{"start": start}}
	t.part(p)
	out := ""
	for _, l := range lines {
		if err := t.wait(20 * time.Millisecond); err != nil {
			return p, err
		}
		out += l
		p["state"] = map[string]any{"status": "running", "input": input, "time": map[string]any{"start": start},
			"metadata": map[string]any{"output": out}}
		t.part(p)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	if name == "bash" {
		meta["output"], meta["exit"] = out, 0
	}
	p["state"] = map[string]any{"status": "completed", "input": input, "output": out, "title": title, "metadata": meta,
		"time": map[string]any{"start": start, "end": time.Now().UnixMilli()}}
	t.part(p)
	return p, nil
}

// play writes the answer to text.
func (t *turn) play(text string) error {
	word, args, _ := strings.Cut(strings.TrimSpace(text), " ")
	fields := strings.Fields(args)
	t.step()
	switch word {
	case "/stream":
		n, every := atoi(fields, 0, 20), time.Duration(atoi(fields, 1, 10))*time.Millisecond
		chunks := make([]string, n)
		for i := range chunks {
			chunks[i] = fmt.Sprintf("w%d ", i+1)
		}
		if err := t.say(chunks, every); err != nil {
			return err
		}
	case "/reason":
		p := t.part(map[string]any{"type": "reasoning", "text": "", "time": map[string]any{"start": time.Now().UnixMilli()}})
		t.s.publish("message.part.delta", map[string]any{"sessionID": t.sid, "messageID": t.msg["id"], "partID": p["id"], "field": "text", "delta": "thinking it over"})
		p["text"], p["time"] = "thinking it over", map[string]any{"start": time.Now().UnixMilli(), "end": time.Now().UnixMilli()}
		t.part(p)
		if err := t.say([]string{"reasoned"}, 5*time.Millisecond); err != nil {
			return err
		}
	case "/tool":
		if _, err := t.tool("bash", map[string]any{"command": "echo hello", "description": "Say hello"},
			[]string{"line 1\n", "line 2\n", "line 3\n"}, nil, "echo hello"); err != nil {
			return err
		}
		t.finish("tool-calls", 100, 10)
		t.step()
		if err := t.say([]string{"The tool ", "finished."}, 5*time.Millisecond); err != nil {
			return err
		}
	case "/edit":
		patch := "Index: a.txt\n===================================================================\n--- a.txt\n+++ a.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n"
		meta := map[string]any{"diff": patch, "filediff": map[string]any{"file": "a.txt", "patch": patch, "additions": 1, "deletions": 1}}
		if _, err := t.tool("edit", map[string]any{"filePath": "a.txt", "oldString": "old", "newString": "new"}, nil, meta, "a.txt"); err != nil {
			return err
		}
		if err := t.say([]string{"edited"}, 5*time.Millisecond); err != nil {
			return err
		}
	case "/todo":
		todos := []map[string]any{{"content": "first", "status": "completed", "priority": "high"},
			{"content": "second", "status": "in_progress", "priority": "medium"}, {"content": "third", "status": "pending", "priority": "low"}}
		if _, err := t.tool("todowrite", map[string]any{"todos": todos}, nil, map[string]any{"todos": todos}, "3 todos"); err != nil {
			return err
		}
		t.s.publish("todo.updated", map[string]any{"sessionID": t.sid, "todos": todos})
		if err := t.say([]string{"planned"}, 5*time.Millisecond); err != nil {
			return err
		}
	case "/subagent":
		if err := t.subagent(); err != nil {
			return err
		}
	case "/limit":
		for attempt := 1; ; attempt++ {
			t.s.publish("session.status", map[string]any{"sessionID": t.sid, "status": map[string]any{"type": "retry",
				"attempt": attempt, "message": "Free usage exceeded, subscribe to Go", "next": time.Now().Add(time.Second).UnixMilli(),
				"action": map[string]any{"reason": "free_tier_limit", "provider": "fake", "title": "Free limit reached",
					"message": "Subscribe to OpenCode Go for reliable access.", "label": "subscribe"}}})
			if err := t.wait(time.Second); err != nil {
				return err
			}
		}
	case "/retry":
		t.s.publish("session.status", map[string]any{"sessionID": t.sid, "status": map[string]any{"type": "retry",
			"attempt": 1, "message": "Provider is overloaded", "next": time.Now().Add(50 * time.Millisecond).UnixMilli()}})
		if err := t.wait(50 * time.Millisecond); err != nil {
			return err
		}
		t.s.publish("session.status", map[string]any{"sessionID": t.sid, "status": map[string]any{"type": "busy"}})
		if err := t.say([]string{"after a retry"}, 5*time.Millisecond); err != nil {
			return err
		}
	case "/error":
		e := map[string]any{"name": "APIError", "data": map[string]any{"message": "User not found.", "statusCode": 401, "isRetryable": false}}
		t.msg["error"] = e
		t.finish("error", 10, 0)
		t.s.publish("session.error", map[string]any{"sessionID": t.sid, "error": e})
		return nil
	case "/perm", "/question":
		answer, err := t.ask(word, firstOr(fields, "") == "unseen")
		if err != nil {
			return err
		}
		if err := t.say([]string{word[1:] + ": " + answer}, 5*time.Millisecond); err != nil {
			return err
		}
	case "/sleep":
		secs, _ := strconv.ParseFloat(firstOr(fields, "1"), 64)
		if err := t.say([]string{"working"}, 5*time.Millisecond); err != nil {
			return err
		}
		if err := t.wait(time.Duration(secs * float64(time.Second))); err != nil {
			return err
		}
	case "/crash":
		p := t.part(map[string]any{"type": "tool", "tool": "bash", "callID": "call_crash",
			"state": map[string]any{"status": "running", "input": map[string]any{"command": "sleep 100"},
				"time": map[string]any{"start": time.Now().UnixMilli()}}})
		_ = p
		os.Exit(3)
	case "/dropstream":
		if err := t.say([]string{"before the drop"}, 5*time.Millisecond); err != nil {
			return err
		}
		t.s.dropStreams()
		if err := t.say([]string{"after the drop"}, 5*time.Millisecond); err != nil {
			return err
		}
	case "/mcp":
		if err := t.say([]string{t.s.mcpStatus()}, 5*time.Millisecond); err != nil {
			return err
		}
	case "/askuser":
		if err := t.askUser(args); err != nil {
			return err
		}
	case "/whoami":
		if err := t.say([]string{"account: " + identity()}, 5*time.Millisecond); err != nil {
			return err
		}
	default:
		reply := fmt.Sprintf("you said: %s (turn %d of %s)", text, t.n, t.sid)
		var chunks []string
		for len(reply) > 0 {
			k := min(8, len(reply))
			chunks = append(chunks, reply[:k])
			reply = reply[k:]
		}
		if err := t.say(chunks, 5*time.Millisecond); err != nil {
			return err
		}
	}
	t.finish("stop", 1000+t.n, 20)
	return nil
}

// subagent runs a task tool with a child session.
func (t *turn) subagent() error {
	child := &session{ID: newID("ses"), ParentID: t.sid, Title: "child", Messages: []*message{}}
	_ = withStore(func(st *store) error {
		child.Directory = st.Sessions[t.sid].Directory
		st.Sessions[child.ID] = child
		return nil
	})
	t.s.publish("session.created", map[string]any{"sessionID": child.ID, "info": sessionInfo(child)})
	start := time.Now().UnixMilli()
	input := map[string]any{"description": "Look around", "prompt": "look", "subagent_type": "explore"}
	meta := map[string]any{"parentSessionId": t.sid, "sessionId": child.ID}
	task := t.part(map[string]any{"type": "tool", "tool": "task", "callID": "call_task",
		"state": map[string]any{"status": "running", "input": input, "title": "Look around", "metadata": meta,
			"time": map[string]any{"start": start}}})
	sub := &turn{s: t.s, sid: child.ID, run: t.run, n: 1}
	sub.s.userMessage(child.ID, "", "explore", []map[string]any{{"type": "text", "text": "look"}})
	sess, _ := loadSession(child.ID)
	sub.user = sess.Messages[0].Info["id"].(string)
	sub.step()
	if err := sub.say([]string{"child ", "says hi"}, 5*time.Millisecond); err != nil {
		return err
	}
	if _, err := sub.tool("read", map[string]any{"filePath": "README"}, nil, nil, "README"); err != nil {
		return err
	}
	sub.finish("stop", 50, 5)
	task["state"] = map[string]any{"status": "completed", "input": input, "title": "Look around", "metadata": meta,
		"output": "task_id: " + child.ID + "\n\n<task_result>child says hi</task_result>",
		"time":   map[string]any{"start": start, "end": time.Now().UnixMilli()}}
	t.part(task)
	return t.say([]string{"the subagent is done"}, 5*time.Millisecond)
}

// ask raises a permission or a question and waits for its answer. unseen
// drops the event streams before a question is asked.
func (t *turn) ask(kind string, unseen bool) (string, error) {
	ch := make(chan string, 1)
	if kind == "/perm" {
		id := newID("per")
		t.s.mu.Lock()
		t.s.pending[id] = ch
		t.s.mu.Unlock()
		t.s.publish("permission.asked", map[string]any{"id": id, "sessionID": t.sid, "permission": "bash",
			"patterns": []string{"rm -rf build"}, "always": []string{"rm *"}, "metadata": map[string]any{}})
	} else {
		id := newID("que")
		props := map[string]any{"id": id, "sessionID": t.sid, "questions": []map[string]any{{
			"question": "Which colour?", "header": "Colour", "options": []map[string]any{{"label": "Red"}, {"label": "Blue"}}}}}
		if unseen {
			t.s.dropStreams()
		}
		t.s.mu.Lock()
		t.s.pending[id] = ch
		t.s.questions[id] = props
		t.s.mu.Unlock()
		t.s.publish("question.asked", props)
	}
	select {
	case a := <-ch:
		return a, nil
	case <-t.run.cancel:
		return "", errAborted
	}
}

// shell runs a "!" line: a user message and a bash tool in an assistant
// message, answered when it is done. A busy session refuses it.
func (s *srv) shell(w http.ResponseWriter, r *http.Request, sid string) {
	var body struct {
		MessageID string `json:"messageID"`
		Command   string `json:"command"`
		Agent     string `json:"agent"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Agent == "" {
		writeJSON(w, 400, map[string]any{"name": "BadRequest", "data": map[string]any{"message": "agent is required"}})
		return
	}
	s.mu.Lock()
	if s.runs[sid] != nil {
		s.mu.Unlock()
		writeJSON(w, 409, map[string]any{"_tag": "SessionBusyError", "sessionID": sid, "message": "Session is busy: " + sid})
		return
	}
	run := &run{cancel: make(chan struct{})}
	s.runs[sid] = run
	s.mu.Unlock()
	s.publish("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "busy"}})
	defer func() {
		s.mu.Lock()
		delete(s.runs, sid)
		s.mu.Unlock()
		s.publish("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "idle"}})
		s.publish("session.idle", map[string]any{"sessionID": sid})
	}()
	id := body.MessageID
	if id == "" {
		id = newID("msg")
	}
	s.userMessage(sid, id, body.Agent, []map[string]any{{"type": "text", "synthetic": true,
		"text": "The following tool was executed by the user"}})
	t := &turn{s: s, sid: sid, run: run, user: id}
	t.step()
	if _, err := t.tool("bash", map[string]any{"command": body.Command}, []string{"fake shell: " + body.Command + "\n"}, nil, body.Command); err != nil {
		t.aborted()
		writeJSON(w, 200, map[string]any{"info": t.msg})
		return
	}
	t.finish("stop", 0, 0)
	writeJSON(w, 200, map[string]any{"info": t.msg})
}

// command runs a slash command as a prompt of its template, answered when the
// turn is done.
func (s *srv) command(w http.ResponseWriter, r *http.Request, sid string) {
	var body struct {
		MessageID string `json:"messageID"`
		Command   string `json:"command"`
		Arguments string `json:"arguments"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	found := false
	for _, c := range commands {
		if c["name"] == body.Command {
			found = true
		}
	}
	if !found {
		writeJSON(w, 400, map[string]any{"name": "UnknownError", "data": map[string]any{"message": "Command not found: " + body.Command}})
		return
	}
	s.userMessage(sid, body.MessageID, "build", []map[string]any{{"type": "text", "text": "ran /" + body.Command + " " + body.Arguments}})
	r2 := s.ensureRun(sid)
	select {
	case <-r2.cancel:
	case <-s.idle(sid):
	}
	sess, _ := loadSession(sid)
	writeJSON(w, 200, sess.Messages[len(sess.Messages)-1])
}

// idle is closed once the session's loop has ended.
func (s *srv) idle(sid string) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		for {
			s.mu.Lock()
			busy := s.runs[sid] != nil
			s.mu.Unlock()
			if !busy {
				close(ch)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return ch
}

// summarize compacts: a user message with a compaction part and a summary
// answer, then session.compacted.
func (s *srv) summarize(w http.ResponseWriter, sid string) {
	s.mu.Lock()
	if s.runs[sid] != nil {
		s.mu.Unlock()
		writeJSON(w, 409, map[string]any{"_tag": "SessionBusyError", "sessionID": sid, "message": "Session is busy: " + sid})
		return
	}
	run := &run{cancel: make(chan struct{})}
	s.runs[sid] = run
	s.mu.Unlock()
	s.publish("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "busy"}})
	id := newID("msg")
	s.userMessage(sid, id, "build", []map[string]any{{"type": "compaction", "auto": false}})
	t := &turn{s: s, sid: sid, run: run, user: id}
	t.step()
	t.msg["summary"], t.msg["mode"] = true, "compaction"
	_ = t.say([]string{"Summary of the conversation."}, 5*time.Millisecond)
	t.finish("stop", 10, 5)
	s.publish("session.compacted", map[string]any{"sessionID": sid})
	s.mu.Lock()
	delete(s.runs, sid)
	s.mu.Unlock()
	s.publish("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "idle"}})
	s.publish("session.idle", map[string]any{"sessionID": sid})
	writeJSON(w, 200, true)
}

func atoi(fields []string, i, fallback int) int {
	if i < len(fields) {
		if n, err := strconv.Atoi(fields[i]); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

func firstOr(fields []string, fallback string) string {
	if len(fields) > 0 {
		return fields[0]
	}
	return fallback
}

// signalIgnoreTerm makes the server deaf to SIGTERM, the way a real one is
// while its first-start npm install runs, so the SIGKILL after the grace is
// tested.
func signalIgnoreTerm() { signal.Ignore(syscall.SIGTERM) }
