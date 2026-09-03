//go:build !windows

package termux

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The ladder is tested without tmux and without a clock.
//
// Every layer sits behind the `source` interface and the `paneLine` struct, so
// a table test can hand the detector any sequence of ticks it likes and assert
// both the committed state and the exact OnActivity calls. What the real tmux
// answers is a different question, and is asked in screen_test.go.

// fakeSource is a scripted exact layer: one answer per tick, and the last one
// repeats once the script runs out.
type fakeSource struct {
	mu      sync.Mutex
	answers []observation
	at      int
}

func (f *fakeSource) Read(context.Context, snapshot) observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.answers) == 0 {
		return missing
	}
	if f.at >= len(f.answers) {
		return f.answers[len(f.answers)-1]
	}
	obs := f.answers[f.at]
	f.at++
	return obs
}

// bench is a Manager with no tmux behind it, a real store, and whatever exact
// layer and screen the test wants.
type bench struct {
	t       *testing.T
	m       *Manager
	st      *store.Store
	dataDir string
	now     time.Time

	mu      sync.Mutex
	changes []ActivityChange
	screen  string
	screenE error
}

func newBench(t *testing.T) *bench {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatalf("could not open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	b := &bench{t: t, st: st, dataDir: dir, now: time.Unix(1788362216, 0)}
	m, err := New(st, Config{
		DataDir:    dir,
		WindowSize: "manual",
		Logf:       func(string, ...any) {},
		OnActivity: func(id string, a Activity) {
			b.mu.Lock()
			b.changes = append(b.changes, ActivityChange{SessionID: id, Activity: a})
			b.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("could not build a manager: %v", err)
	}
	b.m = m
	m.act.capture = func(context.Context, string, int) (string, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.screen, b.screenE
	}
	// NoteInput and MarkRead read the clock themselves, and the ladder's
	// answers are decided by comparing that reading with the tick's.
	m.act.now = func() time.Time {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.now
	}
	return b
}

// sleeper is a child process of this test binary: something for the Shell
// layer's children check to find, and something with no children of its own.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start a child process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

// row makes a running session of one harness, with no pane of its own yet.
func (b *bench) row(id, harness string) store.Session {
	b.t.Helper()
	row := store.Session{
		ID: id, TmuxName: TmuxName(id), Harness: harness,
		Workdir: b.dataDir, State: store.StateRunning,
	}
	if err := b.st.CreateSession(&row); err != nil {
		b.t.Fatalf("could not create a row: %v", err)
	}
	return row
}

// pane is a live pane whose last output was `quiet` ago.
func (b *bench) pane(row store.Session, quiet time.Duration) map[string]paneLine {
	return map[string]paneLine{row.TmuxName: {
		session: row.TmuxName, pid: os.Getpid(),
		activity: b.at().Add(-quiet).Unix(),
	}}
}

// tick runs one tick at the bench's clock and then advances it a second.
func (b *bench) tick(rows []store.Session, panes map[string]paneLine) {
	b.m.act.apply(context.Background(), rows, panes, b.at())
	b.mu.Lock()
	b.now = b.now.Add(ActivityInterval)
	b.mu.Unlock()
}

func (b *bench) at() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.now
}

// until ticks up to n times and stops as soon as the session says what the
// test is waiting for. It is for the assertions about *whether* the ladder
// gets there; the ones about how many ticks it takes count them by hand.
func (b *bench) until(id string, state State, n int, rows []store.Session, panes func() map[string]paneLine) {
	b.t.Helper()
	for i := 0; i < n; i++ {
		b.tick(rows, panes())
		if b.m.ActivityOf(id).State == state {
			return
		}
	}
	b.t.Fatalf("session %s never reached %q in %d ticks; it is %q", id, state, n, b.m.ActivityOf(id).State)
}

func (b *bench) ticks(n int, rows []store.Session, panes func() map[string]paneLine) {
	for i := 0; i < n; i++ {
		b.tick(rows, panes())
	}
}

func (b *bench) setScreen(text string) {
	b.mu.Lock()
	b.screen, b.screenE = text, nil
	b.mu.Unlock()
}

func (b *bench) want(id string, state State) {
	b.t.Helper()
	if got := b.m.ActivityOf(id).State; got != state {
		b.t.Fatalf("session %s is %q, want %q", id, got, state)
	}
}

func (b *bench) states() []State {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]State, 0, len(b.changes))
	for _, change := range b.changes {
		out = append(out, change.Activity.State)
	}
	return out
}

func (b *bench) source(kind harnesses.Kind, answers ...observation) *fakeSource {
	f := &fakeSource{answers: answers}
	b.m.setSource(kind, f)
	return f
}

// TestActivityBusyIsImmediateAndIdleSettles: the spinner has to appear on the
// tick the turn starts, and one stray idle answer between two tool calls must
// not stop it.
func TestActivityBusyIsImmediateAndIdleSettles(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	b.source(harnesses.KindClaude,
		seen(StateBusy, ""),
		seen(StateIdle, ""), // one stray tick between two tool calls
		seen(StateBusy, ""),
		seen(StateBusy, ""),
		seen(StateIdle, ""),
		seen(StateIdle, ""),
	)
	rows := []store.Session{row}

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy) // idle -> busy is immediate

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy) // the stray idle did not commit

	b.ticks(3, rows, func() map[string]paneLine { return b.pane(row, 0) })
	b.want("s1", StateBusy)

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateIdle) // two consecutive idles did

	if got := b.states(); len(got) != 2 || got[0] != StateBusy || got[1] != StateIdle {
		t.Fatalf("the callback saw %v, want exactly [busy idle]", got)
	}
}

// TestActivityLongSilentToolRunStaysBusy: a five minute test suite that prints
// nothing is busy, because the exact layer says so and quiescence never
// overrides an exact answer.
func TestActivityLongSilentToolRunStaysBusy(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	b.source(harnesses.KindClaude, seen(StateBusy, ""))
	rows := []store.Session{row}
	// The screen would say idle, and there has been no output at all.
	b.setScreen("❯ ")

	start := b.at()
	for b.at().Sub(start) < 5*time.Minute {
		b.tick(rows, map[string]paneLine{row.TmuxName: {
			session: row.TmuxName, pid: os.Getpid(), activity: start.Unix(),
		}})
	}
	b.want("s1", StateBusy)
	if got := b.states(); len(got) != 1 {
		t.Fatalf("the callback fired %d times, want once: %v", len(got), got)
	}
}

// TestActivityLadderFallsThroughToTheScreenAndThenToQuiescence: the exact
// layer vanishing must not strand a session, and the layers below have to be
// reached in order.
func TestActivityLadderFallsThroughToTheScreenAndThenToQuiescence(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateBusy, ""), missing)

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy)

	// The file is gone. For three ticks the last exact answer stands.
	b.setScreen("Do you want to proceed?\n 1. Yes\n 2. No")
	for i := 0; i < ExactMissTicks; i++ {
		b.tick(rows, b.pane(row, 0))
		b.want("s1", StateBusy)
	}
	// Then the screen decides, and it says a permission prompt.
	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateWaiting)
	if note := b.m.ActivityOf("s1").Note; note != "permission prompt" {
		t.Fatalf("the note is %q, want the screen's own words", note)
	}
	if src := b.m.ActivityOf("s1").Source; src != sourceScreen {
		t.Fatalf("the answer came from %q, want the screen", src)
	}

	// The screen goes blank - nothing recognised - and only quiescence is
	// left. Waiting is answered first, so a keystroke has to be recorded
	// before silence may release it.
	b.setScreen("")
	b.m.NoteInput("s1")
	b.until("s1", StateIdle, 6, rows, func() map[string]paneLine { return b.pane(row, HardQuiet) })
	if src := b.m.ActivityOf("s1").Source; src != sourceQuiet {
		t.Fatalf("the answer came from %q, want quiescence", src)
	}
}

// TestActivityStuckBusyIsReleasedByQuiescence: the /hang case - busy furniture
// frozen on the screen, no exact signal and no output at all. Thirty seconds
// of silence is idle whatever the screen still shows.
func TestActivityStuckBusyIsReleasedByQuiescence(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateBusy, ""), missing)
	b.setScreen("✻ Cooking… (11s · esc to interrupt)")

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy)

	// The pane's last output recedes into the past while the screen keeps
	// saying "esc to interrupt".
	quiet := time.Duration(0)
	deadline := b.at().Add(HardQuiet + 5*time.Second)
	for b.at().Before(deadline) {
		quiet += ActivityInterval
		b.tick(rows, b.pane(row, quiet))
	}
	b.want("s1", StateIdle)
	if src := b.m.ActivityOf("s1").Source; src != sourceQuiet {
		t.Fatalf("the release came from %q, want quiescence", src)
	}
}

// TestActivityBusyCeilingDropsAStaleExactAnswer: after two minutes with no
// exact answer at all, the remembered one is not evidence any more.
func TestActivityBusyCeilingDropsAStaleExactAnswer(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "codex")
	rows := []store.Session{row}
	src := b.source(harnesses.KindCodex, seen(StateBusy, ""))

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy)

	// The title goes unreadable and stays that way, with the pane still
	// producing output every tick so quiescence has nothing to say.
	src.mu.Lock()
	src.answers, src.at = []observation{missing}, 0
	src.mu.Unlock()
	b.setScreen("Working (3s • esc to interrupt)")

	deadline := b.at().Add(BusyCeiling + 5*time.Second)
	for b.at().Before(deadline) {
		b.tick(rows, b.pane(row, 0))
	}
	// The screen still says busy, so busy it is - but on the screen's
	// authority now, not on a two-minute-old exact answer.
	b.want("s1", StateBusy)
	if src := b.m.ActivityOf("s1").Source; src != sourceScreen {
		t.Fatalf("the answer came from %q, want the screen", src)
	}
	tr := b.m.act.tracks["s1"]
	if !tr.dropped {
		t.Fatal("the exact layer was not dropped after the busy ceiling")
	}
}

// TestActivityWaitingIsStickyAgainstSilence: a permission prompt may sit for
// an hour while the user drives, and silence alone must not blank it.
func TestActivityWaitingIsStickyAgainstSilence(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateWaiting, "permission prompt"), missing)
	b.setScreen("")

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateWaiting)

	// Forty minutes of nothing at all: no signal, no screen, no output.
	quiet := time.Duration(0)
	deadline := b.at().Add(40 * time.Minute)
	for b.at().Before(deadline) {
		quiet += ActivityInterval
		b.tick(rows, b.pane(row, quiet))
	}
	b.want("s1", StateWaiting)

	// The user answers it. Now thirty seconds of silence means the prompt is
	// gone rather than unanswered.
	b.m.NoteInput("s1")
	b.until("s1", StateIdle, 6, rows, func() map[string]paneLine { return b.pane(row, HardQuiet) })
}

// TestActivityWaitingIsReleasedByAScreen: a recognised non-waiting screen is
// evidence the prompt is gone, with or without a keystroke.
func TestActivityWaitingIsReleasedByAScreen(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "codex")
	rows := []store.Session{row}
	b.source(harnesses.KindCodex, seen(StateWaiting, "approval required"), missing)

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateWaiting)

	b.setScreen("Working (3s • esc to interrupt)")
	b.until("s1", StateBusy, ExactMissTicks+2, rows, func() map[string]paneLine { return b.pane(row, 0) })
}

// TestActivityWaitingIsReleasedByAnExactAnswer: the harness's own signal is
// the best evidence there is, so a fresh non-waiting answer ends the prompt at
// once - no keystroke, and with the pane still producing output.
func TestActivityWaitingIsReleasedByAnExactAnswer(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude,
		seen(StateWaiting, "permission prompt"),
		seen(StateIdle, ""),
	)
	b.setScreen("")

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateWaiting)

	// Nobody typed here as far as the detector knows, and the pane is noisy:
	// only the exact layer can release this.
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	if src := b.m.ActivityOf("s1").Source; src != sourceExact {
		t.Fatalf("the release came from %q, want the exact layer", src)
	}
}

// TestActivityRememberedExactAnswerSurvivesSilence: the runaway guard is for a
// signal that has really dropped out, not for one that blinked while the file
// was being rewritten. A busy answer being reused must not be overwritten by
// thirty seconds of quiet.
func TestActivityRememberedExactAnswerSurvivesSilence(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	src := b.source(harnesses.KindClaude, seen(StateBusy, ""))
	b.setScreen("")

	// A long silent tool run: busy on file, and the pane has said nothing for
	// well over HardQuiet.
	b.tick(rows, b.pane(row, HardQuiet))
	b.want("s1", StateBusy)

	// The file is unreadable for the whole reuse window.
	src.mu.Lock()
	src.answers, src.at = []observation{missing}, 0
	src.mu.Unlock()
	for i := 0; i < ExactMissTicks; i++ {
		b.tick(rows, b.pane(row, HardQuiet))
		b.want("s1", StateBusy)
		if got := b.m.ActivityOf("s1").Source; got != sourceExact {
			t.Fatalf("tick %d answered from %q, want the remembered exact answer", i, got)
		}
	}
	// Once it has really gone, silence decides.
	b.until("s1", StateIdle, IdleSettle+2, rows, func() map[string]paneLine { return b.pane(row, HardQuiet) })
	if got := b.m.ActivityOf("s1").Source; got != sourceQuiet {
		t.Fatalf("the release came from %q, want quiescence", got)
	}
}

// TestActivityWaitingIsReleasedByAScreenTheGuardOverwrote: with no exact layer
// and a silent pane, the runaway guard rewrites the answer to a quiet idle -
// but the screen was still read this tick, and a recognised non-waiting screen
// is what ends a prompt.
func TestActivityWaitingIsReleasedByAScreenTheGuardOverwrote(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "codex")
	rows := []store.Session{row}
	b.source(harnesses.KindCodex, seen(StateWaiting, "approval required"), missing)

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateWaiting)

	// The prompt is answered and the pane goes quiet, but the screen is back
	// to Codex's ordinary furniture. Nobody typed at Socrates.
	b.setScreen("Working (3s • esc to interrupt)")
	b.until("s1", StateIdle, ExactMissTicks+IdleSettle+2, rows,
		func() map[string]paneLine { return b.pane(row, HardQuiet) })
}

// TestActivityPaneDeathMarksUnreadAndInputClearsIt.
func TestActivityPaneDeathMarksUnreadAndInputClearsIt(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateBusy, ""))

	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy)
	if b.m.ActivityOf("s1").Unread {
		t.Fatal("a session that is working is not unread")
	}

	dead := map[string]paneLine{row.TmuxName: {session: row.TmuxName, dead: true}}
	b.ticks(UnknownSettle, rows, func() map[string]paneLine { return dead })
	b.want("s1", StateUnknown)
	if !b.m.ActivityOf("s1").Unread {
		t.Fatal("a pane that died mid-turn has to be unread: something ended and nobody saw it")
	}
	if src := b.m.ActivityOf("s1").Source; src != sourcePane {
		t.Fatalf("the answer came from %q, want the pane", src)
	}

	b.m.NoteInput("s1")
	if b.m.ActivityOf("s1").Unread {
		t.Fatal("typing at a session is the whole of having seen it")
	}
}

// TestActivityWaitingMarksUnreadAndMarkReadClearsIt.
func TestActivityWaitingMarksUnreadAndMarkReadClearsIt(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateWaiting, "permission prompt"))

	b.tick(rows, b.pane(row, 0))
	if a := b.m.ActivityOf("s1"); a.State != StateWaiting || !a.Unread {
		t.Fatalf("a prompt has to be unread at once, got %#v", a)
	}
	b.m.MarkRead("s1")
	if b.m.ActivityOf("s1").Unread {
		t.Fatal("an explicit read did not clear the mark")
	}
}

// TestActivityUnreadSurvivesAManagerRebuild: a restart of Socrates must not
// lose "this finished while you were away".
func TestActivityUnreadSurvivesAManagerRebuild(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateBusy, ""), seen(StateIdle, ""))

	b.tick(rows, b.pane(row, 0))
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	if a := b.m.ActivityOf("s1"); !a.Unread {
		t.Fatalf("a finished turn is unread, got %#v", a)
	}

	next, err := New(b.st, Config{DataDir: b.dataDir, WindowSize: "manual", Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("could not rebuild the manager: %v", err)
	}
	next.act.load()
	if a := next.ActivityOf("s1"); a.State != StateUnknown || !a.Unread {
		t.Fatalf("after a rebuild the state is unknown and the mark is kept, got %#v", a)
	}

	// A mark whose session is gone is dropped rather than kept for ever.
	if err := b.st.DeleteSession("s1"); err != nil {
		t.Fatal(err)
	}
	third, err := New(b.st, Config{DataDir: b.dataDir, WindowSize: "manual", Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	third.act.load()
	if third.ActivityOf("s1").Unread {
		t.Fatal("the mark of a deleted session came back")
	}
}

// TestActivityResetKeepsUnreadAndDropsTheState: a restart the user pressed
// starts the detector again, and does not forgive the turn that finished
// before it.
func TestActivityResetKeepsUnreadAndDropsTheState(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateBusy, ""), seen(StateIdle, ""))

	b.tick(rows, b.pane(row, 0))
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	if !b.m.ActivityOf("s1").Unread {
		t.Fatal("the finished turn is unread")
	}

	b.m.ResetActivity("s1")
	a := b.m.ActivityOf("s1")
	if a.State != StateUnknown {
		t.Fatalf("a reset session is %q, want unknown", a.State)
	}
	if !a.Unread {
		t.Fatal("a restart does not clear the unread mark")
	}
}

// TestActivityShellWithAChildIsBusy: `bash script.sh` reports bash as the
// foreground command and would read as idle for its whole run without the
// children check.
func TestActivityShellWithAChildIsBusy(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	// No fake source: this is the real Shell layer.

	// This test process has children only if we make one, so it is its own
	// fixture: a sleep that outlives the assertion.
	sleeper(t)
	self := os.Getpid()

	plan := harnesses.LaunchPlan{Argv: []string{"/bin/bash", "-l"}, Cwd: b.dataDir}
	writePlan(t, b.dataDir, "s1", plan)

	panes := map[string]paneLine{row.TmuxName: {
		session: row.TmuxName, pid: self, command: "bash", activity: b.at().Unix(),
	}}
	b.tick(rows, panes)
	b.want("s1", StateBusy)
	if note := b.m.ActivityOf("s1").Note; note != "bash" {
		t.Fatalf("the note is %q, want the foreground command", note)
	}

	// A foreground command that is not the shell is busy too, whatever the
	// children say.
	panes[row.TmuxName] = paneLine{session: row.TmuxName, pid: 1, command: "sleep", activity: b.at().Unix()}
	b.tick(rows, panes)
	b.want("s1", StateBusy)
}

// TestActivityShellAtItsPromptIsIdle uses a process with no children at all:
// init, which this test can always see and never owns.
func TestActivityShellAtItsPromptIsIdle(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	writePlan(t, b.dataDir, "s1", harnesses.LaunchPlan{Argv: []string{"/bin/sh"}, Cwd: b.dataDir})

	// A process with no children of its own, which is the shape of a shell
	// sitting at its prompt.
	quiet := sleeper(t).Process.Pid
	panes := map[string]paneLine{row.TmuxName: {
		session: row.TmuxName, pid: quiet, command: "sh", activity: b.at().Unix(),
	}}
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return panes })
}

// TestActivityCodexHostnameTitleIsUnavailable: tmux reports the hostname
// before the first OSC title, and "non-empty means idle" would assert idle
// from it.
func TestActivityCodexHostnameTitleIsUnavailable(t *testing.T) {
	plan := harnesses.LaunchPlan{Cwd: "/home/me/SocratesAgent"}
	cases := []struct {
		title string
		want  observation
	}{
		{"", missing},
		{"buildbox", missing},
		{"SocratesAgent", seen(StateIdle, "")},
		{"⠧ SocratesAgent", seen(StateBusy, "")},
		{"[ . ] Action Required | SocratesAgent", seen(StateWaiting, "approval required")},
	}
	for _, c := range cases {
		got := (codexSource{}).Read(context.Background(), snapshot{
			pane: paneLine{title: c.title}, plan: plan,
		})
		if got.ok != c.want.ok || got.state != c.want.state {
			t.Fatalf("title %q read as %#v, want %#v", c.title, got, c.want)
		}
	}
}

// TestActivityClaudeFileOlderThanItsProcessIsIgnored: ~/.claude/sessions is
// never cleaned, so a recycled pid can point at a dead session's file that
// still says busy.
func TestActivityClaudeFileOlderThanItsProcessIsIgnored(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	path := filepath.Join(dir, strconv.Itoa(self)+".json")
	write := func(status string, pid int) {
		raw, _ := json.Marshal(map[string]any{"pid": pid, "status": status})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	src := newClaudeSource()
	snap := snapshot{
		row:  store.Session{ID: "s1"},
		pane: paneLine{pid: self},
		plan: harnesses.LaunchPlan{Env: map[string]string{"HOME": home}},
		now:  time.Now(),
	}

	write("busy", self)
	if got := src.Read(context.Background(), snap); !got.ok || got.state != StateBusy {
		t.Fatalf("a live file read as %#v, want busy", got)
	}

	// The same file, but written before this process started: a leftover from
	// whoever held this pid last.
	started, ok := processStart(self)
	if !ok {
		t.Skip("this platform will not say when a process started")
	}
	if err := os.Chtimes(path, started.Add(-time.Hour), started.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	src = newClaudeSource()
	if got := src.Read(context.Background(), snap); got.ok {
		t.Fatalf("a file older than its process was believed: %#v", got)
	}

	// A file whose own pid disagrees is not this process's either.
	write("busy", self+1)
	src = newClaudeSource()
	if got := src.Read(context.Background(), snap); got.ok {
		t.Fatalf("a file naming another pid was believed: %#v", got)
	}

	write("waiting", self)
	src = newClaudeSource()
	got := src.Read(context.Background(), snap)
	if !got.ok || got.state != StateWaiting {
		t.Fatalf("a waiting file read as %#v", got)
	}
}

// TestActivityClaudeStatusIsFoundOnADescendant: a launcher wrapper means the
// pane's own pid never writes a file, and the answer has to come from the
// process tree below it.
func TestActivityClaudeStatusIsFoundOnADescendant(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	child := sleeper(t).Process.Pid
	var seenChild bool
	for _, pid := range descendants(os.Getpid(), 3, 64) {
		if pid == child {
			seenChild = true
		}
	}
	if !seenChild {
		t.Skip("this platform will not list a process's children")
	}

	path := filepath.Join(dir, strconv.Itoa(child)+".json")
	write := func(status string) {
		raw, _ := json.Marshal(map[string]any{"pid": child, "status": status})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The pane pid is the wrapper's, and it has written nothing of its own.
	src := newClaudeSource()
	snap := snapshot{
		row:  store.Session{ID: "s1"},
		pane: paneLine{pid: os.Getpid()},
		plan: harnesses.LaunchPlan{Env: map[string]string{"HOME": home}},
		now:  time.Now(),
	}

	write("busy")
	if got := src.Read(context.Background(), snap); !got.ok || got.state != StateBusy {
		t.Fatalf("the descendant's file read as %#v, want busy", got)
	}
	// The pid is cached now, so the second read must follow the same process
	// without walking the tree again.
	write("waiting")
	got := src.Read(context.Background(), snap)
	if !got.ok || got.state != StateWaiting {
		t.Fatalf("the remembered descendant read as %#v, want waiting", got)
	}
	// A different pane is a different process: the cache is keyed by it.
	src.mu.Lock()
	cached := src.cache["s1"]
	src.mu.Unlock()
	if cached.pid != child || cached.panePID != os.Getpid() {
		t.Fatalf("the cache holds %+v, want pid %d under pane %d", cached, child, os.Getpid())
	}
}

// TestActivityOpenCodeBusySetKeepsBusy: a parent session and its sub-agents
// all emit session.status, and an idle for one of them must not clear
// another's busy.
func TestActivityOpenCodeBusySetKeepsBusy(t *testing.T) {
	w := &openCodeWatcher{busy: map[string]bool{}, waiting: map[string]openCodePrompt{}}
	event := func(id, kind string) {
		w.event(fmt.Sprintf(`{"type":"session.status","properties":{"sessionID":%q,"status":{"type":%q}}}`, id, kind))
	}
	if got := w.answer(); got.ok {
		t.Fatalf("a stream that has said nothing answered %#v", got)
	}
	event("ses_a", "busy")
	event("ses_b", "busy")
	event("ses_a", "idle")
	if got := w.answer(); !got.ok || got.state != StateBusy {
		t.Fatalf("two busy sessions and one idle read as %#v, want busy", got)
	}
	event("ses_b", "idle")
	if got := w.answer(); !got.ok || got.state != StateIdle {
		t.Fatalf("an empty busy set read as %#v, want idle", got)
	}

	// A retry is a busy, and a permission outranks both.
	event("ses_a", "retry")
	if got := w.answer(); got.state != StateBusy {
		t.Fatalf("a retry read as %#v, want busy", got)
	}
	w.event(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_a"}}`)
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("an asked permission read as %#v, want waiting", got)
	}
	w.event(`{"type":"permission.replied","properties":{"requestID":"per_2","sessionID":"ses_a"}}`)
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("another request's reply cleared this one: %#v", got)
	}
	w.event(`{"type":"permission.replied","properties":{"requestID":"per_1","sessionID":"ses_a"}}`)
	if got := w.answer(); got.state != StateBusy {
		t.Fatalf("the matching reply left %#v, want the busy underneath", got)
	}
}

// TestActivityOpenCodePermissionEvents pins the event names OpenCode 1.17.13
// really emits - `permission.asked` and `permission.replied`, not the
// `permission.v2.*` an earlier pass recorded, which is why waiting was never
// reported at all - and the arithmetic of the waiting set around them.
func TestActivityOpenCodePermissionEvents(t *testing.T) {
	fresh := func() *openCodeWatcher {
		return &openCodeWatcher{busy: map[string]bool{}, waiting: map[string]openCodePrompt{}, ok: true}
	}
	asked := func(name, id string) string {
		return fmt.Sprintf(`{"type":%q,"properties":{"id":%q,"sessionID":"ses_a","permission":"bash","patterns":["echo hello"]}}`, name, id)
	}
	replied := func(name, id string) string {
		return fmt.Sprintf(`{"type":%q,"properties":{"sessionID":"ses_a","requestID":%q,"reply":"once"}}`, name, id)
	}

	// The real name, on an otherwise idle session.
	w := fresh()
	w.event(asked("permission.asked", "per_1"))
	if got := w.answer(); got.state != StateWaiting || got.note != "permission prompt" {
		t.Fatalf("permission.asked read as %#v, want waiting", got)
	}

	// A prompt sits on a session the status map calls busy - which is exactly
	// what the real server reports while a prompt is open - and waiting has to
	// beat it.
	w = fresh()
	w.event(`{"type":"session.status","properties":{"sessionID":"ses_a","status":{"type":"busy"}}}`)
	w.event(asked("permission.asked", "per_1"))
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("a prompt on a busy session read as %#v, want waiting", got)
	}
	// The reply hands the session back to the busy underneath it, not to idle.
	w.event(replied("permission.replied", "per_1"))
	if got := w.answer(); got.state != StateBusy {
		t.Fatalf("the answered prompt left %#v, want busy", got)
	}

	// A reply nobody can match is still a reply: the prompts are gone.
	w = fresh()
	w.event(asked("permission.asked", "per_1"))
	w.event(asked("permission.asked", "per_2"))
	w.event(replied("permission.replied", ""))
	if got := w.answer(); got.state != StateIdle {
		t.Fatalf("an unmatched reply left %#v, want every prompt cleared", got)
	}

	// Two prompts, one answered: the other one still holds the session.
	w = fresh()
	w.event(asked("permission.asked", "per_1"))
	w.event(asked("permission.asked", "per_2"))
	w.event(replied("permission.replied", "per_1"))
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("one of two prompts answered read as %#v, want waiting", got)
	}

	// The legacy spelling is still understood, both halves of it.
	w = fresh()
	w.event(asked("permission.v2.asked", "per_1"))
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("the legacy permission.v2.asked read as %#v, want waiting", got)
	}
	w.event(replied("permission.v2.replied", "per_1"))
	if got := w.answer(); got.state != StateIdle {
		t.Fatalf("the legacy permission.v2.replied left %#v, want idle", got)
	}
}

// TestActivityTmuxUnreachableTouchesNothing: unknown settles only when the
// command succeeded and the session was absent, never on an error.
func TestActivityTmuxUnreachableTouchesNothing(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateBusy, ""))
	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy)

	// The tick that could not ask does not call apply at all, which is what
	// tick() does on an error; the state is simply left alone.
	for i := 0; i < 10; i++ {
		b.mu.Lock()
		b.now = b.now.Add(ActivityInterval)
		b.mu.Unlock()
	}
	b.want("s1", StateBusy)
}

// TestActivityEchoedKeystrokesDoNotFlipAnIdleSession: typing at an idle prompt
// updates #{window_activity}, so quiescence sees "busy" for two seconds. The
// exact layer outranks it, and where there is none the settle absorbs it.
func TestActivityEchoedKeystrokesDoNotFlipAnIdleSession(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	writePlan(t, b.dataDir, "s1", harnesses.LaunchPlan{Argv: []string{"/bin/sh"}, Cwd: b.dataDir})
	quiet := sleeper(t).Process.Pid
	panes := map[string]paneLine{row.TmuxName: {
		session: row.TmuxName, pid: quiet, command: "sh", activity: b.at().Unix(),
	}}
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return panes })

	// One keystroke echoed back: the pane produced output this very second.
	panes[row.TmuxName] = paneLine{session: row.TmuxName, pid: quiet, command: "sh", activity: b.at().Unix()}
	b.tick(rows, panes)
	b.want("s1", StateIdle)
}

// TestActivityListSkipsSessionsThatAreNotRunning.
func TestActivityListSkipsSessionsThatAreNotRunning(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	b.source(harnesses.KindClaude, seen(StateBusy, ""))
	b.tick([]store.Session{row}, b.pane(row, 0))
	if len(b.m.Activities()) != 1 {
		t.Fatalf("a running session is not in the map: %#v", b.m.Activities())
	}

	row.State = store.StateExited
	gone := map[string]paneLine{}
	b.ticks(UnknownSettle, []store.Session{row}, func() map[string]paneLine { return gone })
	if got := b.m.Activities(); len(got) != 0 {
		t.Fatalf("an exited session is still in the map: %#v", got)
	}
	if !b.m.ActivityOf("s1").Unread {
		t.Fatal("a session that ended mid-turn is unread")
	}
}

// TestActivityWaitIdleAnswersOnTheChange is what the operator loop waits on.
func TestActivityWaitIdleAnswersOnTheChange(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	src := b.source(harnesses.KindClaude, seen(StateBusy, ""))
	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy)

	done := make(chan Activity, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a, err := b.m.WaitIdle(ctx, "s1")
		if err != nil {
			t.Errorf("WaitIdle: %v", err)
		}
		done <- a
	}()

	// Give the waiter a moment to subscribe, then finish the turn.
	time.Sleep(50 * time.Millisecond)
	src.mu.Lock()
	src.answers, src.at = []observation{seen(StateIdle, "")}, 0
	src.mu.Unlock()
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })

	select {
	case a := <-done:
		if a.State != StateIdle {
			t.Fatalf("WaitIdle answered %q", a.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle never returned")
	}
}

// TestActivitySubscriberStopRacesTheTick: WaitIdle unsubscribes on every
// return, and the return it makes most often is the one a tick has just caused
// - so the stop and the publish run at the same instant routinely. Closing a
// channel the tick is sending on would panic the tick goroutine and take the
// server with it.
func TestActivitySubscriberStopRacesTheTick(t *testing.T) {
	a := newBench(t).m.act
	changes := []ActivityChange{{SessionID: "s1", Activity: Activity{State: StateIdle}}}
	for i := 0; i < 20000; i++ {
		ch, stop := a.m.SubscribeActivity()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); a.fire(changes) }()
		go func() { defer wg.Done(); stop() }()
		wg.Wait()
		for range ch {
		}
	}
}

// TestParseActivityPanesKeepsACodexTitleWhole: the approval title contains a
// pipe, which is why the title is the last field.
func TestParseActivityPanesKeepsACodexTitleWhole(t *testing.T) {
	out := strings.Join([]string{
		"soc_a|0|3412|1788362216|node|[ . ] Action Required | SocratesAgent",
		"soc_b|1|0|1788362100|bash|buildbox",
		"soc_c|0|77|1788362200|codex|⠧ SocratesAgent",
	}, "\n")
	panes := parseActivityPanes(out)
	if len(panes) != 3 {
		t.Fatalf("parsed %d panes, want 3", len(panes))
	}
	a := panes["soc_a"]
	if a.title != "[ . ] Action Required | SocratesAgent" {
		t.Fatalf("the title was cut: %q", a.title)
	}
	if a.pid != 3412 || a.activity != 1788362216 || a.command != "node" || a.dead {
		t.Fatalf("soc_a parsed as %#v", a)
	}
	if !panes["soc_b"].dead {
		t.Fatal("a dead pane parsed as alive")
	}
	got := (codexSource{}).Read(context.Background(), snapshot{
		pane: a, plan: harnesses.LaunchPlan{Cwd: "/x/SocratesAgent"},
	})
	if got.state != StateWaiting {
		t.Fatalf("the approval title read as %#v", got)
	}
}

// TestScrapeScreens runs each harness's patterns against the verbatim screens
// from the research file.
func TestScrapeScreens(t *testing.T) {
	cases := []struct {
		name   string
		kind   harnesses.Kind
		screen string
		want   State
		ok     bool
	}{
		{"claude busy", harnesses.KindClaude,
			"✻ Cooking… (11s · thinking with high effort)\n  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents",
			StateBusy, true},
		{"claude waiting", harnesses.KindClaude,
			" Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, allow reading from /tmp from this project\n   4. No\n Esc to cancel · Tab to amend",
			StateWaiting, true},
		{"claude idle", harnesses.KindClaude,
			"──────────────\n❯ \n──────────────\n  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
			StateIdle, true},
		// The screen a real `claude` paints with no auto mode and nothing
		// typed: a placeholder in the prompt line and no `⏵⏵` anywhere.
		{"claude idle in manual mode", harnesses.KindClaude,
			"────────────\n❯ Try \"fix typecheck errors\"\n────────────\n  ⏸ manual mode on · ? for shortcuts · ← for agents",
			StateIdle, true},
		{"claude nothing", harnesses.KindClaude, "just some output\n", StateUnknown, false},

		{"codex busy", harnesses.KindCodex, "Working (3s • esc to interrupt)", StateBusy, true},
		{"codex waiting edits", harnesses.KindCodex,
			"  Would you like to make the following edits?\n\n  › 1. Yes, proceed (y)\n\n  Press enter to confirm or esc to cancel",
			StateWaiting, true},
		{"codex waiting update", harnesses.KindCodex,
			"✨ Update available! 0.1 -> 0.2\nPress enter to continue", StateWaiting, true},
		{"codex idle", harnesses.KindCodex, "› Ask Codex to do anything", StateIdle, true},

		{"opencode busy", harnesses.KindOpenCode,
			"⠏ Thinking\n  ⬝⬝⬝⬝⬝⬝■■  esc interrupt                          tab agents  ctrl+p commands",
			StateBusy, true},
		{"opencode idle", harnesses.KindOpenCode,
			"▣  Build · Glm 5.3 · 39.8s\n  ⬝⬝⬝⬝⬝⬝  10.5K (1%) · $0.02   ctrl+p commands",
			StateIdle, true},
		{"opencode nothing", harnesses.KindOpenCode, "Ask anything...", StateUnknown, false},
		// The permission card can keep the bottom bar around it, `ctrl+p
		// commands` and all, so waiting has to be read before idle.
		{"opencode waiting", harnesses.KindOpenCode,
			"  ┃  △ Permission required\n  ┃    # Shell command\n  ┃  $ echo hello-from-bash\n" +
				"  ┃   Allow once   Allow always   Reject          ctrl+f fullscreen  ⇆ select  enter confirm\n" +
				"  ⬝⬝⬝⬝⬝⬝  10.5K (1%) · $0.02   ctrl+p commands",
			StateWaiting, true},
		// The verbatim card at the two narrowest widths that were captured
		// from a real OpenCode 1.17.13 (`card_45x20`, `card_60x20`). These are
		// the rows that prove the heading alternative is the one carrying
		// narrow terminals: below about 80 columns the hint row wraps onto its
		// own `┃`-prefixed line, so `Reject … ctrl+f` is no longer one line and
		// only `△ Permission required` still matches.
		{"opencode waiting at 60 columns", harnesses.KindOpenCode,
			"  ┃                                                         \n" +
				"  ┃  run `echo hello-from-bash` using the bash tool         \n" +
				"  ┃                                                         \n" +
				"                                                            \n" +
				"     $ echo hello-from-bash                                 \n" +
				"                                                            \n" +
				"     ▣  Build · Anthropic Claude Haiku Latest               \n" +
				"                                                            \n" +
				"  ┃                                                         \n" +
				"  ┃  △ Permission required                                  \n" +
				"  ┃    # Shell command                                      \n" +
				"  ┃                                                         \n" +
				"  ┃  $ echo hello-from-bash                                 \n" +
				"  ┃                                                         \n" +
				"  ┃                                                         \n" +
				"  ┃   Allow once   Allow always   Reject                    \n" +
				"  ┃                                                         \n" +
				"  ┃  ctrl+f fullscreen  ⇆ select  enter confirm             \n" +
				"  ┃                                                         \n" +
				"                                                            ",
			StateWaiting, true},
		{"opencode waiting at 45 columns", harnesses.KindOpenCode,
			"                                             \n" +
				"     I'll run that bash command for you.     \n" +
				"                                             \n" +
				"     $ echo hello-from-bash                  \n" +
				"                                             \n" +
				"     ▣  Build · Anthropic Claude Haiku       \n" +
				"     Latest                                  \n" +
				"                                             \n" +
				"  ┃                                          \n" +
				"  ┃  △ Permission required                   \n" +
				"  ┃    # Shell command                       \n" +
				"  ┃                                          \n" +
				"  ┃  $ echo hello-from-bash                  \n" +
				"  ┃                                          \n" +
				"  ┃                                          \n" +
				"  ┃   Allow once   Allow always   Reject     \n" +
				"  ┃                                          \n" +
				"  ┃  ctrl+f fullscreen  ⇆ select  enter confi\n" +
				"  ┃                                          \n" +
				"                                             ",
			StateWaiting, true},
		{"opencode waiting on a busy screen", harnesses.KindOpenCode,
			"⠏ Thinking   esc interrupt  ctrl+p commands\n" +
				"  ┃   Allow once   Allow always   Reject          ctrl+f fullscreen  ⇆ select  enter confirm",
			StateWaiting, true},

		{"shell never scrapes", harnesses.KindShell, "esc to interrupt", StateUnknown, false},
	}
	for _, c := range cases {
		got := scrapeScreen(c.kind, c.screen)
		if got.ok != c.ok || (c.ok && got.state != c.want) {
			t.Fatalf("%s: read as %#v, want state %q ok %t", c.name, got, c.want, c.ok)
		}
	}
	// OpenCode's busy literal is not Claude's: a shared regex would answer
	// busy for an idle OpenCode bottom bar and vice versa.
	if scrapeScreen(harnesses.KindOpenCode, "esc to interrupt").ok {
		t.Fatal("OpenCode answered Claude's busy literal")
	}
	// And a plain busy bar is not a prompt: the waiting pattern is the card's
	// own two rows, not anything the bottom bar carries.
	busyBar := "⠏ Thinking   ⬝⬝⬝⬝⬝⬝■■  esc interrupt      tab agents  ctrl+p commands"
	if got := scrapeScreen(harnesses.KindOpenCode, busyBar); got.state == StateWaiting {
		t.Fatalf("a busy OpenCode bar read as %#v, want busy", got)
	}
	// The words alone are not the card. `capture-pane -S -40` reads the
	// scrollback as well as the pane, and the composer paints the same `┃`
	// gutter the card does, so the only thing that separates a real prompt
	// from somebody typing about one is the card's own furniture: the `△`
	// heading it and the `ctrl+f` hint closing its button row. Both of these
	// were captured from a real OpenCode 1.17.13.
	notCards := map[string]string{
		"a transcript quoting the words": "  ⏺ The tool call failed because Permission required was\n" +
			"    returned by the sandbox; retrying without it.\n" +
			"  ⬝⬝⬝⬝⬝⬝  10.5K (1%) · $0.02   ctrl+p commands",
		"the words typed into the composer": "  ┃\n  ┃  Permission required\n  ┃\n" +
			"  ┃  Build · Anthropic Claude Haiku Latest OpenRouter\n" +
			"  ╹━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
			"                     tab agents  ctrl+p commands",
	}
	for name, screen := range notCards {
		if got := scrapeScreen(harnesses.KindOpenCode, screen); got.state == StateWaiting {
			t.Errorf("%s scraped as %#v, want anything but waiting", name, got)
		}
	}
}

// TestQuiescence is the one layer every harness has.
func TestQuiescence(t *testing.T) {
	now := time.Unix(1788362216, 0)
	cases := []struct {
		quiet time.Duration
		want  State
		ok    bool
	}{
		{0, StateBusy, true},
		{QuietBusy, StateBusy, true},
		{QuietBusy + time.Second, StateUnknown, false},
		{HardQuiet - time.Second, StateUnknown, false},
		{HardQuiet, StateIdle, true},
		{time.Hour, StateIdle, true},
	}
	for _, c := range cases {
		got := quiescence(paneLine{activity: now.Add(-c.quiet).Unix()}, now)
		if got.ok != c.ok || (c.ok && got.state != c.want) {
			t.Fatalf("%s of silence read as %#v, want %q ok %t", c.quiet, got, c.want, c.ok)
		}
	}
	if quiescence(paneLine{}, now).ok {
		t.Fatal("a pane with no activity timestamp answered")
	}
}

// ---------------------------------------------------------------- fixtures

func writePlan(t *testing.T, dataDir, id string, plan harnesses.LaunchPlan) {
	t.Helper()
	dir := SessionDir(dataDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOpenCodeWatcherSeedsThenStreams runs the watcher against a server that
// speaks what OpenCode speaks: the status map it seeds from, and the stream of
// deltas after it. The seed matters because the stream carries deltas only, so
// a change during the gap would otherwise be missed for ever.
func TestOpenCodeWatcherSeedsThenStreams(t *testing.T) {
	events := make(chan string, 8)
	var asked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "opencode" || pass != "shh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/session/status":
			if r.URL.Query().Get("directory") == "" {
				t.Error("the status poll carried no directory")
			}
			if asked.Load() {
				_, _ = io.WriteString(w, `{}`)
				return
			}
			_, _ = io.WriteString(w, `{"ses_seed":{"type":"busy"}}`)
		case "/event":
			flusher := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
			flusher.Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case event := <-events:
					_, _ = io.WriteString(w, "data: "+event+"\n\n")
					flusher.Flush()
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	port, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Skipf("the test server is not on a loopback port: %s", srv.URL)
	}
	w := startOpenCodeWatcher(
		harnesses.ServerAccess{Port: port, Username: "opencode", Password: "shh"}, "/tmp/work")
	defer w.stop()

	// The seed alone is enough to answer, before a single event has arrived.
	waitFor(t, 10*time.Second, "the seeded busy session", func() bool {
		got := w.answer()
		return got.ok && got.state == StateBusy
	})

	asked.Store(true)
	events <- `{"type":"session.status","properties":{"sessionID":"ses_seed","status":{"type":"idle"}}}`
	waitFor(t, 10*time.Second, "the stream to clear the busy set", func() bool {
		got := w.answer()
		return got.ok && got.state == StateIdle
	})

	events <- `{"type":"permission.asked","properties":{"id":"per_9","sessionID":"ses_seed"}}`
	waitFor(t, 10*time.Second, "a permission prompt", func() bool {
		return w.answer().state == StateWaiting
	})
	events <- `{"type":"permission.replied","properties":{"requestID":"per_9","sessionID":"ses_seed"}}`
	waitFor(t, 10*time.Second, "the prompt to be answered", func() bool {
		return w.answer().state == StateIdle
	})
}

// TestOpenCodeWatcherSeedsPendingPermissions: a prompt raised while nothing
// was listening is on GET /permission, and the session it belongs to is `busy`
// in the status map for as long as it stands. Without the second seed that
// session reads as work in progress for ever, which is the bug this pins.
func TestOpenCodeWatcherSeedsPendingPermissions(t *testing.T) {
	events := make(chan string, 8)
	var answered atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			// What the real server says while a prompt is open.
			_, _ = io.WriteString(w, `{"ses_p":{"type":"busy"}}`)
		case "/permission":
			if r.URL.Query().Get("directory") == "" {
				t.Error("the permission poll carried no directory")
			}
			if answered.Load() {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			_, _ = io.WriteString(w, `[{"id":"per_seed","sessionID":"ses_p","permission":"bash"}]`)
		case "/event":
			flusher := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			for {
				select {
				case <-r.Context().Done():
					return
				case event := <-events:
					_, _ = io.WriteString(w, "data: "+event+"\n\n")
					flusher.Flush()
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	port, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Skipf("the test server is not on a loopback port: %s", srv.URL)
	}
	w := startOpenCodeWatcher(harnesses.ServerAccess{Port: port}, "/tmp/work")
	defer w.stop()

	// The seed alone, before one event has arrived: waiting, not the busy the
	// status map is reporting.
	waitFor(t, 10*time.Second, "the seeded permission prompt", func() bool {
		got := w.answer()
		return got.ok && got.state == StateWaiting
	})

	answered.Store(true)
	events <- `{"type":"permission.replied","properties":{"requestID":"per_seed","sessionID":"ses_p"}}`
	waitFor(t, 10*time.Second, "the stream to clear the prompt", func() bool {
		got := w.answer()
		return got.ok && got.state == StateBusy
	})
}

// TestOpenCodeWatcherClearsAPromptAnsweredWhileTheStreamWasDown: the reply
// event is the only thing that retires a prompt on a live stream, and a
// stream that is down carries no events at all. The prompt answered in the gap
// has to come back off the row on the reconnect poll, or the session sits on
// `waiting` until it is killed.
func TestOpenCodeWatcherClearsAPromptAnsweredWhileTheStreamWasDown(t *testing.T) {
	var answered, streamTries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			// The session holding the prompt is busy the whole way through,
			// so busy is exactly what must be left once the prompt is gone.
			_, _ = io.WriteString(w, `{"ses_gap":{"type":"busy"}}`)
		case "/permission":
			if answered.Load() > 0 {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			_, _ = io.WriteString(w, `[{"id":"per_gap","sessionID":"ses_gap"}]`)
		case "/event":
			// The first connection is refused: nothing can be streamed, and
			// the poll is the only witness there is.
			if streamTries.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			flusher := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// A short reconcile period as well as the poll: the prompt has to come
	// off the row whichever of the two re-reads /permission first, and
	// relying on the poll inside the backoff window alone is a race.
	w := startOpenCodeWatcherEvery(harnesses.ServerAccess{Port: portOf(t, srv)}, "/tmp/work", 50*time.Millisecond)
	defer w.stop()
	waitFor(t, 10*time.Second, "the prompt raised while nothing was listening", func() bool {
		got := w.answer()
		return got.ok && got.state == StateWaiting
	})

	// Answered on the server, with no event anybody could have seen.
	answered.Store(1)
	waitFor(t, 10*time.Second, "the poll to retire the answered prompt", func() bool {
		got := w.answer()
		return got.ok && got.state == StateBusy
	})
}

// TestOpenCodeWatcherReconcilesWhileTheStreamIsUp: a prompt can go without a
// reply event anybody can match — the session aborted or deleted, a frame
// lost, a `requestID` that is not the id the prompt was keyed by. The periodic
// re-read of GET /permission is what catches all of those, so this answers the
// prompt behind the stream's back and expects the row to come back anyway.
func TestOpenCodeWatcherReconcilesWhileTheStreamIsUp(t *testing.T) {
	var answered atomic.Int32
	var opened atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			_, _ = io.WriteString(w, `{"ses_r":{"type":"busy"}}`)
		case "/permission":
			if answered.Load() > 0 {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			_, _ = io.WriteString(w, `[{"id":"per_r","sessionID":"ses_r"}]`)
		case "/event":
			flusher := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			opened.Add(1)
			// A stream that stays open for the whole test: the poll never
			// runs again, and only the reconcile can clear anything.
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	w := startOpenCodeWatcherEvery(harnesses.ServerAccess{Port: portOf(t, srv)}, "/tmp/work", 50*time.Millisecond)
	defer w.stop()
	waitFor(t, 10*time.Second, "the stream to be up over a standing prompt", func() bool {
		return opened.Load() > 0 && w.answer().state == StateWaiting
	})

	answered.Store(1)
	waitFor(t, 10*time.Second, "the reconcile to retire the prompt", func() bool {
		got := w.answer()
		return got.ok && got.state == StateBusy
	})
	if opened.Load() != 1 {
		t.Fatalf("the stream reconnected %d times; the reconcile is meant to run underneath one", opened.Load())
	}
}

// TestOpenCodeWatcherPromptRulesWithoutTheEndpoint covers what retires a
// prompt when GET /permission is not answering: the session going quiet, and
// the endpoint failing often enough that no prompt can be confirmed at all.
func TestOpenCodeWatcherPromptRulesWithoutTheEndpoint(t *testing.T) {
	fresh := func() *openCodeWatcher {
		return &openCodeWatcher{busy: map[string]bool{}, waiting: map[string]openCodePrompt{}, ok: true}
	}

	// A session holding a prompt reports busy for as long as it holds it, so
	// a status of anything else is proof the prompt is gone — even though the
	// reply that would have said so never arrived.
	w := fresh()
	w.event(`{"type":"session.status","properties":{"sessionID":"ses_a","status":{"type":"busy"}}}`)
	w.event(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_a"}}`)
	w.event(`{"type":"permission.asked","properties":{"id":"per_2","sessionID":"ses_b"}}`)
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("two open prompts read as %#v, want waiting", got)
	}
	w.event(`{"type":"session.status","properties":{"sessionID":"ses_a","status":{"type":"idle"}}}`)
	w.mu.Lock()
	_, stillA := w.waiting["per_1"]
	_, stillB := w.waiting["per_2"]
	w.mu.Unlock()
	if stillA {
		t.Error("the idle session kept its prompt")
	}
	if !stillB {
		t.Error("one session going idle took another session's prompt with it")
	}

	// Only the idle OpenCode spells out is that proof. A status type this
	// build does not know leaves the busy set — it is not busy — but says
	// nothing about the prompt, which stands until something does.
	w = fresh()
	w.event(`{"type":"session.status","properties":{"sessionID":"ses_a","status":{"type":"busy"}}}`)
	w.event(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_a"}}`)
	w.event(`{"type":"session.status","properties":{"sessionID":"ses_a","status":{"type":"queued"}}}`)
	w.mu.Lock()
	_, keptUnknown := w.waiting["per_1"]
	stillBusy := w.busy["ses_a"]
	w.mu.Unlock()
	if !keptUnknown {
		t.Error("an unrecognised session.status type retired a prompt as if it were idle")
	}
	if stillBusy {
		t.Error("an unrecognised session.status type left the session in the busy set")
	}

	// A failing endpoint is not an answer: one failure must not clear a real
	// prompt, and enough of them must not leave one standing for ever.
	w = fresh()
	w.event(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_a"}}`)
	w.mu.Lock()
	for i := 0; i < openCodePermissionFails-1; i++ {
		w.applyPermissionsLocked(nil, openCodePermissionFailed, time.Now())
	}
	kept := len(w.waiting)
	w.applyPermissionsLocked(nil, openCodePermissionFailed, time.Now())
	gone := len(w.waiting)
	w.mu.Unlock()
	if kept != 1 {
		t.Fatalf("%d failures dropped the prompt already", openCodePermissionFails-1)
	}
	if gone != 0 {
		t.Fatalf("%d failures left %d unconfirmable prompts", openCodePermissionFails, gone)
	}

	// And one good answer both replaces the set and forgives the failures.
	w = fresh()
	w.mu.Lock()
	w.applyPermissionsLocked(nil, openCodePermissionFailed, time.Now())
	w.applyPermissionsLocked(map[string]openCodePrompt{
		"per_9": {session: "ses_z", seen: time.Now(), confirmed: true}}, openCodePermissionOK, time.Now())
	fails := w.permFails
	w.mu.Unlock()
	if fails != 0 {
		t.Fatalf("a good answer left %d failures counted", fails)
	}
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("the endpoint's own prompt read as %#v, want waiting", got)
	}
}

// portOf is the loopback port of a test server, which is what the watcher
// takes instead of a URL.
func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	port, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Skipf("the test server is not on a loopback port: %s", srv.URL)
	}
	return port
}

// TestOpenCodeWatcherSaysNothingWhenTheServerIsGone: a port that refuses is
// not an idle session, it is no answer at all, and the ladder falls through.
func TestOpenCodeWatcherSaysNothingWhenTheServerIsGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	port, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Skipf("the test server is not on a loopback port: %s", srv.URL)
	}
	srv.Close()

	w := startOpenCodeWatcher(harnesses.ServerAccess{Port: port}, "/tmp/work")
	defer w.stop()
	time.Sleep(200 * time.Millisecond)
	if got := w.answer(); got.ok {
		t.Fatalf("a refused port answered %#v", got)
	}
}

// TestOpenCodeWatcherKeepsAPromptAskedDuringAnInFlightReconcile: the answer to
// GET /permission describes the moment the request was computed, not the
// moment it lands. A prompt whose event arrived while it was in flight is
// newer than the answer, so the authoritative replace must carry it over —
// without that, every prompt raised during a reconcile is lost for a whole
// interval, which is exactly the window a prompt is most likely to appear in.
func TestOpenCodeWatcherKeepsAPromptAskedDuringAnInFlightReconcile(t *testing.T) {
	var calls atomic.Int32
	inFlight := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			_, _ = io.WriteString(w, `{"ses_f":{"type":"busy"}}`)
		case "/permission":
			switch calls.Add(1) {
			case 1:
				// The seed poll: nothing open yet.
				_, _ = io.WriteString(w, `[]`)
			case 2:
				// The reconcile the test drives: held open until the event
				// has been fed, then answering with a set that does not
				// contain it — because the server computed it before.
				inFlight <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, `[{"id":"per_other","sessionID":"ses_f"}]`)
			default:
				// Every later read hangs, so nothing but the one answer
				// under test can touch the set.
				<-r.Context().Done()
			}
		case "/event":
			flusher := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	w := startOpenCodeWatcherEvery(harnesses.ServerAccess{Port: portOf(t, srv)}, "/tmp/work", 50*time.Millisecond)
	defer w.stop()
	select {
	case <-inFlight:
	case <-time.After(10 * time.Second):
		t.Fatal("the reconcile never reached the permission endpoint")
	}
	// Asked while that request is on the wire.
	w.event(`{"type":"permission.asked","properties":{"id":"per_race","sessionID":"ses_f"}}`)
	close(release)

	// The answer lands and replaces the set: `per_other` is proof it did.
	waitFor(t, 10*time.Second, "the reconcile's own prompt", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		_, ok := w.waiting["per_other"]
		return ok
	})
	w.mu.Lock()
	_, kept := w.waiting["per_race"]
	w.mu.Unlock()
	if !kept {
		t.Fatal("a prompt asked during the in-flight reconcile was replaced away")
	}
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("the watcher reads as %#v, want waiting", got)
	}
}

// TestOpenCodeWatcherStopDoesNotCountAsAPermissionFailure: stopping a watcher
// cancels whatever request it has open. That cancellation is the watcher's own
// doing, not the endpoint failing, and counting it would spend a third of the
// budget that drops the waiting set on every stop.
func TestOpenCodeWatcherStopDoesNotCountAsAPermissionFailure(t *testing.T) {
	inFlight := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			_, _ = io.WriteString(w, `{"ses_s":{"type":"busy"}}`)
		case "/permission":
			select {
			case inFlight <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		case "/event":
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	w := startOpenCodeWatcherEvery(harnesses.ServerAccess{Port: portOf(t, srv)}, "/tmp/work", time.Hour)
	select {
	case <-inFlight:
	case <-time.After(10 * time.Second):
		w.stop()
		t.Fatal("the poll never reached the permission endpoint")
	}
	w.stop()
	// Long enough for the cancelled request to return and for whatever the
	// poll would have done with it to have happened.
	time.Sleep(300 * time.Millisecond)
	w.mu.Lock()
	fails := w.permFails
	w.mu.Unlock()
	if fails != 0 {
		t.Fatalf("stopping the watcher counted %d permission failures", fails)
	}
}

// TestOpenCodeWatcherWithoutThePermissionEndpoint: a server that answers 404
// does not have the endpoint, which is not the same as an endpoint that is
// failing. It says nothing about the prompts the event stream reported, so it
// must never clear them, must not count towards the failure budget, and must
// not be asked again.
func TestOpenCodeWatcherWithoutThePermissionEndpoint(t *testing.T) {
	var asks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			_, _ = io.WriteString(w, `{"ses_n":{"type":"busy"}}`)
		case "/permission":
			asks.Add(1)
			w.WriteHeader(http.StatusNotFound)
		case "/event":
			flusher := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	w := startOpenCodeWatcherEvery(harnesses.ServerAccess{Port: portOf(t, srv)}, "/tmp/work", 20*time.Millisecond)
	defer w.stop()
	waitFor(t, 10*time.Second, "the endpoint to be found absent", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.noPermissions
	})
	w.event(`{"type":"permission.asked","properties":{"id":"per_ev","sessionID":"ses_n"}}`)
	// Many reconcile intervals: an events-only watcher keeps its prompt.
	for i := 0; i < 10; i++ {
		time.Sleep(20 * time.Millisecond)
		if got := w.answer(); got.state != StateWaiting {
			t.Fatalf("the events-only watcher read as %#v, want waiting", got)
		}
	}
	w.mu.Lock()
	fails, asked := w.permFails, asks.Load()
	w.mu.Unlock()
	if fails != 0 {
		t.Errorf("an absent endpoint counted %d failures", fails)
	}
	if asked > 2 {
		t.Errorf("the absent endpoint was asked %d times; it is meant to be asked once", asked)
	}
	// And the reply event still retires it: events-only is a full mode, not a
	// mode where nothing can change.
	w.event(`{"type":"permission.replied","properties":{"requestID":"per_ev","sessionID":"ses_n"}}`)
	if got := w.answer(); got.state != StateBusy {
		t.Fatalf("the replied prompt left %#v, want busy", got)
	}
}

// TestOpenCodeWatcherEmptyPermissionsAgainstAnEventOnlyPrompt: `[]` is an
// authoritative answer, except about a prompt the endpoint has never confirmed
// and the stream has only just reported — `/permission?directory=` filters by
// directory, and a workdir that does not match the server's answers `[]` to a
// prompt that is genuinely open. A young event-only prompt therefore outranks
// `[]`; an old one does not, or a real directory mismatch would pin the row to
// waiting for the life of the session.
func TestOpenCodeWatcherEmptyPermissionsAgainstAnEventOnlyPrompt(t *testing.T) {
	fresh := func() *openCodeWatcher {
		return &openCodeWatcher{busy: map[string]bool{}, waiting: map[string]openCodePrompt{},
			reconcileEvery: time.Minute, ok: true}
	}
	empty := map[string]openCodePrompt{}

	// Young, event-only: survives.
	w := fresh()
	w.event(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_a"}}`)
	w.mu.Lock()
	w.applyPermissionsLocked(empty, openCodePermissionOK, time.Now())
	kept := len(w.waiting)
	w.mu.Unlock()
	if kept != 1 {
		t.Error("an empty answer dropped a prompt the stream had just reported")
	}

	// Older than one reconcile interval, still event-only: dropped.
	w = fresh()
	w.mu.Lock()
	w.waiting["per_old"] = openCodePrompt{session: "ses_a", seen: time.Now().Add(-2 * time.Minute)}
	w.applyPermissionsLocked(map[string]openCodePrompt{}, openCodePermissionOK, time.Now())
	old := len(w.waiting)
	w.mu.Unlock()
	if old != 0 {
		t.Error("an empty answer left an unconfirmed prompt older than the reconcile interval standing")
	}

	// Young but already confirmed by a non-empty answer: dropped, because the
	// endpoint has been shown to see this directory's prompts.
	w = fresh()
	w.mu.Lock()
	w.waiting["per_c"] = openCodePrompt{session: "ses_a", seen: time.Now(), confirmed: true}
	w.applyPermissionsLocked(map[string]openCodePrompt{}, openCodePermissionOK, time.Now())
	confirmed := len(w.waiting)
	w.mu.Unlock()
	if confirmed != 0 {
		t.Error("an empty answer left a prompt the endpoint had confirmed and now omits")
	}

	// An absent endpoint takes nothing at all, however old the prompt is.
	w = fresh()
	w.mu.Lock()
	w.waiting["per_old"] = openCodePrompt{session: "ses_a", seen: time.Now().Add(-2 * time.Minute)}
	w.applyPermissionsLocked(nil, openCodePermissionAbsent, time.Now())
	absent, gone := len(w.waiting), w.noPermissions
	w.mu.Unlock()
	if absent != 1 || !gone {
		t.Errorf("an absent endpoint left %d prompts (want 1) and noPermissions %t (want true)", absent, gone)
	}
}

// TestOpenCodeWatcherRetiresAnEventOnlyPromptAnsweredWhileTheStreamWasDown:
// a server without GET /permission has only two things that can retire a
// prompt — the reply event, and the status map. If the reply happened while
// the stream was down there is no event to come, so the poll that reconnects
// has to read the prompt's session being absent from /session/status as the
// answer it is. Without that the row would say waiting for the life of the
// session.
func TestOpenCodeWatcherRetiresAnEventOnlyPromptAnsweredWhileTheStreamWasDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			// Nobody is working: the session that held the prompt answered it
			// while the stream was down, and an idle session is simply absent.
			_, _ = io.WriteString(w, `{}`)
		default:
			// No /permission, and no stream either.
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	w := newOpenCodeWatcher(harnesses.ServerAccess{Port: portOf(t, srv)}, "/tmp/work", time.Minute, nil)
	w.poll(ctx)
	if w.permissionsSupported() {
		t.Fatal("a 404 from /permission did not switch the watcher to events-only")
	}
	w.event(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_a"}}`)
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("the prompt read as %#v, want waiting", got)
	}
	if err := w.stream(ctx); err == nil {
		t.Fatal("the stream that answered 404 reported no error")
	}
	w.poll(ctx)
	if got := w.answer(); !got.ok || got.state != StateIdle {
		t.Fatalf("the reconnect poll read as %#v, want idle: the prompt was answered while the stream was down", got)
	}
}

// TestOpenCodeWatcherEmptyPermissionsKeepEveryYoungPrompt: the carve-out is
// about the answer being empty, not about the set being empty as it is
// rebuilt. Two young event-only prompts against one `[]` both survive, and
// which of them is looked at first must not decide it.
func TestOpenCodeWatcherEmptyPermissionsKeepEveryYoungPrompt(t *testing.T) {
	w := &openCodeWatcher{busy: map[string]bool{}, waiting: map[string]openCodePrompt{},
		reconcileEvery: time.Minute, ok: true}
	w.event(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_a"}}`)
	w.event(`{"type":"permission.asked","properties":{"id":"per_2","sessionID":"ses_b"}}`)
	w.mu.Lock()
	w.applyPermissionsLocked(map[string]openCodePrompt{}, openCodePermissionOK, time.Now())
	kept := len(w.waiting)
	w.mu.Unlock()
	if kept != 2 {
		t.Fatalf("an empty answer left %d of two young event-only prompts, want 2", kept)
	}
}

// TestOpenCodePollRejectsAnUndecodableStatus: a status body that will not
// decode is no answer, the same as a 500. Reading it as an empty busy map
// would report idle for a session that is working.
func TestOpenCodePollRejectsAnUndecodableStatus(t *testing.T) {
	var broken atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			if broken.Load() {
				_, _ = io.WriteString(w, `<html>not json</html>`)
				return
			}
			_, _ = io.WriteString(w, `{"ses_b":{"type":"busy"}}`)
		case "/permission":
			_, _ = io.WriteString(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	w := newOpenCodeWatcher(harnesses.ServerAccess{Port: portOf(t, srv)}, "/tmp/work", time.Minute, nil)
	w.poll(context.Background())
	if got := w.answer(); !got.ok || got.state != StateBusy {
		t.Fatalf("the first poll read as %#v, want busy", got)
	}
	broken.Store(true)
	w.poll(context.Background())
	if got := w.answer(); got.ok {
		t.Fatalf("a status body that will not decode answered %#v, want nothing at all", got)
	}
}
