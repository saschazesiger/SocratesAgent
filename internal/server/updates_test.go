package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The updates card reads two files the updater leaves and asks systemd two
// questions. Here systemd is a function that answers from a table, and the
// files are written into the test server's data directory by hand.

// fakeSystemd is a machine's systemd as far as the updates card asks it.
type fakeSystemd struct {
	mu    sync.Mutex
	units map[string]string // unit -> what `systemctl show` prints
	calls [][]string
	// startErr is what `systemctl start` fails with, when it should.
	startErr error
}

func (f *fakeSystemd) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	if name != "systemctl" || len(args) < 2 {
		return nil, errors.New("unexpected command")
	}
	switch args[0] {
	case "show":
		if out, ok := f.units[args[1]]; ok {
			return []byte(out), nil
		}
		// What systemd itself says about a unit it does not know.
		return []byte("LoadState=not-found\nActiveState=inactive\nUnitFileState=\n"), nil
	case "start":
		return nil, f.startErr
	}
	return nil, errors.New("unexpected systemctl verb")
}

func (f *fakeSystemd) starts() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, call := range f.calls {
		if len(call) > 1 && call[1] == "start" {
			out = append(out, call)
		}
	}
	return out
}

// installedSystemd is a machine with the updater installed, its timer enabled
// and the service in the given state.
func installedSystemd(active string) *fakeSystemd {
	return &fakeSystemd{units: map[string]string{
		updateTimer: "LoadState=loaded\nUnitFileState=enabled\n" +
			"NextElapseUSecRealtime=Fri 2026-10-23 04:17:00 UTC\nLastTriggerUSec=n/a\n",
		updateService: "LoadState=loaded\nActiveState=" + active + "\n",
	}}
}

func updatesEnv(t *testing.T, systemd *fakeSystemd) *testEnv {
	t.Helper()
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	if systemd != nil {
		env.srv.updates.run = systemd.run
	} else {
		env.srv.updates.run = func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New(`exec: "systemctl": executable file not found in $PATH`)
		}
	}
	if err := os.MkdirAll(env.srv.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return env
}

func writeUpdatesFile(t *testing.T, env *testEnv, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(env.srv.dataDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const doneReport = `{"schema":1,"state":"done","trigger":"timer",
 "started_at":"2026-10-22T04:17:00Z","finished_at":"2026-10-22T04:19:30Z","ok":true,
 "components":[
  {"name":"claude","version":"2.1.3","previous":"2.1.2","status":"updated","error":"","detail":""},
  {"name":"tmux","version":"3.4","previous":"","status":"held","error":"","detail":"tmux 3.5 is available but held"}]}`

func TestUpdatesWithNothingThere(t *testing.T) {
	env := updatesEnv(t, nil)
	res, data := env.do(t, env.client, "GET", "/api/updates", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/api/updates answered %d", res.StatusCode)
	}
	if data["status"] != nil {
		t.Fatalf("no updates.json, yet a status: %#v", data["status"])
	}
	if data["log_tail"] != "" || data["running"] != false {
		t.Fatalf("a machine that never updated reads %#v", data)
	}
	timer, _ := data["timer"].(map[string]any)
	if timer["installed"] != false || timer["next"] != "" {
		t.Fatalf("a machine without systemctl has a timer: %#v", timer)
	}
	if data["auto"] == "" {
		t.Fatal("the auto-update policy is missing")
	}
}

func TestUpdatesReadsTheLastRun(t *testing.T) {
	env := updatesEnv(t, installedSystemd("inactive"))
	writeUpdatesFile(t, env, updatesFile, doneReport)
	var log strings.Builder
	for i := 1; i <= 100; i++ {
		log.WriteString("line " + strings.Repeat("x", i%3) + "\n")
	}
	log.WriteString("downloading 10%\rdownloading 100%\n\x1b[32mall done\x1b[0m\n")
	writeUpdatesFile(t, env, updatesLog, log.String())

	_, data := env.do(t, env.client, "GET", "/api/updates", "")
	status, _ := data["status"].(map[string]any)
	if status["state"] != "done" || status["ok"] != true || status["trigger"] != "timer" {
		t.Fatalf("the report did not come through: %#v", status)
	}
	components, _ := status["components"].([]any)
	if len(components) != 2 {
		t.Fatalf("components: %#v", status["components"])
	}
	tail, _ := data["log_tail"].(string)
	lines := strings.Split(tail, "\n")
	if len(lines) != logTailLines {
		t.Fatalf("log tail has %d lines, want %d", len(lines), logTailLines)
	}
	if lines[len(lines)-1] != "all done" || lines[len(lines)-2] != "downloading 100%" {
		t.Fatalf("the tail was not cleaned: %q", lines[len(lines)-2:])
	}
	timer, _ := data["timer"].(map[string]any)
	if timer["installed"] != true || timer["enabled"] != true {
		t.Fatalf("timer: %#v", timer)
	}
	if timer["next"] != "2026-10-23T04:17:00Z" || timer["last"] != "" {
		t.Fatalf("timer times: next %v, last %v", timer["next"], timer["last"])
	}
	if data["running"] != false {
		t.Fatal("an inactive service was called running")
	}
}

func TestUpdatesSaysWhenARunIsGoing(t *testing.T) {
	env := updatesEnv(t, installedSystemd("activating"))
	writeUpdatesFile(t, env, updatesFile, `{"schema":1,"state":"running","trigger":"admin","started_at":"2026-10-22T10:00:00Z","finished_at":"","ok":false,"components":[]}`)
	_, data := env.do(t, env.client, "GET", "/api/updates", "")
	if data["running"] != true {
		t.Fatalf("an activating oneshot is a run: %#v", data)
	}
	status, _ := data["status"].(map[string]any)
	if status["state"] != "running" {
		t.Fatalf("status: %#v", status)
	}
}

func TestUpdatesIgnoresATornReport(t *testing.T) {
	env := updatesEnv(t, installedSystemd("inactive"))
	writeUpdatesFile(t, env, updatesFile, `{"schema":1,"state":"do`)
	_, data := env.do(t, env.client, "GET", "/api/updates", "")
	if data["status"] != nil {
		t.Fatalf("half a report was drawn: %#v", data["status"])
	}
}

func TestUpdatesRunRefusesWithoutTheUnit(t *testing.T) {
	systemd := &fakeSystemd{units: map[string]string{}}
	env := updatesEnv(t, systemd)
	res, data := env.do(t, env.client, "POST", "/api/updates/run", "{}")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a run with no updater installed answered %d", res.StatusCode)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "not installed") {
		t.Fatalf("the refusal does not say why: %#v", data)
	}
	if len(systemd.starts()) != 0 {
		t.Fatal("a unit that does not exist was started")
	}
}

func TestUpdatesRunStartsTheUnit(t *testing.T) {
	systemd := installedSystemd("inactive")
	env := updatesEnv(t, systemd)
	res, data := env.do(t, env.client, "POST", "/api/updates/run", "{}")
	if res.StatusCode != http.StatusAccepted || data["started"] != true {
		t.Fatalf("run answered %d %#v", res.StatusCode, data)
	}
	starts := systemd.starts()
	want := []string{"systemctl", "start", "--no-block", updateService}
	if len(starts) != 1 || strings.Join(starts[0], " ") != strings.Join(want, " ") {
		t.Fatalf("systemctl was called with %q, want %q", starts, want)
	}
	// The run is labelled as asked for here, in the file update.sh reads.
	marker := filepath.Join(env.srv.dataDir, nextTriggerFile)
	raw, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(raw)) != "admin" {
		t.Fatalf("the trigger marker is %q, %v", raw, err)
	}
	info, err := os.Stat(marker)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the trigger marker's mode is %v, %v", info.Mode().Perm(), err)
	}
	dir, err := os.Stat(filepath.Dir(marker))
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("the updater's state directory's mode is %v, %v", dir.Mode().Perm(), err)
	}
}

func TestUpdatesRunRefusesWhileOneIsRunning(t *testing.T) {
	systemd := installedSystemd("activating")
	env := updatesEnv(t, systemd)
	res, data := env.do(t, env.client, "POST", "/api/updates/run", "{}")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a run during a run answered %d", res.StatusCode)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "already running") {
		t.Fatalf("the refusal does not say why: %#v", data)
	}
	if len(systemd.starts()) != 0 {
		t.Fatal("a running updater was started again")
	}
	// The run going on was not asked for here and must not be labelled so.
	if _, err := os.Stat(filepath.Join(env.srv.dataDir, nextTriggerFile)); !os.IsNotExist(err) {
		t.Fatalf("a refused run left a trigger marker: %v", err)
	}
}

func TestUpdatesRunReportsAStartThatFails(t *testing.T) {
	systemd := installedSystemd("inactive")
	systemd.startErr = errors.New("Access denied")
	env := updatesEnv(t, systemd)
	res, data := env.do(t, env.client, "POST", "/api/updates/run", "{}")
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("a refused start answered %d", res.StatusCode)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "Access denied") {
		t.Fatalf("the error lost systemd's reason: %#v", data)
	}
	// The label was for this run; the next timer run is not the admin's.
	if _, err := os.Stat(filepath.Join(env.srv.dataDir, nextTriggerFile)); !os.IsNotExist(err) {
		t.Fatalf("a run that did not start left its trigger marker: %v", err)
	}
}

func TestUpdatesEndpointsAreBehindTheWall(t *testing.T) {
	systemd := installedSystemd("inactive")
	env := updatesEnv(t, systemd)
	for _, call := range [][2]string{{"GET", "/api/updates"}, {"POST", "/api/updates/run"}} {
		res, _ := env.do(t, env.anon, call[0], call[1], "")
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d to a stranger", call[0], call[1], res.StatusCode)
		}
	}
	if len(systemd.starts()) != 0 {
		t.Fatal("a stranger started the updater")
	}
}

func TestUpdatesCheck(t *testing.T) {
	finished := time.Date(2026, 10, 22, 4, 19, 30, 0, time.UTC)
	cases := []struct {
		name    string
		systemd *fakeSystemd
		report  string
		now     time.Time
		ok      bool
		summary string
	}{
		{"no timer", &fakeSystemd{units: map[string]string{}}, doneReport, finished, false,
			"the update timer is not installed"},
		{"never ran", installedSystemd("inactive"), "", finished, false, "no update has run yet"},
		{"fresh", installedSystemd("inactive"), doneReport, finished.Add(5 * time.Hour), true, "checked 5 h ago"},
		{"stale", installedSystemd("inactive"), doneReport, finished.Add(72 * time.Hour), false, "last run 3 days ago"},
		{"running", installedSystemd("activating"), doneReport, finished, true, "running now"},
		{"component failed", installedSystemd("inactive"),
			`{"schema":1,"state":"done","trigger":"timer","finished_at":"2026-10-22T04:19:30Z","ok":false,
			  "components":[{"name":"go","version":"1.25","status":"current"},
			                {"name":"claude","version":"2.1.2","status":"failed","error":"download refused"}]}`,
			finished, false, "last run failed: claude: download refused"},
		{"aborted", installedSystemd("inactive"),
			`{"schema":1,"state":"failed","trigger":"timer","finished_at":"2026-10-22T04:19:30Z","ok":false,"components":[]}`,
			finished, false, "the last run was aborted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := updatesEnv(t, tc.systemd)
			env.srv.updates.now = func() time.Time { return tc.now }
			if tc.report != "" {
				writeUpdatesFile(t, env, updatesFile, tc.report)
			}
			row := env.srv.updatesCheck(context.Background())
			if row.OK != tc.ok || row.Summary != tc.summary {
				t.Fatalf("row is %v %q, want %v %q", row.OK, row.Summary, tc.ok, tc.summary)
			}
		})
	}
}

func TestUpdatesCheckListsComponentsOnePerLine(t *testing.T) {
	env := updatesEnv(t, installedSystemd("inactive"))
	writeUpdatesFile(t, env, updatesFile, doneReport)
	row := env.srv.updatesCheck(context.Background())
	want := "claude 2.1.3 (updated from 2.1.2)\ntmux 3.4 (held): tmux 3.5 is available but held"
	if row.Detail != want {
		t.Fatalf("detail is %q, want %q", row.Detail, want)
	}
}

func TestSystemdTime(t *testing.T) {
	for raw, want := range map[string]string{
		"Thu 2026-10-22 04:17:00 UTC": "2026-10-22T04:17:00Z",
		"n/a":                         "",
		"":                            "",
		"tomorrow-ish":                "tomorrow-ish",
	} {
		if got := systemdTime(raw); got != want {
			t.Errorf("systemdTime(%q) = %q, want %q", raw, got, want)
		}
	}
}
