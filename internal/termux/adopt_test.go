//go:build !windows

package termux

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// newAdopter builds a second Manager over the same data directory and
// database, which is what a restarted Socrates is.
func (l *lab) newAdopter() *Manager {
	l.t.Helper()
	m, err := New(l.store, Config{
		DataDir:     l.dataDir,
		TmuxBin:     l.tmuxPath,
		SocratesBin: l.cfg.SocratesBin,
		WindowSize:  "manual",
		Logf:        func(f string, a ...any) { l.t.Logf(f, a...) },
	})
	if err != nil {
		l.t.Fatal(err)
	}
	return m
}

func TestAdoptAfterRestart(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	v := l.attach(row.ID, 90, 28)
	if _, err := v.Write([]byte("echo before-the-restart\n")); err != nil {
		t.Fatal(err)
	}
	waitForRing(t, v, 5*time.Second, "before-the-restart")

	// Socrates goes away. The tmux server does not.
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	next := l.newAdopter()
	ctx := context.Background()
	if err := next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if err := next.Adopt(ctx); err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	got, err := l.store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRunning {
		t.Fatalf("the adopted session is %q, want running", got.State)
	}
	if size := l.tmuxOut("show", "-wv", "-t", row.TmuxName, "window-size"); size != "manual" {
		t.Fatalf("Adopt should re-apply the per window policy, got %q", size)
	}

	// And it is still the same terminal: a viewer of the new manager sees what
	// the old one left on the screen.
	after, err := next.Attach(ctx, row.ID, "", 90, 28)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	waitForRing(t, after, 5*time.Second, "before-the-restart")
}

// TestOrphanSessionIsAdopted: Socrates never kills a tmux session unless the
// user deleted the Socrates session. A restored database, a failed migration
// or a crash in the moment between a session appearing and its row being
// written must not destroy running work.
func TestOrphanSessionIsAdopted(t *testing.T) {
	l := newLab(t)
	// Resolved, because the row's working directory comes back from tmux as
	// `#{pane_current_path}`, which is always the real path.
	dir := realDir(t, t.TempDir())
	// One real session, so that the server and the hooks exist, and then one
	// made by hand with no row at all.
	l.create(shellSpec(dir))
	orphanID := NewID()
	ctx := context.Background()
	if _, err := l.tmux.RunConf(ctx, "new-session", "-d", "-s", TmuxName(orphanID), "-c", dir, "--", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	if err := l.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := l.store.GetSession(orphanID)
	if err != nil {
		t.Fatalf("the unrecorded session was not taken in: %v", err)
	}
	if row.Title != "Recovered session" {
		t.Fatalf("the recovered row is titled %q", row.Title)
	}
	if row.State != store.StateRunning || row.Harness != "shell" {
		t.Fatalf("the recovered row is %+v", row)
	}
	if row.Workdir != dir {
		t.Fatalf("the recovered row's working directory is %q, want %q", row.Workdir, dir)
	}
	if got := l.tmuxOut("list-sessions", "-F", "#{session_name}"); !contains(Lines(got), TmuxName(orphanID)) {
		t.Fatal("the unrecorded session was killed; it must never be")
	}
}

func statPath(p string) (any, error) { return os.Stat(p) }

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestAdoptDetectsReboot: the definition of a reboot is that the row says
// running and the tmux session is gone. There is no uptime check and no boot
// id file.
func TestAdoptDetectsReboot(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	ctx := context.Background()
	if _, err := l.tmux.Run(ctx, "kill-server"); err != nil {
		t.Fatal(err)
	}
	// The socket file itself outlives the server - it sits in the data
	// directory, which survives a reboot - so its absence is not what a
	// reboot looks like. What it looks like is a server that will not answer.
	waitFor(t, 5*time.Second, "the tmux server to be gone", func() bool {
		running, err := l.tmux.Running(ctx)
		return err == nil && !running
	})

	next := l.newAdopter()
	if err := next.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := l.store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateNeedsResume {
		t.Fatalf("after the tmux server disappeared the session is %q, want needs_resume", got.State)
	}

	// The poll reaches the same conclusion on its own.
	other := l.create(shellSpec(t.TempDir()))
	if _, err := l.tmux.Run(ctx, "kill-server"); err != nil {
		t.Fatal(err)
	}
	// Twice, because one failure is not evidence.
	l.Poll(ctx)
	if got, _ := l.store.GetSession(other.ID); got.State != store.StateRunning {
		t.Fatalf("one failed poll moved the session to %q", got.State)
	}
	l.Poll(ctx)
	got, err = l.store.GetSession(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateNeedsResume {
		t.Fatalf("the poll left the session at %q, want needs_resume", got.State)
	}
}

// TestTransientPollFailureDoesNotResume: one bad answer from a busy server
// must not flip every session to needs_resume and set off a wave of relaunches.
func TestTransientPollFailureDoesNotResume(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	rows, err := l.store.ListSessions()
	if err != nil {
		t.Fatal(err)
	}

	if l.notePollFailure() {
		t.Fatal("one failed poll is not evidence of a reboot")
	}
	l.notePollSuccess()
	if l.notePollFailure() {
		t.Fatal("the counter should have been reset by the good poll")
	}
	got, err := l.store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRunning {
		t.Fatalf("the session is %q after a transient failure, want running", got.State)
	}

	// Two in a row is evidence.
	if !l.notePollFailure() {
		t.Fatal("two failed polls in a row should be enough")
	}

	// The same tolerance applies to one session going missing from an
	// otherwise healthy answer.
	l.applyPanes(rows, map[string]pane{})
	if got, _ := l.store.GetSession(row.ID); got.State != store.StateRunning {
		t.Fatalf("one absence moved the session to %q", got.State)
	}
	l.applyPanes(rows, map[string]pane{})
	if got, _ := l.store.GetSession(row.ID); got.State != store.StateNeedsResume {
		t.Fatalf("two absences should mean needs_resume, got %q", got.State)
	}
}

func TestPollReportsADeadPane(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir(), "/bin/sh", "-c", "exit 9"))
	// Dead, and with its status: up to tmux 3.6 a pane is dead a moment
	// before tmux knows how its program ended, and a poll in that moment
	// rightly leaves the row alone.
	waitFor(t, 10*time.Second, "the pane to die", func() bool {
		return l.tmuxOut("list-panes", "-t", row.TmuxName, "-F", "#{pane_dead}:#{pane_dead_status}") == "1:9"
	})
	// Undo whatever the hook already recorded, so that the poll is the only
	// thing that can put it back.
	if err := l.store.SetSessionState(row.ID, store.StateRunning, -1, ""); err != nil {
		t.Fatal(err)
	}
	l.Poll(context.Background())
	got, err := l.store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateExited || got.ExitStatus != 9 {
		t.Fatalf("the poll left the session at %q with status %d", got.State, got.ExitStatus)
	}
}

// TestPollWaitsForTheExitStatus: a pane tmux shows as dead before it knows
// how the program ended used to be recorded as exited -1, and the browser was
// told "exited -1" before the hook brought the real status, or for good when
// tmux had lost the SIGCHLD. The poll leaves it to the hook and to the next
// poll, and says so, so that the caller can make tmux collect it.
func TestPollWaitsForTheExitStatus(t *testing.T) {
	exits := make(chan int, 4)
	l := newLab(t, func(c *Config) {
		c.OnExit = func(id string, status int) { exits <- status }
	})
	row := l.create(shellSpec(t.TempDir(), "/bin/sh"))
	rows, err := l.store.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !l.applyPanes(rows, parsePanes(row.TmuxName+"|1|||0|/")) {
		t.Fatal("a dead pane without a status was not reported as unreaped")
	}
	if got, _ := l.store.GetSession(row.ID); got.State != store.StateRunning {
		t.Fatalf("a dead pane without a status put the session at %q with status %d", got.State, got.ExitStatus)
	}
	select {
	case status := <-exits:
		t.Fatalf("an exit with status %d was announced before tmux knew the status", status)
	default:
	}
	if l.applyPanes(rows, parsePanes(row.TmuxName+"|1|7||0|/")) {
		t.Fatal("a dead pane with its status was reported as unreaped")
	}
	if got, _ := l.store.GetSession(row.ID); got.State != store.StateExited || got.ExitStatus != 7 {
		t.Fatalf("the session is %q with status %d, want exited with 7", got.State, got.ExitStatus)
	}
	if status := <-exits; status != 7 {
		t.Fatalf("the exit was announced with status %d, want 7", status)
	}
}

func TestDeleteKillsTheSessionAndKeepsTheDirectory(t *testing.T) {
	l := newLab(t)
	dir := t.TempDir()
	row := l.create(shellSpec(dir))
	l.attach(row.ID, 80, 24)
	ctx := context.Background()
	if err := l.Delete(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.store.GetSession(row.ID); err == nil {
		t.Fatal("the row should be gone")
	}
	if got := l.tmuxOut("list-sessions", "-F", "#{session_name}"); contains(Lines(got), row.TmuxName) {
		t.Fatal("the tmux session should be gone")
	}
	if _, err := statPath(SessionDir(l.dataDir, row.ID)); err == nil {
		t.Fatal("the session's own directory should be gone, journal and all")
	}
	// The working directory is not ours to throw away.
	if _, err := statPath(dir); err != nil {
		t.Fatalf("the working directory was deleted: %v", err)
	}
}

// TestADeleteCutShortCanBeDoneAgain: a delete removes the session's folder
// before its row, so one that stops in between - a start killed in the
// migration's clean cut - leaves a row that leads to the rest, never a folder
// nothing leads back to. A database that refuses the row stands in for the
// kill.
func TestADeleteCutShortCanBeDoneAgain(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	db, err := sql.Open("sqlite", "file:"+filepath.Join(l.dataDir, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER refuse BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'cut short'); END`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.Delete(ctx, row.ID); err == nil {
		t.Fatal("a delete whose row could not go succeeded")
	}
	if _, err := statPath(SessionDir(l.dataDir, row.ID)); err == nil {
		t.Fatal("the folder outlived a delete cut short before the row")
	}
	if _, err := l.store.GetSession(row.ID); err != nil {
		t.Fatalf("the row of a delete cut short is gone: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	if err := l.Delete(ctx, row.ID); err != nil {
		t.Fatalf("the delete done again: %v", err)
	}
	if _, err := l.store.GetSession(row.ID); err == nil {
		t.Fatal("the row is still there")
	}
}

// TestEmptyServerIsNotAReboot: with exit-empty off, a server that has lost its
// last session stays up - and on it `list-panes -a` fails with "no current
// target", because it is a command that needs one. Taking that for a failure
// would make Adopt error out on a healthy server and leave the poll one tick
// from declaring a reboot for as long as nobody had a session open.
func TestEmptyServerIsNotAReboot(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	row := l.create(shellSpec(t.TempDir()))
	if err := l.Delete(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	running, err := l.tmux.Running(ctx)
	if err != nil || !running {
		t.Fatalf("the server should still be up with exit-empty off: %v, %v", running, err)
	}
	if err := l.Adopt(ctx); err != nil {
		t.Fatalf("Adopt on a live, empty server returned an error: %v", err)
	}
	l.Poll(ctx)
	l.Poll(ctx)
	l.mu.Lock()
	fails := l.pollFails
	l.mu.Unlock()
	if fails != 0 {
		t.Fatalf("an empty server counted %d poll failures", fails)
	}

	// And a row that is mid-create while the server is empty stays that way:
	// the window between writing the row and running new-session must not be
	// a window in which the poll declares a reboot.
	starting := &store.Session{
		ID: NewID(), Harness: "shell", Workdir: l.dataDir, WorkdirMode: store.WorkdirCustom,
		State: store.StateStarting,
	}
	starting.TmuxName = TmuxName(starting.ID)
	if err := l.store.CreateSession(starting); err != nil {
		t.Fatal(err)
	}
	l.Poll(ctx)
	got, err := l.store.GetSession(starting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateStarting {
		t.Fatalf("one poll on an empty server moved a starting row to %q", got.State)
	}
}

// TestPollLeavesACreateInFlightAlone: the poll runs on its own clock, and a
// create writes its row first and only then runs tmux. A poll that lands in
// between - before the first session of a data directory has started the tmux
// server, when there is no socket at all, or twice before new-session has made
// the pane - must not call the new row lost. needs_resume over a pane that is
// about to be alive is a session nobody can open: every open asks for a
// relaunch, the relaunch is refused because the pane is running, and the page
// says "Resuming after a restart…" for ever.
func TestPollLeavesACreateInFlightAlone(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		stop := make(chan struct{})
		polled := make(chan int)
		go func() {
			n := 0
			for {
				select {
				case <-stop:
					polled <- n
					return
				default:
				}
				l.Poll(ctx)
				n++
			}
		}()
		row, err := l.Create(ctx, shellSpec(t.TempDir()))
		close(stop)
		n := <-polled
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if row.State != store.StateRunning {
			t.Fatalf("create %d (with %d polls beside it) answered a session in %q, want running", i, n, row.State)
		}
		l.Poll(ctx)
		l.Poll(ctx)
		if got, _ := l.store.GetSession(row.ID); got.State != store.StateRunning {
			t.Fatalf("create %d: the polls after it left the session in %q, want running", i, got.State)
		}
		ready, err := l.Ensure(ctx, row.ID)
		if err != nil || ready.State != store.StateRunning {
			t.Fatalf("create %d: the new session could not be opened: %v (%+v)", i, err, ready)
		}
	}
}

// TestPollSurvivesATransientFailure drives the whole poll through a tmux that
// fails once, rather than the counter underneath it: one bad answer must not
// flip every session to needs_resume.
func TestPollSurvivesATransientFailure(t *testing.T) {
	real := requireTmux(t)
	dir := t.TempDir()
	fail := filepath.Join(dir, "fail")
	wrapper := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = list-panes ] && [ -f " + fail + " ]; then\n" +
		"    rm -f " + fail + "\n" +
		"    echo 'the server is busy' >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"done\n" +
		"exec " + real + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	l := newLab(t, func(c *Config) { c.TmuxBin = wrapper })
	row := l.create(shellSpec(t.TempDir()))
	ctx := context.Background()

	if err := os.WriteFile(fail, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l.Poll(ctx)
	if got, _ := l.store.GetSession(row.ID); got.State != store.StateRunning {
		t.Fatalf("one failed poll moved the session to %q", got.State)
	}
	l.Poll(ctx) // The good answer clears the count.
	l.mu.Lock()
	fails := l.pollFails
	l.mu.Unlock()
	if fails != 0 {
		t.Fatalf("a good poll left %d failures counted", fails)
	}
	if got, _ := l.store.GetSession(row.ID); got.State != store.StateRunning {
		t.Fatalf("the session ended up %q", got.State)
	}
}
