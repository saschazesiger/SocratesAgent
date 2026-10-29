package testmode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// The findings of the review of the reworked Tests page, each with the
// case that showed it.

// A run whose secrets could not be read again after a restart is paused
// with its cells still running: nothing that reads their evidence (the live
// view, the attempt's view, a call of the agent, a text file of the
// evidence) takes what they wrote while the secrets are not there to cut,
// and once they are, it is taken in redacted.
func TestEvidenceIsNotReadWithoutTheSecrets(t *testing.T) {
	host := newFakeHost()
	data := t.TempDir()
	r := New(data, host)
	r.Start()
	one := strings.Replace(twoSteps, "device: all", "device: [a]", 1)
	repo := fixture(t, map[string]string{"one": one})
	run := startRun(t, r, repo, noReview())
	r.schedule()
	waitFor(t, "a session", func() bool { return len(host.chats()) == 1 })
	chat := host.chats()[0]
	waitFor(t, "the cell to run", func() bool { return r.attemptsOf(run.ID)[0].State == AttemptRunning })
	l, err := r.LaunchFor(refJSON(t, r, chat))
	if err != nil {
		t.Fatal(err)
	}
	evidence := envOf(l.Env, "SOCRATES_TEST_EVIDENCE")
	r.Close()

	// The tool writes a line with a secret while the server is down, and
	// the secrets command fails at the next start.
	tool := filepath.Join(repo, ".socrates", "tests", "tools", "pool", "TOOL.md")
	good, _ := os.ReadFile(tool)
	write(t, tool, strings.Replace(string(good), "printf 'ACCOUNTS", "exit 3; printf 'ACCOUNTS", 1), 0o644)
	line := `{"at":"2026-10-29T10:00:00Z","tool":"pool","action":"open","summary":"logged in as u1:key-one-secret"}` + "\n"
	write(t, filepath.Join(evidence, ActionsFile), line, 0o644)

	r2 := New(data, host)
	r2.Start()
	defer r2.Close()
	waitFor(t, "the run to pause", func() bool { return r2.runState(run.ID) == RunPaused })
	a := r2.attemptsOf(run.ID)[0]
	rows, _ := r2.Cells(run.ID)
	if _, err := r2.AttemptDetail(run.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if text, isErr := call(t, r2, chat, testtools.Steps, nil); !isErr || !strings.Contains(text, "paused") {
		t.Fatalf("a call of the paused cell: %v %s", isErr, text)
	}
	if _, _, err := r2.EvidencePath(run.ID, a.ID, ActionsFile); err == nil {
		t.Fatal("the evidence's text was served without the secrets to cut")
	}
	record, _ := os.ReadFile(filepath.Join(r2.layout.AttemptDir(a), "record.json"))
	if got := r2.attemptsOf(run.ID)[0]; len(got.Actions) != 0 || strings.Contains(string(record), "key-one-secret") || rows[0].Live.Activity != "" {
		t.Fatalf("read without the secrets: %+v %s", got.Actions, record)
	}

	// The secrets are back: the line is read, redacted.
	write(t, tool, string(good), 0o644)
	if err := r2.ResumeRun(run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to run again", func() bool { return r2.runState(run.ID) == RunRunning })
	if _, err := r2.AttemptDetail(run.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	got := r2.attemptsOf(run.ID)[0]
	if len(got.Actions) != 1 || strings.Contains(got.Actions[0].Summary, "key-one-secret") || !strings.Contains(got.Actions[0].Summary, "‹ACCOUNTS›") {
		t.Fatalf("after the resume: %+v", got.Actions)
	}

	// While a run is taken up again, nothing is read either.
	r2.mu.Lock()
	rs := r2.runs[run.ID]
	rs.resuming = true
	unsafe := rs.unredactable()
	rs.resuming = false
	r2.mu.Unlock()
	if !unsafe {
		t.Fatal("a run being taken up again counts as redactable")
	}
}

func readJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// manyRuns is a repository with runs runs of cells cells each, every cell
// flipping between pass and fail over the runs now and then.
func manyRuns(t testing.TB, r *Runner, runs, cells int) []*Run {
	var out []*Run
	for n := range runs {
		v := map[string]string{}
		for c := range cells {
			verdict := testtools.Pass
			if (c+n)%7 == 0 {
				verdict = testtools.Fail
			}
			v[fmt.Sprintf("web/t%03d@x", c)] = verdict
		}
		out = append(out, synthTB(t, r, "shop-1", v, 1000))
	}
	return out
}

// The views the page polls read the repository's history once until a run
// or an attempt changes, and count every run's changes in one pass.
func TestTheHistoryIsKeptUntilSomethingChanges(t *testing.T) {
	r, _ := newRunner(t, newFakeHost())
	runs := manyRuns(t, r, 30, 200)
	r.mu.Lock()
	h := r.historyLocked("shop-1")
	same := r.historyLocked("shop-1") == h
	// The changes in one pass are the ones cell by cell.
	for i := range h.runs {
		want := Changes{}
		for cell, a := range h.cells[i] {
			if _, p := h.prev(i, cell); p != nil {
				switch changeOf(p.Final(), true, a.Final()) {
				case ChangeRegressed:
					want.Regressed++
				case ChangeFixed:
					want.Fixed++
				}
			}
		}
		if got := h.changes(i); *got != want {
			t.Errorf("run %d: changes %+v, want %+v", i+1, *got, want)
		}
	}
	// t006 passed in run 29 and fails in run 30: an override takes the
	// regression back.
	a := cellOf(r.runs[runs[29].ID], "web/t006@x")
	a.Override = &Override{Verdict: testtools.Pass}
	r.saveAttemptLocked(a)
	again := r.historyLocked("shop-1")
	r.mu.Unlock()
	if !same || again == h {
		t.Fatalf("kept: %v, read again after a change: %v", same, again != h)
	}
	if v, _ := r.RunDetail(runs[29].ID); v.Changes == nil || v.Changes.Regressed != h.changes(29).Regressed-1 {
		t.Fatalf("the override is not seen: %+v against %+v", v.Changes, h.changes(29))
	}
	// What the page polls stays fast: 30 runs of 200 cells.
	start := time.Now()
	for range 20 {
		r.Runs("shop-1")
		r.Overview("shop-1")
		r.Cells(runs[29].ID)
		r.Bug("shop-1", "B-1")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("20 polls took %s", d)
	}
}

func BenchmarkThePolledViews(b *testing.B) {
	r := New(b.TempDir(), newFakeHost())
	runs := manyRuns(b, r, 30, 200)
	b.ResetTimer()
	for range b.N {
		r.Runs("shop-1")
		r.Overview("shop-1")
		r.Cells(runs[29].ID)
	}
}

// persist writes a synthesised run and its attempts, as the runner keeps
// them.
func persist(t *testing.T, r *Runner, run *Run) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[run.ID]
	r.saveReposLocked()
	r.saveRunLocked(rs)
	for _, a := range rs.attempts {
		r.saveAttemptLocked(a)
	}
}

func (r *Runner) end(run *Run, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endRunLocked(r.runs[run.ID], state)
}

// A run that was done when the server stopped before the bug list learnt
// of it is merged at the next start; a bug list that cannot be read is
// moved aside, never written over, and built again from the runs, with no
// id given twice.
func TestTheBugListCatchesUpAndSurvivesACorruptFile(t *testing.T) {
	host := newFakeHost()
	data := t.TempDir()
	r := New(data, host)
	r.Start()
	const repo = "shop-1"
	one := synth(t, r, repo, map[string]string{"web/a@x": "fail", "web/b@x": "issue"}, 1000)
	persist(t, r, one)
	bugsOf(t, r, one, testtools.Bug{Title: "The cart is empty", Severity: "major", Cells: []string{"web/a@x"}},
		testtools.Bug{Title: "The logo overlaps", Severity: "minor", Cells: []string{"web/b@x"}})
	r.end(one, RunDone)
	if b := r.bug(repo, "B-1"); b.Status != BugOpen {
		t.Fatalf("B-1 after run 1: %+v", b)
	}
	// Run 2 is saved done, and the server stops before the list is.
	two := synth(t, r, repo, map[string]string{"web/a@x": "pass", "web/b@x": "issue"}, 1000)
	bugsOf(t, r, two, testtools.Bug{ID: "B-2", Title: "The logo overlaps", Severity: "minor", Cells: []string{"web/b@x"}})
	r.mu.Lock()
	r.runs[two.ID].run.BugsPending = true
	r.mu.Unlock()
	persist(t, r, two)
	// Run 3 was stopped: its list is never merged, but its ids stay taken.
	three := synth(t, r, repo, map[string]string{"web/a@x": "fail"}, 1000)
	persist(t, r, three)
	bugsOf(t, r, three, testtools.Bug{Title: "A new one", Severity: "minor", Cells: []string{"web/a@x"}})
	r.end(three, RunStopped)
	r.Close()

	r2 := New(data, host)
	r2.Start()
	if b := r2.bug(repo, "B-1"); b.Status != BugFixed || b.FixedIn != two.ID {
		t.Fatalf("B-1 after the restart: %+v", b)
	}
	if b := r2.bug(repo, "B-2"); len(b.Runs) != 2 || b.LastRun != two.ID {
		t.Fatalf("B-2 after the restart: %+v", b)
	}
	var saved Run
	readJSONFile(t, filepath.Join(r2.layout.RunDir(repo, two.ID), "run.json"), &saved)
	if saved.BugsPending {
		t.Fatal("run 2 is still pending")
	}
	if b := r2.bug(repo, "B-3"); b.ID != "" {
		t.Fatalf("the stopped run was merged: %+v", b)
	}
	if _, err := r2.PatchBug(repo, "B-2", BugPatch{Status: ptr(BugKnown)}); err != nil {
		t.Fatal(err)
	}
	r2.Close()

	path := filepath.Join(data, "tests", repo, "bugs.json")
	write(t, path, `{"next": 3, "bugs": [{"id": "B-1",`, 0o600)
	r3 := New(data, host)
	r3.Start()
	defer r3.Close()
	aside, _ := filepath.Glob(path + ".corrupt-*")
	if len(aside) != 1 {
		t.Fatalf("moved aside: %v", aside)
	}
	if raw, _ := os.ReadFile(aside[0]); string(raw) != `{"next": 3, "bugs": [{"id": "B-1",` {
		t.Fatalf("aside: %s", raw)
	}
	if b := r3.bug(repo, "B-1"); b.Status != BugFixed || b.FixedIn != two.ID || len(b.Runs) != 1 {
		t.Fatalf("B-1 built again: %+v", b)
	}
	if b := r3.bug(repo, "B-2"); b.Status != BugOpen || len(b.Runs) != 2 {
		t.Fatalf("B-2 built again: %+v", b)
	}
	var list bugList
	readJSONFile(t, path, &list)
	if len(list.Bugs) != 2 || list.Next != 4 {
		t.Fatalf("the list on disk: next %d, %d bugs", list.Next, len(list.Bugs))
	}
	// A new bug gets an id no run has had.
	four := synth(t, r3, repo, map[string]string{"web/a@x": "fail"}, 1000)
	if got := bugsOf(t, r3, four, testtools.Bug{Title: "Another", Severity: "minor"}); got[0].ID != "B-4" {
		t.Fatalf("a new bug after the rebuild: %+v", got)
	}
}

// Taken up again for an analysis or a retry, a stopped run ends stopped
// again and changes nothing in the bug list.
func TestAStoppedRunNeverChangesTheBugList(t *testing.T) {
	r, _ := newRunner(t, newFakeHost())
	const repo = "shop-1"
	one := synth(t, r, repo, map[string]string{"web/a@x": "fail"}, 1000)
	bugsOf(t, r, one, testtools.Bug{Title: "The cart is empty", Severity: "major", Cells: []string{"web/a@x"}})
	r.end(one, RunDone)
	two := synth(t, r, repo, map[string]string{"web/a@x": "pass"}, 1000)
	bugsOf(t, r, two, testtools.Bug{Title: "Something else", Severity: "minor", Cells: []string{"web/a@x"}})
	r.end(two, RunStopped)
	if err := r.Analyse(two.ID); err != nil {
		t.Fatal(err)
	}
	// The analyst is done: the run ends as advanceLocked ends it.
	r.mu.Lock()
	rs := r.runs[two.ID]
	rs.run.Settings.Analysis = false
	r.advanceLocked(rs)
	state, stopped := rs.run.State, rs.run.Stopped
	r.mu.Unlock()
	if state != RunStopped || !stopped {
		t.Fatalf("the analysed stopped run is %s", state)
	}
	if b := r.bug(repo, "B-1"); b.Status != BugOpen || len(b.Runs) != 1 {
		t.Fatalf("B-1 after the stopped run: %+v", b)
	}
	if b := r.bug(repo, "B-2"); b.ID != "" {
		t.Fatalf("the stopped run's bug is on the list: %+v", b)
	}
}

// Taken back, a report keeps a bug the person triaged; and a bug fixed by
// hand opens again only when its run is analysed again since, not when the
// same report is merged again.
func TestTriageSurvivesAReanalysis(t *testing.T) {
	r, _ := newRunner(t, newFakeHost())
	const repo = "shop-1"
	one := synth(t, r, repo, map[string]string{"web/a@x": "fail", "web/b@x": "fail"}, 1000)
	bugsOf(t, r, one, testtools.Bug{Title: "Footer", Severity: "minor", Cells: []string{"web/a@x"}},
		testtools.Bug{Title: "Header", Severity: "minor", Cells: []string{"web/b@x"}})
	r.merge(one)
	r.PatchBug(repo, "B-1", BugPatch{Status: ptr(BugNotABug)})
	r.PatchBug(repo, "B-2", BugPatch{Status: ptr(BugFixed)})
	if b := r.bug(repo, "B-2"); b.FixedIn != one.ID || b.FixedAt == 0 {
		t.Fatalf("fixed by hand: %+v", b)
	}
	r.merge(one)
	if b := r.bug(repo, "B-2"); b.Status != BugFixed {
		t.Fatalf("the same report merged again opened it: %+v", b)
	}
	r.mu.Lock()
	r.runs[one.ID].run.Analysed = r.now().UnixMilli() + 1
	r.mu.Unlock()
	bugsOf(t, r, one, testtools.Bug{ID: "B-2", Title: "Header", Severity: "minor", Cells: []string{"web/b@x"}})
	r.merge(one)
	if b := r.bug(repo, "B-2"); b.Status != BugOpen || b.Regressed {
		t.Fatalf("analysed again, reporting it: %+v", b)
	}
	if b := r.bug(repo, "B-1"); b.ID != "B-1" || b.Status != BugNotABug || len(b.Runs) != 0 {
		t.Fatalf("the triaged bug no run reports any longer: %+v", b)
	}
}

// A selection's description names its cells once.
func TestASelectionDescribesItsCellsOnce(t *testing.T) {
	for _, c := range []struct {
		sel  Selection
		want string
	}{
		{Selection{}, "everything"},
		{Selection{Cells: []string{"web/a@x"}}, "cell web/a@x"},
		{Selection{Areas: []string{"web"}, Cells: []string{"web/a@x", "web/b@x", "web/c@x"}}, "areas web; cells web/a@x, web/b@x, web/c@x"},
		{Selection{Cells: []string{"a@1", "b@1", "c@1", "d@1", "e@1"}, ProblemsOf: "r-1"}, "cells a@1, b@1 and 3 more; problems of r-1"},
	} {
		if got := c.sel.Describe(); got != c.want {
			t.Errorf("Describe(%+v) = %q, want %q", c.sel, got, c.want)
		}
	}
}

// A bug's cell that its last run did not run comes from the newest run
// that did, and its row names that run: the page reads its steps there.
func TestABugsCellsNameTheirRun(t *testing.T) {
	r, _ := newRunner(t, newFakeHost())
	const repo = "shop-1"
	one := synth(t, r, repo, map[string]string{"web/a@x": "fail"}, 1000)
	two := synth(t, r, repo, map[string]string{"web/z@x": "fail"}, 1000)
	three := synth(t, r, repo, map[string]string{"web/a@x": "fail"}, 1000)
	bugsOf(t, r, three, testtools.Bug{Title: "Both", Severity: "major", Cells: []string{"web/a@x", "web/z@x"}})
	r.merge(three)
	d, err := r.Bug(repo, "B-1")
	if err != nil || len(d.Cells) != 2 {
		t.Fatalf("detail: %+v %v", d, err)
	}
	if d.Cells[0].CellID != "web/a@x" || d.Cells[0].Run != three.ID || d.Cells[1].CellID != "web/z@x" || d.Cells[1].Run != two.ID {
		t.Fatalf("cells: %s in %s, %s in %s (runs %s %s %s)", d.Cells[0].CellID, d.Cells[0].Run, d.Cells[1].CellID, d.Cells[1].Run, one.ID, two.ID, three.ID)
	}
}
