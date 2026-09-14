package server

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The terminal engine card reports and nothing else. Nothing here installs
// anything: the machine's own tmux answers the "already there" case, and the
// rest is a script in a temporary directory that prints a version.

var errNotOnPath = errors.New("not found")

// fakeTmuxOn points the card's detector at a machine whose only tmux is a
// script that answers `tmux -V` with version.
func fakeTmuxOn(t *testing.T, srv *Server, version string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '"+version+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.tmuxAdmin.detector = termux.Detector{
		LookPath: func(file string) (string, error) {
			if file == "tmux" {
				return path, nil
			}
			return "", errNotOnPath
		},
	}
}

func TestTmuxStatusReportsThisMachine(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux on this machine")
	}
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	_, data := env.do(t, env.client, "GET", "/api/tmux", "")
	if data["ok"] != true || data["installed"] != true {
		t.Fatalf("this machine has tmux, the card says otherwise: %#v", data)
	}
	if data["min"] != "3.3" {
		t.Fatalf("the minimum shown is %v, want 3.3", data["min"])
	}
}

// The checklist item, at the endpoint: 3.2a is amber, never green.
func TestTmuxStatusCallsAnOldTmuxTooOld(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	fakeTmuxOn(t, env.srv, "tmux 3.2a")

	_, data := env.do(t, env.client, "GET", "/api/tmux", "")
	if data["installed"] != true {
		t.Fatalf("the fake tmux was not seen: %#v", data)
	}
	if data["ok"] == true {
		t.Fatalf("tmux 3.2a was reported as ok: %#v", data)
	}
	if data["version"] != "3.2a" {
		t.Fatalf("version is %v, want 3.2a", data["version"])
	}
	if data["reason"] == "" {
		t.Fatal("a tmux that is too old has to be explained")
	}
}

func TestTmuxEndpointIsBehindTheWall(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	res, _ := env.do(t, env.anon, "GET", "/api/tmux", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/tmux answered %d to a stranger", res.StatusCode)
	}
}

// The installer is gone with the routes it used: a POST to where it was has to
// be a 404, not a method error or a silent success.
func TestTmuxInstallEndpointIsGone(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	for _, path := range []string{"/api/tmux/install", "/api/tmux/events"} {
		res, _ := env.do(t, env.client, "POST", path, "{}")
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404", path, res.StatusCode)
		}
	}
}
