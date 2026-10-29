//go:build linux

package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// noticesOf is the texts of the chat's notices with code.
func noticesOf(rec *recorder, code string) []string {
	var out []string
	rec.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.Type == agent.TypeNotice && ev.Code == code {
				out = append(out, ev.Text)
			}
		}
	})
	return out
}

// A folder's MCP server OpenCode could not start is said in the chat, once,
// with why: GET /mcp is the only place OpenCode keeps it.
func TestAnMCPServerThatDidNotStartIsSaid(t *testing.T) {
	r := newRig(t)
	spec := r.spec("")
	spec.MCPServers = []agent.MCPServer{{Name: "broken", Transport: agent.MCPStdio, Command: filepath.Join(r.data, "no-such-server")}}
	if err := r.start(spec); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "the MCP notice", func() bool { return len(noticesOf(r.rec, agent.NoticeMCP)) > 0 })
	said := noticesOf(r.rec, agent.NoticeMCP)
	if len(said) != 1 || !strings.Contains(said[0], `"broken"`) || !strings.Contains(said[0], "no-such-server") {
		t.Fatalf("MCP notices %q", said)
	}
	// Read again (a catch-up, a server whose tools changed), the same
	// state is not said again.
	r.a.checkMCP(context.Background(), r.a.gen, r.a.client())
	r.a.handle(r.a.gen, ocEvent{Type: "mcp.tools.changed", Properties: json.RawMessage(`{"server":"broken"}`)})
	time.Sleep(300 * time.Millisecond)
	if said := noticesOf(r.rec, agent.NoticeMCP); len(said) != 1 {
		t.Fatalf("MCP notices after reading again %q", said)
	}
	r.send("hello")
	r.waitTurns(1)
}

// Every state GET /mcp reports (1.19.5) but connected and disabled - the
// config turns off a project's own servers - is a line, by name.
func TestTheMCPStatesThatAreProblems(t *testing.T) {
	var status map[string]ocMCPStatus
	raw := `{"broken":{"status":"failed","error":"ENOENT: no such file or directory, posix_spawn '/nonexistent/mcp-server'"},
		"crashes":{"status":"failed","error":"MCP error -32000: Connection closed"},"authme":{"status":"needs_auth"},
		"reg":{"status":"needs_client_registration","error":"Server does not support dynamic client registration."},
		"off":{"status":"disabled"},"socrates":{"status":"connected"}}`
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(mcpProblems(status), "\n")
	want := []string{
		`The MCP server "authme" needs a sign-in: run opencode mcp auth authme`,
		`OpenCode could not start the MCP server "broken": ENOENT: no such file or directory, posix_spawn '/nonexistent/mcp-server'`,
		`OpenCode could not start the MCP server "crashes": MCP error -32000: Connection closed`,
		`The MCP server "reg" needs a sign-in OpenCode cannot start: Server does not support dynamic client registration.`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if mcpProblems(map[string]ocMCPStatus{"a": {Status: "connected"}, "b": {Status: "disabled"}}) != "" {
		t.Fatal("connected and disabled servers are not problems")
	}
}

// A warning or an error OpenCode toasts is a notice, once; an info is not,
// and a toast about an MCP server has GET /mcp read instead, whose notice
// names it.
func TestAToastIsANoticeOnce(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/toast")
	r.send("/toast")
	r.waitTurns(2)
	if said := noticesOf(r.rec, NoticeToast); len(said) != 1 || said[0] != "Plugin: The plugin said warning" {
		t.Fatalf("toast notices %q", said)
	}

	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authme":{"status":"needs_auth"}}`))
	}))
	defer srv.Close()
	rec := newRecorder()
	a := New()
	a.out, a.session, a.cl = rec, "ses_root", newClient(srv.URL, "", t.TempDir())
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.life = life
	toast := `{"title":"MCP Authentication Required","message":"Server \"authme\" requires authentication. Run: opencode mcp auth authme","variant":"warning","duration":8000}`
	a.handle(0, ocEvent{Type: "tui.toast.show", Properties: json.RawMessage(toast)})
	a.handle(0, ocEvent{Type: "tui.toast.show", Properties: json.RawMessage(`{"message":"saved","variant":"success"}`)})
	eventually(t, 5*time.Second, "the MCP notice", func() bool { return len(noticesOf(rec, agent.NoticeMCP)) == 1 })
	if said := noticesOf(rec, NoticeToast); len(said) != 0 {
		t.Fatalf("toast notices %q", said)
	}
	if said := noticesOf(rec, agent.NoticeMCP); !strings.Contains(said[0], "opencode mcp auth authme") {
		t.Fatalf("MCP notice %q", said)
	}
}

// What a shell command of a step changed follows the step as a diff part of
// its own, from the summary OpenCode stores after the step; a file an edit
// call showed is not shown again.
func TestWhatAShellCommandChangedIsShown(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/shellchange")
	r.waitTurns(1)
	var edits, steps []DiffContent
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Kind != agent.PartDiff {
					continue
				}
				dc := decode[DiffContent](t, part.Content)
				if dc.Tool == StepTool {
					steps = append(steps, dc)
				} else {
					edits = append(edits, dc)
				}
			}
		}
	})
	if len(edits) != 1 || edits[0].Tool != "edit" || len(edits[0].Files) != 1 || filepath.Base(edits[0].Files[0].Path) != "a.txt" {
		t.Fatalf("edit diffs %+v", edits)
	}
	if len(steps) != 1 || len(steps[0].Files) != 1 || steps[0].Title != "b.txt" {
		t.Fatalf("step diffs %+v", steps)
	}
	f := steps[0].Files[0]
	if f.Path != "b.txt" || !strings.Contains(f.Patch, "+two") || f.Additions != 1 || f.Deletions != 1 || f.Status != "modified" {
		t.Fatalf("the shell command's file %+v", f)
	}
}

// A catch-up reads the summary before the patch parts it covers: they are
// shown when the turn ends, with the newest summary. A patch part of an
// earlier turn is not shown, and without a summary a step's file is named
// as the chat's folder sees it.
func TestAStepsChangesAfterACatchUpAndWithoutASummary(t *testing.T) {
	rec := newRecorder()
	a := New()
	a.out, a.session = rec, "ses_root"
	a.spec.Workdir = "/w/repo"
	a.users["msg_u1"] = &userMsg{our: "m1", journaled: true}
	a.handleMessage(0, ocMessage{ID: "msg_u1", SessionID: "ses_root", Role: "user"})
	a.handleMessage(0, ocMessage{ID: "msg_a1", SessionID: "ses_root", Role: "assistant", ParentID: "msg_u1"})
	a.handleMessage(0, ocMessage{ID: "msg_u1", SessionID: "ses_root", Role: "user",
		Summary: json.RawMessage(`{"diffs":[{"file":"b.txt","patch":"@@ -1 +1 @@\n-one\n+two\n","additions":1,"deletions":1,"status":"modified"}]}`)})
	a.handlePart(0, mustPart(t, `{"id":"prt_p1","sessionID":"ses_root","messageID":"msg_a1","type":"patch","hash":"h","files":["/w/repo/b.txt"]}`))
	// The patch part of a message of an earlier turn.
	a.handlePart(0, mustPart(t, `{"id":"prt_old","sessionID":"ses_root","messageID":"msg_a0","type":"patch","hash":"h","files":["/w/repo/c.txt"]}`))
	if n := rec.count(agent.TypePart, func(ev agent.Event) bool { return ev.Kind == agent.PartDiff }); n != 0 {
		t.Fatalf("%d diff parts before the turn's end", n)
	}
	a.handleIdle(0, "ses_root")
	ev, ok := rec.first(agent.TypePart, func(ev agent.Event) bool { return ev.Kind == agent.PartDiff })
	if !ok || ev.PartID != "prt_p1" || ev.MessageID != "msg_a1" {
		t.Fatalf("the step's diff part %+v", ev)
	}
	if dc := decode[DiffContent](t, ev.Content); len(dc.Files) != 1 || dc.Files[0].Path != "b.txt" || !strings.Contains(dc.Files[0].Patch, "+two") {
		t.Fatalf("the step's diff %s", ev.Content)
	}
	if n := rec.count(agent.TypePart, func(ev agent.Event) bool { return ev.Kind == agent.PartDiff }); n != 1 {
		t.Fatalf("%d diff parts, want the step's alone", n)
	}

	files := stepFiles([]string{"/w/repo/sub/x.go", "/w/repo/a.txt", "/elsewhere/y"}, []string{"a.txt"}, nil, false, "/w/repo")
	if len(files) != 2 || files[0].Path != "sub/x.go" || files[0].Patch != "" || files[1].Path != "/elsewhere/y" {
		t.Fatalf("files without a summary %+v", files)
	}
	// With a summary, a file it does not list is one the turn left as it was.
	if files := stepFiles([]string{"/w/repo/x"}, nil, []ocSnapshotDiff{}, true, "/w/repo"); len(files) != 0 {
		t.Fatalf("a file the summary does not list %+v", files)
	}
}

// A rebuilt history has a step's changes after the step, as live.
func TestARebuildShowsAStepsChanges(t *testing.T) {
	var msgs []ocMessageWithParts
	raw := `[
		{"info":{"id":"msg_u1","sessionID":"ses_root","role":"user","time":{"created":90},
		  "summary":{"diffs":[{"file":"a.txt","patch":"@@ -1 +1 @@\n-old\n+new\n","additions":1,"deletions":1},
		                      {"file":"b.txt","patch":"@@ -1 +1 @@\n-one\n+two\n","additions":1,"deletions":1}]}},"parts":[]},
		{"info":{"id":"msg_a1","sessionID":"ses_root","role":"assistant","parentID":"msg_u1","time":{"created":95,"completed":250},"summary":false},
		 "parts":[
		  {"id":"prt_e","sessionID":"ses_root","messageID":"msg_a1","type":"tool","tool":"edit","callID":"c1",
		   "state":{"status":"completed","input":{"filePath":"/w/a.txt"},"output":"","metadata":{"diff":"@@ -1 +1 @@\n-old\n+new\n"},"time":{"start":96,"end":97}}},
		  {"id":"prt_p","sessionID":"ses_root","messageID":"msg_a1","type":"patch","hash":"h","files":["/w/a.txt","/w/b.txt"]}]}]`
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatal(err)
	}
	var diffs []string
	for _, ev := range historyEvents(msgs, nil) {
		if ev.Type == agent.TypePart && ev.Kind == agent.PartDiff {
			dc := decode[DiffContent](t, ev.Content)
			for _, f := range dc.Files {
				diffs = append(diffs, ev.PartID+" "+dc.Tool+" "+f.Path)
			}
		}
	}
	if strings.Join(diffs, ", ") != "prt_e-diff edit /w/a.txt, prt_p changes b.txt" {
		t.Fatalf("diff parts %q", diffs)
	}
}

// A subagent's retry and its own error are said inside the task part that
// runs it; the turn goes on.
func TestASubagentsErrorIsInItsTask(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/subagentfail")
	if o := r.waitTurns(1); o[0] != agent.OutcomeCompleted {
		t.Fatalf("outcomes %v", o)
	}
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		var task *agent.Part
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Kind == agent.PartTool && decode[ToolContent](t, part.Content).Tool == "task" {
					task = part
				}
			}
		}
		if task == nil {
			t.Fatal("no task part")
		}
		var notes []string
		for _, c := range task.Children {
			if c.Kind == agent.PartNotice {
				notes = append(notes, c.State+" "+c.Text)
			}
		}
		want := []string{"done The subagent's call is retried (attempt 1): Provider is overloaded",
			"error The subagent failed: The model is not available."}
		if strings.Join(notes, "|") != strings.Join(want, "|") {
			t.Fatalf("the task's notices %q", notes)
		}
	})
	if n := r.rec.count(agent.TypeError, nil); n != 0 {
		t.Fatalf("%d errors of the chat: the subagent's is its task's", n)
	}
}
