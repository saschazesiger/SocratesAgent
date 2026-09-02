//go:build !windows

package termux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// TestManualPolicyIsPerWindow creates three sessions in a row, and three is
// the point of it.
//
// A global `window-size manual` lets the *first* session succeed and takes the
// server down on the second, so a one-session test would pass while the
// product loses everything the moment a user opens a second terminal. The
// policy is therefore a per window option, applied after each create, and the
// global is left at tmux's own default.
func TestManualPolicyIsPerWindow(t *testing.T) {
	l := newLab(t)
	dir := t.TempDir()
	var rows []*store.Session
	for i := 0; i < 3; i++ {
		rows = append(rows, l.create(shellSpec(dir)))
	}
	for i, row := range rows {
		if row.State != store.StateRunning {
			t.Fatalf("session %d is %q: %s", i, row.State, row.FailReason)
		}
		if got := l.tmuxOut("show", "-wv", "-t", row.TmuxName, "window-size"); got != "manual" {
			t.Fatalf("session %d has window-size %q, want manual", i, got)
		}
	}
	if got := l.tmuxOut("show", "-gwv", "window-size"); got != "latest" {
		t.Fatalf("the global window-size is %q; it must stay at tmux's default", got)
	}
	live := Lines(l.tmuxOut("list-sessions", "-F", "#{session_name}"))
	if len(live) != 3 {
		t.Fatalf("%d sessions survived, want 3: %v", len(live), live)
	}
}

func TestCreateAttachEcho(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	v := l.attach(row.ID, 100, 30)
	if _, err := v.Write([]byte("echo hello-from-the-pane\n")); err != nil {
		t.Fatal(err)
	}
	waitForRing(t, v, 5*time.Second, "hello-from-the-pane")
}

// TestAttachRedrawsCurrentScreen is the "no replay of megabytes" guarantee: a
// browser that connects late gets the current screen because tmux repaints it
// for that client, not because we replayed anything.
func TestAttachRedrawsCurrentScreen(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	first := l.attach(row.ID, 100, 30)
	if _, err := first.Write([]byte("echo marker-in-the-scrollback\n")); err != nil {
		t.Fatal(err)
	}
	waitForRing(t, first, 5*time.Second, "marker-in-the-scrollback")
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first viewer: %v", err)
	}

	second := l.attach(row.ID, 100, 30)
	var head string
	waitFor(t, 5*time.Second, "the redraw to carry the screen", func() bool {
		out, _ := second.Ring().Since(0)
		if len(out) > 4096 {
			out = out[:4096]
		}
		head = string(out)
		return strings.Contains(head, "marker-in-the-scrollback")
	})
}

func TestTwoViewersDifferentSizes(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))

	wide := l.attach(row.ID, 100, 30)
	waitFor(t, 5*time.Second, "the wide viewer's redraw", func() bool {
		out, _ := wide.Ring().Since(0)
		return strings.Contains(string(out), "\x1b[1;30r")
	})
	narrow := l.attach(row.ID, 60, 20)
	waitFor(t, 5*time.Second, "the narrow viewer's redraw", func() bool {
		out, _ := narrow.Ring().Since(0)
		return strings.Contains(string(out), "\x1b[1;20r")
	})
}

// TestManualSizingIgnoresTyping pins the semantics the default policy is
// chosen for. Under `latest` the window follows whichever client last typed,
// so two devices alternating would re-lay the program out on every keystroke.
func TestManualSizingIgnoresTyping(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	big := l.attach(row.ID, 100, 30)
	small := l.attach(row.ID, 60, 20)

	size := func() string {
		return l.tmuxOut("display-message", "-p", "-t", row.TmuxName, "-F", "#{window_width}x#{window_height}")
	}
	if got := size(); got != "60x20" {
		t.Fatalf("after the second attach the window is %s, want 60x20", got)
	}
	for i := 0; i < 3; i++ {
		if _, err := big.Write([]byte("echo typing\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := small.Write([]byte("echo typing\n")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
		if got := size(); got != "60x20" {
			t.Fatalf("typing moved the window to %s; only a resize may do that", got)
		}
	}

	ctx := context.Background()
	if err := big.Resize(ctx, 90, 28); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "the explicit resize to be obeyed", func() bool { return size() == "90x28" })
}

func TestSizeOwnerHandover(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	laptop := l.attach(row.ID, 100, 30)
	phone := l.attach(row.ID, 60, 20)
	size := func() string {
		return l.tmuxOut("display-message", "-p", "-t", row.TmuxName, "-F", "#{window_width}x#{window_height}")
	}
	if got := size(); got != "60x20" {
		t.Fatalf("the phone should own the size, window is %s", got)
	}

	// The owner goes away: ownership passes at once to the viewer that
	// attached or resized most recently, not after a grace period.
	if err := phone.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "the laptop to take the size back", func() bool { return size() == "100x30" })

	if err := laptop.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := size(); got != "100x30" {
		t.Fatalf("with nobody watching the window should keep its size, got %s", got)
	}
}

// TestOSCAnsweredWithNoClient is the test the white background rests on.
//
// With an explicit window style, tmux answers a pane's colour questions
// itself, with zero clients attached - which is why no session needs a
// rendezvous, a launch script or a process of its own to be light.
func TestOSCAnsweredWithNoClient(t *testing.T) {
	l := newLab(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "osc.txt")
	spec := shellSpec(dir, oscProbeArgv(sinkBinary(t), out)...)
	row := l.create(spec)

	if clients := l.tmuxOut("list-clients", "-F", "#{client_tty}"); clients != "" {
		t.Fatalf("no client should be attached, tmux lists %q", clients)
	}
	got := readWhenWritten(t, out)
	if !strings.Contains(got, "]11;rgb:ffff/ffff/ffff") {
		t.Fatalf("the pane was told the background is %q", got)
	}
	if !strings.Contains(got, "]10;rgb:1717/1818/1b1b") {
		t.Fatalf("the pane was told the foreground is %q", got)
	}
	_ = row
}

// TestOSCRespondsWhite is the same question with somebody looking: the style
// still wins, and on a tmux that answers it the theme mode reads as light.
func TestOSCRespondsWhite(t *testing.T) {
	l := newLab(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "osc.txt")
	first := l.create(shellSpec(dir))
	l.attach(first.ID, 100, 30)

	row := l.create(shellSpec(dir, oscProbeArgv(sinkBinary(t), out)...))
	l.attach(row.ID, 80, 24)

	got := readWhenWritten(t, out)
	if !strings.Contains(got, "]11;rgb:ffff/ffff/ffff") {
		t.Fatalf("with a viewer attached the pane was told %q", got)
	}
	if l.TmuxVersion().Less(3, 6) {
		// tmux answers the theme-mode query from 3.6; 3.3a does not, and
		// nothing depends on it - Codex and OpenCode both read OSC 11.
		t.Skipf("tmux %s does not answer the theme-mode query", l.TmuxVersion())
	}
	if !strings.Contains(got, "\x1b[?997;2n") {
		t.Fatalf("the theme mode should read as light, got %q", got)
	}
}

func readWhenWritten(t *testing.T, path string) string {
	t.Helper()
	var data []byte
	waitFor(t, 10*time.Second, "the pane probe to write its answers", func() bool {
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 {
			return false
		}
		data = b
		return true
	})
	return string(data)
}

// TestPaneDeathIsReported goes through the global hook, which is the fast one
// of the three detectors.
func TestPaneDeathIsReported(t *testing.T) {
	exits := make(chan int, 4)
	l := newLab(t, func(c *Config) {
		c.OnExit = func(id string, status int) { exits <- status }
	})
	row := l.create(shellSpec(t.TempDir(), "/bin/sh", "-c", "exit 7"))
	select {
	case status := <-exits:
		if status != 7 {
			t.Fatalf("the hook reported status %d, want 7", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pane-died hook never arrived")
	}
	waitFor(t, 5*time.Second, "the row to record the exit", func() bool {
		got, err := l.store.GetSession(row.ID)
		return err == nil && got.State == store.StateExited && got.ExitStatus == 7
	})
}

// TestDeadPaneKeepsLastScreen is why remain-on-exit-format is emptied: with
// tmux's own format, a client attaching after the pane died sees "Pane is
// dead" *instead of* the screen the user was looking at.
func TestDeadPaneKeepsLastScreen(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir(), "/bin/sh", "-c", "echo the-last-thing-it-said; exit 3"))
	waitFor(t, 10*time.Second, "the pane to die", func() bool {
		return l.tmuxOut("list-panes", "-t", row.TmuxName, "-F", "#{pane_dead}") == "1"
	})
	v := l.attach(row.ID, 100, 30)
	seen := waitForRing(t, v, 5*time.Second, "the-last-thing-it-said")
	if strings.Contains(seen, "Pane is dead") {
		t.Fatalf("tmux's own dead-pane line replaced the screen:\n%q", seen)
	}
}

// TestPipePaneWithoutToggle covers the trap that would silently close the
// journal: -o toggles rather than turns on.
func TestPipePaneWithoutToggle(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := l.attachJournal(ctx, row.ID, row.TmuxName); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if got := l.tmuxOut("display-message", "-p", "-t", row.TmuxName, "-F", "#{pane_pipe}"); got != "1" {
			t.Fatalf("after %d calls pane_pipe is %q, want 1 every time", i+1, got)
		}
	}
}

func TestJournalRecordsThePane(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	v := l.attach(row.ID, 100, 30)
	if _, err := v.Write([]byte("echo journalled-line\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the journal to record the pane", func() bool {
		data, err := TailJournal(l.dataDir, row.ID, 1<<20)
		return err == nil && strings.Contains(string(data), "journalled-line")
	})
}

// TestCreateFailureMarksRowFailed: the row is written before any tmux command
// runs, so a refusal has somewhere to live. The session shows up in the list
// as failed, with what tmux said, rather than vanishing or sitting in
// "starting" for ever.
func TestCreateFailureMarksRowFailed(t *testing.T) {
	l := newLab(t)
	dir := t.TempDir()
	spec := shellSpec(dir)
	spec.ID = NewID()
	ctx := context.Background()
	// Take the name first, the way a create retried after a partial failure
	// would find it taken.
	if _, err := l.tmux.RunConf(ctx, "new-session", "-d", "-s", TmuxName(spec.ID), "-c", dir, "--", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	row, err := l.Create(ctx, spec)
	if err == nil {
		t.Fatal("creating a session under a name tmux already has should fail")
	}
	if row == nil {
		t.Fatal("the row must exist even when tmux refused, or the failure has nowhere to live")
	}
	stored, getErr := l.store.GetSession(row.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.State != store.StateFailed {
		t.Fatalf("the row is %q, want failed", stored.State)
	}
	if !strings.Contains(strings.ToLower(stored.FailReason), "duplicate session") {
		t.Fatalf("fail_reason should carry what tmux said, got %q", stored.FailReason)
	}
}

func TestDetachClientExitsZero(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	v := l.attach(row.ID, 100, 30)
	if err := v.Close(); err != nil {
		t.Fatalf("closing the viewer: %v", err)
	}
	if code := v.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("the tmux client exited %d; detach-client should make it 0", code)
	}
	if got := l.tmuxOut("list-sessions", "-F", "#{session_name}"); got != row.TmuxName {
		t.Fatalf("the session should survive a detach, list-sessions says %q", got)
	}
}

// TestGlobalHooksFireForEverySession covers the scoping trap and the format
// variables.
//
// Both hooks are global and set once, because a session scoped session-closed
// hook never fires at all and `set-hook -g -t <sess>` sets the global one
// while ignoring the -t. The two events do not name their session the same
// way either: pane-died has #{session_name}, and session-closed has only
// #{hook_session_name}, the session itself being gone by then. Getting either
// wrong leaves the row untouched, which is what this asserts against.
//
// Nothing polls here, so every state change below arrived through the hook.
func TestGlobalHooksFireForEverySession(t *testing.T) {
	l := newLab(t)

	// A session created *after* the hooks were set, which is the case a per
	// session hook would have missed.
	died := l.create(shellSpec(t.TempDir(), "/bin/sh", "-c", "exit 5"))
	waitFor(t, 15*time.Second, "the pane-died hook to name its session", func() bool {
		row, err := l.store.GetSession(died.ID)
		return err == nil && row.State == store.StateExited && row.ExitStatus == 5
	})

	closed := l.create(shellSpec(t.TempDir()))
	ctx := context.Background()
	if _, err := l.tmux.Run(ctx, "kill-session", "-t", closed.TmuxName); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "the session-closed hook to name its session", func() bool {
		row, err := l.store.GetSession(closed.ID)
		return err == nil && row.State == store.StateNeedsResume
	})
	if row, err := l.store.GetSession(died.ID); err != nil || row.ExitStatus != 5 {
		t.Fatalf("the second hook disturbed the first session's row: %+v, %v", row, err)
	}
}

// TestSessionSurvivesManagerKill proves that a session outlives the process
// that made it.
//
// It proves survival of a *process* kill - the crash and the SIGTERM case -
// and nothing else. It says nothing about a cgroup kill, which is what
// `systemctl restart` does by default; that is what KillMode=process in
// deploy/socrates.service and `systemd-run --scope` in supervise.go exist to
// prevent, and it is verified by hand on a machine with systemd, not here.
func TestSessionSurvivesManagerKill(t *testing.T) {
	bin := requireTmux(t)
	dir := t.TempDir()
	id := NewID()
	ready := filepath.Join(dir, "ready")

	cmd := exec.Command(sinkBinary(t), "spawn-session",
		"--data", dir, "--tmux", bin, "--id", id, "--ready", ready)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	waitFor(t, 30*time.Second, "the other process to create its session", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	tm := &Tmux{Sock: filepath.Join(dir, "tmux.sock"), Conf: filepath.Join(dir, "tmux.conf"), Bin: bin}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = tm.Run(ctx, "kill-server")
	})
	ctx := context.Background()
	out, err := tm.Run(ctx, "list-panes", "-a", "-F", "#{session_name}|#{pane_dead}")
	if err != nil {
		t.Fatalf("the tmux server did not survive the process that started it: %v", err)
	}
	if want := TmuxName(id) + "|0"; !strings.Contains(out, want) {
		t.Fatalf("list-panes = %q, want a live %s", out, want)
	}

	// A new Manager over the same data directory takes it back.
	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, err := New(st, Config{DataDir: dir, TmuxBin: bin, WindowSize: "manual",
		Logf: func(f string, a ...any) { t.Logf(f, a...) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.StateRunning {
		t.Fatalf("after adoption the session is %q, want running", row.State)
	}
}

// TestSignalKilledPaneIsReported: a pane killed by a signal has an *empty*
// #{pane_dead_status} and a #{pane_dead_signal} instead. Unquoted, the empty
// expansion would leave the hook's --status without a value and the subcommand
// would exit on its own arguments - so a crash, the one death the fast
// detector exists for, would only ever be noticed by the poll.
func TestSignalKilledPaneIsReported(t *testing.T) {
	exits := make(chan int, 4)
	l := newLab(t, func(c *Config) {
		c.OnExit = func(id string, status int) { exits <- status }
	})
	row := l.create(shellSpec(t.TempDir(), "/bin/sh", "-c", "kill -9 $$"))
	select {
	case status := <-exits:
		if status != 128+9 {
			t.Fatalf("the hook reported %d, want %d for a SIGKILL", status, 128+9)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no pane-died hook for a signal death; only the poll would have caught it")
	}
	got, err := l.store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateExited || got.ExitStatus != 128+9 {
		t.Fatalf("the row is %q with status %d", got.State, got.ExitStatus)
	}
	// And the poll agrees, rather than overwriting it with "unknown".
	l.Poll(context.Background())
	if got, _ = l.store.GetSession(row.ID); got.ExitStatus != 128+9 {
		t.Fatalf("the poll changed the status to %d", got.ExitStatus)
	}
}

// TestAttachToMissingSessionFailsAtOnce: tmux answers "can't find session"
// immediately, and retrying that for five seconds would make every attach to a
// session that has just died cost five seconds.
func TestAttachToMissingSessionFailsAtOnce(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	ctx := context.Background()
	if _, err := l.tmux.Run(ctx, "kill-session", "-t", row.TmuxName); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := l.Attach(ctx, row.ID, "", 80, 24); err == nil {
		t.Fatal("attaching to a session that is gone should fail")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the failed attach took %s; it should be immediate", took)
	}
}

// TestViewerEndingHandsTheSizeOn: a client detached from outside is gone as a
// viewer straight away. Section A.7 passes ownership when the connection is
// lost, and a pseudo terminal that ended is as lost as a socket that dropped -
// waiting for somebody to call Close would pin a laptop's window at a phone's
// size.
func TestViewerEndingHandsTheSizeOn(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	l.attach(row.ID, 100, 30)
	phone := l.attach(row.ID, 60, 20)
	size := func() string {
		return l.tmuxOut("display-message", "-p", "-t", row.TmuxName, "-F", "#{window_width}x#{window_height}")
	}
	if got := size(); got != "60x20" {
		t.Fatalf("the phone should own the size, window is %s", got)
	}

	// Somebody else detaches that client: the viewer never hears about it
	// except as its pseudo terminal ending.
	l.tmuxOut("detach-client", "-t", phone.TTY())
	select {
	case <-phone.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the viewer did not notice its client going away")
	}
	waitFor(t, 3*time.Second, "the size to be handed on", func() bool { return size() == "100x30" })
	if n := len(l.Viewers(row.ID)); n != 1 {
		t.Fatalf("%d viewers are still registered, want 1", n)
	}
	if _, err := phone.Write([]byte("x")); err == nil {
		t.Fatal("writing to a viewer whose terminal ended should fail")
	}
	if err := phone.Close(); err != nil {
		t.Fatalf("Close after the terminal ended: %v", err)
	}
}

// TestStaleHookIsCorrectedByThePoll: the poll is the authority. A pane-died
// that arrives after the session was relaunched under the same name must not
// leave a running terminal behind the exit overlay.
func TestStaleHookIsCorrectedByThePoll(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))

	// A hook naming a session whose pane is alive is ignored outright.
	l.HandleHook(Hook{Event: HookPaneDied, Session: row.TmuxName, Status: "1"})
	if got, _ := l.store.GetSession(row.ID); got.State != store.StateRunning {
		t.Fatalf("a hook for a live pane moved the row to %q", got.State)
	}

	// And if a row is exited while its pane is alive for any other reason, the
	// poll puts it back.
	if err := l.store.SetSessionState(row.ID, store.StateExited, 1, ""); err != nil {
		t.Fatal(err)
	}
	l.Poll(context.Background())
	got, err := l.store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRunning {
		t.Fatalf("the poll left a live session at %q", got.State)
	}
}

// TestRelaunchAfterExit: restarting from the exit overlay is the resume path
// with the state forced, and after an exit the tmux session is still there
// with a dead pane in it. Without clearing that husk, new-session is refused
// as a duplicate and pressing Restart produces a failed session.
func TestRelaunchAfterExit(t *testing.T) {
	l := newLab(t)
	spec := shellSpec(t.TempDir(), "/bin/sh", "-c", "exit 3")
	row := l.create(spec)
	waitFor(t, 10*time.Second, "the program to exit", func() bool {
		got, err := l.store.GetSession(row.ID)
		return err == nil && got.State == store.StateExited
	})
	if err := l.store.SetSessionState(row.ID, store.StateNeedsResume, -1, ""); err != nil {
		t.Fatal(err)
	}
	got, err := l.store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	live := shellSpec(spec.Workdir, "/bin/sh")
	if err := l.Relaunch(context.Background(), got, live.Plan); err != nil {
		t.Fatalf("Relaunch over a session with a dead pane: %v", err)
	}
	if got, _ = l.store.GetSession(row.ID); got.State != store.StateRunning {
		t.Fatalf("after Relaunch the session is %q: %s", got.State, got.FailReason)
	}
	v := l.attach(row.ID, 100, 30)
	if _, err := v.Write([]byte("echo alive-again\n")); err != nil {
		t.Fatal(err)
	}
	waitForRing(t, v, 5*time.Second, "alive-again")

	// A session that is still running is never replaced, even here.
	if err := l.Relaunch(context.Background(), got, live.Plan); err == nil {
		t.Fatal("Relaunch over a live pane should be refused")
	}
}

// TestJournalRotatesUnderTmux is the rotation test with the real pipe-pane
// behind it: a session that writes more than the limit while it runs, rotated
// by the sink tmux started, with the pipe still open afterwards.
func TestJournalRotatesUnderTmux(t *testing.T) {
	l := newLab(t, func(c *Config) { c.JournalMaxBytes = 1 << 20; c.JournalKeep = 1 })
	const line = "0123456789abcdefghijklmnopqrstuvwxyz"
	row := l.create(shellSpec(t.TempDir(), "/bin/sh", "-c",
		"yes "+line+" | head -c 3500000; printf '\\nEND-OF-BURST\\n'; sleep 60"))
	journal := JournalPath(l.dataDir, row.ID)
	waitFor(t, 60*time.Second, "the burst to reach the journal", func() bool {
		data, err := TailJournal(l.dataDir, row.ID, 4096)
		return err == nil && strings.Contains(string(data), "END-OF-BURST")
	})

	older, err := os.Stat(rotatedPath(journal, 1))
	if err != nil {
		t.Fatalf("nothing was rotated: %v", err)
	}
	current, err := os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	if older.Size() != 1<<20 || current.Size() > 1<<20 {
		t.Fatalf("sizes are wrong: current %d, rotated %d", current.Size(), older.Size())
	}
	if _, err := os.Stat(rotatedPath(journal, 2)); err == nil {
		t.Fatal("keep=1 should leave exactly one older file")
	}
	if got := l.tmuxOut("display-message", "-p", "-t", row.TmuxName, "-F", "#{pane_pipe}"); got != "1" {
		t.Fatalf("pane_pipe is %q after a rotation; the sink was lost", got)
	}

	// Nothing is lost across the boundary: every line between the two files is
	// whole.
	oldBytes, err := os.ReadFile(rotatedPath(journal, 1))
	if err != nil {
		t.Fatal(err)
	}
	newBytes, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	whole := strings.ReplaceAll(string(append(oldBytes, newBytes...)), "\r\n", "\n")
	whole = whole[:strings.Index(whole, "END-OF-BURST")]
	lines := strings.Split(strings.TrimSpace(whole), "\n")
	for i, got := range lines[1 : len(lines)-1] {
		if got != line {
			t.Fatalf("line %d is broken across the rotation: %q", i, got)
		}
	}
}

// TestAttachDetachLeavesNothingBehind: a phone on a bad connection attaches
// and detaches all day. Two hundred cycles must leave no goroutine, no file
// descriptor and no tmux client behind.
func TestAttachDetachLeavesNothingBehind(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	ctx := context.Background()

	openFDs := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skip("this system does not expose /proc/self/fd")
		}
		return len(entries)
	}
	// One cycle first, so that everything lazily created exists before the
	// numbers are taken.
	v, err := l.Attach(ctx, row.ID, "warmup", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	goroutines, fds := runtime.NumGoroutine(), openFDs()

	for i := 0; i < 200; i++ {
		v, err := l.Attach(ctx, row.ID, "phone", 80, 24)
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if err := v.Close(); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	if n := len(l.Viewers(row.ID)); n != 0 {
		t.Fatalf("%d viewers are still registered", n)
	}
	if clients := l.tmuxOut("list-clients", "-F", "#{client_tty}"); clients != "" {
		t.Fatalf("tmux still has clients: %q", clients)
	}
	if now := runtime.NumGoroutine(); now > goroutines+4 {
		t.Fatalf("goroutines went from %d to %d", goroutines, now)
	}
	if now := openFDs(); now > fds+4 {
		t.Fatalf("file descriptors went from %d to %d", fds, now)
	}
}

// TestConcurrentViewers: eight browsers attaching, resizing, typing and
// leaving at once. The size bookkeeping and the viewer list are shared state,
// and this is what says so under -race.
func TestConcurrentViewers(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for k := 0; k < 5; k++ {
				v, err := l.Attach(ctx, row.ID, "viewer-"+strconv.Itoa(i), 80+i, 24+k)
				if err != nil {
					t.Errorf("attach: %v", err)
					return
				}
				_ = v.Resize(ctx, 70+i, 20+k)
				_, _ = v.Write([]byte("echo x\n"))
				if err := v.Close(); err != nil {
					t.Errorf("close: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if n := len(l.Viewers(row.ID)); n != 0 {
		t.Fatalf("%d viewers left registered", n)
	}
}

// TestPaneStateTellsTheThreeCasesApart is the fix for the reboot blocker,
// pinned at the level it went wrong.
//
// tmux 3.6 answers `display-message -p -t <missing> -F '#{pane_dead}'` with
// success and an empty line rather than with an error. Folded into "is the
// pane dead?", that empty line read as "no, so the session is still running" -
// and a session that did not exist at all then refused its own relaunch and
// its own deletion. The three answers must stay apart.
func TestPaneStateTellsTheThreeCasesApart(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()

	live := l.create(shellSpec(t.TempDir()))
	dead := l.create(shellSpec(t.TempDir(), "/bin/sh", "-c", "exit 4"))
	waitFor(t, 10*time.Second, "the second program to exit", func() bool {
		got, err := l.store.GetSession(dead.ID)
		return err == nil && got.State == store.StateExited
	})

	for _, c := range []struct {
		what string
		name string
		want paneState
	}{
		{"a running program", live.TmuxName, paneLive},
		{"a program that exited", dead.TmuxName, paneDead},
		{"a session that is not there", TmuxName("0123456789abcdef0123456789abcdef"), paneMissing},
		{"no session name at all", "", paneMissing},
	} {
		got, err := l.paneStateOf(ctx, c.name)
		if err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		if got != c.want {
			t.Fatalf("%s: paneStateOf = %d, want %d", c.what, got, c.want)
		}
		if wantDead := c.want != paneLive; l.paneIsDead(c.name) != wantDead {
			t.Fatalf("%s: paneIsDead = %t, want %t", c.what, !wantDead, wantDead)
		}
	}

	// And with the whole server gone, everything is missing - the reboot case.
	if _, err := l.tmux.Run(ctx, "kill-server"); err != nil {
		t.Fatal(err)
	}
	got, err := l.paneStateOf(ctx, live.TmuxName)
	if err != nil {
		t.Fatalf("after the server was killed: %v", err)
	}
	if got != paneMissing {
		t.Fatalf("with no tmux server the pane state is %d, want missing", got)
	}
	// Which is what lets a relaunch go ahead rather than answering 409.
	if err := l.clearDeadSession(ctx, live.TmuxName); err != nil {
		t.Fatalf("clearDeadSession on a session that is gone: %v", err)
	}
}

// TestALongDataDirectoryIsDiagnosed: a data directory whose path is longer
// than a Unix socket name is a configuration mistake with an unreadable
// symptom - `bind: invalid argument` from the hook listener, and a
// four-hundred-character tmux command line quoted back as the reason a session
// could not start. It is refused up front instead, in words that say what to
// change.
func TestALongDataDirectoryIsDiagnosed(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, strings.Repeat("d", 90), strings.Repeat("e", 40))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m, err := New(st, Config{DataDir: deep, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("the manager should still be built: %v", err)
	}
	unavailable := m.Available()
	if unavailable == nil {
		t.Fatal("a data directory too long for a socket was accepted")
	}
	for _, want := range []string{"too long", "-data"} {
		if !strings.Contains(unavailable.Error(), want) {
			t.Errorf("the reason does not mention %q: %v", want, unavailable)
		}
	}
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("Start should refuse rather than fail on a bind nobody can read")
	}
	// And a directory of an ordinary length is not caught by it.
	if err := CheckSocketPaths(root); err != nil {
		t.Errorf("an ordinary data directory was refused: %v", err)
	}
}
