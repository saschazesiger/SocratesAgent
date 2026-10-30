//go:build !windows

package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// recorderSink is the adapter's Emitter on a real agent.Recorder, as the
// host has it: what is journaled is what the server keeps, todo times and
// all.
type recorderSink struct{ r *agent.Recorder }

func (s recorderSink) Emit(events ...agent.Event)       { _, _ = s.r.Emit(events...) }
func (s recorderSink) Delta(message, part, text string) { s.r.Delta(message, part, text) }
func (s recorderSink) Exited(error)                     {}

// TestLiveTodoTimes runs the real claude on the cheapest model through tasks
// of several steps and two that have none, and reports the task lists it
// kept - an estimate on (nearly) every task, each one timed, the list
// finished - and what the list cost in calls. No list for a question or a
// single edit: the list is not to cost a turn that does not need it. SOCRATES_CLAUDE_LIVE_TODO picks the tasks
// (comma-separated names below; all by default).
func TestLiveTodoTimes(t *testing.T) {
	e := liveEnv(t)
	tasks := []struct {
		name, prompt string
		wantList     bool
	}{
		{"project", "In the current directory build a tiny Python package: a module `textstats.py` with functions " +
			"word_count, line_count and top_words(n); a pytest file testing each of them; run the tests with " +
			"`python3 -m pytest -q` and fix anything that fails; then write a short README.md describing the " +
			"functions.", true},
		{"waits", "Prepare a release folder here: 1) run `python3 -c 'import time; time.sleep(25)' && echo built > " +
			"build.log` (the build), 2) run `python3 -c 'import time; time.sleep(15)' && echo tested > test.log` " +
			"(the tests), 3) write CHANGELOG.md with one entry for version 0.1.0, 4) pack build.log, test.log and " +
			"CHANGELOG.md into release.tar.gz with tar, 5) list the archive's contents.", true},
		{"bugfix", "Create calc.py with a function median(xs) that returns the median of a list of numbers, " +
			"deliberately written wrong for even-length lists (it returns the upper middle). Write a pytest file " +
			"that covers odd and even lengths and an empty list (ValueError), run it to see the failure, fix " +
			"median, and run the tests again until they pass.", true},
		{"edit", "Create a file hello.txt containing the line `hello`.", false},
		{"question", "What is 17 * 23? Answer with the number only.", false},
	}
	pick := os.Getenv("SOCRATES_CLAUDE_LIVE_TODO")
	for _, task := range tasks {
		if pick != "" && !strings.Contains(","+pick+",", ","+task.name+",") {
			continue
		}
		t.Run(task.name, func(t *testing.T) {
			e := e
			work, err := os.MkdirTemp(e.work, task.name+"-")
			if err != nil {
				t.Fatal(err)
			}
			e.work, e.state = work, filepath.Join(work, ".state")
			j, events, err := agent.OpenJournal(filepath.Join(e.state, "journal.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			rec := agent.NewRecorder(j, events, 0)
			defer rec.Close()
			a := New(Options{})
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			spec := e.spec("")
			spec.Effort = ""
			if err := a.Start(ctx, spec, recorderSink{rec}); err != nil {
				cancel()
				t.Fatalf("start: %v", err)
			}
			cancel()
			defer a.Stop()
			began := time.Now()
			send(t, a, "m1", task.prompt)
			deadline := time.Now().Add(10 * time.Minute)
			for {
				ended := false
				for _, ev := range rec.Feed().After(0) {
					ended = ended || ev.Type == agent.TypeTurnEnded
				}
				if ended {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the turn did not end within 10 minutes")
				}
				time.Sleep(500 * time.Millisecond)
			}
			report(t, rec, began, task.wantList)
		})
	}
}

// report logs the turn's task list as the server keeps it - every version,
// each task with its estimate and its time - and what the turn cost, and
// fails a list that is missing, lacks an estimate or was left unfinished.
func report(t *testing.T, rec *agent.Recorder, began time.Time, wantList bool) {
	t.Helper()
	var versions []json.RawMessage
	taskCalls, otherCalls := 0, 0
	var cost float64
	var usage *agent.Usage
	for _, ev := range rec.Feed().After(0) {
		switch {
		case ev.Type == agent.TypePart && ev.Kind == agent.PartTodo:
			versions = append(versions, ev.Content)
		case ev.Type == agent.TypeTurnEnded:
			cost, usage = ev.CostUSD, ev.Usage
		}
	}
	rec.View(func(p *agent.Projection) {
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Kind == agent.PartTool || part.Kind == agent.PartDiff {
					otherCalls++
				}
			}
		}
	})
	taskCalls = len(versions)
	type task struct {
		Subject   string `json:"subject"`
		Status    string `json:"status"`
		Estimate  int64  `json:"estimate_s"`
		StartedAt int64  `json:"started_at"`
		DoneAt    int64  `json:"done_at"`
	}
	var last []task
	for i, v := range versions {
		var c struct {
			Items []task `json:"items"`
		}
		_ = json.Unmarshal(v, &c)
		line := []string{}
		for _, it := range c.Items {
			line = append(line, fmt.Sprintf("%s[%s]", it.Subject, it.Status[:1]))
		}
		t.Logf("v%d: %s", i+1, strings.Join(line, " | "))
		last = c.Items
	}
	t.Logf("wall %s, cost $%.4f, usage %+v, task-list updates %d, other calls %d",
		time.Since(began).Round(time.Second), cost, usage, taskCalls, otherCalls)
	for _, it := range last {
		took := "-"
		if it.StartedAt > 0 && it.DoneAt >= it.StartedAt {
			took = (time.Duration(it.DoneAt-it.StartedAt) * time.Millisecond).Round(time.Second).String()
		}
		t.Logf("  %-60s %-11s took %-6s estimate %s", it.Subject, it.Status, took, time.Duration(it.Estimate)*time.Second)
	}
	switch {
	case !wantList && len(versions) > 0:
		t.Errorf("a task list for a task without steps (%d updates)", len(versions))
	case wantList && len(last) == 0:
		t.Errorf("no task list")
	case wantList:
		// A model forgets an estimate now and then; the list then shows
		// that task without one. Most tasks must have one.
		missing := 0
		for _, it := range last {
			if it.Estimate == 0 {
				missing++
				t.Logf("no estimate on %q", it.Subject)
			}
			if it.Status != "completed" || it.DoneAt == 0 || it.StartedAt == 0 {
				t.Errorf("%q left %s (started %d, done %d)", it.Subject, it.Status, it.StartedAt, it.DoneAt)
			}
		}
		if missing*4 > len(last) {
			t.Errorf("%d of %d tasks without an estimate", missing, len(last))
		}
	}
}
