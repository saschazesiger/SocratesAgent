//go:build !windows

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The session API is tested against the real substrate: a real store, a real
// termux Manager, a real tmux server on a socket of its own under t.TempDir(),
// and the e2e suite's fake CLI on PATH under the three names the launchers
// look for. Nothing here starts a real Claude Code, Codex or OpenCode, and
// nothing touches the machine's own tmux.

// requireTmux skips a test when tmux is missing or older than the generated
// configuration needs.
func requireTmux(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed; skipping the session API tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := termux.BinaryVersion(ctx, bin)
	if err != nil {
		t.Skipf("could not read the tmux version: %v", err)
	}
	if !v.OK() {
		t.Skipf("tmux %s is older than %d.%d", v, termux.MinMajor, termux.MinMinor)
	}
	return bin
}

var (
	fakeOnce sync.Once
	fakeDir  string
	fakeErr  error
)

// removeFakeBin takes the shared fake CLI directory away. It is called from
// TestMain, which is the only point at which the last test that needed it has
// finished - a sync.Once dir belongs to no single test, so t.TempDir cannot
// own it.
func removeFakeBin() {
	if fakeDir != "" {
		os.RemoveAll(fakeDir)
	}
}

// fakeBinDir builds e2e/fakebin/faketui once per test run and links it under
// the three names the launchers look for, so that a session created through
// the API runs the fake and never a real CLI.
func fakeBinDir(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "socrates-fakebin-")
		if err != nil {
			fakeErr = err
			return
		}
		exe := filepath.Join(dir, "faketui")
		build := exec.Command("go", "build", "-o", exe, "./e2e/fakebin/faketui")
		build.Dir = "../.."
		if out, err := build.CombinedOutput(); err != nil {
			fakeErr = fmt.Errorf("%v: %s", err, out)
			return
		}
		for _, name := range []string{"claude", "codex", "opencode"} {
			if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
				fakeErr = err
				return
			}
		}
		fakeDir = dir
	})
	if fakeErr != nil {
		t.Skipf("the fake CLI could not be built: %v", fakeErr)
	}
	return fakeDir
}

// sessionEnv is a signed-in server with its terminal substrate started.
type sessionEnv struct {
	*testEnv
	t       *testing.T
	tmuxBin string
	home    string
	work    string
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	bin := requireTmux(t)
	fakes := fakeBinDir(t)

	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))

	env := newEnv(t)
	e := &sessionEnv{testEnv: env, t: t, tmuxBin: bin, home: home, work: work}
	e.signIn()
	e.configureWorkspace(filepath.Join(home, "workspaces"), true, filepath.Join(home, "preset"))
	e.start(env.srv)
	return e
}

func (e *sessionEnv) signIn() {
	e.t.Helper()
	res, _ := e.do(e.t, e.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("setup: %d", res.StatusCode)
	}
}

// configureWorkspace puts the workspace root and the one preset inside the
// test's own directory, so that no session is ever created anywhere else.
func (e *sessionEnv) configureWorkspace(root string, allowCustom bool, preset string) {
	e.t.Helper()
	for _, dir := range []string{root, preset} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			e.t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`{"settings":{"workspace":{"root":%q,"allow_custom":%t,"default_harness":"shell",
		"presets":[{"label":"Preset","path":%q}]}}}`, root, allowCustom, preset)
	res, payload := e.do(e.t, e.client, "PUT", "/api/settings", body)
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("settings: %d %#v", res.StatusCode, payload)
	}
}

// start brings the substrate up and takes it back down with the test, leaving
// no tmux server behind.
func (e *sessionEnv) start(srv *Server) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.StartSessions(ctx); err != nil {
		cancel()
		e.t.Fatalf("could not start the terminal substrate: %v", err)
	}
	e.t.Cleanup(func() {
		cancel()
		srv.StopSessions()
		_ = exec.Command(e.tmuxBin, "-S", srv.Sessions().Socket(), "kill-server").Run()
	})
	e.guard(srv.Sessions().Socket())
}

// guard makes this test's tmux server die with the test binary.
//
// The cleanup above covers every ordinary ending, a failed assertion
// included - but not a package level timeout, which panics the process before
// any cleanup runs and leaves a daemon and its journal sink on the machine for
// good. This shell loop is orphaned by such a panic rather than killed by it,
// and one second later it takes the server down.
func (e *sessionEnv) guard(socket string) {
	e.t.Helper()
	script := fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 1; done; exec %s -S %s kill-server",
		os.Getpid(), termux.ShellQuote(e.tmuxBin), termux.ShellQuote(socket))
	cmd := exec.Command("/bin/sh", "-c", script)
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("could not arm the tmux server guard: %v", err)
	}
	e.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
}

func (e *sessionEnv) socket() string { return e.srv.Sessions().Socket() }

func (e *sessionEnv) tmux(args ...string) (string, error) {
	full := append([]string{"-S", e.socket()}, args...)
	out, err := exec.Command(e.tmuxBin, full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (e *sessionEnv) hasTmuxSession(id string) bool {
	_, err := e.tmux("has-session", "-t", termux.TmuxName(id))
	return err == nil
}

// create posts one session and returns what the server stored.
func (e *sessionEnv) create(body string) (*http.Response, map[string]any) {
	e.t.Helper()
	res, payload := e.do(e.t, e.client, "POST", "/api/sessions", body)
	session, _ := payload["session"].(map[string]any)
	return res, session
}

func (e *sessionEnv) get(id string) map[string]any {
	e.t.Helper()
	res, payload := e.do(e.t, e.client, "GET", "/api/sessions/"+id, "")
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("GET session %s: %d %#v", id, res.StatusCode, payload)
	}
	session, _ := payload["session"].(map[string]any)
	return session
}

// waitForState polls the API until the session reaches one of the states.
func (e *sessionEnv) waitForState(id string, within time.Duration, want ...string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(within)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = e.get(id)
		for _, w := range want {
			if last["state"] == w {
				return last
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("session %s never reached %v; it is %#v", id, want, last)
	return nil
}

// waitForPane waits until the pane says what the test is waiting for, which is
// how a test knows the program itself started and not merely tmux.
func (e *sessionEnv) waitForPane(id, want string) string {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	screen := ""
	for time.Now().Before(deadline) {
		screen, _ = e.tmux("capture-pane", "-p", "-t", termux.TmuxName(id))
		if strings.Contains(screen, want) {
			return screen
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("the pane of %s never showed %q; it shows:\n%s", id, want, screen)
	return ""
}

// planArgv reads back the argv a session was actually started with.
func (e *sessionEnv) planArgv(id string) []string {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.srv.dataDir, "sessions", id, "plan.json"))
	if err != nil {
		e.t.Fatalf("plan.json: %v", err)
	}
	var plan struct {
		Argv []string `json:"Argv"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		e.t.Fatalf("plan.json: %v", err)
	}
	return plan.Argv
}

func sessionID(t *testing.T, session map[string]any) string {
	t.Helper()
	id, _ := session["id"].(string)
	if id == "" {
		t.Fatalf("the answer carried no session: %#v", session)
	}
	return id
}

// TestSessionLifecycle drives the whole of the API against real tmux: one
// session per harness, then the list, the rename, the archive and the delete.
func TestSessionLifecycle(t *testing.T) {
	e := newSessionEnv(t)

	cases := []struct {
		harness string
		banner  string
	}{
		{"shell", ""},
		{"claude", "FAKE claude"},
		{"codex", "FAKE codex"},
		{"opencode", "FAKE opencode"},
	}
	ids := map[string]string{}
	for _, c := range cases {
		t.Run(c.harness, func(t *testing.T) {
			dir := filepath.Join(e.work, c.harness)
			res, session := e.create(fmt.Sprintf(
				`{"harness":%q,"workdir_mode":"custom","workdir":%q,"cols":100,"rows":30}`, c.harness, dir))
			if res.StatusCode != http.StatusCreated {
				t.Fatalf("create %s: %d %#v", c.harness, res.StatusCode, session)
			}
			id := sessionID(t, session)
			ids[c.harness] = id
			if session["state"] != store.StateRunning {
				t.Fatalf("%s: state = %v (%v)", c.harness, session["state"], session["fail_reason"])
			}
			if session["workdir"] != dir {
				t.Fatalf("%s: workdir = %v", c.harness, session["workdir"])
			}
			if !e.hasTmuxSession(id) {
				t.Fatalf("%s: tmux has no session soc_%s", c.harness, id)
			}
			if c.banner != "" {
				e.waitForPane(id, c.banner)
			}
		})
	}

	res, payload := e.do(t, e.client, "GET", "/api/sessions", "")
	list, _ := payload["sessions"].([]any)
	if res.StatusCode != http.StatusOK || len(list) != len(cases) {
		t.Fatalf("list: %d, %d sessions, want %d", res.StatusCode, len(list), len(cases))
	}

	id := ids["shell"]
	res, payload = e.do(t, e.client, "PATCH", "/api/sessions/"+id, `{"title":"Renamed"}`)
	if res.StatusCode != http.StatusOK || payload["session"].(map[string]any)["title"] != "Renamed" {
		t.Fatalf("rename: %d %#v", res.StatusCode, payload)
	}
	res, _ = e.do(t, e.client, "PATCH", "/api/sessions/"+id, `{"title":"  "}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an empty name should be refused, got %d", res.StatusCode)
	}

	// Archiving hides a session from the default list and leaves it running.
	res, _ = e.do(t, e.client, "POST", "/api/sessions/"+id+"/archive", `{"archived":true}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d", res.StatusCode)
	}
	_, payload = e.do(t, e.client, "GET", "/api/sessions", "")
	if list, _ := payload["sessions"].([]any); len(list) != len(cases)-1 {
		t.Fatalf("an archived session should be hidden, %d of %d left", len(list), len(cases))
	}
	_, payload = e.do(t, e.client, "GET", "/api/sessions?scope=all", "")
	if list, _ := payload["sessions"].([]any); len(list) != len(cases) {
		t.Fatalf("scope=all should show it again, got %d", len(list))
	}
	if !e.hasTmuxSession(id) {
		t.Fatal("archiving must not stop a session")
	}
	res, _ = e.do(t, e.client, "POST", "/api/sessions/"+id+"/archive", `{"archived":false}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unarchive: %d", res.StatusCode)
	}

	// Delete is the one path that kills a tmux session, and it keeps the
	// working directory.
	for _, c := range cases {
		res, payload := e.do(t, e.client, "DELETE", "/api/sessions/"+ids[c.harness], "")
		if res.StatusCode != http.StatusOK || payload["workdir_kept"] != true {
			t.Fatalf("delete %s: %d %#v", c.harness, res.StatusCode, payload)
		}
		if e.hasTmuxSession(ids[c.harness]) {
			t.Fatalf("delete %s left its tmux session behind", c.harness)
		}
		if _, err := os.Stat(filepath.Join(e.work, c.harness)); err != nil {
			t.Fatalf("delete %s removed the working directory: %v", c.harness, err)
		}
		res, _ = e.do(t, e.client, "GET", "/api/sessions/"+ids[c.harness], "")
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("a deleted session should be gone, got %d", res.StatusCode)
		}
	}
}

// TestCreateIsIdempotent is what makes starting a session over a link that
// drops safe: the same client_id twice is one session, not two.
func TestCreateIsIdempotent(t *testing.T) {
	e := newSessionEnv(t)
	body := fmt.Sprintf(`{"client_id":"abc-123","harness":"shell","workdir_mode":"custom","workdir":%q}`,
		filepath.Join(e.work, "idem"))

	res, first := e.create(body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("first create: %d %#v", res.StatusCode, first)
	}
	res, second := e.create(body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a repeated create should answer with the session it made, got %d", res.StatusCode)
	}
	if sessionID(t, first) != sessionID(t, second) {
		t.Fatalf("two sessions for one client_id: %s and %s", first["id"], second["id"])
	}
	_, payload := e.do(t, e.client, "GET", "/api/sessions", "")
	if list, _ := payload["sessions"].([]any); len(list) != 1 {
		t.Fatalf("expected one session, got %d", len(list))
	}
}

// TestWorkspaceRulesAreEnforced holds the server to the rules the sheet only
// displays.
func TestWorkspaceRulesAreEnforced(t *testing.T) {
	e := newSessionEnv(t)

	// A preset the dashboard does not name is refused, and the one it does is
	// accepted.
	res, payload := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"preset","workdir":%q}`, filepath.Join(e.home, "elsewhere")))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown preset should be refused, got %d %#v", res.StatusCode, payload)
	}
	res, session := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"preset","workdir":%q}`, filepath.Join(e.home, "preset")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("the preset should be accepted, got %d %#v", res.StatusCode, session)
	}

	// With custom directories switched off, a typed-in path is refused however
	// the request is dressed up.
	e.configureWorkspace(filepath.Join(e.home, "workspaces"), false, filepath.Join(e.home, "preset"))
	res, payload = e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.home, "anywhere")))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a typed-in directory should be refused, got %d %#v", res.StatusCode, payload)
	}
	if _, err := os.Stat(filepath.Join(e.home, "anywhere")); err == nil {
		t.Fatal("a refused directory must not be created")
	}

	// A dynamic directory is made under the root and nowhere else.
	res, session = e.create(`{"harness":"shell"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("dynamic create: %d %#v", res.StatusCode, session)
	}
	workdir, _ := session["workdir"].(string)
	if !strings.HasPrefix(workdir, filepath.Join(e.home, "workspaces")+string(os.PathSeparator)) {
		t.Fatalf("the dynamic directory %q is not under the workspace root", workdir)
	}
}

// TestRestartAfterExit walks the exit overlay's path: the program ends, the
// row says so with its status, and Restart brings the session back.
func TestRestartAfterExit(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "restart")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)

	if _, err := e.tmux("send-keys", "-t", termux.TmuxName(id), "exit 7", "Enter"); err != nil {
		t.Fatalf("could not type into the pane: %v", err)
	}
	exited := e.waitForState(id, 15*time.Second, store.StateExited)
	if exited["exit_status"] != float64(7) {
		t.Fatalf("exit_status = %v, want 7", exited["exit_status"])
	}

	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/restart", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("restart: %d %#v", res.StatusCode, payload)
	}
	restarted, _ := payload["session"].(map[string]any)
	if restarted["state"] != store.StateRunning {
		t.Fatalf("after a restart the session is %v", restarted["state"])
	}
	if !e.hasTmuxSession(id) {
		t.Fatal("the restarted session has no tmux session")
	}
}

// TestRebootResumesTheConversation is the reboot case end to end: the tmux
// server is killed under the running Socrates, the session becomes one to
// resume, and opening it starts the CLI again on its own conversation - with
// --resume and the id it had, not a fresh one.
func TestRebootResumesTheConversation(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"claude","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "resume")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)
	e.waitForPane(id, "FAKE claude")

	argv := e.planArgv(id)
	cliID := ""
	for i, a := range argv {
		if a == "--session-id" && i+1 < len(argv) {
			cliID = argv[i+1]
		}
	}
	if cliID == "" {
		t.Fatalf("the first launch chose no session id: %v", argv)
	}

	// The reboot, as far as Socrates can tell it apart from one: the tmux
	// server is gone and the rows are not.
	if _, err := e.tmux("kill-server"); err != nil {
		t.Fatalf("kill-server: %v", err)
	}
	// It takes two consecutive failed polls to declare it - a busy moment must
	// not flip every session at once - so the loop is nudged rather than
	// waited on, and the state is then read back through the API.
	ctx := context.Background()
	e.srv.Sessions().Poll(ctx)
	e.srv.Sessions().Poll(ctx)
	e.waitForState(id, 10*time.Second, store.StateNeedsResume)

	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/resume", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("resume: %d %#v", res.StatusCode, payload)
	}
	resumed, _ := payload["session"].(map[string]any)
	if resumed["state"] != store.StateRunning {
		t.Fatalf("after a resume the session is %v (%v)", resumed["state"], resumed["fail_reason"])
	}
	if resumed["resumed"] != true || resumed["resume_count"] != float64(1) {
		t.Fatalf("the resume was not recorded: %#v", resumed)
	}

	argv = e.planArgv(id)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--resume "+cliID) {
		t.Fatalf("the relaunch did not resume the conversation: %s", joined)
	}
	if strings.Contains(joined, "--session-id") {
		t.Fatalf("a resume must never re-use --session-id: %s", joined)
	}
	e.waitForPane(id, "FAKE claude")

	res, payload = e.do(t, e.client, "POST", "/api/sessions/"+id+"/ack-resume", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ack-resume: %d %#v", res.StatusCode, payload)
	}
	if payload["session"].(map[string]any)["resumed"] != false {
		t.Fatal("the resumed banner was not cleared")
	}
}

// TestAdoptAcrossARestart restarts the whole server in-process: a running
// session is taken back, and a tmux session of ours with no row is taken in
// rather than killed.
func TestAdoptAcrossARestart(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "adopt")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)

	// A session of ours that the database has never heard of: what a restored
	// backup, or a crash between the tmux session and its row, leaves behind.
	orphan := termux.NewID()
	if out, err := e.tmux("new-session", "-d", "-s", termux.TmuxName(orphan), "-c", e.work, "/bin/sh"); err != nil {
		t.Fatalf("could not make an orphan session: %v: %s", err, out)
	}

	// The restart. Nothing tmux owns is stopped.
	e.srv.StopSessions()
	next, err := New(e.store, e.srv.dataDir)
	if err != nil {
		t.Fatalf("second server: %v", err)
	}
	e.start(next)

	row, err := e.store.GetSession(id)
	if err != nil || row.State != store.StateRunning {
		t.Fatalf("the running session was not re-adopted: %#v %v", row, err)
	}
	if !e.hasTmuxSession(id) {
		t.Fatal("re-adoption stopped a running session")
	}
	recovered, err := e.store.GetSession(orphan)
	if err != nil {
		t.Fatalf("the unrecorded session was not taken in: %v", err)
	}
	if recovered.State != store.StateRunning || recovered.Title != "Recovered session" {
		t.Fatalf("recovered row = %#v", recovered)
	}
	if !e.hasTmuxSession(orphan) {
		t.Fatal("an unrecorded session of ours was killed, and it never may be")
	}
}

// TestHarnessCatalogue is what the new-session sheet reads: the four
// harnesses, and the rules about where a session may work.
func TestHarnessCatalogue(t *testing.T) {
	e := newSessionEnv(t)
	res, payload := e.do(t, e.client, "GET", "/api/harnesses", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("harnesses: %d %#v", res.StatusCode, payload)
	}
	if payload["sessions_available"] != true {
		t.Fatalf("sessions should be available here: %#v", payload)
	}
	workspace, _ := payload["workspace"].(map[string]any)
	if workspace["allow_custom"] != true || workspace["root"] != filepath.Join(e.home, "workspaces") {
		t.Fatalf("workspace = %#v", workspace)
	}
	if presets, _ := workspace["presets"].([]any); len(presets) != 1 {
		t.Fatalf("the preset list did not survive: %#v", workspace["presets"])
	}
}

// TestSessionAPIRequiresAuth keeps the wall in front of the terminals: every
// route needs a session cookie, and every state change a same-origin request.
func TestSessionAPIRequiresAuth(t *testing.T) {
	e := newSessionEnv(t)

	for _, route := range []struct{ method, path string }{
		{"GET", "/api/sessions"},
		{"POST", "/api/sessions"},
		{"GET", "/api/sessions/x"},
		{"PATCH", "/api/sessions/x"},
		{"DELETE", "/api/sessions/x"},
		{"POST", "/api/sessions/x/archive"},
		{"POST", "/api/sessions/x/restart"},
		{"POST", "/api/sessions/x/resume"},
		{"POST", "/api/sessions/x/ack-resume"},
		{"GET", "/api/sessions/x/journal"},
		{"GET", "/api/harnesses"},
		{"POST", "/api/harnesses/refresh"},
	} {
		res, _ := e.do(t, e.anon, route.method, route.path, "")
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d without a password, want 401", route.method, route.path, res.StatusCode)
		}
	}

	// A signed-in browser on somebody else's page is refused too: the cookie
	// travels, the origin does not.
	req, err := http.NewRequest("POST", e.server.URL+"/api/sessions", strings.NewReader(`{"harness":"shell"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	res, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-origin create answered %d, want 403", res.StatusCode)
	}
	if _, payload := e.do(t, e.client, "GET", "/api/sessions", ""); len(payload["sessions"].([]any)) != 0 {
		t.Fatal("the cross-origin create made a session")
	}
}

// ------------------------------------------------- the WP4 review findings

// typeLine types one line into a session's pane, which is what makes Codex and
// OpenCode write down a conversation of their own.
func (e *sessionEnv) typeLine(id, line string) {
	e.t.Helper()
	if out, err := e.tmux("send-keys", "-t", termux.TmuxName(id), line, "Enter"); err != nil {
		e.t.Fatalf("could not type into %s: %v: %s", id, err, out)
	}
}

// waitForCLIID waits until the session-id watcher has learned the program's
// own conversation id.
func (e *sessionEnv) waitForCLIID(id string, within time.Duration) string {
	e.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		row, err := e.store.GetSession(id)
		if err == nil && row.CLISessionID != "" {
			return row.CLISessionID
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("session %s never learned its conversation id", id)
	return ""
}

// reboot is what a restarted machine looks like from here: the tmux server is
// gone and the rows are not. Two failed polls are what declares it.
func (e *sessionEnv) reboot(id string) {
	e.t.Helper()
	if _, err := e.tmux("kill-server"); err != nil {
		e.t.Fatalf("kill-server: %v", err)
	}
	ctx := context.Background()
	e.srv.Sessions().Poll(ctx)
	e.srv.Sessions().Poll(ctx)
	e.waitForState(id, 10*time.Second, store.StateNeedsResume)
}

// TestRestartRefusesALiveSession is finding 1: a restart aimed at a terminal
// that is working answers 409 and leaves the row exactly as it was. Marking it
// first and asking tmux afterwards used to record a healthy session as failed,
// in a state nothing polls out of again.
func TestRestartRefusesALiveSession(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "live")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)

	for i := 0; i < 2; i++ {
		res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/restart", "")
		if res.StatusCode != http.StatusConflict {
			t.Fatalf("restart of a running session answered %d %#v, want 409", res.StatusCode, payload)
		}
	}
	if got := e.get(id); got["state"] != store.StateRunning || got["fail_reason"] != nil {
		t.Fatalf("the refused restart changed the row: %#v", got)
	}
	if !e.hasTmuxSession(id) {
		t.Fatal("the refused restart killed the session")
	}

	// Resume on a running session is not a restart: it is the ordinary open,
	// and it changes nothing.
	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/resume", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("resume of a running session: %d %#v", res.StatusCode, payload)
	}
	resumed, _ := payload["session"].(map[string]any)
	if resumed["state"] != store.StateRunning || resumed["resume_count"] != float64(0) {
		t.Fatalf("resume relaunched a running session: %#v", resumed)
	}
}

// TestConcurrentResumeRelaunchesOnce is finding 2: two viewers opening the
// same rebooted session at once - a phone and a laptop - get one relaunch
// between them, and neither is answered with a failed row.
func TestConcurrentResumeRelaunchesOnce(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"claude","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "race")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)
	e.waitForPane(id, "FAKE claude")
	e.reboot(id)

	const viewers = 3
	type answer struct {
		code    int
		session map[string]any
	}
	answers := make(chan answer, viewers)
	start := make(chan struct{})
	for i := 0; i < viewers; i++ {
		go func() {
			<-start
			res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/resume", "")
			row, _ := payload["session"].(map[string]any)
			answers <- answer{res.StatusCode, row}
		}()
	}
	close(start)
	for i := 0; i < viewers; i++ {
		got := <-answers
		if got.code != http.StatusOK {
			t.Fatalf("viewer %d was answered %d", i, got.code)
		}
		if got.session["state"] != store.StateRunning {
			t.Fatalf("viewer %d was answered %v (%v)", i, got.session["state"], got.session["fail_reason"])
		}
	}
	final := e.get(id)
	if final["resume_count"] != float64(1) {
		t.Fatalf("%d viewers caused %v resumes, want 1", viewers, final["resume_count"])
	}
	if final["state"] != store.StateRunning {
		t.Fatalf("the session ended up %v", final["state"])
	}
	e.waitForPane(id, "FAKE claude")
}

// TestResumeSaysWhenItHadToStartFresh is finding 4: a conversation that is
// gone is not the same event as one that came back, and the row alone cannot
// tell them apart.
func TestResumeSaysWhenItHadToStartFresh(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"claude","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "fresh")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)
	e.waitForPane(id, "FAKE claude")

	row, err := e.store.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	lost := row.CLISessionID

	// A genuine resume says nothing about freshness.
	e.reboot(id)
	_, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/resume", "")
	resumed, _ := payload["session"].(map[string]any)
	if resumed["resume_fresh"] != nil {
		t.Fatalf("a real resume claimed to be fresh: %#v", resumed)
	}
	if resumed["cli_session_state"] != store.CLIVerified {
		t.Fatalf("cli_session_state = %v, want verified", resumed["cli_session_state"])
	}
	e.waitForPane(id, "FAKE claude")

	// Now the transcript is gone, which is what a conversation the CLI has
	// forgotten looks like from here.
	transcripts, _ := filepath.Glob(filepath.Join(e.home, ".claude", "projects", "*", "*.jsonl"))
	for _, path := range transcripts {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	e.reboot(id)
	_, payload = e.do(t, e.client, "POST", "/api/sessions/"+id+"/resume", "")
	fresh, _ := payload["session"].(map[string]any)
	if fresh["state"] != store.StateRunning {
		t.Fatalf("the fresh start did not run: %#v", fresh)
	}
	if fresh["resume_fresh"] != true {
		t.Fatalf("a lost conversation was not reported as fresh: %#v", fresh)
	}
	if fresh["resumed_from"] != lost {
		t.Fatalf("resumed_from = %v, want the id that was lost (%s)", fresh["resumed_from"], lost)
	}
	if !strings.Contains(strings.Join(e.planArgv(id), " "), "--session-id") {
		t.Fatal("a lost conversation must be started fresh, with a new session id")
	}

	// Acknowledging the banner clears what it said with it.
	_, payload = e.do(t, e.client, "POST", "/api/sessions/"+id+"/ack-resume", "")
	acked, _ := payload["session"].(map[string]any)
	if acked["resumed"] != false || acked["resume_fresh"] != nil {
		t.Fatalf("the banner was not cleared: %#v", acked)
	}
}

// TestCodexAndOpenCodeResumeTheirConversation is finding 3's first half: the
// two harnesses whose id has to be discovered are resumed on it, which is the
// whole reason WP4 runs the discoverers at all.
func TestCodexAndOpenCodeResumeTheirConversation(t *testing.T) {
	for _, c := range []struct{ harness, flag string }{
		{"codex", "resume "},
		{"opencode", "--session "},
	} {
		t.Run(c.harness, func(t *testing.T) {
			e := newSessionEnv(t)
			res, session := e.create(fmt.Sprintf(
				`{"harness":%q,"workdir_mode":"custom","workdir":%q}`, c.harness, filepath.Join(e.work, c.harness)))
			if res.StatusCode != http.StatusCreated {
				t.Fatalf("create: %d %#v", res.StatusCode, session)
			}
			id := sessionID(t, session)
			e.waitForPane(id, "FAKE "+c.harness)

			// Neither writes a conversation down until a real turn happens.
			e.typeLine(id, "hello")
			e.waitForPane(id, "you said: hello")
			cli := e.waitForCLIID(id, 30*time.Second)

			e.reboot(id)
			res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/resume", "")
			resumed, _ := payload["session"].(map[string]any)
			if res.StatusCode != http.StatusOK || resumed["state"] != store.StateRunning {
				t.Fatalf("resume: %d %#v", res.StatusCode, payload)
			}
			if joined := strings.Join(e.planArgv(id), " "); !strings.Contains(joined, c.flag+cli) {
				t.Fatalf("the relaunch did not continue the conversation %s: %s", cli, joined)
			}
			e.waitForPane(id, "FAKE "+c.harness)
		})
	}
}

// TestTheIDWatcherSurvivesARestart is finding 3's second half: a session that
// outlives the Socrates which launched it still has to learn its conversation
// id, or it can never be resumed - and for OpenCode that means recovering the
// server password from plan.json, because the process that held it is gone.
func TestTheIDWatcherSurvivesARestart(t *testing.T) {
	for _, harness := range []string{"codex", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			e := newSessionEnv(t)
			res, session := e.create(fmt.Sprintf(
				`{"harness":%q,"workdir_mode":"custom","workdir":%q}`, harness, filepath.Join(e.work, harness)))
			if res.StatusCode != http.StatusCreated {
				t.Fatalf("create: %d %#v", res.StatusCode, session)
			}
			id := sessionID(t, session)
			e.waitForPane(id, "FAKE "+harness)
			if row, _ := e.store.GetSession(id); row.CLISessionState != store.CLIPending {
				t.Fatalf("cli_session_state = %s, want pending", row.CLISessionState)
			}

			// The restart. The session keeps running; the watcher does not.
			e.srv.StopSessions()
			next, err := New(e.store, e.srv.dataDir)
			if err != nil {
				t.Fatalf("second server: %v", err)
			}
			e.start(next)

			e.typeLine(id, "hello")
			e.waitForPane(id, "you said: hello")
			if cli := e.waitForCLIID(id, 30*time.Second); cli == "" {
				t.Fatal("no conversation id was learned after the restart")
			}
		})
	}
}

// TestVerifyErrorStillTriesTheResume is finding 5, and §C.5/§C.6: "could not
// tell" is not "gone". A question that could not be answered keeps the id and
// tries the resume; only a provable absence starts fresh.
func TestVerifyErrorStillTriesTheResume(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"codex","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "unsure")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)
	e.waitForPane(id, "FAKE codex")
	e.typeLine(id, "hello")
	e.waitForPane(id, "you said: hello")
	cli := e.waitForCLIID(id, 30*time.Second)
	e.reboot(id)

	// With neither CODEX_HOME nor HOME, Codex's verification cannot answer at
	// all - the transient failure the finding is about.
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", "")
	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/resume", "")
	resumed, _ := payload["session"].(map[string]any)
	if res.StatusCode != http.StatusOK || resumed["state"] != store.StateRunning {
		t.Fatalf("resume: %d %#v", res.StatusCode, payload)
	}
	if joined := strings.Join(e.planArgv(id), " "); !strings.Contains(joined, "resume "+cli) {
		t.Fatalf("an unanswerable verification threw the conversation away: %s", joined)
	}
	if resumed["cli_session_state"] == store.CLILost || resumed["resume_fresh"] == true {
		t.Fatalf("an unanswerable verification recorded the conversation as gone: %#v", resumed)
	}
}

// TestConcurrentCreatesLeaveOneDirectory is finding 6: the requests that lose
// the idempotency race take the empty directory they made back with them.
func TestConcurrentCreatesLeaveOneDirectory(t *testing.T) {
	e := newSessionEnv(t)
	root := filepath.Join(e.home, "workspaces")
	const tries = 6
	codes := make(chan int, tries)
	start := make(chan struct{})
	for i := 0; i < tries; i++ {
		go func() {
			<-start
			res, _ := e.create(`{"client_id":"one-key","harness":"shell"}`)
			codes <- res.StatusCode
		}()
	}
	close(start)
	created := 0
	for i := 0; i < tries; i++ {
		switch code := <-codes; code {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Fatalf("a create answered %d", code)
		}
	}
	if created != 1 {
		t.Fatalf("%d of %d requests reported making a session, want 1", created, tries)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("%d working directories were left behind: %v", len(entries), names)
	}
	_, payload := e.do(t, e.client, "GET", "/api/sessions", "")
	if list, _ := payload["sessions"].([]any); len(list) != 1 {
		t.Fatalf("expected one session, got %d", len(list))
	}
}

// TestTerminalSizeIsBounded is finding 8: a size that tmux would refuse is the
// client's mistake, and it is caught before a row or a directory exists.
func TestTerminalSizeIsBounded(t *testing.T) {
	e := newSessionEnv(t)
	for _, body := range []string{
		`{"harness":"shell","cols":-5,"rows":40}`,
		`{"harness":"shell","cols":80,"rows":100000}`,
		`{"harness":"shell","cols":2,"rows":40}`,
	} {
		res, payload := e.create(body)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s answered %d %#v, want 400", body, res.StatusCode, payload)
		}
	}
	_, payload := e.do(t, e.client, "GET", "/api/sessions?scope=all", "")
	if list, _ := payload["sessions"].([]any); len(list) != 0 {
		t.Fatalf("a refused size made %d sessions", len(list))
	}
	entries, err := os.ReadDir(filepath.Join(e.home, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused size made %d working directories", len(entries))
	}
}

// TestStartingRowWithALivePaneIsPromoted is finding 9: when the last write of
// a create does not land, tmux is the authority and the poll says so - rather
// than the next viewer writing "failed" over a working terminal.
func TestStartingRowWithALivePaneIsPromoted(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "starting")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)
	if err := e.store.SetSessionState(id, store.StateStarting, -1, ""); err != nil {
		t.Fatal(err)
	}
	e.srv.Sessions().Poll(context.Background())
	if got := e.get(id); got["state"] != store.StateRunning {
		t.Fatalf("a live pane left the row at %v", got["state"])
	}
}

// TestRebootResumesEverySession is the blocker WP9a found: after a reboot only
// the first session could be resumed.
//
// The first resume starts the tmux server again, and from that moment the
// sessions still waiting have a live server with no session of their own in
// it. tmux 3.6 answers `display-message -p -t <missing> -F '#{pane_dead}'`
// with success and an empty line rather than with an error, which read as
// "the pane is not dead" and therefore "the session is still running": every
// resume after the first was refused with 409 and left sitting under
// "Resuming after a restart…". Two sessions is the smallest number that can
// show it, so this test uses two.
func TestRebootResumesEverySession(t *testing.T) {
	e := newSessionEnv(t)
	var ids []string
	for _, name := range []string{"first", "second"} {
		res, session := e.create(fmt.Sprintf(
			`{"harness":"shell","workdir_mode":"custom","workdir":%q}`,
			filepath.Join(e.work, name)))
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: %d %#v", name, res.StatusCode, session)
		}
		ids = append(ids, sessionID(t, session))
	}

	// The reboot takes both of them, and both are declared before either is
	// opened - which is what a person coming back to a rebooted machine finds.
	if _, err := e.tmux("kill-server"); err != nil {
		t.Fatalf("kill-server: %v", err)
	}
	ctx := context.Background()
	e.srv.Sessions().Poll(ctx)
	e.srv.Sessions().Poll(ctx)
	for _, id := range ids {
		e.waitForState(id, 10*time.Second, store.StateNeedsResume)
	}

	for i, id := range ids {
		res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/resume", "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("resume %d: %d %#v", i, res.StatusCode, payload)
		}
		resumed, _ := payload["session"].(map[string]any)
		if resumed["state"] != store.StateRunning {
			t.Fatalf("session %d is %v after a resume (%v)", i, resumed["state"], resumed["fail_reason"])
		}
		if !e.hasTmuxSession(id) {
			t.Fatalf("session %d has no tmux session after its resume", i)
		}
	}
}

// TestRestartAndDeleteWorkWhenTheTmuxSessionIsGone is the second half of the
// same 3.6 answer, from the two buttons a person actually presses.
//
// A relaunch that failed leaves a row pointing at a tmux session that is not
// there. "Try again" used to answer 409 - the missing target read as a live
// pane - and Delete used to answer 500 for the same reason, which left the
// session on screen with no button that did anything.
func TestRestartAndDeleteWorkWhenTheTmuxSessionIsGone(t *testing.T) {
	e := newSessionEnv(t)
	// Two sessions, so that killing one leaves the server up: the case under
	// test is a live server with no session of ours in it, not a reboot.
	res, keep := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "keep")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, keep)
	}
	res, session := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"custom","workdir":%q}`, filepath.Join(e.work, "gone")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)
	if out, err := e.tmux("kill-session", "-t", termux.TmuxName(id)); err != nil {
		t.Fatalf("could not take the tmux session away: %v: %s", err, out)
	}

	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/restart", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("restart: %d %#v", res.StatusCode, payload)
	}
	restarted, _ := payload["session"].(map[string]any)
	if restarted["state"] != store.StateRunning {
		t.Fatalf("after Try again the session is %v (%v)", restarted["state"], restarted["fail_reason"])
	}

	// And once more, deleted this time, from the same starting point.
	if out, err := e.tmux("kill-session", "-t", termux.TmuxName(id)); err != nil {
		t.Fatalf("could not take the tmux session away again: %v: %s", err, out)
	}
	res, payload = e.do(t, e.client, "DELETE", "/api/sessions/"+id, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %#v", res.StatusCode, payload)
	}
	if _, err := e.store.GetSession(id); err == nil {
		t.Fatal("the row survived a delete that answered 200")
	}
}
