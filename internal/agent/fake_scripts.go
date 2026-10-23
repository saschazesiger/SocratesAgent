//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The scripted agent's richer turns: the shapes a chat view has to draw, with
// the content documented in AGENT-HOST.md §2.1, so that the view and its
// end-to-end scenarios can be built and measured before a real adapter emits
// the same shapes.

// fakeCommands is the agent's palette: what the composer offers after "/".
var fakeCommands = []CommandInfo{
	{Name: "compact", Description: "Summarize the conversation to free up context"},
	{Name: "context", Description: "Show how much of the context window is in use"},
}

// fakeInfo is what the scripted agent says about itself (agent.info).
var fakeInfo = json.RawMessage(`{"agent":"fake","version":"1.0.0",` +
	`"commands":[{"name":"compact","description":"Summarize the conversation to free up context"},` +
	`{"name":"context","description":"Show how much of the context window is in use"}],` +
	`"models":[{"id":"fake-small","label":"Fake small","efforts":["low","medium","high"]},` +
	`{"id":"fake-large","label":"Fake large","efforts":["low","medium","high"]}]}`)

// fakeAskPayload is a full ask_user call (REBUILD.md §5.1): a title, a
// context, and one question of every kind, with options that carry
// descriptions, details and a recommendation.
var fakeAskPayload = json.RawMessage(`{
  "title": "Deployment setup",
  "context": "We are about to deploy **the API**. These choices decide how it runs.",
  "questions": [
    {"id": "db", "header": "Database", "question": "Which database should the service use?",
     "context": "Both work. PostgreSQL scales further; SQLite is one file.\n\n` + "```sql\\nSELECT version();\\n```" + `",
     "kind": "single", "default": "pg",
     "options": [
       {"id": "pg", "label": "PostgreSQL", "description": "The robust default", "recommended": true,
        "details": "**Pros**\n\n- JSONB and full text search\n- streaming replication\n\n**Cons**: one more server to run."},
       {"id": "sqlite", "label": "SQLite", "description": "One file, no server",
        "details": "Fine for one machine. Backups are a file copy."},
       {"id": "mysql", "label": "MySQL", "description": "If the team already knows it"}
     ]},
    {"id": "features", "header": "Features", "question": "Which features ship first?", "kind": "multi",
     "min": 1, "max": 3,
     "options": [
       {"id": "dark", "label": "Dark mode"},
       {"id": "search", "label": "Search", "description": "Full text over every record"},
       {"id": "export", "label": "Export", "description": "CSV and JSON"},
       {"id": "offline", "label": "Offline sync"}
     ]},
    {"id": "name", "header": "Name", "question": "What should the service be called?", "kind": "text"},
    {"id": "migrate", "header": "Migrations", "question": "Run the migrations on every deploy?", "kind": "confirm",
     "default": true},
    {"id": "replicas", "header": "Replicas", "question": "How many replicas should run?", "kind": "number",
     "min": 1, "max": 10, "default": 2, "allow_other": false}
  ]
}`)

// fakeMarkdown is the long answer /md streams.
const fakeMarkdown = "## Plan for the API\n\n" +
	"The service needs **three** changes before it can ship. Each one is small, and together they " +
	"remove the last `TODO` from the handler.\n\n" +
	"1. Validate the request body.\n" +
	"2. Return `404` for an unknown id.\n" +
	"3. Log every failure with its request id.\n\n" +
	"Here is the handler after the change:\n\n" +
	"```go\n" +
	"func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {\n" +
	"\tid := r.PathValue(\"id\")\n" +
	"\trow, err := s.store.Get(id)\n" +
	"\tif errors.Is(err, store.ErrNotFound) {\n" +
	"\t\thttp.Error(w, \"not found\", http.StatusNotFound)\n" +
	"\t\treturn\n" +
	"\t}\n" +
	"\twriteJSON(w, http.StatusOK, row)\n" +
	"}\n" +
	"```\n\n" +
	"| Case | Before | After |\n" +
	"| --- | --- | --- |\n" +
	"| unknown id | 500 | 404 |\n" +
	"| bad body | 500 | 400 |\n\n" +
	"> The tests in `handler_test.go` cover both cases.\n\n" +
	"See [the HTTP status list](https://developer.mozilla.org/en-US/docs/Web/HTTP/Status) for the codes. " +
	"A very long line of output follows, so that a phone has to scroll it inside its block rather than the page: " +
	"`aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`.\n"

// markdown streams a reasoning part, then fakeMarkdown in small chunks.
func (f *fakeAdapter) markdown(ctx context.Context, t *fakeTurn, every time.Duration) bool {
	id := t.msgID + "_think"
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, Kind: PartReasoning, State: PartStreaming})
	thought := "The person wants the plan in one place. A list, the code and a table will do."
	var said strings.Builder
	for _, chunk := range fakeChunks(thought, 12) {
		if !fakeWait(ctx, every) {
			return false
		}
		said.WriteString(chunk)
		f.out.Delta(t.msgID, id, chunk)
	}
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, Kind: PartReasoning, State: PartDone, Text: said.String()})
	for _, chunk := range fakeChunks(fakeMarkdown, 8) {
		if !fakeWait(ctx, every) {
			return false
		}
		t.say(chunk)
	}
	return true
}

// toolPart is one tool call from running to its end, its output streamed.
func (f *fakeAdapter) toolPart(ctx context.Context, t *fakeTurn, id, parent, name string, input any,
	output []string, state string, extra map[string]any) bool {
	content := map[string]any{"name": name, "input": input}
	raw, _ := json.Marshal(content)
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, ParentPartID: parent, Kind: PartTool,
		State: PartRunning, Content: raw})
	var text strings.Builder
	for _, line := range output {
		if !fakeWait(ctx, 30*time.Millisecond) {
			return false
		}
		text.WriteString(line + "\n")
		f.out.Delta(t.msgID, id, line+"\n")
	}
	for k, v := range extra {
		content[k] = v
	}
	raw, _ = json.Marshal(content)
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, ParentPartID: parent, Kind: PartTool,
		State: state, Content: raw, Text: text.String()})
	return true
}

// fakeDiff is the edit /tools makes, as a unified diff of two files.
const fakeDiff = "--- a/server/handler.go\n+++ b/server/handler.go\n" +
	"@@ -10,7 +10,10 @@ func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {\n" +
	" \tid := r.PathValue(\"id\")\n" +
	" \trow, err := s.store.Get(id)\n" +
	"-\tif err != nil {\n" +
	"-\t\thttp.Error(w, err.Error(), 500)\n" +
	"+\tif errors.Is(err, store.ErrNotFound) {\n" +
	"+\t\thttp.Error(w, \"not found\", http.StatusNotFound)\n" +
	"+\t\treturn\n" +
	"+\t}\n" +
	"+\tif err != nil {\n" +
	"+\t\thttp.Error(w, \"failed\", http.StatusInternalServerError)\n" +
	" \t\treturn\n" +
	" \t}\n" +
	" \twriteJSON(w, http.StatusOK, row)\n" +
	"--- a/server/handler_test.go\n+++ b/server/handler_test.go\n" +
	"@@ -1,3 +1,4 @@\n" +
	" package server\n" +
	" \n" +
	"+// TestUnknownIDIsNotFound covers the new branch.\n" +
	" import \"testing\"\n"

// tools is a read, a command with streamed output, an edit with its diff, and
// a search that fails.
func (f *fakeAdapter) tools(ctx context.Context, t *fakeTurn) bool {
	steps := []func() bool{
		func() bool {
			return f.toolPart(ctx, t, t.msgID+"_read", "", "Read", map[string]any{"file_path": "/srv/app/server/handler.go"},
				[]string{"package server", "", "func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {"}, PartDone, nil)
		},
		func() bool {
			return f.toolPart(ctx, t, t.msgID+"_bash", "", "Bash", map[string]any{"command": "go test ./server/...", "description": "Run the server tests"},
				[]string{"=== RUN   TestHandleGet", "--- PASS: TestHandleGet (0.01s)", "PASS", "ok  \tapp/server\t0.214s"},
				PartDone, map[string]any{"exit_code": 0})
		},
		func() bool {
			return f.toolPart(ctx, t, t.msgID+"_edit", "", "Edit", map[string]any{"file_path": "/srv/app/server/handler.go",
				"old_string": "if err != nil {", "new_string": "if errors.Is(err, store.ErrNotFound) {"}, nil, PartDone, nil)
		},
	}
	for _, step := range steps {
		if !step() {
			return false
		}
	}
	diff, _ := json.Marshal(map[string]any{"files": []map[string]any{
		{"path": "server/handler.go", "status": "modified", "diff": fakeDiff[:strings.Index(fakeDiff, "--- a/server/handler_test.go")]},
		{"path": "server/handler_test.go", "status": "modified", "diff": fakeDiff[strings.Index(fakeDiff, "--- a/server/handler_test.go"):]},
	}})
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: t.msgID + "_diff", Kind: PartDiff, State: PartDone, Content: diff})
	return f.toolPart(ctx, t, t.msgID+"_grep", "", "Grep", map[string]any{"pattern": "TODO", "path": "/srv/app"},
		[]string{"grep: /srv/app/vendor: Permission denied"}, PartError,
		map[string]any{"exit_code": 2, "error": "Permission denied"})
}

// todoContent is a task list with the first `done` tasks completed and the
// next one in progress.
func todoContent(done int) json.RawMessage {
	items := []string{"Validate the request body", "Return 404 for an unknown id", "Log every failure"}
	todos := make([]map[string]any, len(items))
	for i, item := range items {
		status := "pending"
		switch {
		case i < done:
			status = "completed"
		case i == done:
			status = "in_progress"
		}
		todos[i] = map[string]any{"content": item, "status": status, "active_form": "Working on: " + strings.ToLower(item)}
	}
	raw, _ := json.Marshal(map[string]any{"todos": todos})
	return raw
}

// todo works through three tasks, replacing the list part after each one.
func (f *fakeAdapter) todo(ctx context.Context, t *fakeTurn, every time.Duration) bool {
	id := t.msgID + "_todo"
	for done := 0; done <= 3; done++ {
		state := PartRunning
		if done == 3 {
			state = PartDone
		}
		f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, Kind: PartTodo, State: state, Content: todoContent(done)})
		if done < 3 && !fakeWait(ctx, every) {
			return false
		}
	}
	return true
}

// workflow is a Workflow tool part with three phases, and an agent per phase
// nested in it, each with a part of its own nested in the agent.
func (f *fakeAdapter) workflow(ctx context.Context, t *fakeTurn, every time.Duration) bool {
	group := t.msgID + "_wf"
	phases := []map[string]any{
		{"id": "plan", "title": "Plan", "state": "pending"},
		{"id": "build", "title": "Build", "state": "pending"},
		{"id": "review", "title": "Review", "state": "pending"},
	}
	agents := []struct{ id, phase, task string }{
		{"planner", "plan", "Write the plan"},
		{"api", "build", "Build the API"},
		{"ui", "build", "Build the page"},
		{"reviewer", "review", "Review the change"},
	}
	emitGroup := func(state string) {
		raw, _ := json.Marshal(map[string]any{"name": "Workflow", "input": map[string]any{"description": "Ship the feature"},
			"workflow": map[string]any{"phases": phases}})
		f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: group, Kind: PartTool, State: state, Content: raw})
	}
	agent := func(i int, state string) {
		a := agents[i]
		raw, _ := json.Marshal(map[string]any{"name": "Agent", "input": map[string]any{"description": a.task}, "phase": a.phase,
			"agent": a.id})
		f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: group + "_" + a.id, ParentPartID: group,
			Kind: PartTool, State: state, Content: raw})
	}
	emitGroup(PartRunning)
	for p, phase := range phases {
		phase["state"] = "running"
		emitGroup(PartRunning)
		for i, a := range agents {
			if a.phase == phase["id"] {
				agent(i, PartRunning)
			}
		}
		if !fakeWait(ctx, every) {
			return false
		}
		for i, a := range agents {
			if a.phase != phase["id"] {
				continue
			}
			f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: group + "_" + a.id + "_text",
				ParentPartID: group + "_" + a.id, Kind: PartText, State: PartDone, Text: a.task + ": done."})
			agent(i, PartDone)
		}
		phase["state"] = "done"
		if p == len(phases)-1 {
			emitGroup(PartDone)
		} else {
			emitGroup(PartRunning)
		}
	}
	return true
}

// fakePlan is the plan /plan asks to have approved.
const fakePlan = "## Plan\n\n1. Add the `404` branch to `handleGet`.\n2. Cover it with a test.\n3. Run the whole suite."

// plan shows a plan and waits until it is approved or sent back.
func (f *fakeAdapter) plan(ctx context.Context, t *fakeTurn) bool {
	askID := "ask_" + fakeID()
	content, _ := json.Marshal(map[string]any{"plan": fakePlan, "ask_id": askID})
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: t.msgID + "_plan", Kind: PartPlan, State: PartDone,
		Text: fakePlan, Content: content})
	payload, _ := json.Marshal(map[string]any{"kind": "plan", "title": "Approve the plan", "plan": fakePlan})
	answer, ok := f.askID(ctx, t, askID, payload)
	if !ok {
		return false
	}
	var reply struct {
		Approved bool   `json:"approved"`
		Note     string `json:"note"`
	}
	_ = json.Unmarshal(answer, &reply)
	if reply.Approved {
		t.say("The plan is approved; starting on it.")
	} else {
		t.say(fmt.Sprintf("Planning on: %s", reply.Note))
	}
	return true
}

// dividers writes one of every line a chat shows between messages.
func (f *fakeAdapter) dividers(t *fakeTurn) {
	f.out.Emit(
		Event{Type: TypeNotice, Code: "compacted", Text: "The conversation was compacted."},
		Event{Type: TypeAccountSwitched, From: "Work", To: "Personal", Reason: "The limit of Work was reached."},
		Event{Type: TypeModelChanged, Model: "fake-large", Effort: "high"},
		Event{Type: TypeError, Code: "send_failed", Text: "A message could not be handed to the agent."},
	)
	f.mu.Lock()
	f.model, f.effort = "fake-large", "high"
	f.mu.Unlock()
}
