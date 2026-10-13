package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// listProfiles answers GET /api/profiles as a list.
func (e *sessionEnv) listProfiles() []map[string]any {
	e.t.Helper()
	res, payload := e.do(e.t, e.client, "GET", "/api/profiles", "")
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("profiles: %d %#v", res.StatusCode, payload)
	}
	raw, _ := payload["profiles"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

func (e *sessionEnv) addProfile(name string) string {
	e.t.Helper()
	res, payload := e.do(e.t, e.client, "POST", "/api/profiles", fmt.Sprintf(`{"name":%q}`, name))
	if res.StatusCode != http.StatusCreated {
		e.t.Fatalf("add profile: %d %#v", res.StatusCode, payload)
	}
	p, _ := payload["profile"].(map[string]any)
	id, _ := p["id"].(string)
	if id == "" {
		e.t.Fatalf("the new profile has no id: %#v", payload)
	}
	return id
}

// planEnv reads back the environment a session was actually started with.
func (e *sessionEnv) planEnv(id string) map[string]string {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.srv.dataDir, "sessions", id, "plan.json"))
	if err != nil {
		e.t.Fatalf("plan.json: %v", err)
	}
	var plan struct {
		Env map[string]string `json:"Env"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		e.t.Fatalf("plan.json: %v", err)
	}
	return plan.Env
}

func TestProfilesCanBeAddedRenamedAndDeleted(t *testing.T) {
	e := newSessionEnv(t)
	list := e.listProfiles()
	if len(list) != 1 || list[0]["default"] != true || list[0]["id"] != "" {
		t.Fatalf("a fresh installation has %#v", list)
	}

	for _, body := range []string{`{"name":""}`, `{"name":"default"}`, `{"name":"` + string(make([]byte, 41)) + `"}`} {
		if res, _ := e.do(t, e.client, "POST", "/api/profiles", body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s answered %d", body, res.StatusCode)
		}
	}
	id := e.addProfile("  Business  ")
	if !harnesses.ValidProfileID(id) {
		t.Fatalf("the profile id %q cannot name a directory", id)
	}
	if res, _ := e.do(t, e.client, "POST", "/api/profiles", `{"name":"business"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a second profile with the same name answered %d", res.StatusCode)
	}
	dir := harnesses.ProfileDir(e.srv.dataDir, id)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("the profile directory was not made: %v", err)
	}

	res, payload := e.do(t, e.client, "PATCH", "/api/profiles/"+id, `{"name":"Work"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %#v", res.StatusCode, payload)
	}
	list = e.listProfiles()
	if len(list) != 2 || list[1]["name"] != "Work" {
		t.Fatalf("after the rename: %#v", list)
	}
	if res, _ := e.do(t, e.client, "PATCH", "/api/profiles/nope", `{"name":"X"}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("renaming a profile that is not there answered %d", res.StatusCode)
	}

	// The order is the person's, the default profile included. An order that
	// does not name every profile exactly once is from a stale page.
	private := e.addProfile("Private")
	for _, body := range []string{
		fmt.Sprintf(`{"order":[%q,""]}`, id),
		fmt.Sprintf(`{"order":[%q,%q,"",""]}`, id, private),
		fmt.Sprintf(`{"order":[%q,%q,"","p-000000000000"]}`, id, private),
	} {
		if res, _ := e.do(t, e.client, "PUT", "/api/profiles/order", body); res.StatusCode != http.StatusConflict {
			t.Errorf("order %s answered %d", body, res.StatusCode)
		}
	}
	res, payload = e.do(t, e.client, "PUT", "/api/profiles/order", fmt.Sprintf(`{"order":[%q,"",%q]}`, private, id))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reorder: %d %#v", res.StatusCode, payload)
	}
	list = e.listProfiles()
	if len(list) != 3 || list[0]["name"] != "Private" || list[1]["default"] != true || list[2]["name"] != "Work" {
		t.Fatalf("after the reorder: %#v", list)
	}
	// A profile added after the order was stored comes last.
	later := e.addProfile("Later")
	if list = e.listProfiles(); len(list) != 4 || list[3]["id"] != later {
		t.Fatalf("a new profile is not last: %#v", list)
	}
	for _, extra := range []string{private, later} {
		if res, _ := e.do(t, e.client, "DELETE", "/api/profiles/"+extra, ""); res.StatusCode != http.StatusOK {
			t.Fatalf("delete %s: %d", extra, res.StatusCode)
		}
	}

	res, payload = e.do(t, e.client, "DELETE", "/api/profiles/"+id, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %#v", res.StatusCode, payload)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the profile directory survived its profile: %v", err)
	}
	if list = e.listProfiles(); len(list) != 1 {
		t.Fatalf("after the delete: %#v", list)
	}
}

func TestSessionRunsInItsProfile(t *testing.T) {
	e := newSessionEnv(t)
	id := e.addProfile("Business")

	for _, body := range []string{
		`{"harness":"claude","profile":"p-000000000000"}`,
		`{"harness":"claude","profile":"../etc"}`,
		fmt.Sprintf(`{"harness":"shell","shell_execution":"host","profile":%q}`, id),
	} {
		if res, _ := e.create(body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("create %s answered %d", body, res.StatusCode)
		}
	}

	res, session := e.create(fmt.Sprintf(`{"harness":"claude","profile":%q,"options":{"note":"kept"}}`, id))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	sid := sessionID(t, session)
	options, _ := session["options"].(map[string]any)
	if options["profile"] != id || options["note"] != "kept" {
		t.Fatalf("stored options = %#v", options)
	}
	want := harnesses.ClaudeConfigDirFor(e.srv.dataDir, id)
	if got := e.planEnv(sid)["CLAUDE_CONFIG_DIR"]; got != want {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
	e.waitForPane(sid, "FAKE claude")

	// A profile a session still runs in cannot be deleted from under it.
	if res, _ := e.do(t, e.client, "DELETE", "/api/profiles/"+id, ""); res.StatusCode != http.StatusConflict {
		t.Fatalf("deleting a profile in use answered %d", res.StatusCode)
	}
	if list := e.listProfiles(); list[1]["sessions"] != float64(1) {
		t.Fatalf("the profile does not count its session: %#v", list[1])
	}

	// A resume is planned from the row, so it has to come back in the same
	// profile.
	if _, err := e.tmux("kill-session", "-t", termux.TmuxName(sid)); err != nil {
		t.Fatal(err)
	}
	e.waitForState(sid, 15*time.Second, store.StateNeedsResume)
	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+sid+"/resume", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("resume: %d %#v", res.StatusCode, payload)
	}
	if got := e.planEnv(sid)["CLAUDE_CONFIG_DIR"]; got != want {
		t.Fatalf("after the resume CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}

	res, _ = e.do(t, e.client, "DELETE", "/api/sessions/"+sid, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete session: %d", res.StatusCode)
	}
	if res, _ := e.do(t, e.client, "DELETE", "/api/profiles/"+id, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("deleting a profile nothing uses any more answered %d", res.StatusCode)
	}
}

func TestCatalogueListsTheProfiles(t *testing.T) {
	e := newSessionEnv(t)
	e.addProfile("Business")
	res, payload := e.do(t, e.client, "GET", "/api/harnesses", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("harnesses: %d", res.StatusCode)
	}
	profiles, _ := payload["profiles"].([]any)
	if len(profiles) != 2 {
		t.Fatalf("profiles in the catalogue = %#v", payload["profiles"])
	}
	if p, _ := profiles[1].(map[string]any); p["name"] != "Business" {
		t.Fatalf("the second profile is %#v", p)
	}
}
