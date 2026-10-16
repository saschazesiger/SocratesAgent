//go:build !windows

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// preparedEnv is a signed-in server whose terminal substrate has NOT been
// started: preparing a session must work without tmux.
func preparedEnv(t *testing.T) *sessionEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := &sessionEnv{testEnv: newEnv(t), t: t, home: home, work: filepath.Join(home, "work")}
	e.signIn()
	e.configureWorkspace(filepath.Join(home, "workspaces"), true, filepath.Join(home, "preset"))
	return e
}

func (e *sessionEnv) prepare(body string) map[string]any {
	e.t.Helper()
	res, payload := e.do(e.t, e.client, "POST", "/api/sessions", body)
	if res.StatusCode != http.StatusCreated {
		e.t.Fatalf("prepare: %d %#v", res.StatusCode, payload)
	}
	session, _ := payload["session"].(map[string]any)
	return session
}

// putDraft saves a draft with a JSON-encoded text.
func (e *sessionEnv) putDraft(id, text string, base int64) (*http.Response, map[string]any) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"text": text, "base_rev": base})
	return e.do(e.t, e.client, "PUT", "/api/sessions/"+id+"/draft", string(body))
}

// Preparing stores the settings and the draft, makes no directory and runs
// nothing - and is held to the same rules as a create.
func TestPrepareStoresARowAndNothingElse(t *testing.T) {
	e := preparedEnv(t)
	root := filepath.Join(e.home, "workspaces")

	session := e.prepare(`{"prestart":true,"harness":"claude","model":"m1","ultracode":true,
		"profile_auto":true,"prompt":"first line\nsecond","title":"Planned work"}`)
	id := sessionID(t, session)
	if session["state"] != store.StatePrestart || session["workdir"] != "" || session["draft_rev"] != float64(1) ||
		session["draft_preview"] != "first line" || session["title"] != "Planned work" {
		t.Fatalf("prepared session = %#v", session)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("preparing made %d directories in the workspace", len(entries))
	}
	row, _ := e.store.GetSession(id)
	if row.TmuxName != "" || !strings.Contains(string(row.Options), `"profile_auto":true`) ||
		!strings.Contains(string(row.Options), `"ultracode":true`) {
		t.Fatalf("stored row = %#v (options %s)", row, row.Options)
	}
	if _, err := os.Stat(termux.SessionDir(e.srv.dataDir, id)); !os.IsNotExist(err) {
		t.Fatalf("preparing made a session directory: %v", err)
	}

	for _, c := range []struct {
		body string
		code int
	}{
		{`{"prestart":true,"harness":"shell"}`, http.StatusBadRequest},
		{`{"prestart":true,"harness":"vscode"}`, http.StatusBadRequest},
		{`{"prestart":true,"harness":"codex","ultracode":true}`, http.StatusBadRequest},
		{`{"prestart":true,"harness":"claude","profile":"p-nothere"}`, http.StatusBadRequest},
		{`{"prestart":true,"harness":"claude","workdir_mode":"preset","workdir":"/not/a/preset"}`, http.StatusBadRequest},
		{`{"prestart":true,"harness":"claude","workdir_mode":"custom","workdir":"/etc"}`, http.StatusBadRequest},
		{fmt.Sprintf(`{"prestart":true,"harness":"claude","start_at":%d}`, time.Now().Add(-time.Minute).UnixMilli()), http.StatusBadRequest},
		{`{"prestart":true,"harness":"claude","prompt":"` + strings.Repeat("x", maxDraftBytes+1) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		res, payload := e.do(t, e.client, "POST", "/api/sessions", c.body)
		if res.StatusCode != c.code {
			t.Errorf("%.80s: %d %#v, want %d", c.body, res.StatusCode, payload, c.code)
		}
	}

	// Idempotent on the client key, like a create.
	first := e.prepare(`{"prestart":true,"harness":"codex","client_id":"k1"}`)
	res, payload := e.do(t, e.client, "POST", "/api/sessions", `{"prestart":true,"harness":"codex","client_id":"k1"}`)
	again, _ := payload["session"].(map[string]any)
	if res.StatusCode != http.StatusOK || again["id"] != first["id"] {
		t.Fatalf("a repeated prepare: %d %#v", res.StatusCode, payload)
	}
}

func TestPreparedSettingsAndDraft(t *testing.T) {
	e := preparedEnv(t)
	id := sessionID(t, e.prepare(`{"prestart":true,"harness":"claude","ultracode":true}`))

	// Switching to Codex drops Claude Code's ultracode rather than refusing.
	at := time.Now().Add(2 * time.Hour).UnixMilli()
	res, payload := e.do(t, e.client, "PATCH", "/api/sessions/"+id+"/prestart",
		fmt.Sprintf(`{"harness":"codex","model":"gpt-x","workdir_mode":"preset","workdir":%q,"start_at":%d}`,
			filepath.Join(e.home, "preset"), at))
	session, _ := payload["session"].(map[string]any)
	if res.StatusCode != http.StatusOK || session["harness"] != "codex" || session["model"] != "gpt-x" ||
		session["workdir_mode"] != "preset" || session["start_at"] != float64(at) {
		t.Fatalf("patch: %d %#v", res.StatusCode, payload)
	}
	if row, _ := e.store.GetSession(id); strings.Contains(string(row.Options), "ultracode") {
		t.Fatalf("ultracode survived the switch to Codex: %s", row.Options)
	}
	for _, body := range []string{
		`{"harness":"shell"}`,
		`{"ultracode":true}`,
		`{"start_at":5}`,
		`{"workdir_mode":"custom","workdir":"/proc/x"}`,
	} {
		if res, _ := e.do(t, e.client, "PATCH", "/api/sessions/"+id+"/prestart", body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("patch %s: %d", body, res.StatusCode)
		}
	}
	res, payload = e.do(t, e.client, "PATCH", "/api/sessions/"+id+"/prestart", `{"start_at":0}`)
	if session, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK || session["start_at"] != float64(0) {
		t.Fatalf("clearing the schedule: %d %#v", res.StatusCode, payload)
	}

	// The draft: revision checked, conflicts handed back, size bounded.
	res, payload = e.do(t, e.client, "GET", "/api/sessions/"+id+"/draft", "")
	if res.StatusCode != http.StatusOK || payload["text"] != "" || payload["rev"] != float64(0) || payload["backed_up"] != false {
		t.Fatalf("empty draft: %d %#v", res.StatusCode, payload)
	}
	if res, payload = e.putDraft(id, "one", 0); res.StatusCode != http.StatusOK || payload["rev"] != float64(1) {
		t.Fatalf("first save: %d %#v", res.StatusCode, payload)
	}
	if res, payload = e.putDraft(id, "two", 1); res.StatusCode != http.StatusOK || payload["rev"] != float64(2) {
		t.Fatalf("second save: %d %#v", res.StatusCode, payload)
	}
	res, payload = e.putDraft(id, "stale", 1)
	if res.StatusCode != http.StatusConflict || payload["conflict"] != true || payload["text"] != "two" || payload["rev"] != float64(2) {
		t.Fatalf("stale save: %d %#v", res.StatusCode, payload)
	}
	if res, _ = e.putDraft(id, strings.Repeat("x", maxDraftBytes+1), 2); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized draft: %d", res.StatusCode)
	}
	res, payload = e.do(t, e.client, "GET", "/api/sessions", "")
	list, _ := payload["sessions"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["draft_rev"] != float64(2) || list[0].(map[string]any)["draft_preview"] != "two" {
		t.Fatalf("list = %#v", list)
	}

	// Nothing that needs a terminal works on it, and renaming does.
	for _, route := range [][2]string{
		{"POST", "/resume"}, {"POST", "/restart"}, {"GET", "/journal"}, {"POST", "/upload"}, {"GET", "/ws"},
	} {
		if res, _ := e.do(t, e.client, route[0], "/api/sessions/"+id+route[1], ""); res.StatusCode != http.StatusConflict {
			t.Errorf("%s %s on a prepared session: %d", route[0], route[1], res.StatusCode)
		}
	}
	if res, payload := e.do(t, e.client, "PATCH", "/api/sessions/"+id, `{"title":"Renamed"}`); res.StatusCode != http.StatusOK ||
		payload["session"].(map[string]any)["title"] != "Renamed" {
		t.Fatalf("rename: %d %#v", res.StatusCode, payload)
	}
	if res, _ := e.do(t, e.client, "DELETE", "/api/sessions/"+id, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if _, err := e.store.GetDraft(id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the draft outlived the session: %v", err)
	}
}

// bigPrompt is about fifty kilobytes of what a shell would mangle.
func bigPrompt() string {
	var b strings.Builder
	b.WriteString("Plan: 'single' \"double\" $HOME `id` ünïcödé 日本語 🙂\n")
	for i := 0; b.Len() < 50<<10; i++ {
		fmt.Fprintf(&b, "%d: $(echo nope) `uname` \\ ' \" ; | & > %% * ? [x] {y} ~ # ß\n", i)
	}
	return b.String() + "--\nend\n"
}

// fakeLaunchPrompt is the prompt the fake recorded for a session's launch.
func fakeLaunchPrompt(t *testing.T, path, sessionID string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	prompt, found := "", false
	for _, line := range strings.Split(string(raw), "\n") {
		var rec struct {
			Env    map[string]string `json:"env"`
			Prompt *string           `json:"prompt"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Env["SOCRATES_SESSION"] != sessionID {
			continue
		}
		found = true
		prompt = ""
		if rec.Prompt != nil {
			prompt = *rec.Prompt
		}
	}
	return prompt, found
}

// Start is a create: the prepared session runs the program it would have
// run from the sheet, at the size the page measured, with the draft - fifty
// kilobytes of quotes, dollars and backticks - as its first prompt, through
// the real tmux launch. Afterwards the draft is kept and read-only, a second
// Start answers with the running session, and a restart does not send the
// prompt again.
func TestStartRunsThePreparedSessionWithItsPrompt(t *testing.T) {
	fakeLog := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", fakeLog)
	e := newSessionEnv(t)

	id := sessionID(t, e.prepare(`{"prestart":true,"harness":"claude","title":"Big one","profile_auto":true}`))
	prompt := bigPrompt()
	if res, payload := e.putDraft(id, prompt, 0); res.StatusCode != http.StatusOK {
		t.Fatalf("draft: %d %#v", res.StatusCode, payload)
	}

	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/start", `{"cols":100,"rows":30}`)
	session, _ := payload["session"].(map[string]any)
	if res.StatusCode != http.StatusOK || session["state"] != store.StateRunning {
		t.Fatalf("start: %d %#v", res.StatusCode, payload)
	}
	if session["cols"] != float64(100) || session["rows"] != float64(30) || session["start_at"] != float64(0) {
		t.Fatalf("the started session = %#v", session)
	}
	e.waitForPane(id, "prompt: Plan: 'single'")
	var got string
	waitForCond(t, 10*time.Second, "the fake to log its launch", func() bool {
		var ok bool
		got, ok = fakeLaunchPrompt(t, fakeLog, id)
		return ok
	})
	if want := strings.TrimRight(prompt, "\n"); got != want {
		t.Fatalf("the program got %d bytes of prompt, want %d", len(got), len(want))
	}

	row, _ := e.store.GetSession(id)
	if strings.Contains(string(row.Options), "profile_auto") {
		t.Fatalf("the running session still asks for a profile to be chosen: %s", row.Options)
	}
	if !strings.HasPrefix(row.Workdir, filepath.Join(e.home, "workspaces")) {
		t.Fatalf("the dynamic directory is %q", row.Workdir)
	}
	argv := e.planArgv(id)
	if argv[0] != "/bin/sh" || !strings.Contains(strings.Join(argv, " "), "--name Big one") {
		t.Fatalf("plan argv = %q", argv)
	}
	if raw, _ := os.ReadFile(filepath.Join(e.srv.dataDir, "sessions", id, "plan.json")); strings.Contains(string(raw), "$(echo nope)") {
		t.Fatal("plan.json carries the prompt")
	}
	if _, err := os.Stat(filepath.Join(e.srv.dataDir, "sessions", id, harnesses.InitialPromptFile)); !os.IsNotExist(err) {
		t.Fatalf("the prompt file outlived the start: %v", err)
	}

	// The draft is kept, and is read-only now.
	res, payload = e.do(t, e.client, "GET", "/api/sessions/"+id+"/draft", "")
	if res.StatusCode != http.StatusOK || payload["text"] != prompt {
		t.Fatalf("the draft after the start: %d", res.StatusCode)
	}
	if res, payload = e.putDraft(id, "edit", 1); res.StatusCode != http.StatusConflict || payload["started"] != true {
		t.Fatalf("editing a started draft: %d %#v", res.StatusCode, payload)
	}
	if res, _ = e.do(t, e.client, "PATCH", "/api/sessions/"+id+"/prestart", `{"model":"x"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("changing a started session's settings: %d", res.StatusCode)
	}
	// Start again: idempotent.
	res, payload = e.do(t, e.client, "POST", "/api/sessions/"+id+"/start", `{}`)
	if again, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK || again["state"] != store.StateRunning {
		t.Fatalf("a second start: %d %#v", res.StatusCode, payload)
	}

	// A restart plans from the row, and the row has no prompt.
	e.typeLine(id, "/exit 0")
	e.waitForState(id, 15*time.Second, store.StateExited)
	if res, payload = e.do(t, e.client, "POST", "/api/sessions/"+id+"/restart", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("restart: %d %#v", res.StatusCode, payload)
	}
	argv = e.planArgv(id)
	if argv[0] == "/bin/sh" || strings.Contains(strings.Join(argv, " "), "@socrates-initial-prompt@") {
		t.Fatalf("the restart sent the prompt again: %q", argv)
	}
}

// A start whose settings no longer work leaves the session prepared, with the
// reason and the draft, and creates nothing.
func TestAFailedStartKeepsTheSessionPrepared(t *testing.T) {
	e := newSessionEnv(t)
	preset := filepath.Join(e.home, "preset")
	id := sessionID(t, e.prepare(fmt.Sprintf(`{"prestart":true,"harness":"codex","workdir_mode":"preset","workdir":%q,"prompt":"keep me"}`, preset)))
	if err := os.RemoveAll(preset); err != nil {
		t.Fatal(err)
	}
	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/start", `{}`)
	session, _ := payload["session"].(map[string]any)
	if res.StatusCode != http.StatusBadRequest || payload["error"] == nil || session["state"] != store.StatePrestart ||
		session["fail_reason"] == "" || session["fail_reason"] == nil {
		t.Fatalf("a start into a missing preset: %d %#v", res.StatusCode, payload)
	}
	if e.hasTmuxSession(id) {
		t.Fatal("a failed start left a tmux session")
	}
	if d, _ := e.store.GetDraft(id); d.Text != "keep me" {
		t.Fatalf("the draft after a failed start = %q", d.Text)
	}
	// Fixing the setting clears the reason, and the start works.
	if err := os.MkdirAll(preset, 0o755); err != nil {
		t.Fatal(err)
	}
	res, payload = e.do(t, e.client, "PATCH", "/api/sessions/"+id+"/prestart", `{"model":""}`)
	if session, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK || session["fail_reason"] != nil {
		t.Fatalf("patch after a failure: %d %#v", res.StatusCode, payload)
	}
	res, payload = e.do(t, e.client, "POST", "/api/sessions/"+id+"/start", `{}`)
	if session, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK || session["state"] != store.StateRunning {
		t.Fatalf("the second start: %d %#v", res.StatusCode, payload)
	}
	e.waitForPane(id, "prompt: keep me (7 bytes)")
}

func waitForCond(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// subscribe gives the server one push subscription and returns what is sent
// to it.
func subscribe(t *testing.T, e *sessionEnv) <-chan sentPush {
	t.Helper()
	sent := catch(e.srv.push)
	if err := e.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	return sent
}

func nextPush(t *testing.T, sent <-chan sentPush) pushNote {
	t.Helper()
	select {
	case p := <-sent:
		return p.note
	case <-time.After(10 * time.Second):
		t.Fatal("no push was sent")
	}
	return pushNote{}
}

// The scheduler, on a clock of the test's choosing: nothing before its time,
// the due session at its time and the other one not, a failure reported once
// and never retried, and nothing at all while the gate is shut.
func TestSchedulerStartsWhatIsDue(t *testing.T) {
	e := newSessionEnv(t)
	sent := subscribe(t, e)
	ctx := context.Background()
	now := time.Now()
	hour := time.Hour.Milliseconds()

	soon := sessionID(t, e.prepare(fmt.Sprintf(`{"prestart":true,"harness":"claude","title":"Soon","prompt":"go now","start_at":%d}`, now.UnixMilli()+hour)))
	later := sessionID(t, e.prepare(fmt.Sprintf(`{"prestart":true,"harness":"codex","title":"Later","start_at":%d}`, now.UnixMilli()+3*hour)))

	if n := e.srv.sched.runDue(ctx, now.Add(30*time.Minute)); n != 0 {
		t.Fatalf("%d sessions started before their time", n)
	}
	if next := e.srv.sched.nextDue(); next != now.UnixMilli()+hour {
		t.Fatalf("next due = %d", next)
	}
	if n := e.srv.sched.runDue(ctx, now.Add(2*time.Hour)); n != 1 {
		t.Fatalf("%d sessions started at the first one's time", n)
	}
	row, _ := e.store.GetSession(soon)
	if row.State != store.StateRunning || row.StartAt != 0 {
		t.Fatalf("the due session = %#v", row)
	}
	e.waitForPane(soon, "prompt: go now (6 bytes)")
	if note := nextPush(t, sent); note.Title != "Started: Soon" || note.ID != soon {
		t.Fatalf("push = %#v", note)
	}
	if row, _ := e.store.GetSession(later); row.State != store.StatePrestart || row.StartAt == 0 {
		t.Fatalf("the later session = %#v", row)
	}

	// The gate shut - a fenced mirror - starts nothing and keeps the schedule.
	e.srv.SetAutoStartGate(func() error { return errors.New("another Socrates holds the bucket") })
	if n := e.srv.sched.runDue(ctx, now.Add(4*time.Hour)); n != 0 {
		t.Fatalf("%d sessions started through a shut gate", n)
	}
	if row, _ := e.store.GetSession(later); row.State != store.StatePrestart || row.StartAt != now.UnixMilli()+3*hour {
		t.Fatalf("a shut gate touched the schedule: %#v", row)
	}
	e.srv.SetAutoStartGate(nil)

	// A start that fails is reported, keeps the session prepared with the
	// reason, clears the schedule and is not tried again.
	preset := filepath.Join(e.home, "preset")
	res, _ := e.do(t, e.client, "PATCH", "/api/sessions/"+later+"/prestart",
		fmt.Sprintf(`{"workdir_mode":"preset","workdir":%q}`, preset))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d", res.StatusCode)
	}
	if err := os.RemoveAll(preset); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.sched.runDue(ctx, now.Add(4*time.Hour)); n != 1 {
		t.Fatalf("%d starts claimed", n)
	}
	row, _ = e.store.GetSession(later)
	if row.State != store.StatePrestart || row.StartAt != 0 || row.FailReason == "" {
		t.Fatalf("after a failed scheduled start = %#v", row)
	}
	if note := nextPush(t, sent); note.Title != "Scheduled start failed: Later" || note.Body == "" {
		t.Fatalf("push = %#v", note)
	}
	if n := e.srv.sched.runDue(ctx, now.Add(5*time.Hour)); n != 0 {
		t.Fatalf("a failed start was tried again: %d", n)
	}
}

// A schedule that fell due while the server was down starts as soon as the
// substrate is up, by the scheduler's first pass and on the real clock.
func TestOverdueScheduleStartsAtStartup(t *testing.T) {
	bin := requireTmux(t)
	fakes := fakeBinDir(t)
	e := preparedEnv(t)
	e.tmuxBin = bin
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(e.home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(e.home, ".codex"))

	id := sessionID(t, e.prepare(fmt.Sprintf(`{"prestart":true,"harness":"claude","prompt":"overdue start","start_at":%d}`,
		time.Now().Add(time.Hour).UnixMilli())))
	// The server was down when it fell due.
	row, _ := e.store.GetSession(id)
	if err := e.store.UpdatePrestart(id, store.PrestartSettings{
		Harness: row.Harness, Model: row.Model, Effort: row.Effort, WorkdirMode: row.WorkdirMode,
		Workdir: row.Workdir, Options: row.Options, StartAt: time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	e.start(e.srv)
	e.waitForState(id, 20*time.Second, store.StateRunning)
	e.waitForPane(id, "prompt: overdue start (13 bytes)")
}

// The scheduler owns only the starts it claims: a session somebody started
// between its read of the list and its turn is not its start, and it says
// nothing about it.
func TestTheSchedulerDoesNotClaimAStartItDidNotMake(t *testing.T) {
	e := preparedEnv(t)
	id := sessionID(t, e.prepare(fmt.Sprintf(`{"prestart":true,"harness":"claude","start_at":%d}`,
		time.Now().Add(time.Hour).UnixMilli())))
	if err := e.store.SetSessionState(id, store.StateRunning, -1, ""); err != nil { // the Start button won
		t.Fatal(err)
	}
	row, status, err := e.srv.startPrepared(context.Background(), id, 0, 0, true)
	if !errors.Is(err, errNotDue) || status != http.StatusConflict || row == nil {
		t.Fatalf("scheduled start of a started session = %v %d %v", row, status, err)
	}
	// The button's own repeat is still idempotent.
	if _, status, err := e.srv.startPrepared(context.Background(), id, 0, 0, false); err != nil || status != http.StatusOK {
		t.Fatalf("a repeated Start = %d %v", status, err)
	}
}

// A draft saved while a start is under way waits for it, and is then told
// the truth: a start that went through has sent the older text, so the save
// is refused as "started" rather than acknowledged for nothing.
func TestADraftSaveDuringAStartWaitsForIt(t *testing.T) {
	e := preparedEnv(t)
	id := sessionID(t, e.prepare(`{"prestart":true,"harness":"claude","prompt":"the text that was sent"}`))
	unlock := e.srv.starts.lock(id) // a start in progress
	done := make(chan int, 1)
	go func() {
		res, _ := e.putDraft(id, "typed during the start", 1)
		done <- res.StatusCode
	}()
	select {
	case code := <-done:
		unlock()
		t.Fatalf("the save did not wait for the start: %d", code)
	case <-time.After(300 * time.Millisecond):
	}
	if err := e.store.SetSessionState(id, store.StateRunning, -1, ""); err != nil {
		t.Fatal(err)
	}
	unlock()
	select {
	case code := <-done:
		if code != http.StatusConflict {
			t.Fatalf("a save that lost to the start = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the save never finished")
	}
	if draft, _ := e.store.GetDraft(id); draft.Text != "the text that was sent" {
		t.Fatalf("draft = %q", draft.Text)
	}
}

// A prepared session nobody named is listed under what it will run: a change
// of harness renames the placeholder, a name somebody gave stays.
func TestAPreparedSessionsPlaceholderFollowsItsHarness(t *testing.T) {
	e := preparedEnv(t)
	id := sessionID(t, e.prepare(`{"prestart":true,"harness":"claude"}`))
	if row, _ := e.store.GetSession(id); !strings.HasPrefix(row.Title, "Claude") {
		t.Fatalf("placeholder = %q", row.Title)
	}
	if res, _ := e.do(t, e.client, "PATCH", "/api/sessions/"+id+"/prestart", `{"harness":"codex"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", res.StatusCode)
	}
	if row, _ := e.store.GetSession(id); !strings.HasPrefix(row.Title, "Codex") {
		t.Fatalf("after switching to Codex the session is listed as %q", row.Title)
	}

	named := sessionID(t, e.prepare(`{"prestart":true,"harness":"claude","title":"Mine"}`))
	if res, _ := e.do(t, e.client, "PATCH", "/api/sessions/"+named+"/prestart", `{"harness":"codex"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", res.StatusCode)
	}
	if row, _ := e.store.GetSession(named); row.Title != "Mine" {
		t.Fatalf("a given name became %q", row.Title)
	}
}

func TestCompanionProfilesAreTheActiveSessionsInTheFolder(t *testing.T) {
	session := func(id, harness, workdir, state, status, options string, activeAt int64) store.Session {
		return store.Session{ID: id, Harness: harness, Workdir: workdir, State: state, Status: status,
			Options: json.RawMessage(options), ActiveAt: activeAt}
	}
	rows := []store.Session{
		session("a", "claude", "/srv/app", store.StateRunning, store.StatusInProgress, `{"profile":"p-a"}`, 10),
		session("b", "claude", "/srv/app/", store.StateRunning, store.StatusReview, `{"profile":"p-b"}`, 30),
		session("c", "claude", "/srv/app", store.StateRunning, store.StatusDone, `{"profile":"p-c"}`, 50),
		session("d", "claude", "/srv/app", store.StateExited, store.StatusInProgress, `{"profile":"p-d"}`, 50),
		session("e", "codex", "/srv/app", store.StateRunning, store.StatusInProgress, `{"profile":"p-e"}`, 50),
		session("f", "claude", "/srv/other", store.StateRunning, store.StatusInProgress, `{"profile":"p-f"}`, 50),
		session("g", "claude", "/srv/app", store.StateStarting, store.StatusInProgress, `{}`, 20),
		session("self", "claude", "/srv/app", store.StateRunning, store.StatusInProgress, `{"profile":"p-self"}`, 90),
	}
	got := companionProfiles(rows, "claude", "/srv/app", "self")
	want := map[string]int64{"p-a": 10, "p-b": 30, "": 20}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("companionProfiles = %v, want %v", got, want)
	}
	views := []profileView{{ID: ""}, {ID: "p-a"}, {ID: "p-b"}}
	// The recommended profile wins when a companion runs in it...
	if id, ok := companionProfile(views, got, "p-a"); !ok || id != "p-a" {
		t.Fatalf("companionProfile with p-a recommended = %q, %v", id, ok)
	}
	// ...and otherwise the one active last.
	if id, ok := companionProfile(views, got, "p-x"); !ok || id != "p-b" {
		t.Fatalf("companionProfile = %q, %v; want p-b", id, ok)
	}
	// A deleted profile is not joined.
	if _, ok := companionProfile(views[:1], map[string]int64{"p-gone": 5}, ""); ok {
		t.Fatal("companionProfile chose a profile that no longer exists")
	}
	// A host shell has no profile to share.
	shells := []store.Session{session("h", "shell", "/srv/app", store.StateRunning, store.StatusInProgress, `{"shell_execution":"host","profile":"p-a"}`, 5)}
	if got := companionProfiles(shells, "shell", "/srv/app", ""); len(got) != 0 {
		t.Fatalf("companionProfiles for a host shell = %v", got)
	}
}
