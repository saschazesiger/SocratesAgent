//go:build !windows

package claude

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// open is every part of the projection still running or streaming, nested
// ones included.
func open(p *agent.Projection) []*agent.Part {
	var out []*agent.Part
	var walk func(parts []*agent.Part)
	walk = func(parts []*agent.Part) {
		for _, part := range parts {
			if part.State == agent.PartRunning || part.State == agent.PartStreaming {
				out = append(out, part)
			}
			walk(part.Children)
		}
	}
	for _, m := range p.Messages {
		walk(m.Parts)
	}
	return out
}

func kept(a *Adapter) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.st.parts)
}

// A background command that is over before its call's result names it is
// not shown as running for ever.
func TestABackgroundTaskThatEndsBeforeItsResultIsNotLeftRunning(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/bg fast")
	r.turnsEnded(t, 1)
	r.view(func(p *agent.Projection) {
		if o := open(p); len(o) != 0 {
			t.Fatalf("parts still open: %+v", o)
		}
		bash := content(t, partsOf(p.Messages[0], agent.PartTool)[0])
		if bash.Task == nil || bash.Task.Status != "completed" {
			t.Fatalf("background shell = %+v", bash.Task)
		}
		if p.Tasks != 0 {
			t.Fatalf("tasks = %d", p.Tasks)
		}
	})
	if n := kept(a); n != 0 || a.Background() != 0 {
		t.Fatalf("%d parts kept, background %d", n, a.Background())
	}
}

// A task that leaves the CLI's list without a notification closes its
// group: the list is what runs.
func TestATaskGoneFromTheListClosesItsGroup(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/bg lost 0.3")
	r.turnsEnded(t, 1)
	r.waitFor(t, "the shell to be closed", func([]agent.Event) bool {
		closed := false
		r.view(func(p *agent.Projection) {
			c := content(t, partsOf(p.Messages[0], agent.PartTool)[0])
			closed = len(open(p)) == 0 && p.Tasks == 0 && c.Task != nil && c.Task.Status != "running"
		})
		return closed
	})
	r.view(func(p *agent.Projection) {
		part := partsOf(p.Messages[0], agent.PartTool)[0]
		if c := content(t, part); part.State != agent.PartDone || c.Task == nil || c.Task.Status != "completed" {
			t.Fatalf("shell %q %+v", part.State, c.Task)
		}
	})
	if n := kept(a); n != 0 {
		t.Fatalf("%d parts kept", n)
	}
}

// When the CLI goes - stopped here, parked or switched away from alike - the
// background work it ran is over: the workflow, its agents and their calls
// are closed, and no task is counted any more.
func TestTheEndOfTheCLIClosesItsBackgroundWork(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/workflow 30")
	r.turnsEnded(t, 1)
	r.waitFor(t, "an agent's call", func([]agent.Event) bool {
		nested := false
		r.view(func(p *agent.Projection) {
			for _, part := range open(p) {
				if part.ParentID != "" && content(t, part).Tool == "Bash" {
					nested = true
				}
			}
		})
		return nested
	})
	r.view(func(p *agent.Projection) {
		if p.Tasks != 1 || p.Agents != 1 {
			t.Fatalf("while the workflow runs: tasks %d, agents %d", p.Tasks, p.Agents)
		}
	})
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	r.view(func(p *agent.Projection) {
		if o := open(p); len(o) != 0 {
			t.Fatalf("parts still open after the CLI went: %d, first %+v", len(o), o[0])
		}
		if p.Tasks != 0 || p.Agents != 0 {
			t.Fatalf("after the CLI went: tasks %d, agents %d", p.Tasks, p.Agents)
		}
		wf := partsOf(p.Messages[0], agent.PartTool)[0]
		if c := content(t, wf); wf.State != agent.PartInterrupted || c.Workflow.Status != "killed" {
			t.Fatalf("workflow %q %+v", wf.State, c.Workflow)
		}
	})
	if n := kept(a); n != 0 || a.Background() != 0 {
		t.Fatalf("%d parts kept, background %d", n, a.Background())
	}
}

// A notification that comes after the list dropped its task still says how
// the task ended.
func TestALateNotificationCorrectsTheClosedGroup(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/bg late 0.2")
	r.turnsEnded(t, 1)
	r.waitFor(t, "the shell to be killed", func([]agent.Event) bool {
		killed := false
		r.view(func(p *agent.Projection) {
			part := partsOf(p.Messages[0], agent.PartTool)[0]
			c := content(t, part)
			killed = part.State == agent.PartInterrupted && c.Task != nil && c.Task.Status == "killed"
		})
		return killed
	})
	if n := kept(a); n != 0 {
		t.Fatalf("%d parts kept", n)
	}
}

// A CLI that no longer reads its input is ended rather than left to hold up
// whoever writes to it; its exit is reported, so the next host resumes.
func TestACLIThatStopsReadingIsEnded(t *testing.T) {
	was := writeWait
	writeWait = 300 * time.Millisecond
	t.Cleanup(func() { writeWait = was })
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/deaf")
	big := strings.Repeat("x", 1<<20)
	if err := a.Send(context.Background(), agent.Outgoing{MessageID: "m2", ClientID: "c_m2", Text: big}); err == nil {
		t.Fatal("a line nobody reads was taken")
	}
	select {
	case err := <-r.exited:
		if err == nil {
			t.Fatal("the exit has no reason")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stuck CLI was not ended")
	}
}
