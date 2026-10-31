package chattests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var base = time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)

// newStore is a store under a temporary folder whose clock moves a second
// on with every reading, so that every change has a time of its own.
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s := New(func(chat string) string { return filepath.Join(root, "chats", chat) })
	var ticks atomic.Int64
	s.now = func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Second) }
	clock = func() time.Time { return base }
	t.Cleanup(func() { clock = time.Now })
	return s, root
}

func steps(n int) []Step {
	out := make([]Step, n)
	for i := range out {
		out[i] = Step{Action: fmt.Sprintf("Do thing %d", i+1), Expect: fmt.Sprintf("Thing %d shows", i+1)}
	}
	return out
}

func mustCreate(t *testing.T, s *Store, chat, title string, n int) Test {
	t.Helper()
	tt, err := s.Create(chat, Fields{Title: title, Steps: steps(n)}, ByAgent)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func mustStart(t *testing.T, s *Store, chat, ref string) (Test, Run) {
	t.Helper()
	tt, r, err := s.Start(chat, ref, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return tt, r
}

func mustJudge(t *testing.T, s *Store, chat, ref string, j Judgement) (Test, Judged, []Evidence) {
	t.Helper()
	if j.Observed == "" {
		j.Observed = "Seen"
	}
	tt, st, ev, err := s.Judge(chat, ref, j)
	if err != nil {
		t.Fatal(err)
	}
	return tt, st, ev
}

// file writes a file under dir and answers its path.
func file(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func ptr[T any](v T) *T { return &v }

func wantErr(t *testing.T, err error, part string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), part) {
		t.Fatalf("error = %v, want one saying %q", err, part)
	}
}

func TestListOfAChatWithoutTestsCreatesNothing(t *testing.T) {
	s, root := newStore(t)
	tests, err := s.List("c1")
	if err != nil || len(tests) != 0 {
		t.Fatalf("List = %v, %v", tests, err)
	}
	if rev, err := s.Rev("c1"); err != nil || rev != 0 {
		t.Fatalf("Rev = %d, %v", rev, err)
	}
	if _, err := os.Stat(filepath.Join(root, "chats")); !os.IsNotExist(err) {
		t.Fatalf("a folder was made: %v", err)
	}
}

func TestCreateListGet(t *testing.T) {
	s, root := newStore(t)
	t1, err := s.Create("c1", Fields{
		Title: "  Sign in\nwith a wrong password ", Goal: " Context ", Environment: "iPhone 15",
		Tags:  []string{"auth", " auth ", "", "login"},
		Steps: []Step{{Action: " Open /login ", Expect: " The form "}},
	}, ByAgent)
	if err != nil {
		t.Fatal(err)
	}
	if t1.ID != "T1" || t1.Title != "Sign in with a wrong password" || t1.Goal != "Context" ||
		strings.Join(t1.Tags, ",") != "auth,login" || t1.Steps[0] != (Step{"Open /login", "The form"}) {
		t.Fatalf("created %+v", t1)
	}
	if t1.Status() != StatusDraft || t1.Stale() || len(t1.Events) != 1 || t1.Events[0].What != EventCreated {
		t.Fatalf("a new test: status %s, stale %v, events %+v", t1.Status(), t1.Stale(), t1.Events)
	}
	mustCreate(t, s, "c1", "Second", 2)

	tests, err := s.List("c1")
	if err != nil || len(tests) != 2 || tests[0].ID != "T1" || tests[1].ID != "T2" {
		t.Fatalf("List = %+v, %v", tests, err)
	}
	for _, ref := range []string{"t2", "T2", " Second "} {
		if got, err := s.Get("c1", ref); err != nil || got.ID != "T2" {
			t.Fatalf("Get(%q) = %s, %v", ref, got.ID, err)
		}
	}
	_, err = s.Get("c1", "T9")
	wantErr(t, err, "test T9 does not exist; the chat has T1, T2")
	_, err = s.Get("c1", "second")
	wantErr(t, err, `no test is titled "second"`)
	_, err = s.Get("c2", "T1")
	wantErr(t, err, "the chat has no tests yet")
	mustCreate(t, s, "c1", "Second", 1)
	_, err = s.Get("c1", "Second")
	wantErr(t, err, "2 tests are titled \"Second\" (T2, T3)")

	// The file is the one the spec describes.
	var d struct {
		Version int               `json:"version"`
		Next    int               `json:"next"`
		Rev     int64             `json:"rev"`
		Tests   []json.RawMessage `json:"tests"`
	}
	blob, _ := os.ReadFile(filepath.Join(root, "chats", "c1", "tests", "tests.json"))
	if err := json.Unmarshal(blob, &d); err != nil || d.Version != 1 || d.Next != 4 || d.Rev != 3 || len(d.Tests) != 3 {
		t.Fatalf("tests.json = %s (%v)", blob, err)
	}
}

func TestIDsAreNeverGivenAgain(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "One", 1)
	mustCreate(t, s, "c1", "Two", 1)
	mustStart(t, s, "c1", "T2")
	mustJudge(t, s, "c1", "T2", Judgement{Step: 1, Verdict: "pass", Output: "ok"})
	dir := s.FilesDir("c1", "T2")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("c1", "T2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the deleted test's files are still there: %v", err)
	}
	if t3 := mustCreate(t, s, "c1", "Three", 1); t3.ID != "T3" {
		t.Fatalf("after deleting T2 the next test is %s", t3.ID)
	}
	wantErr(t, s.Delete("c1", "T2"), "test T2 does not exist; the chat has T1, T3")
}

func TestUpdateAndStale(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 2)
	_, err := s.Update("c1", "T1", Patch{}, ByPerson)
	wantErr(t, err, "nothing to change")

	mustStart(t, s, "c1", "T1")
	title := "Flow, renamed"
	got, err := s.Update("c1", "T1", Patch{Title: &title}, ByPerson)
	if err != nil || got.Title != title || got.Stale() {
		t.Fatalf("a new title: %+v, stale %v, %v", got.Title, got.Stale(), err)
	}
	if e := got.Events[len(got.Events)-1]; e.What != EventEdited || e.By != ByPerson || e.Detail != "title" {
		t.Fatalf("event %+v", e)
	}

	// The same steps again change nothing, and nothing is saved.
	rev, _ := s.Rev("c1")
	same := steps(2)
	if _, err := s.Update("c1", "T1", Patch{Steps: &same}, ByAgent); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Rev("c1"); again != rev {
		t.Fatalf("an edit that changed nothing was saved: rev %d -> %d", rev, again)
	}

	more := steps(3)
	got, err = s.Update("c1", "T1", Patch{Steps: &more}, ByAgent)
	if err != nil || !got.Stale() || len(got.Steps) != 3 {
		t.Fatalf("new steps: stale %v, %d steps, %v", got.Stale(), len(got.Steps), err)
	}
	// The open run keeps the steps it began with.
	if r := got.Open(); r == nil || len(r.Steps) != 2 {
		t.Fatalf("the open run's steps changed: %+v", r)
	}
	_, r := mustStart(t, s, "c1", "T1")
	if got, _ = s.Get("c1", "T1"); got.Stale() || len(r.Steps) != 3 {
		t.Fatalf("a run of the new steps: stale %v, %d steps", got.Stale(), len(r.Steps))
	}

	bad := []Step{{Action: "Open", Expect: " "}}
	_, err = s.Update("c1", "T1", Patch{Steps: &bad}, ByAgent)
	wantErr(t, err, "step 1 has no expect")
}

func TestSkip(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 1)
	reason := "No iPhone today"
	got, err := s.Update("c1", "T1", Patch{Skip: &reason}, ByPerson)
	if err != nil || got.Status() != StatusSkipped || got.Skip.Reason != reason || got.Skip.By != ByPerson {
		t.Fatalf("skip: %+v, %v", got.Skip, err)
	}
	_, _, err = s.Start("c1", "T1", "", nil)
	wantErr(t, err, `T1 is set aside by the person ("No iPhone today")`)

	empty := ""
	got, err = s.Update("c1", "T1", Patch{Skip: &empty}, ByAgent)
	if err != nil || got.Skip != nil || got.Status() != StatusDraft {
		t.Fatalf("unskip: %+v, %v", got.Skip, err)
	}
	var what []string
	for _, e := range got.Events {
		what = append(what, e.What)
	}
	if strings.Join(what, ",") != "created,skip,unskip" {
		t.Fatalf("events %v", what)
	}
}

func TestRunVerdict(t *testing.T) {
	for _, c := range []struct {
		verdicts string
		want     string
	}{
		{"pass pass", Pass},
		{"na na", Pass},
		{"na pass", Pass},
		{"pass issue na", Issue},
		{"issue blocked pass", Blocked},
		{"blocked fail issue", Fail},
		{"fail na", Fail},
	} {
		var js []Judged
		for _, v := range strings.Fields(c.verdicts) {
			js = append(js, Judged{Verdict: v})
		}
		if got := RunVerdict(js); got != c.want {
			t.Errorf("RunVerdict(%s) = %s, want %s", c.verdicts, got, c.want)
		}
	}
}

func TestStartJudgeFinish(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 4)
	_, _, _, err := s.Judge("c1", "T1", Judgement{Step: 1, Verdict: "pass", Observed: "x"})
	wantErr(t, err, "T1 has no open run: call test_start first")

	got, r, err := s.Start("c1", "T1", "Pixel 8 · Chrome", map[string]int{"browserstack": 12})
	if err != nil || r.N != 1 || r.Environment != "Pixel 8 · Chrome" || len(r.Steps) != 4 || got.Status() != StatusRunning {
		t.Fatalf("Start = %+v, %v", r, err)
	}
	_, _, _, err = s.Judge("c1", "T1", Judgement{Step: 5, Verdict: "pass", Observed: "x"})
	wantErr(t, err, "step 5 does not exist: run 1 of T1 has 4 steps")
	_, _, _, err = s.Judge("c1", "T1", Judgement{Step: 1, Verdict: "great", Observed: "x"})
	wantErr(t, err, `"great" is not a verdict`)
	_, _, _, err = s.Judge("c1", "T1", Judgement{Step: 1, Verdict: "pass"})
	wantErr(t, err, "observed is empty")

	var seen map[string]int
	mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "Passed", Recorded: func(c map[string]int) ([]Attach, map[string]int) {
		seen = c
		return nil, map[string]int{"browserstack": 15}
	}})
	if seen["browserstack"] != 12 {
		t.Fatalf("the cursors the recorder was asked with: %v", seen)
	}
	got, st, _ := mustJudge(t, s, "c1", "T1", Judgement{Step: 2, Verdict: "issue", Observed: "The button is cut", Note: "only on narrow screens"})
	if st.Verdict != Issue || st.Observed != "The button is cut" || st.Note != "only on narrow screens" || st.At == nil {
		t.Fatalf("judged %+v", st)
	}
	if r := got.Open(); r.Steps[0].Verdict != Pass || r.Cursors["browserstack"] != 15 || r.JudgedCount() != 2 {
		t.Fatalf("open run %+v", r)
	}

	_, _, err = s.Finish("c1", "T1", " ", "")
	wantErr(t, err, "the summary is empty")
	got, r, err = s.Finish("c1", "T1", "Two steps not reached.", "- The button is cut")
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Blocked || r.EndedAt == nil || r.Steps[2].Verdict != Blocked || r.Steps[3].Observed != "Not reached" {
		t.Fatalf("finished %+v", r)
	}
	if got.Status() != StatusBlocked || got.Open() != nil || r.Findings != "- The button is cut" {
		t.Fatalf("status %s", got.Status())
	}
	_, _, err = s.Finish("c1", "T1", "Again", "")
	wantErr(t, err, "T1 has no open run (run 1 was finished")

	// A failing step outranks blocked ones.
	mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 3, Verdict: "fail", Observed: "Error 500"})
	got, r, _ = s.Finish("c1", "T1", "Fails at step 3.", "")
	if r.N != 2 || r.Verdict != Fail || got.Status() != StatusFailed {
		t.Fatalf("run 2: %s, status %s", r.Verdict, got.Status())
	}
}

func TestRejudgeKeepsEvidence(t *testing.T) {
	s, _ := newStore(t)
	src := t.TempDir()
	mustCreate(t, s, "c1", "Flow", 2)
	mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "fail", Observed: "Blank", Note: "first look",
		Files: []Attach{{Path: file(t, src, "a.png", "png"), Caption: "Blank page"}}})
	_, st, added := mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass", Observed: "Loaded after a while",
		Files: []Attach{{Path: file(t, src, "b.png", "png2"), Source: "browserstack"}}})
	if st.Verdict != Pass || st.Observed != "Loaded after a while" || st.Note != "" {
		t.Fatalf("the judgement was not replaced: %+v", st)
	}
	if len(st.Evidence) != 2 || st.Evidence[0].File != "r1/01-a.png" || st.Evidence[1].File != "r1/02-b.png" ||
		st.Evidence[1].Source != "browserstack" || st.Evidence[0].Source != "agent" {
		t.Fatalf("evidence %+v", st.Evidence)
	}
	if len(added) != 1 || added[0].File != "r1/02-b.png" {
		t.Fatalf("added %+v", added)
	}
}

func TestStartSupersedesAnOpenRun(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 3)
	mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass"})
	got, r := mustStart(t, s, "c1", "T1")
	old := got.Runs[0]
	if r.N != 2 || old.IsOpen() || old.Verdict != Blocked || old.Steps[0].Verdict != Pass ||
		old.Steps[1].Verdict != Blocked || old.Steps[2].Observed != "Superseded by run 2" {
		t.Fatalf("the old run: %+v", old)
	}
	if got.Status() != StatusRunning || got.Open().N != 2 {
		t.Fatalf("status %s", got.Status())
	}
	if text := StartText(got, r); !strings.Contains(text, "Run 1 was still open") {
		t.Fatalf("StartText does not say run 1 was superseded:\n%s", text)
	}
}

func TestOverride(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 1)
	mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "fail", Observed: "Red banner"})
	s.Finish("c1", "T1", "Fails.", "")

	_, err := s.SetOverride("c1", "T1", "running", "")
	wantErr(t, err, `"running" cannot be an override`)
	got, err := s.SetOverride("c1", "T1", "passed", "The banner is expected here")
	if err != nil || got.Status() != StatusPassed || got.Override.Note != "The banner is expected here" {
		t.Fatalf("override: %+v, %v", got.Override, err)
	}
	got, err = s.SetOverride("c1", "T1", "", "")
	if err != nil || got.Override != nil || got.Status() != StatusFailed {
		t.Fatalf("removed: %+v, %v", got.Override, err)
	}
	rev, _ := s.Rev("c1")
	if _, err := s.SetOverride("c1", "T1", "", ""); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Rev("c1"); again != rev {
		t.Fatal("removing no override was saved")
	}

	// A skip is beneath an override, and a new run removes the override.
	s.SetOverride("c1", "T1", "fail", "Really broken")
	got, r := mustStart(t, s, "c1", "T1")
	if got.Override != nil || got.Status() != StatusRunning {
		t.Fatalf("a new run kept the override: %+v", got.Override)
	}
	e := got.Events[len(got.Events)-2]
	if e.What != EventOverrideRemoved || !strings.Contains(e.Detail, `Failed ("Really broken")`) {
		t.Fatalf("event %+v", e)
	}
	if text := StartText(got, r); !strings.Contains(text, `The person's override (Failed ("Really broken"))`) {
		t.Fatalf("StartText does not name the removed override:\n%s", text)
	}
	reason := "later"
	s.Update("c1", "T1", Patch{Skip: &reason}, ByAgent)
	got, _ = s.SetOverride("c1", "T1", "issue", "")
	if got.Status() != StatusIssue {
		t.Fatalf("an override under a skip: %s", got.Status())
	}
}

func TestRunsKept(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 1)
	for i := 0; i < KeepRuns; i++ {
		mustStart(t, s, "c1", "T1")
		mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass", Output: fmt.Sprintf("run %d", i+1)})
	}
	first := filepath.Join(s.FilesDir("c1", "T1"), "r1", "01-output.txt")
	if _, err := os.Stat(first); err != nil {
		t.Fatal(err)
	}
	got, r := mustStart(t, s, "c1", "T1")
	if len(got.Runs) != KeepRuns || got.Runs[0].N != 2 || r.N != KeepRuns+1 {
		t.Fatalf("%d runs, the first %d", len(got.Runs), got.Runs[0].N)
	}
	if _, err := os.Stat(filepath.Dir(first)); !os.IsNotExist(err) {
		t.Fatalf("run 1's files are still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.FilesDir("c1", "T1"), "r2", "01-output.txt")); err != nil {
		t.Fatalf("run 2's files are gone: %v", err)
	}
}

func TestAttach(t *testing.T) {
	s, _ := newStore(t)
	src := t.TempDir()
	mustCreate(t, s, "c1", "Flow", 2)
	_, _, err := s.Attach("c1", "T1", 1, []Attach{{Path: file(t, src, "x.png", "x")}})
	wantErr(t, err, "T1 has no open run")
	mustStart(t, s, "c1", "T1")

	shot := file(t, src, "Screen shot (1).PNG", "image bytes")
	got, added, err := s.Attach("c1", "T1", 2, []Attach{{Path: shot, Caption: "The home page"}})
	if err != nil {
		t.Fatal(err)
	}
	e := added[0]
	if e.File != "r1/01-Screen-shot-1-.PNG" || e.Kind != KindImage || e.Size != 11 || e.Caption != "The home page" {
		t.Fatalf("evidence %+v", e)
	}
	if _, err := os.Stat(shot); err != nil {
		t.Fatal("the source was moved, not copied")
	}
	if blob, _ := os.ReadFile(filepath.Join(s.FilesDir("c1", "T1"), "r1", "01-Screen-shot-1-.PNG")); string(blob) != "image bytes" {
		t.Fatalf("copied %q", blob)
	}
	if len(got.Open().Steps[1].Evidence) != 1 {
		t.Fatal("the step has no evidence")
	}

	// Step 0 is the run itself; the numbers go on across the run.
	got, added, err = s.Attach("c1", "T1", 0, []Attach{{Path: file(t, src, "console.log", "line")}, {Path: file(t, src, "rec.webm", "v")}, {Path: file(t, src, "dump.bin", "b")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Open().Evidence) != 3 || added[0].File != "r1/02-console.log" || added[0].Kind != KindText || added[1].Kind != KindVideo || added[2].Kind != KindFile {
		t.Fatalf("run evidence %+v", added)
	}

	_, _, err = s.Attach("c1", "T1", 3, []Attach{{Path: shot}})
	wantErr(t, err, "step 3 does not exist: run 1 of T1 has 2 steps")
	_, _, err = s.Attach("c1", "T1", 1, []Attach{{Path: "shot.png"}})
	wantErr(t, err, "is not an absolute path")
	_, _, err = s.Attach("c1", "T1", 1, []Attach{{Path: filepath.Join(src, "nothing.png")}})
	wantErr(t, err, "does not exist")
	_, _, err = s.Attach("c1", "T1", 1, []Attach{{Path: src}})
	wantErr(t, err, "is a folder")
	link := filepath.Join(src, "link.png")
	if err := os.Symlink(shot, link); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Attach("c1", "T1", 1, []Attach{{Path: link}})
	wantErr(t, err, "is a symbolic link")

	big := filepath.Join(src, "big.mp4")
	f, _ := os.Create(big)
	f.Truncate(MaxFileSize + 1)
	f.Close()
	ok := file(t, src, "ok.png", "fine")
	_, _, err = s.Attach("c1", "T1", 1, []Attach{{Path: ok}, {Path: big}})
	wantErr(t, err, "is 20 MB; a file over 20 MB is not kept")
	// A refused call keeps none of its files.
	if got, _ := s.Get("c1", "T1"); len(got.Open().Steps[0].Evidence) != 0 {
		t.Fatal("a refused call kept a file")
	}
	entries, _ := os.ReadDir(filepath.Join(s.FilesDir("c1", "T1"), "r1"))
	if len(entries) != 4 {
		t.Fatalf("%d files in the run's folder, want 4", len(entries))
	}
}

func TestAttachLimits(t *testing.T) {
	s, _ := newStore(t)
	src := t.TempDir()
	mustCreate(t, s, "c1", "Flow", 1)
	mustStart(t, s, "c1", "T1")
	var many []Attach
	for i := 0; i < MaxStepFiles; i++ {
		many = append(many, Attach{Path: file(t, src, fmt.Sprintf("s%d.png", i), "x")})
	}
	if _, _, err := s.Attach("c1", "T1", 1, many); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Attach("c1", "T1", 1, many[:1])
	wantErr(t, err, "a step (or a run) keeps at most 40")
	_, _, _, err = s.Judge("c1", "T1", Judgement{Step: 1, Verdict: "pass", Observed: "x", Output: "more"})
	wantErr(t, err, "a step (or a run) keeps at most 40")

	// The chat's total: what tests.json says is kept already counts.
	d, _ := s.load("c1")
	d.Tests[0].Runs[0].Steps[0].Evidence[0].Size = MaxChatSize - 2
	if err := s.save("c1", d); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Attach("c1", "T1", 0, []Attach{{Path: file(t, src, "three.txt", "abc")}})
	wantErr(t, err, "would pass the 500 MB a chat may keep")
}

func TestOutputIsKeptAsText(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 1)
	mustStart(t, s, "c1", "T1")
	_, st, _ := mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass", Output: "HTTP/1.1 200 OK\n"})
	e := st.Evidence[0]
	if e.File != "r1/01-output.txt" || e.Kind != KindText || e.Size != 16 || e.Caption != "Output" {
		t.Fatalf("evidence %+v", e)
	}
	p, err := s.FilePath("c1", "T1", e.File)
	if err != nil {
		t.Fatal(err)
	}
	if blob, _ := os.ReadFile(p); string(blob) != "HTTP/1.1 200 OK\n" {
		t.Fatalf("kept %q", blob)
	}
}

func TestFilePath(t *testing.T) {
	s, root := newStore(t)
	mustCreate(t, s, "c1", "Flow", 1)
	mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass", Output: "ok"})
	p, err := s.FilePath("c1", "T1", "r1/01-output.txt")
	if err != nil || p != filepath.Join(root, "chats", "c1", "tests", "files", "T1", "r1", "01-output.txt") {
		t.Fatalf("FilePath = %s, %v", p, err)
	}
	secret := file(t, root, "secret.txt", "no")
	dir := s.FilesDir("c1", "T1")
	os.Symlink(secret, filepath.Join(dir, "r1", "02-link.txt"))
	os.Symlink(filepath.Join(dir, "r1"), filepath.Join(dir, "r9"))
	for _, c := range []struct{ test, file string }{
		{"T1", "../T1/r1/01-output.txt"},
		{"T1", "r1/../../../../secret.txt"},
		{"T1", "/etc/passwd"},
		{"T1", "r1\\01-output.txt"},
		{"T1", "r1/./01-output.txt"},
		{"T1", ""},
		{"T1", "r1"},
		{"T1", "r1/02-link.txt"},
		{"T1", "r9/01-output.txt"},
		{"T1", "r1/03-missing.txt"},
		{"t1", "r1/01-output.txt"},
		{"..", "secret.txt"},
		{"T1/r1", "01-output.txt"},
	} {
		if p, err := s.FilePath("c1", c.test, c.file); err == nil {
			t.Errorf("FilePath(%q, %q) = %s, want a refusal", c.test, c.file, p)
		}
	}
}

func TestLimits(t *testing.T) {
	s, _ := newStore(t)
	for _, c := range []struct {
		in   Fields
		want string
	}{
		{Fields{Steps: steps(1)}, "needs a title"},
		{Fields{Title: strings.Repeat("é", MaxTitle+1), Steps: steps(1)}, "the title is 201 characters long"},
		{Fields{Title: "x", Goal: strings.Repeat("g", MaxGoal+1), Steps: steps(1)}, "the goal is 20 KB; keep it to 20 KB"},
		{Fields{Title: "x"}, "at least one step"},
		{Fields{Title: "x", Steps: steps(MaxSteps + 1)}, "the test has 51 steps; a test has at most 50"},
		{Fields{Title: "x", Steps: []Step{{Action: " ", Expect: "e"}}}, "step 1 has no action"},
	} {
		_, err := s.Create("c1", c.in, ByAgent)
		wantErr(t, err, c.want)
	}
	if _, err := s.Create("c1", Fields{Title: strings.Repeat("é", MaxTitle), Steps: steps(MaxSteps)}, ByAgent); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxTests; i++ {
		mustCreate(t, s, "c1", "x", 1)
	}
	_, err := s.Create("c1", Fields{Title: "one more", Steps: steps(1)}, ByAgent)
	wantErr(t, err, "the chat already has 200 tests")
	_, err = s.Get("c1", "nothing")
	wantErr(t, err, "and 170 more (tests_list names them all)")
}

func TestUnreadableFile(t *testing.T) {
	s, root := newStore(t)
	file := filepath.Join(root, "chats", "c1", "tests", "tests.json")
	os.MkdirAll(filepath.Dir(file), 0o700)
	os.WriteFile(file, []byte(`{"version":1,"tests":[{"id":`), 0o600)
	_, err := s.List("c1")
	wantErr(t, err, "the chat's tests are unreadable")
	_, err = s.Create("c1", Fields{Title: "x", Steps: steps(1)}, ByAgent)
	wantErr(t, err, "unreadable")
	if blob, _ := os.ReadFile(file); string(blob) != `{"version":1,"tests":[{"id":` {
		t.Fatal("an unreadable file was written over")
	}
	os.WriteFile(file, []byte(`{"version":7,"tests":[]}`), 0o600)
	_, err = s.List("c1")
	wantErr(t, err, "has version 7")

	// A file whose next is behind its ids still never gives an id twice,
	// and odd contents derive without a panic.
	os.WriteFile(file, []byte(`{"version":1,"next":1,"tests":[{"id":"T5","title":"x","steps":[],"runs":[{"n":1,"verdict":"odd","ended_at":"2026-10-31T10:00:00Z"}]}]}`), 0o600)
	tests, err := s.List("c1")
	if err != nil || tests[0].Status() != StatusBlocked {
		t.Fatalf("List = %v, %v", tests, err)
	}
	ListText(tests)
	TestText(tests[0], 0, "/x")
	if t6 := mustCreate(t, s, "c1", "y", 1); t6.ID != "T6" {
		t.Fatalf("next id %s", t6.ID)
	}
}

func TestOnChange(t *testing.T) {
	s, _ := newStore(t)
	var calls []string
	s.SetOnChange(func(chat string) {
		// It runs without the lock: reading the store must not deadlock.
		s.List(chat)
		calls = append(calls, chat)
	})
	mustCreate(t, s, "c1", "Flow", 1)
	s.Create("c2", Fields{}, ByAgent)                   // refused
	s.SetOverride("c1", "T1", "", "")                   // nothing to remove
	s.Update("c1", "T1", Patch{Goal: ptr("")}, ByAgent) // no change
	mustStart(t, s, "c1", "T1")
	if strings.Join(calls, ",") != "c1,c1" {
		t.Fatalf("OnChange calls %v", calls)
	}
}

func TestConcurrentJudgements(t *testing.T) {
	s, _ := newStore(t)
	const n = 8
	for i := 0; i < n; i++ {
		mustCreate(t, s, "c1", fmt.Sprintf("Flow %d", i), 5)
		mustStart(t, s, "c1", fmt.Sprintf("T%d", i+1))
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		for step := 1; step <= 5; step++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, _, err := s.Judge("c1", fmt.Sprintf("T%d", i+1), Judgement{Step: step, Verdict: "pass", Observed: "ok", Output: "out"}); err != nil {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	tests, _ := s.List("c1")
	for _, tt := range tests {
		r := tt.Open()
		if r.JudgedCount() != 5 {
			t.Fatalf("%s: %d of 5 judged", tt.ID, r.JudgedCount())
		}
		seen := map[string]bool{}
		for _, st := range r.Steps {
			if len(st.Evidence) != 1 || seen[st.Evidence[0].File] {
				t.Fatalf("%s: evidence %+v", tt.ID, st.Evidence)
			}
			seen[st.Evidence[0].File] = true
		}
	}
	if rev, _ := s.Rev("c1"); rev != 2*n+5*n {
		t.Fatalf("rev %d", rev)
	}
}

func TestViewForTheAPI(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 2)
	mustStart(t, s, "c1", "T1")
	s.Finish("c1", "T1", "Nothing reached.", "")
	got, _ := mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass", Output: "ok"})
	got, _ = s.Get("c1", "T1")

	blob, _ := json.Marshal(View(got, false))
	var v map[string]any
	json.Unmarshal(blob, &v)
	if v["status"] != "running" || v["stale"] != false || v["id"] != "T1" {
		t.Fatalf("view %s", blob)
	}
	runs := v["runs"].([]any)
	first := runs[0].(map[string]any)
	if len(runs) != 2 || first["verdict"] != "blocked" || first["steps"] != nil || first["ended_at"] == nil {
		t.Fatalf("runs in a list %s", blob)
	}
	latest := v["latest"].(map[string]any)
	if latest["n"] != 2.0 || len(latest["steps"].([]any)) != 2 || v["events"] != nil {
		t.Fatalf("latest %s", blob)
	}
	blob, _ = json.Marshal(View(got, true))
	json.Unmarshal(blob, &v)
	if v["runs"].([]any)[0].(map[string]any)["steps"] == nil || v["events"] == nil {
		t.Fatalf("full view %s", blob)
	}
	if blob, _ := json.Marshal(View(Test{ID: "T1"}, false)); !strings.Contains(string(blob), `"tags":[],"steps":[]`) || !strings.Contains(string(blob), `"runs":[]`) {
		t.Fatalf("empty lists %s", blob)
	}
}

func TestChatIDsStayInTheirFolder(t *testing.T) {
	s, _ := newStore(t)
	for _, chat := range []string{"", "..", "a/b", `a\b`} {
		_, err := s.List(chat)
		if err == nil {
			t.Errorf("List(%q) was not refused", chat)
		}
		if _, err := s.FilePath(chat, "T1", "r1/01-x.png"); err == nil {
			t.Errorf("FilePath(%q) was not refused", chat)
		}
	}
}
