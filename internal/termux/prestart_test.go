//go:build !windows

package termux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// prepare stores a prepared row the way the server does: no tmux name, no
// directory, nothing started.
func (l *lab) prepare(id string) *store.Session {
	l.t.Helper()
	row := &store.Session{
		ID: id, Title: "Prepared", Harness: "shell", WorkdirMode: store.WorkdirDynamic,
		State: store.StatePrestart, StartAt: time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := l.store.CreateSession(row); err != nil {
		l.t.Fatal(err)
	}
	got, err := l.store.GetSession(id)
	if err != nil {
		l.t.Fatal(err)
	}
	return got
}

// Every loop that looks at sessions leaves a prepared one exactly as it was:
// the reconcile at start-up (with and without a tmux server), the lifecycle
// poll, the activity tick and the hooks. And every way of opening a terminal
// on one refuses, because there is none.
func TestPrestartRowSurvivesEveryLoop(t *testing.T) {
	l := newLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before := l.prepare("prep1")

	// No tmux server yet: Adopt's reboot path.
	if err := l.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	// A server with an unrelated session on it: Adopt's ordinary path, the
	// poll and the detector.
	other := shellSpec(realDir(t, t.TempDir()))
	other.ID = "other1"
	l.create(other)
	if err := l.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pollTolerance+1; i++ {
		l.Poll(ctx)
	}
	l.act.tick(ctx, time.Now())
	l.HandleHook(Hook{Event: HookSessionClosed, Session: TmuxName("prep1")})
	l.HandleHook(Hook{Event: HookPaneDied, Session: TmuxName("prep1"), Status: "0"})

	after, err := l.store.GetSession("prep1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a loop changed the prepared row:\n%#v\n%#v", before, after)
	}

	if _, err := l.Ensure(ctx, "prep1"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Ensure on a prepared session: %v", err)
	}
	if _, err := l.Restart(ctx, "prep1"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Restart on a prepared session: %v", err)
	}
	if _, err := l.Attach(ctx, "prep1", "", 80, 24); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Attach on a prepared session: %v", err)
	}
	// Filing it as done closes nothing and does not make it resumable.
	if err := l.Park(ctx, "prep1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.store.GetSession("prep1"); got.State != store.StatePrestart {
		t.Fatalf("parking a prepared session made it %s", got.State)
	}
	// And deleting it works, without a terminal to kill.
	if err := l.Delete(ctx, "prep1"); err != nil {
		t.Fatal(err)
	}
}

// StartPrestart is Create on a row that already exists: the row takes on what
// the start resolved and runs. A second start finds it started and says so,
// and the prompt file is recorded in plan.json by name only.
func TestStartPrestartLaunchesOnce(t *testing.T) {
	l := newLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l.prepare("prep2")
	cwd := realDir(t, t.TempDir())
	spec := shellSpec(cwd)
	spec.ID = "prep2"
	spec.Options = json.RawMessage(`{"ultracode":true}`)
	promptPath := filepath.Join(SessionDir(l.dataDir, "prep2"), harnesses.InitialPromptFile)
	spec.Plan.Files = []harnesses.GenFile{{Path: promptPath, Mode: 0o600, Data: []byte("the secret prompt"), Transient: true}}

	row, err := l.StartPrestart(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.StateRunning || row.TmuxName != TmuxName("prep2") || row.Workdir != cwd ||
		row.StartAt != 0 || string(row.Options) != `{"ultracode":true}` {
		t.Fatalf("started row = %#v", row)
	}
	if out := l.tmuxOut("has-session", "-t", TmuxName("prep2")); out != "" {
		t.Fatalf("has-session said %q", out)
	}
	raw, err := os.ReadFile(filepath.Join(SessionDir(l.dataDir, "prep2"), "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dGhlIHNlY3JldCBwcm9tcHQ") || !strings.Contains(string(raw), harnesses.InitialPromptFile) {
		t.Fatalf("plan.json carries the prompt, or not its name:\n%s", raw)
	}

	again, err := l.StartPrestart(ctx, spec)
	if !errors.Is(err, store.ErrNotPrestart) || again == nil || again.State != store.StateRunning {
		t.Fatalf("a second start: %#v %v", again, err)
	}
}

// A start that fails before anything ran puts the row back to prepared, with
// the reason, and leaves nothing behind on disk.
func TestStartPrestartRevertsWhenNothingRan(t *testing.T) {
	l := newLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prepared := l.prepare("prep3")
	root := t.TempDir()
	cwd := filepath.Join(root, "dynamic")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := shellSpec(cwd)
	spec.ID = "prep3"
	spec.WorkdirMode = store.WorkdirDynamic
	// A generated file that cannot be written fails the launch before tmux
	// is ever asked for a session.
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	spec.Plan.Files = []harnesses.GenFile{{Path: filepath.Join(blocker, "under-a-file"), Data: []byte("x")}}

	row, err := l.StartPrestart(ctx, spec)
	if err == nil {
		t.Fatal("the start did not fail")
	}
	if row == nil || row.State != store.StatePrestart || row.TmuxName != "" || row.FailReason == "" ||
		row.Workdir != prepared.Workdir || row.StartAt != 0 {
		t.Fatalf("after a failed start = %#v", row)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatalf("the empty dynamic directory was left behind: %v", err)
	}
	if _, err := os.Stat(SessionDir(l.dataDir, "prep3")); !os.IsNotExist(err) {
		t.Fatalf("the session directory was left behind: %v", err)
	}
}

// The first prompt is sent once. A resume, and the Restart button, plan from
// the stored row - and the row has no prompt to give them.
func TestPlansFromTheRowNeverCarryAPrompt(t *testing.T) {
	l := newLab(t, func(c *Config) { c.Settings = func() config.Settings { return config.Default() } })
	row := &store.Session{ID: "r1", Harness: "claude", Workdir: "/srv", CLISessionID: "conv",
		Options: json.RawMessage(`{"profile":"","ultracode":true}`)}
	req, err := l.planRequest(row)
	if err != nil {
		t.Fatal(err)
	}
	if req.InitialPrompt != "" {
		t.Fatalf("a plan from the row carries a prompt: %q", req.InitialPrompt)
	}
}
