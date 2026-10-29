package testmode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// The tests of the reworked Tests page's server side (TEST-MODE.md,
// "Revision: the reworked Tests page"): run numbers, the selection of
// cells, overrides, changes against the run before, health, estimates, the
// bug list over runs, the report, GitHub issues and the live view.

// synth puts a run that is done into the runner as if it had run: one ended
// attempt of one step per cell with the verdict given, each taking ms.
func synth(t *testing.T, r *Runner, repo string, verdicts map[string]string, ms int64) *Run {
	t.Helper()
	return synthTB(t, r, repo, verdicts, ms)
}

func synthTB(t testing.TB, r *Runner, repo string, verdicts map[string]string, ms int64) *Run {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repos[repo] == nil {
		r.repos[repo] = &Repo{Key: repo, Path: t.TempDir()}
	}
	r.numbers[repo]++
	n := r.numbers[repo]
	at := int64(n) * 3_600_000
	run := &Run{ID: fmt.Sprintf("r-20261029-%06d-abcd", n), Repo: repo, Number: n, State: RunDone, CreatedAt: at,
		StartedAt: at, EndedAt: at + ms, Settings: DefaultSettings()}
	rs := newRunState(run)
	ids := make([]string, 0, len(verdicts))
	for id := range verdicts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		test, cell, _ := strings.Cut(id, "@")
		v := verdicts[id]
		a := &Attempt{ID: newAttemptID(), Run: run.ID, Repo: repo, Test: test, Area: strings.Split(test, "/")[0],
			Title: "Title of " + test, Priority: "P1", Cell: cell, CellID: id, N: 1, State: AttemptEnded, Verdict: v,
			StartedAt: at, EndedAt: at + ms, Chat: "chat-" + run.ID + "-" + id,
			Dims:  []Dim{{Name: "device", ID: cell, Label: "Device " + strings.ToUpper(cell)}},
			Steps: []Step{{N: 1, Do: "Open the page.", Expect: "The page.", Verdict: v, Observed: "It shows " + v + "."}}}
		rs.attempts[a.ID] = a
		run.Order = append(run.Order, AttemptRef{ID: a.ID, Test: a.Test, Cell: a.Cell, N: 1})
	}
	r.runs[run.ID] = rs
	r.changedLocked()
	return run
}

func attemptOf(r *Runner, run, cell string) *Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cellOf(r.runs[run], cell)
}

func TestRunsAreNumberedAndOldRunsGetNumbersAtLoad(t *testing.T) {
	host := newFakeHost()
	data := t.TempDir()
	r := New(data, host)
	r.Start()
	repo := fixture(t, map[string]string{"two": twoSteps})
	s := noReview()
	first := startRun(t, r, repo, s)
	r.StopRun(first.ID)
	second, err := r.StartRun(first.Repo, "", Selection{}, s)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second run", func() bool { return r.runState(second.ID) == RunRunning })
	r.StopRun(second.ID)
	if first.Number != 1 || second.Number != 2 {
		t.Fatalf("numbers %d, %d", first.Number, second.Number)
	}
	waitFor(t, "the end hooks", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.runs[first.ID].ending)+len(r.runs[second.ID].ending) == 0
	})
	r.Close()
	// Runs from before run numbers have none.
	for _, id := range []string{first.ID, second.ID} {
		path := filepath.Join(data, "tests", first.Repo, "runs", id, "run.json")
		var m map[string]any
		raw, _ := os.ReadFile(path)
		json.Unmarshal(raw, &m)
		delete(m, "number")
		raw, _ = json.Marshal(m)
		os.WriteFile(path, raw, 0o600)
	}
	r2 := New(data, host)
	r2.Load()
	defer r2.Close()
	views := r2.Runs(first.Repo)
	if len(views) != 2 || views[0].ID != second.ID || views[0].Number != 2 || views[1].Number != 1 {
		t.Fatalf("runs after the load: %+v", views)
	}
	var saved Run
	if err := readJSON(filepath.Join(data, "tests", first.Repo, "runs", first.ID, "run.json"), &saved); err != nil || saved.Number != 1 {
		t.Fatalf("the number was not saved: %d %v", saved.Number, err)
	}
	r2.StartRun(first.Repo, "", Selection{}, s)
	if views := r2.Runs(first.Repo); views[0].Number != 3 {
		t.Fatalf("the next run is %d", views[0].Number)
	}
}

func TestASelectionOfCellsAndTheProblemsOfTheLatestRun(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	repo := fixture(t, map[string]string{"two": twoSteps})
	got, _ := r.AddRepo(repo)
	if _, err := r.StartRun(got.Key, "", Selection{ProblemsOf: ProblemsLatest}, noReview()); err == nil ||
		!strings.Contains(err.Error(), "no run that is over") {
		t.Fatalf("the problems of the latest run without one: %v", err)
	}
	sel := Selection{Cells: []string{" web/two@b "}}
	if d := sel.Describe(); d != "cell  web/two@b " && !strings.Contains(d, "web/two@b") {
		t.Fatalf("describe: %q", d)
	}
	run, err := r.StartRun(got.Key, "", sel, noReview())
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run", func() bool { return r.runState(run.ID) == RunRunning })
	if list := r.attemptsOf(run.ID); len(list) != 1 || list[0].CellID != "web/two@b" {
		t.Fatalf("attempts: %d", len(list))
	}
	r.schedule()
	waitFor(t, "the session", func() bool { return len(host.chats()) == 1 })
	chat := host.chats()[0]
	if msg := host.spec(chat).Message; !strings.Contains(msg, "- Run: 1\n") {
		t.Fatalf("the first message names no run: %s", msg)
	}
	act(t, r, chat, "open", 1)
	mustCall(t, r, chat, testtools.Judge, map[string]any{"step": 1, "verdict": "fail", "observed": "an error"})
	mustCall(t, r, chat, testtools.Judge, map[string]any{"rest": true, "verdict": "blocked", "observed": "not reached"})
	mustCall(t, r, chat, testtools.End, map[string]any{"summary": "fails"})
	waitFor(t, "the run to be done", func() bool { r.schedule(); return r.runState(run.ID) == RunDone })

	next, err := r.StartRun(got.Key, "", Selection{ProblemsOf: ProblemsLatest, Areas: []string{"web"}}, noReview())
	if err != nil || next.Selection.ProblemsOf != run.ID || next.Number != 2 {
		t.Fatalf("problems of the latest: %+v %v", next, err)
	}
	waitFor(t, "the second run", func() bool { return r.runState(next.ID) == RunRunning })
	if list := r.attemptsOf(next.ID); len(list) != 1 || list[0].CellID != "web/two@b" {
		t.Fatalf("second run's attempts: %+v", list)
	}
	if !strings.Contains(next.Selection.Describe(), "problems of "+run.ID) {
		t.Fatalf("describe: %s", next.Selection.Describe())
	}
}

func TestAnOverrideCountsOverTheReviewAndTheAgent(t *testing.T) {
	a := &Attempt{Verdict: testtools.Fail}
	if a.Final() != testtools.Fail {
		t.Fatal(a.Final())
	}
	a.Reviewed = testtools.Issue
	if a.Final() != testtools.Issue {
		t.Fatal(a.Final())
	}
	a.Override = &Override{Verdict: testtools.Pass}
	if a.Final() != testtools.Pass {
		t.Fatal(a.Final())
	}

	r, _ := newRunner(t, newFakeHost())
	run := synth(t, r, "shop-1", map[string]string{"web/a@x": "fail", "web/a@y": "pass"}, 1000)
	fail := attemptOf(r, run.ID, "web/a@x")
	if _, err := r.SetOverride(run.ID, fail.ID, "great", "x"); err == nil {
		t.Fatal("a verdict that is none was taken")
	}
	row, err := r.SetOverride(run.ID, fail.ID, "Pass", "the error is the test data's")
	if err != nil || row.Final != testtools.Pass || row.Override == nil || row.Override.Note != "the error is the test data's" {
		t.Fatalf("override: %+v %v", row, err)
	}
	v, _ := r.RunDetail(run.ID)
	if v.Counts.Verdicts[testtools.Pass] != 2 || v.Counts.Verdicts[testtools.Fail] != 0 {
		t.Fatalf("counts: %+v", v.Counts)
	}
	var saved Attempt
	readJSON(filepath.Join(r.layout.AttemptDir(fail), "record.json"), &saved)
	if saved.Override == nil || saved.Override.Verdict != testtools.Pass {
		t.Fatalf("the override was not saved: %+v", saved.Override)
	}
	if row, err := r.SetOverride(run.ID, fail.ID, "", ""); err != nil || row.Final != testtools.Fail || row.Override != nil {
		t.Fatalf("removing: %+v %v", row, err)
	}
	r.mu.Lock()
	fail.State = AttemptRunning
	r.mu.Unlock()
	if _, err := r.SetOverride(run.ID, fail.ID, "pass", ""); err == nil {
		t.Fatal("a running attempt was overridden")
	}
}

func TestCellsAgainstTheRunBefore(t *testing.T) {
	r, _ := newRunner(t, newFakeHost())
	synth(t, r, "shop-1", map[string]string{"web/a@x": "pass", "web/a@y": "fail", "web/a@z": "pass", "web/b@x": "issue"}, 1000)
	middle := synth(t, r, "shop-1", map[string]string{"web/b@x": "blocked"}, 1000)
	last := synth(t, r, "shop-1", map[string]string{"web/a@x": "fail", "web/a@y": "pass", "web/a@z": "pass",
		"web/b@x": "issue", "web/c@x": "pass"}, 1000)
	rows, _ := r.Cells(last.ID)
	got := map[string]CellRow{}
	for _, row := range rows {
		got[row.CellID] = row
	}
	if c := got["web/a@x"]; c.Change != ChangeRegressed || c.Prev == nil || c.Prev.Verdict != "pass" || c.Prev.Number != 1 {
		t.Fatalf("a@x: %+v %+v", c, c.Prev)
	}
	if c := got["web/a@y"]; c.Change != ChangeFixed {
		t.Fatalf("a@y: %+v", c)
	}
	if c := got["web/a@z"]; c.Change != "" || c.Prev == nil {
		t.Fatalf("a@z: %+v", c)
	}
	if c := got["web/c@x"]; c.Change != ChangeNew || c.Prev != nil {
		t.Fatalf("c@x: %+v", c)
	}
	// The run before with a verdict of it, which may be blocked; its streak
	// of problems skips the blocked run.
	if c := got["web/b@x"]; c.Prev == nil || c.Prev.Number != middle.Number || c.Prev.Verdict != "blocked" || c.Change != "" ||
		c.Since == nil || c.Since.Number != 1 {
		t.Fatalf("b@x: %+v prev %+v since %+v", c, c.Prev, c.Since)
	}
	v, _ := r.RunDetail(last.ID)
	if v.Number != 3 || v.Changes == nil || v.Changes.Regressed != 1 || v.Changes.Fixed != 1 || v.DurationMS != 1000 {
		t.Fatalf("view: %+v %+v", v.Run, v.Changes)
	}
	// An override changes what the change is.
	r.SetOverride(last.ID, got["web/a@x"].ID, "pass", "")
	if v, _ := r.RunDetail(last.ID); v.Changes.Regressed != 0 {
		t.Fatalf("after the override: %+v", v.Changes)
	}
}

func TestHealthFlakinessAndTheOverview(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	for i, v := range []string{"pass", "fail", "blocked", "pass", "issue"} {
		synth(t, r, "shop-1", map[string]string{"web/flaky@x": v, "web/steady@x": "pass", "web/broken@x": []string{"pass", "pass", "fail", "issue", "fail"}[i]}, 1000)
	}
	r.mu.Lock()
	h := r.historyLocked("shop-1")
	flaky, steady, broken := h.health("web/flaky@x"), h.health("web/steady@x"), h.health("web/broken@x")
	r.mu.Unlock()
	if flaky.Flips != 3 || !flaky.Flaky || flaky.Runs != 5 || flaky.Passes != 2 || flaky.Latest != "issue" || flaky.Since == nil || flaky.Since.Number != 5 {
		t.Fatalf("flaky: %+v", flaky)
	}
	if steady.Flaky || steady.Flips != 0 || steady.Since != nil || steady.Number != 5 {
		t.Fatalf("steady: %+v", steady)
	}
	if broken.Flips != 1 || broken.Flaky || broken.Since == nil || broken.Since.Number != 3 {
		t.Fatalf("broken: %+v since %+v", broken, broken.Since)
	}
	// A run that is active is neither the latest nor the previous.
	active := synth(t, r, "shop-1", map[string]string{"web/steady@x": "pass"}, 1000)
	r.mu.Lock()
	r.runs[active.ID].run.State = RunRunning
	r.mu.Unlock()
	host.costs = map[string]float64{"chat-r-20261029-000005-abcd-web/broken@x": 0.5}
	o, err := r.Overview("shop-1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Latest == nil || o.Latest.Number != 5 || o.Previous == nil || o.Previous.Number != 4 || len(o.Trend) != 6 || o.Trend[0].Number != 1 {
		t.Fatalf("overview: latest %+v previous %+v trend %d", o.Latest, o.Previous, len(o.Trend))
	}
	if o.Latest.Cost == nil || *o.Latest.Cost != 0.5 || o.Previous.Cost != nil {
		t.Fatalf("costs: %v %v", o.Latest.Cost, o.Previous.Cost)
	}
	if len(o.Regressed) != 1 || o.Regressed[0].CellID != "web/flaky@x" || len(o.Failing) != 2 || len(o.Fixed) != 0 {
		t.Fatalf("regressed %+v failing %+v", o.Regressed, o.Failing)
	}
	for _, f := range o.Failing {
		if f.CellID == "web/broken@x" && (f.Since == nil || f.Since.Number != 3) {
			t.Fatalf("failing since: %+v", f.Since)
		}
	}
	if len(o.Flaky) != 1 || o.Flaky[0].Test != "web/flaky" || o.Flaky[0].Flips != 3 {
		t.Fatalf("flaky: %+v", o.Flaky)
	}
}

func TestTheCatalogAndATestWithTheirHealth(t *testing.T) {
	r, _ := newRunner(t, newFakeHost())
	repo, _ := r.AddRepo(fixture(t, map[string]string{"two": twoSteps}))
	synth(t, r, repo.Key, map[string]string{"web/two@a": "pass", "web/two@b": "fail"}, 1000)
	synth(t, r, repo.Key, map[string]string{"web/two@a": "fail", "web/two@b": "pass"}, 1000)
	last := synth(t, r, repo.Key, map[string]string{"web/two@a": "pass", "web/two@b": "fail", "web/gone@a": "fail"}, 1000)
	cat, err := r.Catalog(repo.Key)
	if err != nil {
		t.Fatal(err)
	}
	ct := cat.Areas[0].Tests[0]
	if len(ct.CellHealth) != 3 || ct.Cells[0] != "a" || ct.CellHealth[0].Cell != "a" || ct.CellHealth[0].Dims[0].Label != "Device A" ||
		ct.CellHealth[1].Latest != "fail" || !ct.CellHealth[1].Flaky || ct.CellHealth[1].Attempt == "" || ct.CellHealth[2].Latest != "" {
		t.Fatalf("cells: %+v", ct.CellHealth)
	}
	if ct.Health == nil || ct.Health.Latest != "fail" || !ct.Health.Flaky || ct.Health.LastRun == nil || ct.Health.LastRun.Run != last.ID ||
		ct.Health.Since == nil || ct.Health.Since.Number != 3 {
		t.Fatalf("health: %+v", ct.Health)
	}
	v, err := r.TestDetail(repo.Key, "web/two")
	if err != nil {
		t.Fatal(err)
	}
	if v.Test == nil || len(v.Test.Steps) != 2 || len(v.Cells) != 3 || len(v.Runs) != 3 || v.Runs[0].ID != last.ID ||
		v.Grid["web/two@b"][last.ID].Verdict != "fail" || v.Grid["web/two@a"][last.ID].Attempt == "" {
		t.Fatalf("test: %+v", v)
	}
	// A test gone from the catalog still has its history.
	if v, err := r.TestDetail(repo.Key, "web/gone"); err != nil || v.Test != nil || len(v.Cells) != 1 || len(v.Runs) != 1 {
		t.Fatalf("a gone test: %+v %v", v, err)
	}
	if _, err := r.TestDetail(repo.Key, "web/never"); err != ErrNotFound {
		t.Fatalf("a test that never was: %v", err)
	}
}

func TestTheEstimateAndTheTimeLeft(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	repo, _ := r.AddRepo(fixture(t, map[string]string{"two": twoSteps}))
	if e, err := r.Estimate(repo.Key, Selection{}, nil); err != nil || e.Cells != 3 || e.Tests != 1 || e.EtaMS != 0 || e.Cost != nil || len(e.Errors) != 0 {
		t.Fatalf("without history: %+v %v", e, err)
	}
	one := synth(t, r, repo.Key, map[string]string{"web/two@a": "pass", "web/two@b": "pass"}, 60_000)
	synth(t, r, repo.Key, map[string]string{"web/two@a": "pass", "web/two@b": "pass", "web/two@c": "fail"}, 120_000)
	host.costs = map[string]float64{"chat-" + one.ID + "-web/two@a": 1, "chat-" + one.ID + "-web/two@b": 2}
	s := DefaultSettings()
	s.Parallel = 2
	e, err := r.Estimate(repo.Key, Selection{Cells: []string{"web/two@a", "web/two@b", "web/two@c"}}, &s)
	// The median cell took 120 s; three cells two at a time; 1.50 a cell.
	if err != nil || e.Cells != 3 || e.EtaMS != 180_000 || e.Cost == nil || *e.Cost != 4.5 {
		t.Fatalf("estimate: %+v %v", e, err)
	}
	// Without a limit of its own a run with a pool is limited by it: the two
	// members the runs leased.
	r.mu.Lock()
	for i, a := range latestAttempts(r.runs[one.ID]) {
		a.Leases = []Lease{{Tool: "pool", Member: i + 1}}
	}
	r.mu.Unlock()
	if e, _ := r.Estimate(repo.Key, Selection{}, nil); e.EtaMS != 180_000 {
		t.Fatalf("with the pool: %+v", e)
	}
	if e, _ := r.Estimate(repo.Key, Selection{Tests: []string{"web/nope"}}, nil); len(e.Errors) != 1 || e.Cells != 0 {
		t.Fatalf("a selection of nothing: %+v", e)
	}
	// While a run is active, its view says about how long it still takes.
	active := synth(t, r, repo.Key, map[string]string{"web/two@a": "pass", "web/two@b": "pass", "web/two@c": "pass"}, 120_000)
	r.mu.Lock()
	rs := r.runs[active.ID]
	rs.run.State, rs.run.Settings.Parallel, rs.plan = RunRunning, 1, &plan{}
	for _, a := range rs.attempts {
		if a.CellID != "web/two@a" {
			a.State = AttemptQueued
		}
	}
	r.mu.Unlock()
	if v, _ := r.RunDetail(active.ID); v.EtaMS != 240_000 {
		t.Fatalf("time left: %d", v.EtaMS)
	}
}

// bugsOf writes what a run's analyst reported, with the ids it settled.
func bugsOf(t *testing.T, r *Runner, run *Run, bugs ...testtools.Bug) []testtools.Bug {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[run.ID]
	if err := r.settleBugIDsLocked(rs, bugs); err != nil {
		t.Fatal(err)
	}
	writeJSON(r.layout.runBugsPath(run.Repo, run.ID), bugs)
	rs.forgetReported()
	return bugs
}

func (r *Runner) merge(run *Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mergeBugsLocked(r.runs[run.ID])
}

func (r *Runner) bug(repo, id string) TrackedBug {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b := r.bugListLocked(repo).find(id); b != nil {
		return *b
	}
	return TrackedBug{}
}

func TestTheBugListOverRuns(t *testing.T) {
	r, data := newRunner(t, newFakeHost())
	const repo = "shop-1"
	one := synth(t, r, repo, map[string]string{"web/a@x": "fail", "web/b@x": "issue"}, 1000)
	got := bugsOf(t, r, one,
		testtools.Bug{ID: "B1", Title: "The cart is empty", Severity: "major", Cells: []string{"web/a@x"}},
		testtools.Bug{Title: "The logo overlaps", Severity: "cosmetic", Cells: []string{"web/b@x"}})
	if got[0].ID != "B-1" || got[1].ID != "B-2" {
		t.Fatalf("ids: %+v", got)
	}
	// Written again by the same analysis, a new bug keeps its id.
	if again := bugsOf(t, r, one, testtools.Bug{Title: "The logo overlaps", Severity: "minor", Cells: []string{"web/b@x"}}); again[0].ID != "B-2" {
		t.Fatalf("written again: %+v", again)
	}
	bugsOf(t, r, one, got...)
	r.merge(one)
	b1 := r.bug(repo, "B-1")
	if b1.Status != BugOpen || b1.FirstRun != one.ID || b1.LastRun != one.ID || len(b1.Runs) != 1 || len(b1.Events) != 1 {
		t.Fatalf("B-1 after run 1: %+v", b1)
	}
	if _, err := os.Stat(filepath.Join(data, "tests", repo, "bugs.json")); err != nil {
		t.Fatal(err)
	}

	two := synth(t, r, repo, map[string]string{"web/a@x": "fail", "web/b@x": "issue"}, 1000)
	r.mu.Lock()
	known := r.knownBugsText(r.runs[two.ID])
	r.mu.Unlock()
	if !strings.Contains(known, "## B-1 [open, major] The cart is empty") || !strings.Contains(known, "Cells: web/a@x") {
		t.Fatalf("known: %s", known)
	}
	if _, err := r.PatchBug(repo, "B-2", BugPatch{Status: ptr(BugKnown)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PatchBug(repo, "B-1", BugPatch{Severity: ptr("critical")}); err != nil {
		t.Fatal(err)
	}
	bugsOf(t, r, two, testtools.Bug{ID: "b-1", Title: "The cart stays empty", Severity: "major", Cells: []string{"web/a@x"}},
		testtools.Bug{ID: "B-2", Title: "The logo overlaps", Severity: "cosmetic", Cells: []string{"web/b@x"}})
	r.merge(two)
	b1 = r.bug(repo, "B-1")
	if b1.LastRun != two.ID || len(b1.Runs) != 2 || b1.Title != "The cart stays empty" || b1.Severity != "critical" {
		t.Fatalf("B-1 after run 2: %+v", b1)
	}
	// A run that does not report them, whose cells pass: the open bug is
	// fixed, the known one stays known.
	three := synth(t, r, repo, map[string]string{"web/a@x": "pass", "web/b@x": "pass"}, 1000)
	r.merge(three)
	if b := r.bug(repo, "B-1"); b.Status != BugFixed || b.FixedIn != three.ID {
		t.Fatalf("B-1 after run 3: %+v", b)
	}
	if b := r.bug(repo, "B-2"); b.Status != BugKnown {
		t.Fatalf("B-2 after run 3: %+v", b)
	}
	// A run that did not run the cell fixes nothing; reported again, a
	// fixed bug is open again and regressed.
	four := synth(t, r, repo, map[string]string{"web/a@x": "fail"}, 1000)
	bugsOf(t, r, four, testtools.Bug{ID: "B-1", Title: "The cart stays empty", Severity: "major", Cells: []string{"web/a@x"}})
	r.merge(four)
	if b := r.bug(repo, "B-1"); b.Status != BugOpen || !b.Regressed || b.FixedIn != "" || len(b.Runs) != 3 {
		t.Fatalf("B-1 after run 4: %+v", b)
	}
	// Analysed again without it, run 4 takes its report back.
	bugsOf(t, r, four)
	r.merge(four)
	if b := r.bug(repo, "B-1"); len(b.Runs) != 2 || b.LastRun != two.ID {
		t.Fatalf("B-1 after run 4 was analysed again: %+v", b)
	}
	// Two runs that report the same known id are refused.
	r.mu.Lock()
	err := r.settleBugIDsLocked(r.runs[four.ID], []testtools.Bug{{ID: "B-1", Title: "x"}, {ID: "B-1", Title: "y"}})
	r.mu.Unlock()
	if err == nil {
		t.Fatal("one id twice was taken")
	}
	// A bug fixed more than ten runs ago is not shown to the analyst.
	r.PatchBug(repo, "B-1", BugPatch{Status: ptr(BugFixed)})
	for range 11 {
		synth(t, r, repo, map[string]string{"web/c@x": "pass"}, 1000)
	}
	latest := synth(t, r, repo, map[string]string{"web/c@x": "pass"}, 1000)
	r.mu.Lock()
	known = r.knownBugsText(r.runs[latest.ID])
	r.mu.Unlock()
	if strings.Contains(known, "B-1") || !strings.Contains(known, "B-2") {
		t.Fatalf("known much later: %s", known)
	}
	// The list: the worst first; a status filters it.
	list, _ := r.BugList(repo, "")
	if len(list) != 2 || list[0].ID != "B-1" || list[0].FirstNumber != 1 {
		t.Fatalf("list: %+v", list)
	}
	if list, _ := r.BugList(repo, BugKnown); len(list) != 1 || list[0].ID != "B-2" {
		t.Fatalf("known list: %+v", list)
	}
	if _, err := r.BugList(repo, "gone"); err == nil {
		t.Fatal("a status that is none was taken")
	}
	d, err := r.Bug(repo, "B-2")
	if err != nil || len(d.Cells) != 1 || d.Cells[0].CellID != "web/b@x" || d.Cells[0].Prev == nil || len(d.Runs) != 2 || d.Runs[0].Number != 2 {
		t.Fatalf("detail: %+v %v", d, err)
	}
	if repos := r.Repos(); len(repos) != 1 || repos[0].OpenBugs != 0 {
		t.Fatalf("repos: %+v", repos)
	}
}

func ptr[T any](v T) *T { return &v }

func TestPatchingABugIsValidated(t *testing.T) {
	r, _ := newRunner(t, newFakeHost())
	run := synth(t, r, "shop-1", map[string]string{"web/a@x": "fail"}, 1000)
	bugsOf(t, r, run, testtools.Bug{Title: "Broken", Severity: "major", Cells: []string{"web/a@x"}})
	r.merge(run)
	for _, p := range []BugPatch{{Status: ptr("closed")}, {Severity: ptr("huge")}, {Issue: ptr("javascript:alert(1)")},
		{Note: ptr(strings.Repeat("x", maxNote+1))}} {
		if _, err := r.PatchBug("shop-1", "B-1", p); err == nil {
			t.Fatalf("taken: %+v", p)
		}
	}
	if _, err := r.PatchBug("shop-1", "B-9", BugPatch{Note: ptr("x")}); err != ErrNotFound {
		t.Fatalf("a bug that is none: %v", err)
	}
	v, err := r.PatchBug("shop-1", "B-1", BugPatch{Status: ptr(BugNotABug), Note: ptr(" the test data was wrong "),
		Issue: ptr("https://github.com/acme/shop/issues/7"), Severity: ptr("minor")})
	if err != nil || v.Status != BugNotABug || v.Note != "the test data was wrong" || v.Issue == "" || !v.SeverityByHand ||
		len(v.Events) != 5 {
		t.Fatalf("patched: %+v %v", v, err)
	}
	// Reported again, a bug that is not one stays so.
	two := synth(t, r, "shop-1", map[string]string{"web/a@x": "fail"}, 1000)
	bugsOf(t, r, two, testtools.Bug{ID: "B-1", Title: "Broken", Severity: "critical", Cells: []string{"web/a@x"}})
	r.merge(two)
	if b := r.bug("shop-1", "B-1"); b.Status != BugNotABug || b.Severity != "minor" {
		t.Fatalf("after run 2: %+v", b)
	}
}

func TestTheReport(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	synth(t, r, "shop-1", map[string]string{"web/a@x": "pass", "web/b@x": "pass"}, 1000)
	run := synth(t, r, "shop-1", map[string]string{"web/a@x": "fail", "web/b@x": "pass"}, 125_000)
	a := attemptOf(r, run.ID, "web/a@x")
	r.mu.Lock()
	a.Actions = []Action{{N: 1, Action: "shot", Summary: "took it", Screenshot: "01.png"}}
	a.Steps[0].Actions = []int{1}
	a.Reviewed = testtools.Issue
	a.Steps[0].Review = &StepReview{Verdict: testtools.Issue, Note: "It works, slowly."}
	r.runs[run.ID].run.Name = "nightly"
	r.mu.Unlock()
	bugsOf(t, r, run, testtools.Bug{Title: "Slow checkout", Severity: "minor", Cells: []string{"web/a@x"}})
	r.merge(run)
	os.WriteFile(filepath.Join(r.layout.RunDir("shop-1", run.ID), "conclusions.md"), []byte("# Summary\n\nSlow."), 0o600)
	host.costs = map[string]float64{a.Chat: 1.234}
	md, err := r.Report(run.ID, ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"· Run 2: nightly", "- Duration: 2 min", "- Cost: $1.23", "2 cells: 1 pass, 1 issue.",
		"1 regressed, 0 fixed", "### Minor", "**B-1** Slow checkout (open). Cells: web/a@x", "### web/a@x: Title of web/a",
		"Device X (x)", "Verdict: **issue** (the agent said fail, the reviewer made it issue); in run 1: pass, regressed",
		"- Step 1, issue: Open the page.", "  - Reviewer (issue): It works, slowly.", "  - Screenshots: 01.png",
		"## Every cell", "| web/b@x | pass | pass (run 1) |", "## Conclusions", "### Summary"} {
		if !strings.Contains(md, want) {
			t.Errorf("the report lacks %q:\n%s", want, md)
		}
	}
	md, _ = r.Report(run.ID, ReportOptions{Paths: true, ProblemsOnly: true})
	if strings.Contains(md, "## Every cell") || !strings.Contains(md, filepath.Join(r.layout.EvidenceDir(a), "01.png")) {
		t.Fatalf("problems only, with paths:\n%s", md)
	}
	if _, err := r.Report("r-nope", ReportOptions{}); err != ErrNotFound {
		t.Fatal(err)
	}
	if name := ReportFileName("shop-1a2b", 12); name != "shop-1a2b-run-12.md" {
		t.Fatal(name)
	}
}

func TestGitHubIssuesThroughGh(t *testing.T) {
	bin := t.TempDir()
	got := filepath.Join(t.TempDir(), "issue")
	write(t, filepath.Join(bin, "gh"), `#!/bin/sh
case "$1 $2" in
"auth status") exit 0 ;;
"issue create") echo "$@" > "`+got+`.args"; cat > "`+got+`.body"; echo "Creating issue in acme/shop"; echo "https://github.com/acme/shop/issues/42" ;;
*) exit 2 ;;
esac
`, 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r, _ := newRunner(t, newFakeHost())
	repo := fixture(t, map[string]string{"two": twoSteps})
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:acme/shop.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	key, _ := r.AddRepo(repo)
	st, err := r.GitHub(key.Key)
	if err != nil || !st.Available || st.Repo != "acme/shop" {
		t.Fatalf("status: %+v %v", st, err)
	}
	run := synth(t, r, key.Key, map[string]string{"web/two@a": "fail"}, 1000)
	bugsOf(t, r, run, testtools.Bug{Title: "Broken", Severity: "major", Cells: []string{"web/two@a"}})
	r.merge(run)
	url, err := r.CreateIssue(key.Key, "B-1", "Broken checkout", "It breaks.\nSee run 1.")
	if err != nil || url != "https://github.com/acme/shop/issues/42" {
		t.Fatalf("issue: %q %v", url, err)
	}
	args, _ := os.ReadFile(got + ".args")
	body, _ := os.ReadFile(got + ".body")
	if !strings.Contains(string(args), "--repo acme/shop --title Broken checkout --body-file -") || string(body) != "It breaks.\nSee run 1." {
		t.Fatalf("gh got %q, %q", args, body)
	}
	if b := r.bug(key.Key, "B-1"); b.Issue != url || !strings.Contains(b.Events[len(b.Events)-1].What, "GitHub issue created") {
		t.Fatalf("bug: %+v", b)
	}
	if _, err := r.CreateIssue(key.Key, "B-1", " ", ""); err == nil {
		t.Fatal("an issue without a title")
	}
	for url, slug := range map[string]string{"https://github.com/acme/shop.git": "acme/shop", "https://x:tok@github.com/a/b": "a/b",
		"ssh://git@github.com/a/b.git": "a/b", "https://gitlab.com/a/b": "", "git@github.com:a/b/c": ""} {
		if got := githubSlug(url); got != slug {
			t.Errorf("githubSlug(%q) = %q", url, got)
		}
	}
	// Another origin: no issues, and the answer is kept a minute.
	exec.Command("git", "-C", repo, "remote", "set-url", "origin", "https://gitlab.com/acme/shop").Run()
	if st, _ := r.GitHub(key.Key); !st.Available {
		t.Fatal("the reading was not kept")
	}
	r.mu.Lock()
	delete(r.github, key.Key)
	r.mu.Unlock()
	if st, _ := r.GitHub(key.Key); st.Available || !strings.Contains(st.Reason, "not on GitHub") {
		t.Fatalf("gitlab: %+v", st)
	}
}

// A running cell's live view reads its evidence at most every liveEvery,
// and what it read is still the next judgement's evidence.
func TestTheLiveViewKeepsTheJudgementsEvidence(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	one := strings.Replace(twoSteps, "device: all", "device: [a]", 1)
	run := startRun(t, r, fixture(t, map[string]string{"one": one}), noReview())
	r.schedule()
	waitFor(t, "a session", func() bool { return len(host.chats()) == 1 })
	chat := host.chats()[0]
	waitFor(t, "the cell to run", func() bool { return r.attemptsOf(run.ID)[0].State == AttemptRunning })
	live := func() *Live {
		rows, _ := r.Cells(run.ID)
		return rows[0].Live
	}
	unthrottle := func() {
		r.mu.Lock()
		for k := range r.runs[run.ID].peeked {
			r.runs[run.ID].peeked[k] = time.Time{}
		}
		r.mu.Unlock()
	}
	if l := live(); l == nil || l.Step != 1 || l.Steps != 2 || l.Shot != "" {
		t.Fatalf("before any action: %+v", l)
	}
	unthrottle()
	act(t, r, chat, "open", 1)
	if l := live(); l.Shot != "01-open.png" || !strings.Contains(l.Activity, "open done") || l.At == 0 {
		t.Fatalf("after the first action: %+v", l)
	}
	act(t, r, chat, "scroll", 2)
	if l := live(); l.Shot != "01-open.png" {
		t.Fatalf("read again within %s: %+v", liveEvery, l)
	}
	unthrottle()
	if l := live(); l.Shot != "02-scroll.png" {
		t.Fatalf("read again after %s: %+v", liveEvery, l)
	}
	mustCall(t, r, chat, testtools.Judge, map[string]any{"step": 1, "verdict": "pass", "observed": "the page"})
	act(t, r, chat, "tap", 3)
	unthrottle()
	if l := live(); l.Step != 2 || l.Shot != "03-tap.png" {
		t.Fatalf("on step 2: %+v", l)
	}
	mustCall(t, r, chat, testtools.Judge, map[string]any{"step": 2, "verdict": "issue", "observed": "it works, slowly"})
	a := r.attemptsOf(run.ID)[0]
	if fmt.Sprint(a.Steps[0].Actions) != "[1 2]" || fmt.Sprint(a.Steps[1].Actions) != "[3]" || len(a.Actions) != 3 {
		t.Fatalf("evidence: %v %v of %d", a.Steps[0].Actions, a.Steps[1].Actions, len(a.Actions))
	}
	mustCall(t, r, chat, testtools.End, map[string]any{"summary": "slow"})
	rows, _ := r.Cells(run.ID)
	if rows[0].Live != nil || rows[0].Shot != "03-tap.png" {
		t.Fatalf("ended: live %+v shot %q", rows[0].Live, rows[0].Shot)
	}
}
