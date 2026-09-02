package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// Nothing in this file installs anything. The machine's own tmux answers the
// "already there" case, and every other case is a script in a temporary
// directory that prints what a package manager prints and does nothing else.

var errNotOnPath = errors.New("not found")

func fakeManager(t *testing.T, name, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the installer shells out, which this test cannot do on Windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// withoutTmux points the server's installer at a machine where tmux is absent
// and only the named package manager is present.
func withoutTmux(srv *Server) {
	srv.tmuxAdmin.installer = termux.Installer{
		Geteuid: func() int { return 0 },
		LookPath: func(file string) (string, error) {
			if file == "tmux" {
				return "", errNotOnPath
			}
			return exec.LookPath(file)
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
	if _, ok := data["log"].([]any); !ok {
		t.Fatalf("no log in %#v", data)
	}
}

// The checklist item, at the endpoint: 3.2a is amber, never green.
func TestTmuxStatusCallsAnOldTmuxTooOld(t *testing.T) {
	dir := fakeManager(t, "tmux", "#!/bin/sh\necho 'tmux 3.2a'\n")
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.srv.tmuxAdmin.installer = termux.Installer{
		Geteuid: func() int { return 0 },
		LookPath: func(file string) (string, error) {
			if file == "tmux" {
				return filepath.Join(dir, "tmux"), nil
			}
			return "", errNotOnPath
		},
	}

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
}

// The install runs, its output reaches the event stream line by line, and the
// result is still on the card after a reload - which is the whole of the
// tmuxinstaller acceptance, at the level the browser talks to.
func TestTmuxInstallStreamsAndSurvivesAReload(t *testing.T) {
	dir := fakeManager(t, "apt-get", `#!/bin/sh
if [ "$1" = "update" ]; then echo "Reading package lists..."; exit 0; fi
echo "Setting up tmux (3.6a-2) ..."
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	withoutTmux(env.srv)

	_, before := env.do(t, env.client, "GET", "/api/tmux", "")
	if before["manager"] != "apt-get" || before["can_install"] != true {
		t.Fatalf("the card would not offer an install: %#v", before)
	}

	// The stream is opened first, so that no line can be printed before there
	// is anybody to hear it.
	req, err := http.NewRequest("GET", env.server.URL+"/api/tmux/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := env.client.Do(req)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("the stream is %q, not an event stream", got)
	}

	if code, _ := env.do(t, env.client, "POST", "/api/tmux/install", "{}"); code.StatusCode != http.StatusAccepted {
		t.Fatalf("install was not accepted: %d", code.StatusCode)
	}

	var lines []string
	done := false
	scanner := bufio.NewScanner(res.Body)
	// The check is on the way in, not on the way out: a loop that scans once
	// more after the last frame waits out the stream's ten second heartbeat.
	for !done {
		if !scanner.Scan() {
			break
		}
		payload, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Line string `json:"line"`
			Exit int    `json:"exit"`
			OK   bool   `json:"ok"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("the stream sent something that is not a frame: %q", payload)
		}
		switch event.Type {
		case "line":
			lines = append(lines, event.Line)
		case "done":
			done = true
			if !event.OK || event.Exit != 0 {
				t.Fatalf("the fake install failed: exit %d", event.Exit)
			}
		}
	}
	if !done {
		t.Fatalf("the stream never finished; it carried %#v", lines)
	}
	log := strings.Join(lines, "\n")
	for _, want := range []string{"apt-get update", "Reading package lists...", "Setting up tmux"} {
		if !strings.Contains(log, want) {
			t.Fatalf("the stream did not carry %q:\n%s", want, log)
		}
	}

	// A reload is a fresh GET, and it has to find the same output: the tail of
	// the log is in the database, not only in the page that watched it.
	_, after := env.do(t, env.client, "GET", "/api/tmux", "")
	stored, _ := after["log"].([]any)
	if len(stored) != len(lines) {
		t.Fatalf("the reload shows %d lines, the stream carried %d", len(stored), len(lines))
	}
	var kept struct {
		Lines []string `json:"lines"`
		Exit  int      `json:"exit"`
	}
	if err := env.store.GetJSON(installLogKey, &kept); err != nil {
		t.Fatalf("nothing was written to the database: %v", err)
	}
	if kept.Exit != 0 || !strings.Contains(strings.Join(kept.Lines, "\n"), "Setting up tmux") {
		t.Fatalf("what was kept is not what ran: %#v", kept)
	}
}

func TestTmuxInstallRefusesASecondOne(t *testing.T) {
	dir := fakeManager(t, "yum", "#!/bin/sh\nsleep 2\nexit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	withoutTmux(env.srv)

	if res, _ := env.do(t, env.client, "POST", "/api/tmux/install", "{}"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("the first install was not accepted: %d", res.StatusCode)
	}
	// The goroutine has to be inside the installer before a second call can be
	// refused by it rather than by the flag this handler sets.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, _ := env.do(t, env.client, "POST", "/api/tmux/install", "{}")
		if res.StatusCode == http.StatusConflict {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a second install was never refused")
}

func TestTmuxEndpointsAreBehindTheWall(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	for _, path := range []string{"/api/tmux", "/api/tmux/events"} {
		res, _ := env.do(t, env.anon, "GET", path, "")
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s answered %d to a stranger", path, res.StatusCode)
		}
	}
	res, _ := env.do(t, env.anon, "POST", "/api/tmux/install", "{}")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the installer answered %d to a stranger", res.StatusCode)
	}
}
