//go:build linux

package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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
	r.a.requestMCP(context.Background(), r.a.gen, r.a.client())
	r.a.handle(r.a.gen, ocEvent{Type: "mcp.tools.changed", Properties: json.RawMessage(`{"server":"broken"}`)})
	time.Sleep(300 * time.Millisecond)
	if said := noticesOf(r.rec, agent.NoticeMCP); len(said) != 1 {
		t.Fatalf("MCP notices after reading again %q", said)
	}
	r.send("hello")
	r.waitTurns(1)
}

// However often GET /mcp is asked for while a read is under way (a burst
// of mcp.tools.changed, MCP toasts), one read runs and one more after it.
func TestMCPReadsAreCoalesced(t *testing.T) {
	release := make(chan struct{})
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reads.Add(1) == 1 {
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"x":{"status":"failed","error":"boom"}}`))
	}))
	defer srv.Close()
	rec := newRecorder()
	a := New()
	a.out, a.session, a.cl = rec, "ses_root", newClient(srv.URL, "", t.TempDir())
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.life = life
	a.requestMCP(life, 0, a.cl)
	eventually(t, 5*time.Second, "the first read", func() bool { return reads.Load() == 1 })
	for range 20 {
		a.handle(0, ocEvent{Type: "mcp.tools.changed", Properties: json.RawMessage(`{"server":"x"}`)})
		a.handle(0, ocEvent{Type: "tui.toast.show", Properties: json.RawMessage(`{"title":"MCP Authentication Required","message":"m","variant":"warning"}`)})
	}
	close(release)
	eventually(t, 5*time.Second, "the reads to end", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return !a.notes.mcpRunning
	})
	if n := reads.Load(); n != 2 {
		t.Fatalf("%d reads of GET /mcp, want 2", n)
	}
	if said := noticesOf(rec, agent.NoticeMCP); len(said) != 1 {
		t.Fatalf("MCP notices %q", said)
	}
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

// stepRig is an adapter in a turn of the user message msg_u1 in the folder
// /w/repo/pkg, whose worktree is /w/repo, with one assistant message,
// msg_a1.
func stepRig(t *testing.T) (*Adapter, *recorder) {
	t.Helper()
	rec := newRecorder()
	a := New()
	a.out, a.session = rec, "ses_root"
	a.spec.Workdir = "/w/repo/pkg"
	a.users["msg_u1"] = &userMsg{our: "m1", journaled: true}
	a.handleMessage(0, ocMessage{ID: "msg_u1", SessionID: "ses_root", Role: "user"})
	a.handleMessage(0, ocMessage{ID: "msg_a1", SessionID: "ses_root", Role: "assistant", ParentID: "msg_u1",
		Path: &ocPath{Cwd: "/w/repo/pkg", Root: "/w/repo"}})
	return a, rec
}

// summary is a user message whose summary.diffs lists files, each with one
// line changed.
func summary(id string, files ...string) ocMessage {
	diffs := []ocSnapshotDiff{}
	for _, f := range files {
		diffs = append(diffs, ocSnapshotDiff{File: f, Patch: "@@ -1 +1 @@\n-one\n+" + f + "\n", Additions: 1, Deletions: 1, Status: "modified"})
	}
	raw, _ := json.Marshal(map[string]any{"diffs": diffs})
	return ocMessage{ID: id, SessionID: "ses_root", Role: "user", Summary: raw}
}

// stepFilesOf is the files of a step's diff part as its newest event has
// them: "path", or "path (no lines)" for one named without its diff.
func stepFilesOf(t *testing.T, rec *recorder, part string) []string {
	t.Helper()
	var out []string
	rec.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.Type != agent.TypePart || ev.PartID != part {
				continue
			}
			out = nil
			for _, f := range decode[DiffContent](t, ev.Content).Files {
				if f.Patch == "" {
					out = append(out, f.Path+" (no lines)")
				} else {
					out = append(out, f.Path)
				}
			}
		}
	})
	return out
}

func patchPart(id, msg string, files ...string) ocPart {
	return ocPart{ID: id, SessionID: "ses_root", MessageID: msg, Type: partPatch, Files: files}
}

// A catch-up reads the summary before the patch parts it covers: they are
// shown when the turn ends, with it. A patch part of an earlier turn is not
// shown; a file is named as the chat's folder sees it, the summary's from
// the worktree.
func TestAStepsChangesAfterACatchUp(t *testing.T) {
	a, rec := stepRig(t)
	a.handleMessage(0, summary("msg_u1", "pkg/b.txt"))
	a.handlePart(0, patchPart("prt_p1", "msg_a1", "/w/repo/pkg/b.txt"))
	a.handlePart(0, patchPart("prt_old", "msg_a0", "/w/repo/pkg/c.txt"))
	if n := rec.count(agent.TypePart, func(ev agent.Event) bool { return ev.Kind == agent.PartDiff }); n != 0 {
		t.Fatalf("%d diff parts before the turn's end", n)
	}
	a.handleIdle(0, "ses_root")
	if got := stepFilesOf(t, rec, "prt_p1"); strings.Join(got, ",") != "b.txt" {
		t.Fatalf("the step's files %q", got)
	}
	if n := rec.count(agent.TypePart, func(ev agent.Event) bool { return ev.Kind == agent.PartDiff }); n != 1 {
		t.Fatalf("%d diff parts, want the step's alone", n)
	}
}

// No file of a step is dropped: one the summary that came does not list, or
// one whose summary never came, is named without lines, and gets them when
// a summary brings them - after the turn's end too.
func TestAStepsFileIsNeverDropped(t *testing.T) {
	a, rec := stepRig(t)
	a.handlePart(0, patchPart("prt_p1", "msg_a1", "/w/repo/pkg/b.txt", "/w/repo/pkg/c.txt", "/elsewhere/y"))
	a.handleMessage(0, summary("msg_u1", "pkg/b.txt"))
	if got := stepFilesOf(t, rec, "prt_p1"); strings.Join(got, ",") != "b.txt,c.txt (no lines),/elsewhere/y (no lines)" {
		t.Fatalf("the step's files %q", got)
	}
	a.handlePart(0, patchPart("prt_p2", "msg_a1", "/w/repo/pkg/d.txt"))
	a.handleIdle(0, "ses_root")
	if got := stepFilesOf(t, rec, "prt_p2"); strings.Join(got, ",") != "d.txt (no lines)" {
		t.Fatalf("a step whose summary did not come by the end %q", got)
	}
	// The summary after session.idle.
	a.handleMessage(0, summary("msg_u1", "pkg/b.txt", "pkg/c.txt", "pkg/d.txt"))
	if got := stepFilesOf(t, rec, "prt_p2"); strings.Join(got, ",") != "d.txt" {
		t.Fatalf("the step after a late summary %q", got)
	}
	if got := stepFilesOf(t, rec, "prt_p1"); strings.Join(got, ",") != "b.txt,c.txt,/elsewhere/y (no lines)" {
		t.Fatalf("the first step after a late summary %q", got)
	}
}

// The steps after a compaction answer the compaction's user message, whose
// summary is theirs; a turn that adopted its user message takes the summary
// of the one its steps answer (and the worktree the turn before named,
// until a step names it).
func TestAStepAfterACompactionTakesItsSummary(t *testing.T) {
	a, rec := stepRig(t)
	// OpenCode's own user message inside the running turn.
	a.handleMessage(0, ocMessage{ID: "msg_c", SessionID: "ses_root", Role: "user"})
	a.handleMessage(0, ocMessage{ID: "msg_a2", SessionID: "ses_root", Role: "assistant", ParentID: "msg_c"})
	a.handlePart(0, patchPart("prt_p", "msg_a2", "/w/repo/pkg/b.txt"))
	a.handleMessage(0, summary("msg_c", "pkg/b.txt"))
	if got := stepFilesOf(t, rec, "prt_p"); strings.Join(got, ",") != "b.txt" {
		t.Fatalf("the step after the compaction %q", got)
	}
	a.handleIdle(0, "ses_root")
	a.users["msg_u2"] = &userMsg{our: "m2", journaled: true, adopt: true}
	a.handleMessage(0, ocMessage{ID: "msg_u2", SessionID: "ses_root", Role: "user"})
	a.handleMessage(0, ocMessage{ID: "msg_a3", SessionID: "ses_root", Role: "assistant", ParentID: "msg_x"})
	a.handlePart(0, patchPart("prt_q", "msg_a3", "/w/repo/pkg/e.txt"))
	a.handleMessage(0, summary("msg_x", "pkg/e.txt"))
	if got := stepFilesOf(t, rec, "prt_q"); strings.Join(got, ",") != "e.txt" {
		t.Fatalf("the adopted turn's step %q", got)
	}
}

// A file is the same file by its whole path: an edit of a.txt does not hide
// a shell command's change to sub/a.txt, a relative name is the folder
// OpenCode works in's, and an edit of an earlier turn a catch-up reads
// hides nothing.
func TestAStepsFilesAreMatchedByTheirWholePath(t *testing.T) {
	a, rec := stepRig(t)
	edit := func(id, msg, path string) ocPart {
		return mustPart(t, `{"id":"`+id+`","sessionID":"ses_root","messageID":"`+msg+`","type":"tool","tool":"edit","callID":"c_`+id+`",
			"state":{"status":"completed","input":{"filePath":"`+path+`"},"output":"","metadata":{"diff":"@@ -1 +1 @@\n-a\n+b\n"},"time":{"start":1,"end":2}}}`)
	}
	a.handlePart(0, edit("prt_e1", "msg_a1", "a.txt"))
	a.handlePart(0, edit("prt_e0", "msg_a0", "/w/repo/pkg/c.txt"))
	a.handlePart(0, patchPart("prt_p", "msg_a1", "/w/repo/pkg/a.txt", "/w/repo/pkg/sub/a.txt", "/w/repo/pkg/c.txt"))
	a.handleMessage(0, summary("msg_u1", "pkg/a.txt", "pkg/sub/a.txt", "pkg/c.txt"))
	if got := stepFilesOf(t, rec, "prt_p"); strings.Join(got, ",") != "sub/a.txt,c.txt" {
		t.Fatalf("the step's files %q", got)
	}
}

// A rebuilt history has a step's changes after the step, as live, named as
// the chat's folder sees them.
func TestARebuildShowsAStepsChanges(t *testing.T) {
	var msgs []ocMessageWithParts
	raw := `[
		{"info":{"id":"msg_u1","sessionID":"ses_root","role":"user","time":{"created":90},
		  "summary":{"diffs":[{"file":"pkg/a.txt","patch":"@@ -1 +1 @@\n-old\n+new\n","additions":1,"deletions":1},
		                      {"file":"pkg/b.txt","patch":"@@ -1 +1 @@\n-one\n+two\n","additions":1,"deletions":1}]}},"parts":[]},
		{"info":{"id":"msg_a1","sessionID":"ses_root","role":"assistant","parentID":"msg_u1","time":{"created":95,"completed":250},"summary":false,
		  "path":{"cwd":"/w/repo/pkg","root":"/w/repo"}},
		 "parts":[
		  {"id":"prt_e","sessionID":"ses_root","messageID":"msg_a1","type":"tool","tool":"edit","callID":"c1",
		   "state":{"status":"completed","input":{"filePath":"/w/repo/pkg/a.txt"},"output":"","metadata":{"diff":"@@ -1 +1 @@\n-old\n+new\n"},"time":{"start":96,"end":97}}},
		  {"id":"prt_p","sessionID":"ses_root","messageID":"msg_a1","type":"patch","hash":"h",
		   "files":["/w/repo/pkg/a.txt","/w/repo/pkg/b.txt","/w/repo/pkg/c.txt"]}]}]`
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatal(err)
	}
	var diffs []string
	for _, ev := range historyEvents(msgs, "/w/repo/pkg", nil) {
		if ev.Type == agent.TypePart && ev.Kind == agent.PartDiff {
			dc := decode[DiffContent](t, ev.Content)
			for _, f := range dc.Files {
				diffs = append(diffs, ev.PartID+" "+dc.Tool+" "+f.Path+" "+strconv.Itoa(f.Additions))
			}
		}
	}
	if strings.Join(diffs, ", ") != "prt_e-diff edit /w/repo/pkg/a.txt 1, prt_p changes b.txt 1, prt_p changes c.txt 0" {
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
