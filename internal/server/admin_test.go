package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// putSettings sends the whole document back the way the dashboard does, with
// one mutation applied to it first.
func putSettings(t *testing.T, env *testEnv, mutate func(map[string]any)) (*http.Response, map[string]any) {
	t.Helper()
	_, data := env.do(t, env.client, "GET", "/api/settings", "")
	settings, _ := data["settings"].(map[string]any)
	if settings == nil {
		t.Fatalf("no settings in %#v", data)
	}
	mutate(settings)
	body, err := json.Marshal(map[string]any{"settings": settings})
	if err != nil {
		t.Fatal(err)
	}
	return env.do(t, env.client, "PUT", "/api/settings", string(body))
}

func adminEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	return env
}

// The basics of every harness, saved and read back. This is the server half of
// the adminoptions scenario: whatever shape a control has in the page, the
// value it writes has to survive a round trip through the settings document.
func TestHarnessOptionsRoundTrip(t *testing.T) {
	env := adminEnv(t)

	res, saved := putSettings(t, env, func(settings map[string]any) {
		harnesses := settings["harnesses"].(map[string]any)
		harnesses["shell"].(map[string]any)["binary"] = "/bin/sh"

		claude := harnesses["claude"].(map[string]any)
		claude["binary"] = "/opt/claude"
		claude["default_model"] = "opus"
		claude["default_effort"] = "high"
		claude["models"] = []any{map[string]any{"id": "opus", "effort": "high"}}

		codex := harnesses["codex"].(map[string]any)
		codex["default_model"] = "gpt-5.6-terra"
		codex["default_effort"] = "xhigh"

		opencode := harnesses["opencode"].(map[string]any)
		opencode["default_model"] = "anthropic/claude-sonnet-4-5"
		opencode["enabled"] = false
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save failed: %d %#v", res.StatusCode, saved)
	}

	// Read it back with a fresh request rather than trusting the response, so
	// what is asserted is what was stored.
	_, data := env.do(t, env.client, "GET", "/api/settings", "")
	got := data["settings"].(map[string]any)["harnesses"].(map[string]any)
	for _, want := range []struct {
		harness, key string
		value        any
	}{
		{"shell", "binary", "/bin/sh"},
		{"claude", "binary", "/opt/claude"},
		{"claude", "default_model", "opus"},
		{"claude", "default_effort", "high"},
		{"codex", "default_model", "gpt-5.6-terra"},
		{"codex", "default_effort", "xhigh"},
		{"opencode", "default_model", "anthropic/claude-sonnet-4-5"},
		{"opencode", "enabled", false},
	} {
		if got[want.harness].(map[string]any)[want.key] != want.value {
			t.Errorf("harnesses.%s.%s is %#v, want %#v", want.harness, want.key,
				got[want.harness].(map[string]any)[want.key], want.value)
		}
	}
	models := got["claude"].(map[string]any)["models"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["id"] != "opus" {
		t.Errorf("the model short list came back as %#v", models)
	}
}

// A document written before the dashboard was cut back to the basics is
// accepted and its dead keys are dropped: an installation upgrading into this
// version must not be met with a 400 it cannot fix.
func TestSettingsIgnoreTheOptionsThatAreNowPolicy(t *testing.T) {
	env := adminEnv(t)
	res, saved := putSettings(t, env, func(settings map[string]any) {
		claude := settings["harnesses"].(map[string]any)["claude"].(map[string]any)
		claude["default_model"] = "opus"
		// Every one of these used to be a control, and three of them used to
		// be refused when they were wrong.
		claude["permission_mode"] = "invented"
		claude["autocompact"] = "lots"
		claude["setting_sources"] = []any{"user", "global"}
		claude["remote_control"] = true
		claude["extra_args"] = []any{"--verbose"}
		codex := settings["harnesses"].(map[string]any)["codex"].(map[string]any)
		codex["config_overrides"] = []any{"not-an-assignment"}
		codex["sandbox"] = "danger-full-access"
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answered %d, want 200: %#v", res.StatusCode, saved)
	}
	claude := saved["settings"].(map[string]any)["harnesses"].(map[string]any)["claude"].(map[string]any)
	if claude["default_model"] != "opus" {
		t.Errorf("the setting that is still a setting was lost: %#v", claude)
	}
	for _, gone := range []string{"permission_mode", "autocompact", "remote_control", "extra_args"} {
		if _, found := claude[gone]; found {
			t.Errorf("%s came back out of the stored document", gone)
		}
	}
}

// A closed list in the page is only closed if the server says so too: the API
// takes whatever is sent to it.
func TestSettingsRefusesWhatTheControlsCannotProduce(t *testing.T) {
	for _, bad := range []struct {
		name   string
		mutate func(map[string]any)
		says   string
	}{
		{"relative workspace root", func(s map[string]any) {
			s["workspace"].(map[string]any)["root"] = "workspaces"
		}, "absolute"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			env := adminEnv(t)
			res, body := putSettings(t, env, bad.mutate)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("answered %d, want 400", res.StatusCode)
			}
			if message, _ := body["error"].(string); !strings.Contains(message, bad.says) {
				t.Fatalf("the message %q does not mention %q", message, bad.says)
			}
		})
	}
}

// A preset that is not there yet is saved with a warning, not refused: a mount
// that is down must not take the other nine presets with it.
func TestSavingPresetsWarnsAboutAMissingOne(t *testing.T) {
	env := adminEnv(t)
	here := t.TempDir()
	gone := filepath.Join(here, "not-mounted")

	res, saved := putSettings(t, env, func(settings map[string]any) {
		settings["workspace"].(map[string]any)["presets"] = []any{
			map[string]any{"label": "Here", "path": here},
			map[string]any{"label": "Away", "path": gone},
		}
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save failed: %d %#v", res.StatusCode, saved)
	}
	presets := saved["settings"].(map[string]any)["workspace"].(map[string]any)["presets"].([]any)
	if len(presets) != 2 {
		t.Fatalf("both presets should have been kept: %#v", presets)
	}
	warnings, _ := saved["warnings"].([]any)
	if len(warnings) != 1 || !strings.Contains(warnings[0].(string), gone) {
		t.Fatalf("the missing preset was not warned about: %#v", warnings)
	}

	// And the sheet has to be offered them, which is where they are used.
	_, catalogue := env.do(t, env.client, "GET", "/api/harnesses", "")
	offered := catalogue["workspace"].(map[string]any)["presets"].([]any)
	if len(offered) != 2 {
		t.Fatalf("the new-session sheet is offered %#v", offered)
	}
}

// The workspace root is created when it is saved, so that Start never fails on
// a directory the dashboard already accepted.
func TestSavingTheWorkspaceRootCreatesIt(t *testing.T) {
	env := adminEnv(t)
	root := filepath.Join(t.TempDir(), "deep", "workspaces")

	res, _ := putSettings(t, env, func(settings map[string]any) {
		settings["workspace"].(map[string]any)["root"] = root
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save failed: %d", res.StatusCode)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("the workspace root was not created: %v", err)
	}
}

// The terminal card rewrites the generated tmux configuration, and never puts
// window-size in it - a global window-size takes the server down on the next
// new-session.
func TestSavingTheTerminalCardRewritesTheConf(t *testing.T) {
	env := adminEnv(t)
	if err := env.srv.Sessions().Start(t.Context()); err != nil {
		t.Skipf("no terminal substrate here: %v", err)
	}
	t.Cleanup(func() { _ = env.srv.Sessions().Close() })

	res, _ := putSettings(t, env, func(settings map[string]any) {
		terminal := settings["terminal"].(map[string]any)
		terminal["history_limit"] = 12345
		terminal["window_size"] = "largest"
		terminal["mouse"] = false
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save failed: %d", res.StatusCode)
	}
	conf, err := os.ReadFile(env.srv.Sessions().ConfPath())
	if err != nil {
		t.Fatalf("conf: %v", err)
	}
	if !strings.Contains(string(conf), "history-limit 12345") {
		t.Fatalf("the new history limit is not in the conf:\n%s", conf)
	}
	if !strings.Contains(string(conf), "mouse off") {
		t.Fatalf("the mouse setting is not in the conf:\n%s", conf)
	}
	if strings.Contains(string(conf), "window-size") {
		t.Fatalf("the conf mentions window-size, which segfaults tmux:\n%s", conf)
	}
}

// The diagnostics list is what §F.7 asks for, and every row has to be about
// something on this machine rather than a hard-coded string.
func TestDiagnosticsCoversTheEngineTheProgramsAndTheDisk(t *testing.T) {
	env := adminEnv(t)
	_, data := env.do(t, env.client, "POST", "/api/diagnostics", "{}")
	checks, _ := data["checks"].([]any)
	if len(checks) == 0 {
		t.Fatalf("no checks in %#v", data)
	}
	seen := map[string]map[string]any{}
	for _, entry := range checks {
		row := entry.(map[string]any)
		seen[row["name"].(string)] = row
	}
	for _, name := range []string{
		"tmux", "tmux socket", "Recovered sessions", "Workspace", "Shell", "Claude Code",
		"Claude Code state", "Codex", "Codex state", "OpenCode", "OpenCode state", "OpenRouter",
		"Speech to text", "Remote access", "Disk",
	} {
		if _, ok := seen[name]; !ok {
			t.Errorf("no %q row in %v", name, seen)
		}
	}
	if summary, _ := seen["Disk"]["summary"].(string); !strings.HasSuffix(summary, "free") {
		t.Errorf("the disk row says %q", summary)
	}
	if detail, _ := seen["Disk"]["detail"].(string); !strings.Contains(detail, env.srv.dataDir) {
		t.Errorf("the disk detail does not name the directory: %q", detail)
	}
}

// §E.10 rule 3: a version, a path, a socket or an error message is technical
// state and lives behind the row's "i". The visible half of a row is a
// verdict, and the server is where that split is made.
func TestDiagnosticsKeepsPathsAndVersionsOutOfTheVisibleHalf(t *testing.T) {
	env := adminEnv(t)
	_, data := env.do(t, env.client, "POST", "/api/diagnostics", "{}")
	for _, entry := range data["checks"].([]any) {
		row := entry.(map[string]any)
		name, _ := row["name"].(string)
		summary, _ := row["summary"].(string)
		if strings.TrimSpace(summary) == "" {
			t.Errorf("%q has nothing visible to say", name)
			continue
		}
		if strings.Contains(summary, "/") {
			t.Errorf("%q shows a path in its visible half: %q", name, summary)
		}
		// A free-space figure is the verdict itself - the review names
		// "1.7 GiB free" as the shape a disk row should have - so the only
		// number forbidden here is one that is not carrying a unit with it.
		if regexp.MustCompile(`\d+\.\d+(?:[a-z]|$)`).MatchString(summary) {
			t.Errorf("%q shows a version in its visible half: %q", name, summary)
		}
		if len(summary) > 40 {
			t.Errorf("%q is not a verdict, it is a sentence: %q", name, summary)
		}
	}
}

// A program whose --version prints escape sequences - which the e2e fake does
// - must not put escape glyphs on the dashboard.
func TestDiagnosticsCleansWhatAProgramPrintedForItsVersion(t *testing.T) {
	noisy := "\x1b]11;?\x1b\\FAKE claude\x07 " + strings.Repeat("x", 300)
	got := clean(noisy)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("escapes survived: %q", got)
	}
	if !strings.HasPrefix(got, "FAKE claude") {
		t.Fatalf("the version itself was lost: %q", got)
	}
	if len([]rune(got)) > 121 {
		t.Fatalf("the version was not clamped: %d runes", len([]rune(got)))
	}
}

// A session Socrates took in without a row of its own is the one thing on the
// machine nobody asked for by name, so the Setup check counts them - and stops
// counting one that has been renamed, because a renamed one has been dealt
// with.
func TestDiagnosticsCountsTheSessionsThatWereTakenIn(t *testing.T) {
	env := adminEnv(t)
	row := func(name string) map[string]any {
		_, data := env.do(t, env.client, "POST", "/api/diagnostics", "{}")
		checks, _ := data["checks"].([]any)
		for _, entry := range checks {
			check := entry.(map[string]any)
			if check["name"] == name {
				return check
			}
		}
		t.Fatalf("no %q row in %#v", name, data)
		return nil
	}
	if summary := row("Recovered sessions")["summary"]; summary != "none" {
		t.Fatalf("a fresh machine reports %q", summary)
	}

	taken := &store.Session{
		ID: termux.NewID(), Title: termux.RecoveredTitle, Harness: "shell",
		Workdir: t.TempDir(), WorkdirMode: store.WorkdirCustom,
		TmuxName: "soc_taken", State: store.StateRunning,
	}
	if err := env.store.CreateSession(taken); err != nil {
		t.Fatal(err)
	}
	found := row("Recovered sessions")
	if found["summary"] != "1 taken in" || !strings.Contains(found["detail"].(string), taken.ID) {
		t.Fatalf("the row is %#v", found)
	}
	if found["ok"] != true {
		t.Errorf("taking a session in is not a failure: %#v", found)
	}

	if err := env.store.UpdateSessionTitle(taken.ID, "The one I kept"); err != nil {
		t.Fatal(err)
	}
	if summary := row("Recovered sessions")["summary"]; summary != "none" {
		t.Fatalf("a renamed session is still counted: %q", summary)
	}
}
