//go:build !windows

package termux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The tests in this package run against the real tmux, never a fake one.
//
// A fake would test our idea of tmux, and almost everything this package
// exists for is a fact about the real one that contradicts what its
// documentation suggests: a global window-size manual segfaults the server on
// the next session, window-size latest follows typing rather than attaching,
// pipe-pane -o is a toggle, a session scoped session-closed hook never fires,
// and an explicit window style makes tmux answer colour queries itself with
// nobody attached. None of those could be learned from a stub.
//
// Every server here lives on its own socket under t.TempDir() and is killed
// when the test ends. Nothing touches the user's tmux or their tmux.conf.

// requireTmux skips a test when tmux is missing or older than the version the
// generated configuration needs.
func requireTmux(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed; skipping the substrate tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := BinaryVersion(ctx, bin)
	if err != nil {
		t.Skipf("could not read the tmux version: %v", err)
	}
	if !v.OK() {
		t.Skipf("tmux %s is older than %d.%d", v, MinMajor, MinMinor)
	}
	return bin
}

// lab is a Manager on a private socket, with its own data directory and
// database.
type lab struct {
	*Manager
	t       *testing.T
	dataDir string
	store   *store.Store
}

func newLab(t *testing.T, tweak ...func(*Config)) *lab {
	t.Helper()
	bin := requireTmux(t)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatalf("could not open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := Config{
		DataDir:     dir,
		TmuxBin:     bin,
		SocratesBin: sinkBinary(t),
		WindowSize:  "manual",
		Logf:        func(format string, args ...any) { t.Logf(format, args...) },
	}
	for _, f := range tweak {
		f(&cfg)
	}
	m, err := New(st, cfg)
	if err != nil {
		t.Fatalf("could not build a manager: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("could not start the manager: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = m.tmux.Run(ctx, "kill-server")
	})
	return &lab{Manager: m, t: t, dataDir: dir, store: st}
}

// tmuxOut runs a tmux command on the lab's socket and returns its output.
func (l *lab) tmuxOut(args ...string) string {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := l.tmux.Run(ctx, args...)
	if err != nil {
		l.t.Fatalf("tmux %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(out)
}

// shellSpec is a session running a plain shell, which is the one harness that
// needs no fake.
func shellSpec(cwd string, argv ...string) Spec {
	if len(argv) == 0 {
		argv = []string{"/bin/sh"}
	}
	return Spec{
		Harness:     "shell",
		Workdir:     cwd,
		WorkdirMode: store.WorkdirCustom,
		Cols:        100,
		Rows:        30,
		Plan: harnesses.LaunchPlan{
			Argv: argv,
			Cwd:  cwd,
			Env:  map[string]string{"SOCRATES": "1"},
		},
	}
}

func (l *lab) create(spec Spec) *store.Session {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	row, err := l.Create(ctx, spec)
	if err != nil {
		l.t.Fatalf("could not create a session: %v", err)
	}
	return row
}

func (l *lab) attach(id string, cols, rows int) *Viewer {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, err := l.Attach(ctx, id, "", cols, rows)
	if err != nil {
		l.t.Fatalf("could not attach a viewer: %v", err)
	}
	l.t.Cleanup(func() { _ = v.Close() })
	return v
}

// waitFor polls until the condition holds, and fails the test when it does not.
func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", within, what)
}

// waitForRing waits until a viewer's ring contains the text.
func waitForRing(t *testing.T, v *Viewer, within time.Duration, want string) string {
	t.Helper()
	var seen string
	waitFor(t, within, "the terminal to show "+want, func() bool {
		out, _ := v.Ring().Since(v.Ring().Base())
		seen = string(out)
		return strings.Contains(seen, want)
	})
	return seen
}

// sinkBinary is the test binary itself, standing in for the Socrates
// executable so that the journal sink and the tmux hooks are the real code
// paths rather than a stub. TestMain dispatches on the first argument.
func sinkBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("could not find the test binary: %v", err)
	}
	return exe
}
