package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/chattests"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

// recorder is a recording server whose evidence folder the test writes.
type recorder struct{ dir string }

func (r recorder) EvidenceDir(session string) string { return filepath.Join(r.dir, session) }

// record appends an action of a recording server, with its screenshot.
func record(t *testing.T, dir, summary, shot string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := map[string]any{"at": "2026-10-31T10:00:00Z", "action": "tap", "summary": summary}
	if shot != "" {
		line["screenshot"] = shot
		if err := os.WriteFile(filepath.Join(dir, shot), []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(line)
	f, err := os.OpenFile(filepath.Join(dir, "actions.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

type env struct {
	t     *testing.T
	srv   *Server
	store *chattests.Store
	sess  mcpserver.Session
	rec   string
}

func newEnv(t *testing.T) *env {
	data, rec := t.TempDir(), t.TempDir()
	st := chattests.New(func(chat string) string { return filepath.Join(data, chat) })
	srv := New(Deps{Store: st, Recorders: func() []NamedRecorder {
		return []NamedRecorder{{Name: "browserstack", Recorder: recorder{rec}}}
	}})
	return &env{t: t, srv: srv, store: st, sess: mcpserver.Session{ID: "c1", Workdir: t.TempDir()}, rec: rec}
}

// call calls a tool and fails the test when the result is not as wanted.
func (e *env) call(tool, args string, wantErr bool) string {
	e.t.Helper()
	r := e.srv.Call(context.Background(), e.sess, tool, json.RawMessage(args))
	if len(r.Content) != 1 || r.IsError != wantErr {
		e.t.Fatalf("%s %s: error %v, want %v: %+v", tool, args, r.IsError, wantErr, r)
	}
	return r.Content[0].Text
}

func TestEveryToolIsDescribedAndNamedInTheInstructions(t *testing.T) {
	s := New(Deps{})
	seen := map[string]bool{}
	for _, tool := range s.Tools() {
		if tool.Description == "" || tool.Schema["type"] != "object" || seen[tool.Name] {
			t.Fatalf("tool %+v", tool)
		}
		seen[tool.Name] = true
		if !strings.Contains(s.Instructions(), tool.Name) {
			t.Errorf("the instructions do not name %s", tool.Name)
		}
	}
	if len(seen) != 9 {
		t.Fatalf("%d tools", len(seen))
	}
	if r := s.Call(context.Background(), mcpserver.Session{ID: "c1"}, List, nil); !r.IsError || !strings.Contains(r.Content[0].Text, "not running") {
		t.Fatalf("without a store: %+v", r)
	}
}

// A test is written, run step by step with a file of the agent's and the
// screenshots BrowserStack took meanwhile, and finished; the answers say
// what was recorded.
func TestATestIsWrittenRunAndFinished(t *testing.T) {
	e := newEnv(t)
	out := e.call(Create, `{"title":"Sign in","environment":"Pixel 9","steps":[{"action":"Open /login","expect":"The form"},{"action":"Submit a wrong password","expect":"An error"}]}`, false)
	if !strings.Contains(out, "T1") {
		t.Fatalf("create: %s", out)
	}
	e.call(Create, `{"title":"x","steps":[]}`, true)
	e.call(Create, `{"title":"x","steps":[{"action":"a","expect":"b"}],"colour":"red"}`, true)

	// What BrowserStack did before the run is not this run's.
	dir := filepath.Join(e.rec, "c1")
	record(t, dir, "an older tap", "01-old.png")
	out = e.call(Start, `{"test":"T1"}`, false)
	if !strings.Contains(out, "Open /login") || !strings.Contains(out, "test_finish") {
		t.Fatalf("start: %s", out)
	}
	record(t, dir, "opened /login", "02-login.png")
	record(t, dir, "scrolled", "")
	if err := os.WriteFile(filepath.Join(e.sess.Workdir, "mine.png"), []byte("\x89PNG\r\n\x1a\nmine"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = e.call(Step, `{"test":"T1","step":1,"verdict":"pass","observed":"The form shows","files":["mine.png"]}`, false)
	if !strings.Contains(out, "Step 2 not judged yet") {
		t.Fatalf("step: %s", out)
	}
	test, _ := e.store.Get("c1", "T1")
	ev := test.Latest().Steps[0].Evidence
	if len(ev) != 2 || ev[0].Source != "agent" || ev[1].Source != "browserstack" || ev[1].Caption != "opened /login" {
		t.Fatalf("the step's evidence: %+v", ev)
	}
	// The next step takes only what came after.
	out = e.call(Step, `{"test":"T1","step":2,"verdict":"fail","observed":"Nothing happens","output":"POST /login 500"}`, false)
	test, _ = e.store.Get("c1", "T1")
	if ev := test.Latest().Steps[1].Evidence; len(ev) != 1 || ev[0].Kind != "text" {
		t.Fatalf("step 2's evidence: %+v (%s)", ev, out)
	}
	e.call(Step, `{"test":"T1","step":3,"verdict":"pass","observed":"x"}`, true)
	e.call(Attach, `{"test":"T1","files":["missing.png"]}`, true)
	out = e.call(Finish, `{"test":"T1","summary":"Sign in fails on a wrong password.","findings":"- the API answers 500"}`, false)
	if !strings.Contains(out, "fail") {
		t.Fatalf("finish: %s", out)
	}
	out = e.call(List, `{}`, false)
	if !strings.Contains(out, "T1") || !strings.Contains(out, "Failed") {
		t.Fatalf("list: %s", out)
	}
	out = e.call(Read, `{"test":"Sign in"}`, false)
	if !strings.Contains(out, e.store.FilesDir("c1", "T1")) || !strings.Contains(out, "Nothing happens") {
		t.Fatalf("read: %s", out)
	}
	e.call(Step, `{"test":"T1","step":1,"verdict":"pass","observed":"again"}`, true)
	e.call(Update, `{"test":"T1","skip":"Waits for the API fix"}`, false)
	if test, _ := e.store.Get("c1", "T1"); test.Status() != chattests.StatusSkipped {
		t.Fatalf("skipped: %s", test.Status())
	}
	e.call(Delete, `{"test":"T1"}`, false)
	e.call(Read, `{"test":"T1"}`, true)
	e.call("test_judge", `{}`, true)
}

// A recording started over (its folder emptied) is read from its start.
func TestARecordingStartedOverIsReadAgain(t *testing.T) {
	e := newEnv(t)
	e.call(Create, `{"title":"Home","steps":[{"action":"Open /","expect":"The page"}]}`, false)
	dir := filepath.Join(e.rec, "c1")
	record(t, dir, "one", "01.png")
	record(t, dir, "two", "02.png")
	e.call(Start, `{"test":"T1"}`, false)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	record(t, dir, "after the restart", "01.png")
	e.call(Step, `{"test":"T1","step":1,"verdict":"pass","observed":"ok"}`, false)
	test, _ := e.store.Get("c1", "T1")
	if ev := test.Latest().Steps[0].Evidence; len(ev) != 1 || ev[0].Caption != "after the restart" {
		t.Fatalf("evidence: %+v", ev)
	}
}

// Recorded screenshots never fail a judgement: past the step's room the
// newest are kept, and one that is gone is left out.
func TestRecordedScreenshotsNeverFailAStep(t *testing.T) {
	e := newEnv(t)
	e.call(Create, `{"title":"Long","steps":[{"action":"Tap a lot","expect":"Still fine"}]}`, false)
	e.call(Start, `{"test":"T1"}`, false)
	dir := filepath.Join(e.rec, "c1")
	for i := 0; i < chattests.MaxStepFiles+5; i++ {
		record(t, dir, fmt.Sprintf("tap %d", i), fmt.Sprintf("%02d.png", i))
	}
	record(t, dir, "a shot that is gone", "gone.png")
	if err := os.Remove(filepath.Join(dir, "gone.png")); err != nil {
		t.Fatal(err)
	}
	e.call(Step, `{"test":"T1","step":1,"verdict":"pass","observed":"fine"}`, false)
	test, _ := e.store.Get("c1", "T1")
	ev := test.Latest().Steps[0].Evidence
	if len(ev) != chattests.MaxStepFiles || ev[len(ev)-1].Caption != fmt.Sprintf("tap %d", chattests.MaxStepFiles+4) {
		t.Fatalf("%d files, the last %+v", len(ev), ev[len(ev)-1])
	}
	// Judged again, the step has no room left and nothing new: it is still
	// judged.
	record(t, dir, "one more", "more.png")
	e.call(Step, `{"test":"T1","step":1,"verdict":"issue","observed":"slow"}`, false)
}

// Two steps judged at once never take the same screenshot.
func TestStepsJudgedAtOnceShareNoScreenshot(t *testing.T) {
	for round := 0; round < 10; round++ {
		e := newEnv(t)
		e.call(Create, `{"title":"Two","steps":[{"action":"a","expect":"b"},{"action":"c","expect":"d"}]}`, false)
		e.call(Start, `{"test":"T1"}`, false)
		record(t, filepath.Join(e.rec, "c1"), "the one tap", "01.png")
		done := make(chan struct{})
		for step := 1; step <= 2; step++ {
			go func() {
				defer func() { done <- struct{}{} }()
				e.srv.Call(context.Background(), e.sess, Step, json.RawMessage(fmt.Sprintf(`{"test":"T1","step":%d,"verdict":"pass","observed":"ok"}`, step)))
			}()
		}
		<-done
		<-done
		test, _ := e.store.Get("c1", "T1")
		if n := len(test.Latest().Steps[0].Evidence) + len(test.Latest().Steps[1].Evidence); n != 1 {
			t.Fatalf("round %d: the screenshot was attached %d times", round, n)
		}
	}
}

// A line still being written is not counted until it is whole.
func TestAHalfWrittenLineWaits(t *testing.T) {
	e := newEnv(t)
	e.call(Create, `{"title":"Half","steps":[{"action":"a","expect":"b"},{"action":"c","expect":"d"}]}`, false)
	e.call(Start, `{"test":"T1"}`, false)
	dir := filepath.Join(e.rec, "c1")
	record(t, dir, "whole", "01.png")
	if err := os.WriteFile(filepath.Join(dir, "02.png"), []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(filepath.Join(dir, "actions.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`{"summary":"half","screenshot":"02.png"`)
	e.call(Step, `{"test":"T1","step":1,"verdict":"pass","observed":"ok"}`, false)
	_, _ = f.WriteString("}\n")
	f.Close()
	e.call(Step, `{"test":"T1","step":2,"verdict":"pass","observed":"ok"}`, false)
	test, _ := e.store.Get("c1", "T1")
	s := test.Latest().Steps
	if len(s[0].Evidence) != 1 || len(s[1].Evidence) != 1 || s[1].Evidence[0].Caption != "half" {
		t.Fatalf("evidence: %+v / %+v", s[0].Evidence, s[1].Evidence)
	}
}
