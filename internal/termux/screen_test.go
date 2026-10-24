//go:build !windows

package termux

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These run against the real tmux, on a socket under t.TempDir(), because
// every one of them is a fact about tmux rather than about our code: what
// `#{window_activity}` counts in, and whether `#{pane_current_command}`
// follows the foreground job.

// typeIn types a line into a session's pane with nobody attached, and tells
// the detector that somebody typed, as a viewer's keystrokes do.
func (l *lab) typeIn(ctx context.Context, id, line string) {
	l.t.Helper()
	name := l.tmuxNameOf(id)
	if _, err := l.tmux.Run(ctx, "send-keys", "-t", name, "-l", line); err != nil {
		l.t.Fatalf("could not type into the pane: %v", err)
	}
	if _, err := l.tmux.Run(ctx, "send-keys", "-t", name, "Enter"); err != nil {
		l.t.Fatalf("could not press Enter in the pane: %v", err)
	}
	l.NoteInput(id)
}

// TestCapturePaneReadsWhatThePanePrinted, with nobody attached.
func TestCapturePaneReadsWhatThePanePrinted(t *testing.T) {
	l := newLab(t)
	dir := realDir(t, l.dataDir)
	row := l.create(shellSpec(dir, "/bin/sh"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	l.typeIn(ctx, row.ID, "echo capture-me-please")

	var screen string
	waitFor(t, 10*time.Second, "the echo to appear on the screen", func() bool {
		var err error
		screen, err = l.CapturePane(ctx, row.ID, 40)
		return err == nil && strings.Contains(screen, "capture-me-please")
	})
	// -p without -e: no escape sequences at all, which is why nothing
	// downstream has to strip ANSI.
	if strings.Contains(screen, "\x1b") {
		t.Fatalf("the capture carried escapes:\n%q", screen)
	}
	// The trailing blank half of an empty pane is trimmed away.
	if strings.HasSuffix(screen, "\n") {
		t.Fatalf("the capture kept its trailing blank lines:\n%q", screen)
	}

	// The line count is honoured: -S -n hands back n lines of history *plus*
	// the visible pane, and the caller asked for the last two.
	short, err := l.CapturePane(ctx, row.ID, 2)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if got := len(strings.Split(short, "\n")); got > 2 {
		t.Fatalf("a two line capture returned %d lines:\n%s", got, short)
	}
}

// TestCaptureRefusesAMissingSession: a session that is not there is an error
// and never a silent success.
func TestCaptureRefusesAMissingSession(t *testing.T) {
	l := newLab(t)
	_ = l.create(shellSpec(realDir(t, l.dataDir), "/bin/sh"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := l.CapturePane(ctx, "nosuchsession", 10); err == nil {
		t.Fatal("capturing a session that is not there succeeded")
	}
}

// TestActivityPaneFormatParsesOnThisTmux checks the five fields the detector
// asks for against the tmux on this machine: all five have to populate, and
// #{window_activity} has to be the epoch second of the pane's last output.
func TestActivityPaneFormatParsesOnThisTmux(t *testing.T) {
	l := newLab(t)
	dir := realDir(t, l.dataDir)
	row := l.create(shellSpec(dir, "/bin/sh"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var line paneLine
	waitFor(t, 10*time.Second, "the pane to appear in list-panes", func() bool {
		panes, err := l.listActivityPanes(ctx)
		if err != nil {
			return false
		}
		line = panes[row.TmuxName]
		return line.session == row.TmuxName
	})
	if line.dead {
		t.Fatal("a pane that has just started is not dead")
	}
	if line.pid <= 0 {
		t.Fatalf("the pane pid parsed as %d", line.pid)
	}
	if line.command == "" {
		t.Fatal("#{pane_current_command} was empty")
	}
	// window_activity is seconds since the epoch, not milliseconds and not a
	// count: a value a minute either side of now is the only shape that makes
	// the quiescence layer's arithmetic mean anything.
	drift := time.Since(time.Unix(line.activity, 0))
	if drift < -time.Minute || drift > time.Minute {
		t.Fatalf("#{window_activity} is %d, which is %s away from now", line.activity, drift)
	}

	// The foreground command follows the job, which is the whole of the Shell
	// layer's first half.
	l.typeIn(ctx, row.ID, "sleep 4")
	waitFor(t, 10*time.Second, "the foreground command to become sleep", func() bool {
		panes, err := l.listActivityPanes(ctx)
		return err == nil && panes[row.TmuxName].command == "sleep"
	})
	// And the shell has a child while it runs, which is the second half.
	panes, err := l.listActivityPanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasChild(panes[row.TmuxName].pid) {
		t.Fatal("a shell running a command has a child process")
	}
	waitFor(t, 15*time.Second, "the foreground command to go back to the shell", func() bool {
		panes, err := l.listActivityPanes(ctx)
		return err == nil && panes[row.TmuxName].command != "sleep"
	})
}

// TestActivityTickAnswersForARealPane runs the detector's own tick against a
// real shell session, with no fake anywhere: the ladder has to reach idle from
// tmux alone.
func TestActivityTickAnswersForARealPane(t *testing.T) {
	l := newLab(t)
	dir := realDir(t, l.dataDir)
	row := l.create(shellSpec(dir, "/bin/sh"))
	// What the Shell layer saw, beside any verdict that fails: the answer is
	// one name and one process table, and both differ between systems.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		panes, err := l.listActivityPanes(ctx)
		pane := panes[row.TmuxName]
		t.Logf("activity %+v; pane command %q, pid %d (list-panes error %v); plan argv %q; the pane's process has a child: %t",
			l.ActivityOf(row.ID), pane.command, pane.pid, err, l.planFor(row.ID).Argv, hasChild(pane.pid))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	waitFor(t, 20*time.Second, "the session to settle on idle", func() bool {
		l.act.tick(ctx, time.Now())
		return l.ActivityOf(row.ID).State == StateIdle
	})
	if got := l.ActivityOf(row.ID); got.Source != sourceExact {
		t.Fatalf("a shell at its prompt was answered by %q, want the exact layer", got.Source)
	}

	// A command running in the foreground is busy, whatever it prints.
	l.typeIn(ctx, row.ID, "sleep 5")
	waitFor(t, 10*time.Second, "the session to become busy", func() bool {
		l.act.tick(ctx, time.Now())
		return l.ActivityOf(row.ID).State == StateBusy
	})
	waitFor(t, 20*time.Second, "the session to go back to idle", func() bool {
		l.act.tick(ctx, time.Now())
		return l.ActivityOf(row.ID).State == StateIdle
	})
	if !l.ActivityOf(row.ID).Unread {
		t.Fatal("a command that finished while nobody looked leaves the row unread")
	}
	l.NoteInput(row.ID)
	if l.ActivityOf(row.ID).Unread {
		t.Fatal("typing at the session did not clear the mark")
	}

	// A pane that is gone settles on unknown rather than sticking.
	if err := l.Delete(ctx, row.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	l.act.tick(ctx, time.Now())
	if got := l.ActivityOf(row.ID).State; got != StateUnknown {
		t.Fatalf("a deleted session is %q, want unknown", got)
	}
}
