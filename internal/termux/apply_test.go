//go:build !windows

package termux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// ApplyTerminal is the dashboard's terminal card reaching tmux, and the one
// invariant it must never break is the reason this package exists: a global
// `window-size` of any value takes the server down on the *next* new-session.
// So the policy is asserted in three places at once - not global, on every
// live window, and on a session created afterwards.
func TestApplyTerminalIsLiveAndNeverGlobal(t *testing.T) {
	l := newLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first := l.create(shellSpec(t.TempDir()))
	if err := l.ApplyTerminal(ctx, ConfOptions{
		HistoryLimit: 12345, Mouse: false, ExtendedKeys: true,
	}, "largest"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got := l.tmuxOut("show", "-gwv", "window-size"); got != "latest" {
		t.Fatalf("the global window-size is %q; it must stay at tmux's default", got)
	}
	if got := l.tmuxOut("show", "-wv", "-t", first.TmuxName, "window-size"); got != "largest" {
		t.Fatalf("the live session is %q, want largest", got)
	}
	for _, want := range []struct {
		args  []string
		value string
	}{
		{[]string{"show", "-gv", "history-limit"}, "12345"},
		{[]string{"show", "-gv", "mouse"}, "off"},
		{[]string{"show", "-sv", "extended-keys"}, "on"},
	} {
		if got := l.tmuxOut(want.args...); got != want.value {
			t.Errorf("%s is %q, want %q", strings.Join(want.args, " "), got, want.value)
		}
	}

	// The segfault case: a session created after the policy was applied. Both
	// have to be alive, and the new one has to carry the policy too.
	second := l.create(shellSpec(t.TempDir()))
	names := l.tmuxOut("list-sessions", "-F", "#{session_name}")
	for _, name := range []string{first.TmuxName, second.TmuxName} {
		if !strings.Contains(names, name) {
			t.Fatalf("session %s did not survive the apply: %q", name, names)
		}
	}
	if got := l.tmuxOut("show", "-wv", "-t", second.TmuxName, "window-size"); got != "largest" {
		t.Fatalf("the session created after the apply is %q, want largest", got)
	}

	conf, err := os.ReadFile(l.ConfPath())
	if err != nil {
		t.Fatalf("conf: %v", err)
	}
	if strings.Contains(string(conf), "window-size") {
		t.Fatalf("the generated configuration mentions window-size:\n%s", conf)
	}
	if !strings.Contains(string(conf), "history-limit 12345") {
		t.Fatalf("the conf was not rewritten:\n%s", conf)
	}
}

// A configuration tmux has already refused once is not written again: the
// fallback is in place precisely because the full file did not load.
func TestApplyTerminalLeavesTheFallbackConfAlone(t *testing.T) {
	l := newLab(t)
	if err := writeConf(l.ConfPath(), MinimalConf(ConfOptions{})); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.confFallback = true
	l.mu.Unlock()

	before, err := os.ReadFile(l.ConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ApplyTerminal(context.Background(), ConfOptions{HistoryLimit: 999}, "manual"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	after, err := os.ReadFile(l.ConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("the fallback configuration was overwritten:\n%s", after)
	}
}

// The race the review found by inspection: a save of the terminal card while
// sessions are being created and viewers resized. Under -race this is the
// test that fails if the policy is read without the lock again.
func TestApplyTerminalRacesWithSessionsAndViewers(t *testing.T) {
	l := newLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	row := l.create(shellSpec(t.TempDir()))
	viewer := l.attach(row.ID, 90, 24)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			policy := []string{"manual", "latest", "largest"}[i%3]
			if err := l.ApplyTerminal(ctx, ConfOptions{HistoryLimit: 1000 + i}, policy); err != nil {
				t.Errorf("apply: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 3; i++ {
		_ = l.create(shellSpec(t.TempDir()))
		if err := viewer.Resize(ctx, 80+i, 24+i); err != nil {
			t.Errorf("resize: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	if got := l.tmuxOut("show", "-gwv", "window-size"); got != "latest" {
		t.Fatalf("the global window-size ended up %q", got)
	}
}

// Redetect is what makes a successful install mean something: the manager was
// built on a machine with no tmux, one appears, and sessions become possible
// without restarting Socrates.
func TestRedetectUnlocksSessionsWhenTmuxAppears(t *testing.T) {
	bin := requireTmux(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// A PATH with nothing on it: this is a machine without tmux.
	t.Setenv("PATH", path)

	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := New(st, Config{DataDir: filepath.Join(dir, "data"), SocratesBin: sinkBinary(t),
		WindowSize: "manual", Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if m.Available() == nil {
		t.Fatal("a manager built with no tmux on PATH thinks sessions are possible")
	}

	// The installer's effect, without installing anything: tmux appears where
	// PATH can see it.
	if err := os.Symlink(bin, filepath.Join(path, "tmux")); err != nil {
		t.Fatal(err)
	}
	if err := m.Redetect(context.Background()); err != nil {
		t.Fatalf("redetect: %v", err)
	}
	if err := m.Available(); err != nil {
		t.Fatalf("sessions are still impossible after the install: %v", err)
	}
	if !m.TmuxVersion().OK() {
		t.Fatalf("no version was learned: %q", m.TmuxVersion())
	}
	if _, err := os.Stat(m.ConfPath()); err != nil {
		t.Fatalf("the generated configuration was not written: %v", err)
	}
	// And a second call is not a second hook listener.
	if err := m.Redetect(context.Background()); err != nil {
		t.Fatalf("redetect twice: %v", err)
	}
}
