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
