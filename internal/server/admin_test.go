package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// Every group of every harness, saved and read back. This is the server half
// of the adminoptions scenario: whatever shape a control has in the page, the
// value it writes has to survive a round trip through the settings document.
func TestHarnessOptionsRoundTrip(t *testing.T) {
	env := adminEnv(t)
	dir := t.TempDir()

	res, saved := putSettings(t, env, func(settings map[string]any) {
		harnesses := settings["harnesses"].(map[string]any)
		shell := harnesses["shell"].(map[string]any)
		shell["login"] = false
		shell["extra_args"] = []any{"-x"}

		claude := harnesses["claude"].(map[string]any)
		claude["default_effort"] = "high"
		claude["autocompact"] = "200k"
		claude["permission_mode"] = "plan"
		claude["setting_sources"] = []any{"user", "project"}
		claude["add_dirs"] = []any{dir}
		claude["remote_control_prefix"] = "socrates-"
		claude["agent"] = "reviewer"
		claude["mcp_config"] = []any{filepath.Join(dir, "mcp.json")}
		claude["disable_mouse"] = true
		claude["verbose"] = true
		claude["settings_overrides"] = `{"env":{"FOO":"bar"}}`

		codex := harnesses["codex"].(map[string]any)
		codex["default_effort"] = "xhigh"
		codex["sandbox"] = "read-only"
		codex["remote_auth_token_env"] = "CODEX_TOKEN"
		codex["web_search"] = true
		codex["tui_theme"] = "ocean-light"
		codex["config_overrides"] = []any{"tools.web_search=true"}

		opencode := harnesses["opencode"].(map[string]any)
		opencode["small_model"] = "openai/gpt-5-mini"
		opencode["permission_json"] = `{"*":"ask"}`
		opencode["enabled_providers"] = []any{"anthropic"}
		opencode["pure"] = true
		opencode["share"] = "manual"
		opencode["tui_theme"] = "nord"
		opencode["log_level"] = "WARN"
		opencode["config_content"] = `{"theme":"nord"}`
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
		{"shell", "login", false},
		{"claude", "default_effort", "high"},
		{"claude", "autocompact", "200k"},
		{"claude", "permission_mode", "plan"},
		{"claude", "agent", "reviewer"},
		{"claude", "remote_control_prefix", "socrates-"},
		{"claude", "disable_mouse", true},
		{"claude", "verbose", true},
		{"claude", "settings_overrides", `{"env":{"FOO":"bar"}}`},
		{"codex", "default_effort", "xhigh"},
		{"codex", "sandbox", "read-only"},
		{"codex", "remote_auth_token_env", "CODEX_TOKEN"},
		{"codex", "web_search", true},
		{"codex", "tui_theme", "ocean-light"},
		{"opencode", "small_model", "openai/gpt-5-mini"},
		{"opencode", "pure", true},
		{"opencode", "share", "manual"},
		{"opencode", "tui_theme", "nord"},
		{"opencode", "log_level", "WARN"},
		{"opencode", "config_content", `{"theme":"nord"}`},
	} {
		if got[want.harness].(map[string]any)[want.key] != want.value {
			t.Errorf("harnesses.%s.%s is %#v, want %#v", want.harness, want.key,
				got[want.harness].(map[string]any)[want.key], want.value)
		}
	}
	if sources := got["claude"].(map[string]any)["setting_sources"].([]any); len(sources) != 2 {
		t.Errorf("setting_sources came back as %#v", sources)
	}
	if overrides := got["codex"].(map[string]any)["config_overrides"].([]any); len(overrides) != 1 {
		t.Errorf("config_overrides came back as %#v", overrides)
	}
}

// WP1 left these two to WP8's controls, and a closed list in the page is only
// closed if the server says so too.
func TestSettingsRefusesWhatTheControlsCannotProduce(t *testing.T) {
	for _, bad := range []struct {
		name   string
		mutate func(map[string]any)
		says   string
	}{
		{"autocompact", func(s map[string]any) {
			s["harnesses"].(map[string]any)["claude"].(map[string]any)["autocompact"] = "lots"
		}, "autocompact"},
		{"autocompact out of range", func(s map[string]any) {
			s["harnesses"].(map[string]any)["claude"].(map[string]any)["autocompact"] = "4M"
		}, "100k to 1M"},
		{"setting sources", func(s map[string]any) {
			s["harnesses"].(map[string]any)["claude"].(map[string]any)["setting_sources"] = []any{"user", "global"}
		}, "setting_sources"},
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
	seen := map[string]string{}
	for _, entry := range checks {
		row := entry.(map[string]any)
		seen[row["name"].(string)] = row["detail"].(string)
	}
	for _, name := range []string{
		"tmux", "tmux socket", "Workspace", "Shell", "Claude Code", "Claude Code state",
		"Codex", "Codex state", "OpenCode", "OpenCode state", "OpenRouter", "Speech to text",
		"Remote access", "Disk",
	} {
		if _, ok := seen[name]; !ok {
			t.Errorf("no %q row in %v", name, seen)
		}
	}
	if !strings.Contains(seen["Disk"], "free of") {
		t.Errorf("the disk row says %q", seen["Disk"])
	}
}
