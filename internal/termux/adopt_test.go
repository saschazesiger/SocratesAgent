//go:build !windows

package termux

import (
	"context"
	"os"
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
	dir := t.TempDir()
	// One real session, so that the server and the hooks exist, and then one
	// made by hand with no row at all.
	l.create(shellSpec(dir))
	orphanID := NewID()
	ctx := context.Background()
	if _, err := l.tmux.RunStart(ctx, "new-session", "-d", "-s", TmuxName(orphanID), "-c", dir, "--", "/bin/sh"); err != nil {
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
		return !l.tmux.Running(ctx)
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
	rows, err := l.store.ListSessions(true)
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
	waitFor(t, 10*time.Second, "the pane to die", func() bool {
		return l.tmuxOut("list-panes", "-t", row.TmuxName, "-F", "#{pane_dead}") == "1"
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
