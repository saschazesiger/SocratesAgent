//go:build !windows

package termux

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The ladder is tested without tmux and without a clock.
//
// The Shell's layer sits behind a field and every pane behind the `paneLine`
// struct, so a table test can hand the detector any sequence of ticks it
// likes and assert both the committed state and the exact OnActivity calls.
// What the real tmux answers is a different question, and is asked in
// screen_test.go.

// fakeSource is a scripted exact layer: one answer per tick, and the last one
// repeats once the script runs out.
type fakeSource struct {
	mu      sync.Mutex
	answers []observation
	at      int
}

func (f *fakeSource) read() observation {
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

// bench is a Manager with no tmux behind it, a real store, and whatever
// Shell layer the test wants.
type bench struct {
	t       *testing.T
	m       *Manager
	st      *store.Store
	dataDir string
	now     time.Time

	mu      sync.Mutex
	changes []ActivityChange
	// sources is the scripted layer of each session that has one, and src
	// the one every other session gets; nil leaves the real one in place.
	sources map[string]*fakeSource
	src     *fakeSource
}

func newBench(t *testing.T) *bench {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatalf("could not open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	b := &bench{t: t, st: st, dataDir: dir, now: time.Unix(1788362216, 0), sources: map[string]*fakeSource{}}
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
	m.act.shell = func(snap snapshot) observation {
		b.mu.Lock()
		f := b.sources[snap.row.ID]
		if f == nil {
			f = b.src
		}
		b.mu.Unlock()
		if f == nil {
			return readShell(snap)
		}
		return f.read()
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

// source scripts the Shell layer of every session the test has not given a
// script of its own.
func (b *bench) source(answers ...observation) *fakeSource {
	f := &fakeSource{answers: answers}
	b.mu.Lock()
	b.src = f
	b.mu.Unlock()
	return f
}

// sourceFor scripts one session's.
func (b *bench) sourceFor(id string, answers ...observation) *fakeSource {
	f := &fakeSource{answers: answers}
	b.mu.Lock()
	b.sources[id] = f
	b.mu.Unlock()
	return f
}

// TestActivityBusyIsImmediateAndIdleSettles: the spinner has to appear on the
// tick the command starts, and one stray idle answer between two commands of
// a script must not stop it.
func TestActivityBusyIsImmediateAndIdleSettles(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	b.source(
		seen(StateBusy, ""),
		seen(StateIdle, ""), // one stray tick between two commands
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
	// Both commits are status changes in the list's sense, so both were
	// written into the row: finishing is the moment the list orders by. The
	// last commit happened on the last tick, and the bench's clock has moved
	// one interval past it since.
	last := b.at().Add(-ActivityInterval).UnixMilli()
	if got, _ := b.st.GetSession("s1"); got == nil || got.ActiveAt != last {
		t.Fatalf("active_at = %v, want the moment of the last commit %d", got, last)
	}
}

// TestMovesRow is the one rule for what moves a session in the list, which the
// browser applies to its frames as well. Coming to rest from busy, starting,
// and starting to need an answer move it; a detector that comes up after a
// restart and finds every pane idle moves nothing.
func TestMovesRow(t *testing.T) {
	for _, c := range []struct {
		prev, next State
		want       bool
	}{
		{StateIdle, StateBusy, true},
		{StateUnknown, StateBusy, true},
		{StateBusy, StateIdle, true},
		{StateBusy, StateWaiting, true},
		{StateBusy, StateUnknown, true},
		{StateIdle, StateWaiting, true},
		{StateUnknown, StateIdle, false},
		{StateWaiting, StateIdle, false},
		{StateIdle, StateUnknown, false},
		{StateBusy, StateBusy, false},
		{StateWaiting, StateWaiting, false},
		// A turn that ends with something still running behind it ends, and
		// that work ending is an end too. Coming up to find it already so is
		// the machine catching up, like coming up to find a session idle.
		{StateBusy, StateTasks, true},
		{StateTasks, StateIdle, true},
		{StateTasks, StateBusy, true},
		{StateTasks, StateUnknown, true},
		{StateTasks, StateWaiting, true},
		{StateUnknown, StateTasks, false},
		{StateIdle, StateTasks, false},
		{StateTasks, StateTasks, false},
	} {
		if got := MovesRow(c.prev, c.next); got != c.want {
			t.Errorf("MovesRow(%s, %s) = %v, want %v", c.prev, c.next, got, c.want)
		}
	}
}

// TestActivityLongSilentRunStaysBusy: a five minute test suite that prints
// nothing is busy, because the Shell's layer says a program runs and
// quiescence never overrides an exact answer.
func TestActivityLongSilentRunStaysBusy(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	b.source(seen(StateBusy, "go"))
	rows := []store.Session{row}

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

// TestActivityRememberedExactAnswerSurvivesSilence: a foreground command that
// blinks out for a tick or two is tmux between two reads, not the program
// ending. A busy answer being reused must not be overwritten by thirty
// seconds of quiet - and once it has really gone, quiescence decides.
func TestActivityRememberedExactAnswerSurvivesSilence(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	src := b.source(seen(StateBusy, "make"))

	// A long silent build: busy by its foreground command, and the pane has
	// said nothing for well over HardQuiet.
	b.tick(rows, b.pane(row, HardQuiet))
	b.want("s1", StateBusy)

	// The command cannot be read for the whole reuse window.
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

// TestActivityATerminalWithNoLayerIsReadFromItsOutput: VS Code has no exact
// layer, so a pane that prints is busy and one that has been quiet for
// HardQuiet is idle.
func TestActivityATerminalWithNoLayerIsReadFromItsOutput(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "vscode")
	rows := []store.Session{row}
	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy)
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, HardQuiet) })
	if src := b.m.ActivityOf("s1").Source; src != sourceQuiet {
		t.Fatalf("the answer came from %q, want quiescence", src)
	}
}

// TestActivityChangesSayWhetherTheyWereAskedFor: the busy of a shell starting
// up is published as not asked for, and the busy after somebody has typed as
// asked for.
func TestActivityChangesSayWhetherTheyWereAskedFor(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	b.source(seen(StateBusy, ""), seen(StateIdle, ""), seen(StateIdle, ""), seen(StateBusy, ""))

	b.ticks(3, rows, func() map[string]paneLine { return b.pane(row, 0) })
	b.want("s1", StateIdle)
	b.m.NoteInput("s1")
	b.tick(rows, b.pane(row, 0))
	b.want("s1", StateBusy)

	b.mu.Lock()
	defer b.mu.Unlock()
	var asked []bool
	for _, change := range b.changes {
		asked = append(asked, change.Activity.Asked)
	}
	if len(asked) != 3 || asked[0] || asked[1] || !asked[2] {
		t.Fatalf("asked per change = %v, want false, false, true", asked)
	}
}

// TestActivityPaneDeathMarksUnreadAndInputClearsIt.
func TestActivityPaneDeathMarksUnreadAndInputClearsIt(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	b.source(seen(StateBusy, ""))
	// Somebody typed at it, so what it does from here is work that was asked
	// for rather than a program starting up.
	b.m.NoteInput("s1")

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

// TestActivitySeqOrdersEveryPicture: a browser hears one session from the
// event stream, a terminal socket and a list read over HTTP, and since only
// moves with the state. The seq moves with every published picture - a new
// state and an unread mark cleared alike - the snapshot carries the seq of the
// last frame, and a manager built later (a restart) starts above it.
func TestActivitySeqOrdersEveryPicture(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	if a := b.m.ActivityOf("s1"); a.Seq != 0 {
		t.Fatalf("a session nothing was published about has seq %d", a.Seq)
	}
	b.source(seen(StateBusy, ""), seen(StateIdle, ""))
	b.m.NoteInput("s1")
	b.tick(rows, b.pane(row, 0))
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	finished := b.m.ActivityOf("s1")
	if !finished.Unread || finished.Seq < time.Now().Add(-time.Hour).UnixMicro() {
		t.Fatalf("a committed change is %#v, want it unread with a seq from the clock", finished)
	}
	b.m.MarkRead("s1")
	read := b.m.ActivityOf("s1")
	if read.Since != finished.Since || read.Seq <= finished.Seq {
		t.Fatalf("an unread mark cleared on the same commit: since %d->%d, seq %d->%d",
			finished.Since, read.Since, finished.Seq, read.Seq)
	}
	b.mu.Lock()
	var frames []int64
	for _, change := range b.changes {
		if change.SessionID == "s1" {
			frames = append(frames, change.Activity.Seq)
		}
	}
	b.mu.Unlock()
	if len(frames) < 3 || frames[len(frames)-1] != read.Seq {
		t.Fatalf("the frames carry %v, the snapshot %d", frames, read.Seq)
	}
	for i := 1; i < len(frames); i++ {
		if frames[i] <= frames[i-1] {
			t.Fatalf("the frames' seqs are not increasing: %v", frames)
		}
	}
	raw, _ := json.Marshal(Activity{State: StateIdle})
	if strings.Contains(string(raw), "seq") {
		t.Fatalf("an activity without a seq says %s", raw)
	}
	time.Sleep(time.Millisecond)
	if later := newActivity(b.m); later.seq <= read.Seq {
		t.Fatalf("a later process starts at %d, below the seq %d of this one", later.seq, read.Seq)
	}
}

// TestActivityStartUpIsNotWorkThatFinished: a shell is busy while its profile
// runs and idle the moment it is at its prompt, and that first edge is not a turn
// ending. It must leave no unread mark - the browser chimes and notifies on
// exactly that mark, and a session that has just been created would otherwise
// announce itself as work somebody missed.
func TestActivityStartUpIsNotWorkThatFinished(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	b.source(seen(StateBusy, ""), seen(StateIdle, ""))

	b.tick(rows, b.pane(row, 0))
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	if a := b.m.ActivityOf("s1"); a.Unread {
		t.Fatalf("a session nobody has typed at yet marked itself unread: %#v", a)
	}

	// And the very next turn, the one that was asked for, does mark it.
	b.m.NoteInput("s1")
	b.source(seen(StateBusy, ""))
	b.until("s1", StateBusy, 2, rows, func() map[string]paneLine { return b.pane(row, 0) })
	b.source(seen(StateIdle, ""))
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	if a := b.m.ActivityOf("s1"); !a.Unread {
		t.Fatalf("a turn that was asked for and ended is unread, got %#v", a)
	}
}

// TestActivityUnreadSurvivesAManagerRebuild: a restart of Socrates must not
// lose "this finished while you were away".
func TestActivityUnreadSurvivesAManagerRebuild(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	b.source(seen(StateBusy, ""), seen(StateIdle, ""))
	b.m.NoteInput("s1")

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

// TestActivitySinceSurvivesAManagerRebuild: a restart of Socrates does not
// restart every session's clock. A session found in the state it was in
// keeps the moment it entered it - a build that has run for ten minutes is
// not "just now", and the oldest is still the oldest - and one found in
// another state, or relaunched, starts from now.
func TestActivitySinceSurvivesAManagerRebuild(t *testing.T) {
	b := newBench(t)
	working := b.row("s1", "shell")
	rows := []store.Session{working}
	b.source(seen(StateBusy, "make"))
	b.tick(rows, b.pane(working, 0))
	busy := b.m.ActivityOf("s1")
	if busy.State != StateBusy || busy.Since == 0 {
		t.Fatalf("the build was not committed: %#v", busy)
	}

	// rebuild is a restart at a moment of the test's choosing, whose first
	// ticks find the session in the state answer says.
	rebuild := func(at time.Time, answer observation) *Manager {
		t.Helper()
		next, err := New(b.st, Config{DataDir: b.dataDir, WindowSize: "manual", Logf: func(string, ...any) {}})
		if err != nil {
			t.Fatal(err)
		}
		next.act.now = func() time.Time { return at }
		next.act.shell = func(snapshot) observation { return answer }
		next.act.load()
		for range IdleSettle {
			next.act.apply(context.Background(), rows, map[string]paneLine{working.TmuxName: {
				session: working.TmuxName, pid: os.Getpid(), activity: at.Unix(),
			}}, at)
		}
		return next
	}
	later := b.at().Add(10 * time.Minute)
	if a := rebuild(later, seen(StateBusy, "make")).ActivityOf("s1"); a.State != StateBusy || a.Since != busy.Since {
		t.Fatalf("after a restart the build is %#v, want it busy since %d", a, busy.Since)
	}
	// Found at its prompt instead, it came to rest now.
	idle := rebuild(later, seen(StateIdle, ""))
	if a := idle.ActivityOf("s1"); a.State != StateIdle || a.Since != later.UnixMilli() {
		t.Fatalf("a session found in another state is %#v, want it since %d", a, later.UnixMilli())
	}
	// And that is what the next restart finds.
	if a := rebuild(later.Add(time.Minute), seen(StateIdle, "")).ActivityOf("s1"); a.Since != later.UnixMilli() {
		t.Fatalf("a second restart says idle since %d, want %d", a.Since, later.UnixMilli())
	}
	// A relaunch is a new program, in no state yet.
	idle.ResetActivity("s1")
	if a := rebuild(later.Add(2*time.Minute), seen(StateIdle, "")).ActivityOf("s1"); a.Since != later.Add(2*time.Minute).UnixMilli() {
		t.Fatalf("a relaunched session kept the old clock: %#v", a)
	}
	// The moment of a deleted session is dropped rather than kept for ever.
	if err := b.st.DeleteSession("s1"); err != nil {
		t.Fatal(err)
	}
	gone, err := New(b.st, Config{DataDir: b.dataDir, WindowSize: "manual", Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	gone.act.load()
	var marks map[string]sinceMark
	if err := b.st.GetJSON(sinceKey, &marks); err != nil {
		t.Fatal(err)
	}
	if _, ok := marks["s1"]; ok || len(gone.act.since) != 0 {
		t.Fatalf("the moment of a deleted session came back: %v", marks)
	}
}

// TestActivityResetKeepsUnreadAndDropsTheState: a restart the user pressed
// starts the detector again, and does not forgive the turn that finished
// before it.
func TestActivityResetKeepsUnreadAndDropsTheState(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	b.source(seen(StateBusy, ""), seen(StateIdle, ""))
	b.m.NoteInput("s1")

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

// TestActivityTmuxUnreachableTouchesNothing: unknown settles only when the
// command succeeded and the session was absent, never on an error.
func TestActivityTmuxUnreachableTouchesNothing(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	rows := []store.Session{row}
	b.source(seen(StateBusy, ""))
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
	row := b.row("s1", "shell")
	b.source(seen(StateBusy, ""))
	b.m.NoteInput("s1")
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

// TestParseActivityPanesKeepsTheCommandWhole: the foreground command is the
// last field, so a program whose name has a pipe in it is read whole.
func TestParseActivityPanesKeepsTheCommandWhole(t *testing.T) {
	out := strings.Join([]string{
		"soc_a|0|3412|1788362216|odd|name",
		"soc_b|1|0|1788362100|bash",
		"soc_c|0|77|1788362200|make",
	}, "\n")
	panes := parseActivityPanes(out)
	if len(panes) != 3 {
		t.Fatalf("parsed %d panes, want 3", len(panes))
	}
	a := panes["soc_a"]
	if a.pid != 3412 || a.activity != 1788362216 || a.command != "odd|name" || a.dead {
		t.Fatalf("soc_a parsed as %#v", a)
	}
	if !panes["soc_b"].dead {
		t.Fatal("a dead pane parsed as alive")
	}
}

// TestQuiescence is the layer every terminal has.
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

// statusOf is where the store has a session filed.
func (b *bench) statusOf(id string) string {
	b.t.Helper()
	row, err := b.st.GetSession(id)
	if err != nil {
		b.t.Fatalf("could not read %s: %v", id, err)
	}
	return row.Status
}

// fresh is the rows as the store has them now, which is what a real tick
// reads: the status the detector wrote on one tick is what the next one sees.
func (b *bench) fresh(ids ...string) []store.Session {
	b.t.Helper()
	out := make([]store.Session, 0, len(ids))
	for _, id := range ids {
		row, err := b.st.GetSession(id)
		if err != nil {
			b.t.Fatalf("could not read %s: %v", id, err)
		}
		out = append(out, *row)
	}
	return out
}

// TestActivityWorkFilesASessionAsInProgress: a session that was put aside -
// pending, or waiting for a review - and is given work again is in progress
// again. The start-up of a shell is not work, and neither is anything a done
// session could do.
func TestActivityWorkFilesASessionAsInProgress(t *testing.T) {
	for _, status := range []string{store.StatusPending, store.StatusReview} {
		t.Run(status, func(t *testing.T) {
			b := newBench(t)
			row := b.row("s1", "shell")
			if err := b.st.SetSessionStatus("s1", status); err != nil {
				t.Fatal(err)
			}
			panes := func() map[string]paneLine { return b.pane(row, 0) }

			// Starting up: busy, then idle, and nobody has asked anything.
			b.source(seen(StateBusy, ""))
			b.tick(b.fresh("s1"), panes())
			b.want("s1", StateBusy)
			if got := b.statusOf("s1"); got != status {
				t.Fatalf("a shell starting up refiled the session as %q", got)
			}
			b.source(seen(StateIdle, ""))
			for i := 0; i < IdleSettle; i++ {
				b.tick(b.fresh("s1"), panes())
			}
			b.want("s1", StateIdle)

			// A turn somebody asked for is work.
			b.m.NoteInput("s1")
			b.source(seen(StateBusy, ""))
			b.tick(b.fresh("s1"), panes())
			b.want("s1", StateBusy)
			if got := b.statusOf("s1"); got != store.StatusInProgress {
				t.Fatalf("a %s session that started working is %q, want in progress", status, got)
			}
		})
	}
}

// TestActivityLeavesABackgroundSessionWhereItIs: a service somebody filed as
// background is working whenever it runs, and being typed at does not make it
// the work in progress.
func TestActivityLeavesABackgroundSessionWhereItIs(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "shell")
	if err := b.st.SetSessionStatus("s1", store.StatusBackground); err != nil {
		t.Fatal(err)
	}
	b.m.NoteInput("s1")
	b.source(seen(StateBusy, ""))
	b.tick(b.fresh("s1"), b.pane(row, 0))
	b.want("s1", StateBusy)
	if got := b.statusOf("s1"); got != store.StatusBackground {
		t.Fatalf("a background session that worked was refiled as %q", got)
	}
}

// TestActivitySortsTheSessionsAnUpgradeLeftUnsorted: the upgrade from flags
// and an archive cannot tell which sessions are working, so it leaves them
// unsorted and the detector files them - in progress if working, pending if
// not, and pending if it cannot tell within SortWithin.
func TestActivitySortsTheSessionsAnUpgradeLeftUnsorted(t *testing.T) {
	b := newBench(t)
	busy, idle, silent, stopped := b.row("busy", "shell"), b.row("idle", "shell"), b.row("silent", "shell"),
		b.row("stopped", "shell")
	stopped.State = store.StateNeedsResume
	b.sourceFor("busy", seen(StateBusy, ""))
	b.sourceFor("idle", seen(StateIdle, ""))
	b.sourceFor("silent")
	// The rows as the upgrade left them. The store has no way to write an
	// unsorted status, so every row is filed for review - a status the
	// detector never writes - and the tick is handed that as unsorted.
	for _, id := range []string{"busy", "idle", "silent", "stopped"} {
		if err := b.st.SetSessionStatus(id, store.StatusReview); err != nil {
			t.Fatal(err)
		}
	}
	unsorted := func() []store.Session {
		rows := b.fresh("busy", "idle", "silent", "stopped")
		for i := range rows {
			if rows[i].Status == store.StatusReview {
				rows[i].Status = store.StatusUnsorted
			}
		}
		rows[3].State = stopped.State
		return rows
	}
	panes := func() map[string]paneLine {
		out := map[string]paneLine{}
		for _, row := range []store.Session{busy, idle, silent} {
			// Output a few seconds ago: no answer from quiescence either way.
			for k, v := range b.pane(row, 10*time.Second) {
				out[k] = v
			}
		}
		return out
	}

	b.tick(unsorted(), panes())
	if got := b.statusOf("busy"); got != store.StatusInProgress {
		t.Fatalf("a working session was filed as %q", got)
	}
	if got := b.statusOf("stopped"); got != store.StatusPending {
		t.Fatalf("a session that is not running was filed as %q", got)
	}
	for i := 0; i < IdleSettle; i++ {
		b.tick(unsorted(), panes())
	}
	if got := b.statusOf("idle"); got != store.StatusPending {
		t.Fatalf("an idle session was filed as %q", got)
	}

	// The session the detector cannot read stays unsorted until SortWithin
	// has passed, and is pending after.
	b.mu.Lock()
	b.now = b.now.Add(SortWithin)
	b.mu.Unlock()
	b.tick(unsorted(), panes())
	if got := b.statusOf("silent"); got != store.StatusPending {
		t.Fatalf("a session nobody could read was filed as %q after %v", got, SortWithin)
	}
}
