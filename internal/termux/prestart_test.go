//go:build !windows

package termux

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// prepare stores a prepared row the way the server does: a chat, with no
// tmux name, no directory, nothing started.
func (l *lab) prepare(id string) *store.Session {
	l.t.Helper()
	row := &store.Session{
		ID: id, Title: "Prepared", Harness: "claude", Kind: store.KindChat, WorkdirMode: store.WorkdirDynamic,
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
