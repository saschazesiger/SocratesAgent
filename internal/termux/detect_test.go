package termux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var errNotOnPath = errors.New("not found")

// fakeTmux writes a script that answers `tmux -V` with version, and returns a
// LookPath that finds it and nothing else.
func fakeTmux(t *testing.T, version string) func(string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '"+version+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return func(file string) (string, error) {
		if file == "tmux" {
			return path, nil
		}
		return "", errNotOnPath
	}
}

func TestDetectAcceptsATmuxThatIsNewEnough(t *testing.T) {
	rep := Detector{LookPath: fakeTmux(t, "tmux 3.4")}.Detect(context.Background())
	if !rep.Installed || !rep.OK {
		t.Fatalf("expected an installed, usable tmux, got %+v", rep)
	}
	if rep.Version != "3.4" {
		t.Errorf("version = %q, want 3.4", rep.Version)
	}
	if rep.Reason != "" {
		t.Errorf("a usable tmux should need no explanation, got %q", rep.Reason)
	}
}

// The version floor is what the generated configuration needs, so a tmux below
// it is reported as present and unusable rather than as absent.
func TestDetectCallsTmux32aTooOld(t *testing.T) {
	rep := Detector{LookPath: fakeTmux(t, "tmux 3.2a")}.Detect(context.Background())
	if !rep.Installed {
		t.Fatal("3.2a is installed, it is only too old")
	}
	if rep.OK {
		t.Fatal("3.2a is below the floor and must not be OK")
	}
	if rep.Version != "3.2a" || rep.Reason == "" {
		t.Errorf("expected a version and a reason, got %+v", rep)
	}
}

func TestDetectSaysSoWhenTmuxIsMissing(t *testing.T) {
	rep := Detector{LookPath: func(string) (string, error) { return "", errNotOnPath }}.
		Detect(context.Background())
	if rep.Installed || rep.OK {
		t.Fatalf("expected nothing found, got %+v", rep)
	}
	if rep.Reason == "" {
		t.Error("a missing tmux has to be explained")
	}
}
