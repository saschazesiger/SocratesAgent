package accounts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func codexEnv(t *testing.T, data string, a *store.Account) []string {
	t.Helper()
	if err := Prepare(data, a); err != nil {
		t.Fatal(err)
	}
	return Env(data, a, os.Environ())
}

// An API key is logged in into the account's own CODEX_HOME by codex, and the
// account reads as signed in with the limits codex reports.
func TestACodexKeyIsLoggedInByCodex(t *testing.T) {
	data := t.TempDir()
	a := &store.Account{ID: NewID(), Agent: Codex, Kind: APIKey}
	env := codexEnv(t, data, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if got := Detect(data, a); got.SignedIn {
		t.Fatalf("signed in before the key: %+v", got)
	}
	if err := CodexKeyLogin(ctx, fakeCodex, env, "sk-test"); err != nil {
		t.Fatal(err)
	}
	if got := Detect(data, a); !got.SignedIn {
		t.Fatalf("not signed in after the key: %+v", got)
	}
	usage, err := ReadCodexUsage(ctx, fakeCodex, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Windows) != 2 || usage.Windows[0].Label != "5h" || usage.Windows[1].Label != "7d" ||
		usage.Windows[1].UsedPercent != 60 || usage.Windows[0].ResetsAt == 0 {
		t.Fatalf("usage %+v", usage)
	}
	// The shared rollouts are a link, the login is the account's own file.
	if fi, err := os.Lstat(filepath.Join(Folder(data, Codex, a.ID), "sessions")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("sessions is not shared: %v %v", fi, err)
	}
}

// A ChatGPT sign-in by device code gives a page and a code, and ends signed
// in once codex says so; one that is cancelled ends with an error and no
// login.
func TestACodexDeviceSignIn(t *testing.T) {
	data := t.TempDir()
	t.Setenv("FAKECODEX_LOGIN_MS", "300")
	a := &store.Account{ID: NewID(), Agent: Codex, Kind: Subscription}
	env := codexEnv(t, data, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d, err := CodexDeviceLogin(ctx, fakeCodex, env, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.URL, "https://") || !strings.HasPrefix(d.Code, "FAKE-") {
		t.Fatalf("device %q %q", d.URL, d.Code)
	}
	select {
	case <-d.Done:
	case <-ctx.Done():
		t.Fatal("the sign-in never ended")
	}
	if d.Err() != nil {
		t.Fatal(d.Err())
	}
	if got := Detect(data, a); !got.SignedIn || got.Email != "device@example.com" || got.Plan != "plus" {
		t.Fatalf("after the sign-in: %+v", got)
	}

	other := &store.Account{ID: NewID(), Agent: Codex, Kind: Subscription}
	t.Setenv("FAKECODEX_LOGIN_MS", "60000")
	envB := codexEnv(t, data, other)
	d2, err := CodexDeviceLogin(ctx, fakeCodex, envB, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	d2.Cancel()
	select {
	case <-d2.Done:
	case <-ctx.Done():
		t.Fatal("the cancelled sign-in never ended")
	}
	if d2.Err() == nil || Detect(data, other).SignedIn {
		t.Fatalf("a cancelled sign-in: %v %+v", d2.Err(), Detect(data, other))
	}
}
