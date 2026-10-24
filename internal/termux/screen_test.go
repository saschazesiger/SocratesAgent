//go:build !windows

package termux

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// These run against the real tmux, on a socket under t.TempDir(), because
// every one of them is a fact about tmux rather than about our code: what
// `#{window_activity}` counts in, whether `#{pane_current_command}` follows the
// foreground job, and whether send-keys needs a viewer.

// TestCapturePaneReadsWhatThePanePrinted, with nobody attached.
func TestCapturePaneReadsWhatThePanePrinted(t *testing.T) {
	l := newLab(t)
	dir := realDir(t, l.dataDir)
	row := l.create(shellSpec(dir, "/bin/sh"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := l.SendKeys(ctx, row.ID, []Key{
		{Text: "echo capture-me-please"}, {Name: "Enter", Wait: 200 * time.Millisecond},
	}); err != nil {
		t.Fatalf("could not type into the pane: %v", err)
	}

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

// TestVisiblePaneLeavesTheHistoryOut: a program that clears its screen
// leaves the old one in the history (scroll-on-clear), so a line it took away
// is still in a capture that reaches back and must not be in the visible one.
func TestVisiblePaneLeavesTheHistoryOut(t *testing.T) {
	l := newLab(t)
	dir := realDir(t, l.dataDir)
	row := l.create(shellSpec(dir, "/bin/sh"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := l.SendKeys(ctx, row.ID, []Key{
		{Text: `printf '1 background terminal running\n'; printf '\033[2J\033[H'; echo after-the-clear`},
		{Name: "Enter", Wait: 200 * time.Millisecond},
	}); err != nil {
		t.Fatalf("could not type into the pane: %v", err)
	}
	var visible string
	waitFor(t, 10*time.Second, "the screen after the clear", func() bool {
		var err error
		visible, err = l.visiblePane(ctx, row.ID)
		return err == nil && strings.Contains(visible, "after-the-clear")
	})
	if hasLine(visible, "1 background terminal running") {
		t.Fatalf("the visible screen kept a line the program cleared away:\n%s", visible)
	}
	history, err := l.CapturePane(ctx, row.ID, 40)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !hasLine(history, "1 background terminal running") {
		t.Logf("this tmux did not keep the cleared screen in its history:\n%s", history)
	}
}

// hasLine is whether a screen has a line that is exactly want.
func hasLine(screen, want string) bool {
	for _, line := range strings.Split(screen, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// TestSendKeysNeedsNoViewer is the whole reason the operator loop runs on the
// server: a run has to keep working with every browser closed.
func TestSendKeysNeedsNoViewer(t *testing.T) {
	l := newLab(t)
	dir := realDir(t, l.dataDir)
	row := l.create(shellSpec(dir, "/bin/sh"))
	if got := l.hub(); got != 0 {
		t.Fatalf("%d viewers are attached; this test wants none", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	marker := "no-viewer-here"
	// A named key and hex text in one call, with a pause between them.
	if err := l.SendKeys(ctx, row.ID, []Key{
		{Text: "echo " + marker},
		{Wait: 100 * time.Millisecond},
		{Name: "Enter"},
	}); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
	waitFor(t, 10*time.Second, "the pane to run what was typed", func() bool {
		screen, err := l.CapturePane(ctx, row.ID, 40)
		return err == nil && strings.Count(screen, marker) >= 2
	})

	// Text is sent as hex, so a backslash is a backslash on every tmux.
	if err := l.SendKeys(ctx, row.ID, []Key{
		{Text: `echo back\\slash`}, {Name: "Enter"},
	}); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
	waitFor(t, 10*time.Second, "the backslash to survive", func() bool {
		screen, err := l.CapturePane(ctx, row.ID, 40)
		return err == nil && strings.Contains(screen, `back\slash`)
	})
}

// TestSendKeysAndCaptureRefuseAMissingSession: a session that is not there is
// an error and never a silent success.
func TestSendKeysAndCaptureRefuseAMissingSession(t *testing.T) {
	l := newLab(t)
	_ = l.create(shellSpec(realDir(t, l.dataDir), "/bin/sh"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := l.CapturePane(ctx, "nosuchsession", 10); err == nil {
		t.Fatal("capturing a session that is not there succeeded")
	}
	if err := l.SendKeys(ctx, "nosuchsession", []Key{{Name: "Enter"}}); err == nil {
		t.Fatal("typing into a session that is not there succeeded")
	}
}

// TestActivityPaneFormatParsesOnThisTmux checks the six fields the detector
// asks for against the tmux on this machine: all six have to populate, and
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
	if line.title == "" {
		t.Fatal("#{pane_title} was empty; the detector's Codex layer lives on it")
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
	if err := l.SendKeys(ctx, row.ID, []Key{{Text: "sleep 4"}, {Name: "Enter"}}); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
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
	if err := l.SendKeys(ctx, row.ID, []Key{{Text: "sleep 5"}, {Name: "Enter"}}); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
	waitFor(t, 10*time.Second, "the session to become busy", func() bool {
		l.act.tick(ctx, time.Now())
		return l.ActivityOf(row.ID).State == StateBusy
	})
	waitFor(t, 20*time.Second, "the session to go back to idle", func() bool {
		l.act.tick(ctx, time.Now())
		return l.ActivityOf(row.ID).State == StateIdle
	})
	if !l.ActivityOf(row.ID).Unread {
		t.Fatal("a command that finished while nobody typed leaves the row unread")
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

// TestShellIsKnownUnderAnotherShellsName: macOS's /bin/sh execs bash, and
// tmux there names the foreground process by its executable, so a /bin/sh at
// its prompt is "bash". A shell's plan whose foreground command is a shell is
// the shell; anything else in the foreground is not.
func TestShellIsKnownUnderAnotherShellsName(t *testing.T) {
	plan := func(argv ...string) harnesses.LaunchPlan { return harnesses.LaunchPlan{Argv: argv} }
	cases := []struct {
		cmd  string
		plan harnesses.LaunchPlan
		want bool
	}{
		{"sh", plan("/bin/sh", "-l"), true},
		{"bash", plan("/bin/sh", "-l"), true}, // macOS
		{"zsh", plan("/bin/sh"), true},        // macOS, with /private/var/select/sh changed
		{"-bash", plan("/bin/bash", "-l"), true},
		{"sleep", plan("/bin/sh", "-l"), false},
		{"vim", plan("/bin/bash"), false},
		// A program somebody configured as their shell is only ever itself.
		{"bash", plan("/usr/local/bin/xonsh"), false},
		{"xonsh", plan("/usr/local/bin/xonsh"), true},
		// No plan to read: the names alone.
		{"bash", plan(), true},
		{"python3", plan(), false},
	}
	for _, c := range cases {
		if got := isTheShell(c.cmd, c.plan); got != c.want {
			t.Errorf("isTheShell(%q, %q) = %t, want %t", c.cmd, c.plan.Argv, got, c.want)
		}
	}
}

// hub is how many viewers this lab has attached, which the send-keys test
// needs to be none.
func (l *lab) hub() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, live := range l.live {
		n += len(live.viewers)
	}
	return n
}
