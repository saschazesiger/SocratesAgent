//go:build linux

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// Prepared chats ("PreStart"): a chat's settings and first message written
// before anything runs. A start that has to run something runs the scripted
// agent in a real host (chats_test.go); everything else is the API alone.

// Preparing stores the settings and the draft, makes no directory and runs
// nothing - and is held to the same rules as a chat that starts at once. A
// profile, which the agents' terminals ran in before accounts, is no setting
// of a chat any more.
func TestPrepareStoresARowAndNothingElse(t *testing.T) {
	e := preparedEnv(t)
	root := filepath.Join(e.home, "workspaces")

	session := e.prepare(`{"prestart":true,"harness":"claude","model":"m1","ultracode":true,
		"profile_auto":true,"prompt":"first line\nsecond","title":"Planned work"}`)
	id := sessionID(t, session)
	if session["state"] != store.StatePrestart || session["workdir"] != "" || session["draft_rev"] != float64(1) ||
		session["draft_preview"] != "first line" || session["title"] != "Planned work" || session["kind"] != store.KindChat {
		t.Fatalf("prepared session = %#v", session)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("preparing made %d directories in the workspace", len(entries))
	}
	row, _ := e.store.GetSession(id)
	if row.TmuxName != "" || strings.Contains(string(row.Options), "profile") ||
		!strings.Contains(string(row.Options), `"ultracode":true`) {
		t.Fatalf("stored row = %#v (options %s)", row, row.Options)
	}
	// Ultracode is Claude Code's alone, and another agent is prepared without.
	codex := e.prepare(`{"prestart":true,"harness":"codex","ultracode":true}`)
	if options, _ := codex["options"].(map[string]any); options["ultracode"] != nil {
		t.Fatalf("a prepared Codex chat keeps %#v", options)
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
		{`{"prestart":true,"harness":"claude","account":"a-nothere"}`, http.StatusBadRequest},
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
		`{"account":"a-nothere"}`,
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
	if res.StatusCode != http.StatusOK || payload["text"] != "" || payload["rev"] != float64(0) {
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

	// Nothing that needs a terminal works on it - a chat has none, and a
	// prepared one is not started - and renaming does.
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

// A prepared session's typed-in folder is made when it starts, so one that is
// not there yet is not a folder the session is missing; a preset is never
// made, so a prepared session whose preset is gone is.
func TestAPreparedSessionMissesOnlyAFolderNothingWillMake(t *testing.T) {
	e := preparedEnv(t)
	later := filepath.Join(e.home, "new-project")
	typed := sessionID(t, e.prepare(fmt.Sprintf(`{"prestart":true,"harness":"claude","workdir_mode":"custom","workdir":%q}`, later)))
	preset := filepath.Join(e.home, "preset")
	gone := sessionID(t, e.prepare(fmt.Sprintf(`{"prestart":true,"harness":"codex","workdir_mode":"preset","workdir":%q}`, preset)))
	if err := os.RemoveAll(preset); err != nil {
		t.Fatal(err)
	}
	_, payload := e.do(t, e.client, "GET", "/api/sessions", "")
	missing := map[string]any{}
	for _, one := range payload["sessions"].([]any) {
		session := one.(map[string]any)
		missing[session["id"].(string)] = session["workdir_missing"]
	}
	if missing[typed] != nil || missing[gone] != true {
		t.Fatalf("workdir_missing of the typed-in folder %v, of the gone preset %v", missing[typed], missing[gone])
	}
	if _, err := os.Stat(later); !os.IsNotExist(err) {
		t.Fatalf("listing the sessions made the typed-in folder: %v", err)
	}
}

// prepareChat prepares a chat of the scripted agent, which a start runs in a
// real host.
func (c *chatEnv) prepareChat(t *testing.T, body map[string]any) string {
	t.Helper()
	body["prestart"], body["harness"] = true, agent.FakeHarness
	code, made := c.post(t, "/api/sessions", body)
	if code != http.StatusCreated {
		t.Fatalf("prepare: %d %v", code, made)
	}
	return made["session"].(map[string]any)["id"].(string)
}

// workspace points the chat server's workspace at a root and one preset.
func (c *chatEnv) workspace(t *testing.T, root, preset string) {
	t.Helper()
	for _, dir := range []string{root, preset} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`{"settings":{"workspace":{"root":%q,"allow_custom":true,"presets":[{"label":"Preset","path":%q}]}}}`,
		root, preset)
	if res, payload := c.do(t, c.client, "PUT", "/api/settings", body); res.StatusCode != http.StatusOK {
		t.Fatalf("settings: %d %#v", res.StatusCode, payload)
	}
}

// A start whose settings no longer work leaves the chat prepared, with the
// reason and the draft, and starts nothing; fixed, it starts with the draft
// as its first message, exactly once.
func TestAFailedStartKeepsTheSessionPrepared(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	preset := filepath.Join(c.data, "preset")
	c.workspace(t, filepath.Join(c.data, "workspaces"), preset)
	id := c.prepareChat(t, map[string]any{"workdir_mode": "preset", "workdir": preset, "prompt": "keep me"})
	if err := os.RemoveAll(preset); err != nil {
		t.Fatal(err)
	}
	res, payload := c.do(t, c.client, "POST", "/api/sessions/"+id+"/start", `{}`)
	session, _ := payload["session"].(map[string]any)
	if res.StatusCode != http.StatusBadRequest || payload["error"] == nil || session["state"] != store.StatePrestart ||
		session["fail_reason"] == "" || session["fail_reason"] == nil {
		t.Fatalf("a start into a missing preset: %d %#v", res.StatusCode, payload)
	}
	if alive, _ := c.srv.chats.launcher.Alive(id); alive {
		t.Fatal("a failed start started a host")
	}
	if d, _ := c.store.GetDraft(id); d.Text != "keep me" {
		t.Fatalf("the draft after a failed start = %q", d.Text)
	}
	// Fixing the setting clears the reason, and the start works.
	if err := os.MkdirAll(preset, 0o755); err != nil {
		t.Fatal(err)
	}
	res, payload = c.do(t, c.client, "PATCH", "/api/sessions/"+id+"/prestart", `{"model":""}`)
	if session, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK || session["fail_reason"] != nil {
		t.Fatalf("patch after a failure: %d %#v", res.StatusCode, payload)
	}
	res, payload = c.do(t, c.client, "POST", "/api/sessions/"+id+"/start", `{}`)
	if session, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK || session["state"] != store.StateRunning {
		t.Fatalf("the second start: %d %#v", res.StatusCode, payload)
	}
	events := c.waitJournal(t, id, 20*time.Second, "the first turn", turnsEnded(1))
	if users := count(events, func(ev agent.Event) bool { return ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser }); users != 1 ||
		!strings.Contains(lastSaid(events), "keep me") {
		t.Fatalf("%d user messages, the agent said %q", users, lastSaid(events))
	}
	// Start again: idempotent, and nothing is sent twice.
	res, payload = c.do(t, c.client, "POST", "/api/sessions/"+id+"/start", `{}`)
	if again, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK || again["state"] != store.StateRunning {
		t.Fatalf("a second start: %d %#v", res.StatusCode, payload)
	}
}

// A prepared terminal of an agent is from before the agents were chats; the
// migration made it a chat or took it away (REBUILD.md §8.3). One that is
// still there is neither changed nor started.
func TestAPreparedAgentTerminalIsNotStarted(t *testing.T) {
	e := preparedEnv(t)
	if err := e.store.CreateSession(&store.Session{ID: "old", Harness: "claude", Workdir: e.home,
		WorkdirMode: store.WorkdirCustom, State: store.StatePrestart, Kind: store.KindTerminal}); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.do(t, e.client, "PATCH", "/api/sessions/old/prestart", `{"model":"x"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a change: %d", res.StatusCode)
	}
	if res, _ := e.do(t, e.client, "POST", "/api/sessions/old/start", `{}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a start: %d", res.StatusCode)
	}
	if row, _ := e.store.GetSession("old"); row.State != store.StatePrestart {
		t.Fatalf("the row is %s", row.State)
	}
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
func subscribe(t *testing.T, e *testEnv) <-chan sentPush {
	t.Helper()
	sent := catch(e.srv.push)
	if err := e.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	return sent
}

// The scheduler, on a clock of the test's choosing: nothing before its time,
// the due chat at its time - its draft the first message - and the other one
// not, a failure reported once and never retried.
func TestSchedulerStartsWhatIsDue(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	preset := filepath.Join(c.data, "preset")
	c.workspace(t, filepath.Join(c.data, "workspaces"), preset)
	sent := subscribe(t, c.testEnv)
	ctx := context.Background()
	now := time.Now()
	hour := time.Hour.Milliseconds()

	soon := c.prepareChat(t, map[string]any{"title": "Soon", "prompt": "go now", "start_at": now.UnixMilli() + hour,
		"workdir_mode": "custom", "workdir": c.data})
	later := c.prepareChat(t, map[string]any{"title": "Later", "start_at": now.UnixMilli() + 3*hour})

	if n := c.srv.sched.runDue(ctx, now.Add(30*time.Minute)); n != 0 {
		t.Fatalf("%d sessions started before their time", n)
	}
	if next := c.srv.sched.nextDue(); next != now.UnixMilli()+hour {
		t.Fatalf("next due = %d", next)
	}
	if n := c.srv.sched.runDue(ctx, now.Add(2*time.Hour)); n != 1 {
		t.Fatalf("%d sessions started at the first one's time", n)
	}
	row, _ := c.store.GetSession(soon)
	if row.State != store.StateRunning || row.StartAt != 0 {
		t.Fatalf("the due session = %#v", row)
	}
	if note := pushOfKind(t, sent, pushStarted, 10*time.Second); note.Title != "Started: Soon" || note.ID != soon {
		t.Fatalf("push = %#v", note)
	}
	events := c.waitJournal(t, soon, 20*time.Second, "the first turn", turnsEnded(1))
	if !strings.Contains(lastSaid(events), "go now") {
		t.Fatalf("the agent said %q", lastSaid(events))
	}
	if row, _ := c.store.GetSession(later); row.State != store.StatePrestart || row.StartAt == 0 {
		t.Fatalf("the later session = %#v", row)
	}

	// A start that fails is reported, keeps the chat prepared with the
	// reason, clears the schedule and is not tried again.
	res, _ := c.do(t, c.client, "PATCH", "/api/sessions/"+later+"/prestart",
		fmt.Sprintf(`{"workdir_mode":"preset","workdir":%q}`, preset))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d", res.StatusCode)
	}
	if err := os.RemoveAll(preset); err != nil {
		t.Fatal(err)
	}
	if n := c.srv.sched.runDue(ctx, now.Add(4*time.Hour)); n != 1 {
		t.Fatalf("%d starts claimed", n)
	}
	row, _ = c.store.GetSession(later)
	if row.State != store.StatePrestart || row.StartAt != 0 || row.FailReason == "" {
		t.Fatalf("after a failed scheduled start = %#v", row)
	}
	if note := pushOfKind(t, sent, pushStarted, 10*time.Second); note.Title != "Scheduled start failed: Later" || note.Body == "" {
		t.Fatalf("push = %#v", note)
	}
	if n := c.srv.sched.runDue(ctx, now.Add(5*time.Hour)); n != 0 {
		t.Fatalf("a failed start was tried again: %d", n)
	}
}

// A schedule that fell due while the server was down starts as soon as the
// scheduler is up, by its first pass and on the real clock.
func TestOverdueScheduleStartsAtStartup(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.prepareChat(t, map[string]any{"prompt": "overdue start", "start_at": time.Now().Add(time.Hour).UnixMilli(),
		"workdir_mode": "custom", "workdir": c.data})
	// The server was down when it fell due.
	row, _ := c.store.GetSession(id)
	if err := c.store.UpdatePrestart(id, store.PrestartSettings{
		Harness: row.Harness, Model: row.Model, Effort: row.Effort, WorkdirMode: row.WorkdirMode,
		Workdir: row.Workdir, Options: row.Options, StartAt: time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.srv.sched.start(ctx)
	events := c.waitJournal(t, id, 20*time.Second, "the first turn", turnsEnded(1))
	if !strings.Contains(lastSaid(events), "overdue start") {
		t.Fatalf("the agent said %q", lastSaid(events))
	}
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
	row, status, err := e.srv.startPrepared(context.Background(), id, true)
	if !errors.Is(err, errNotDue) || status != http.StatusConflict || row == nil {
		t.Fatalf("scheduled start of a started session = %v %d %v", row, status, err)
	}
	// The button's own repeat is still idempotent.
	if _, status, err := e.srv.startPrepared(context.Background(), id, false); err != nil || status != http.StatusOK {
		t.Fatalf("a repeated Start = %d %v", status, err)
	}
}

// A draft saved while a start is under way waits for it, and is then told
// the truth: a start that went through has sent the older text and emptied
// the chat's composer, so the save is a conflict with what is there now
// rather than acknowledged for nothing.
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
	// What a start that went through does: the row is running, and the
	// draft it sent is taken out of the composer.
	if err := e.store.SetSessionState(id, store.StateRunning, -1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.SaveDraft(id, "", 1); err != nil {
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
	if draft, _ := e.store.GetDraft(id); draft.Text != "" {
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
