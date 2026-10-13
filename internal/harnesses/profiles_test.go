package harnesses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// profileReq is the lab's request, in a profile.
func (l *lab) profileReq(profile string) PlanRequest {
	req := l.req()
	req.Profile = profile
	return req
}

func TestDefaultProfileChangesNothing(t *testing.T) {
	l := newLab(t)
	for _, h := range []Harness{Claude{}, Codex{}, OpenCode{}, Shell{}} {
		plan := l.plan(h)
		for _, key := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_DATA_HOME"} {
			if _, set := plan.Env[key]; set {
				t.Errorf("%s in the default profile sets %s", h.Kind(), key)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(l.data, "profiles")); !os.IsNotExist(err) {
		t.Fatalf("the default profile made a profiles directory: %v", err)
	}
}

func TestEachHarnessGetsItsProfileDirectory(t *testing.T) {
	l := newLab(t)
	root := ProfileDir(l.data, "p-work")
	cases := []struct {
		h    Harness
		want map[string]string
	}{
		{Claude{}, map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(root, "claude")}},
		{Codex{}, map[string]string{"CODEX_HOME": filepath.Join(root, "codex")}},
		{OpenCode{}, map[string]string{"XDG_DATA_HOME": filepath.Join(root, "xdg-data")}},
		{Shell{}, map[string]string{
			"CLAUDE_CONFIG_DIR": filepath.Join(root, "claude"),
			"CODEX_HOME":        filepath.Join(root, "codex"),
			"XDG_DATA_HOME":     filepath.Join(root, "xdg-data"),
		}},
	}
	for _, c := range cases {
		plan, err := c.h.Plan(context.Background(), l.profileReq("p-work"))
		if err != nil {
			t.Fatalf("%s: %v", c.h.Kind(), err)
		}
		for key, want := range c.want {
			if plan.Env[key] != want {
				t.Errorf("%s: %s = %q, want %q", c.h.Kind(), key, plan.Env[key], want)
			}
			if info, err := os.Stat(want); err != nil || !info.IsDir() {
				t.Errorf("%s: %s was not created: %v", c.h.Kind(), want, err)
			}
		}
	}
	// OpenCode's own directory is the profile's, never a link to the default
	// one: that is where its sign-in lives.
	if info, err := os.Lstat(filepath.Join(root, "xdg-data", "opencode")); err != nil || !info.IsDir() {
		t.Fatalf("OpenCode's data directory in the profile is %v, %v", info, err)
	}
}

func TestProfileSharesConfigurationButNotTheAccount(t *testing.T) {
	l := newLab(t)
	claude := filepath.Join(l.home, ".claude")
	codex := filepath.Join(l.home, ".codex")
	xdg := filepath.Join(l.home, "xdg")
	for path, data := range map[string]string{
		filepath.Join(claude, "settings.json"):        `{"model":"opus"}`,
		filepath.Join(claude, "CLAUDE.md"):            "be brief",
		filepath.Join(claude, "skills", "a", "x.md"):  "skill",
		filepath.Join(claude, ".credentials.json"):    `{"secret":true}`,
		filepath.Join(claude, "projects", "p", "x"):   "transcript",
		filepath.Join(codex, "config.toml"):           "model = \"x\"",
		filepath.Join(codex, "auth.json"):             `{"OPENAI_API_KEY":"k"}`,
		filepath.Join(xdg, "other-tool", "data"):      "kept",
		filepath.Join(xdg, "opencode", "auth.json"):   `{"openai":{}}`,
		filepath.Join(xdg, "opencode", "opencode.db"): "db",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (Shell{}).Plan(context.Background(), l.profileReq("p-work")); err != nil {
		t.Fatal(err)
	}
	root := ProfileDir(l.data, "p-work")
	linked := map[string]string{
		filepath.Join(root, "claude", "settings.json"): filepath.Join(claude, "settings.json"),
		filepath.Join(root, "claude", "CLAUDE.md"):     filepath.Join(claude, "CLAUDE.md"),
		filepath.Join(root, "claude", "skills"):        filepath.Join(claude, "skills"),
		filepath.Join(root, "codex", "config.toml"):    filepath.Join(codex, "config.toml"),
		filepath.Join(root, "xdg-data", "other-tool"):  filepath.Join(xdg, "other-tool"),
	}
	for path, want := range linked {
		got, err := os.Readlink(path)
		if err != nil || got != want {
			t.Errorf("%s links to %q (%v), want %q", path, got, err, want)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "claude", ".credentials.json"),
		filepath.Join(root, "claude", "projects"),
		filepath.Join(root, "codex", "auth.json"),
		filepath.Join(root, "xdg-data", "opencode", "auth.json"),
		filepath.Join(root, "xdg-data", "opencode", "opencode.db"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s is shared with the default profile: %v", path, err)
		}
	}
}

func TestProfileKeepsItsOwnVersionOfASharedFile(t *testing.T) {
	l := newLab(t)
	claude := filepath.Join(l.home, ".claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(ClaudeConfigDirFor(l.data, "p-work"), "settings.json")
	if err := os.MkdirAll(filepath.Dir(own), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte(`{"mine":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l.planIn(Claude{}, "p-work")
	l.planIn(Claude{}, "p-work")
	if raw, err := os.ReadFile(own); err != nil || string(raw) != `{"mine":true}` {
		t.Fatalf("the profile's own settings became %q, %v", raw, err)
	}
}

func (l *lab) planIn(h Harness, profile string) LaunchPlan {
	l.t.Helper()
	plan, err := h.Plan(context.Background(), l.profileReq(profile))
	if err != nil {
		l.t.Fatalf("%s in %s: %v", h.Kind(), profile, err)
	}
	return plan
}

func TestClaudeProfileGetsTheSharedMCPServersAndItsOwnTrust(t *testing.T) {
	l := newLab(t)
	defaultConfig := filepath.Join(l.home, ".claude", ".claude.json")
	if err := os.MkdirAll(filepath.Dir(defaultConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	shared := map[string]any{"docs": map[string]any{"type": "http", "url": "https://example.invalid/mcp"}}
	raw, _ := json.Marshal(map[string]any{"mcpServers": shared, "oauthAccount": map[string]any{"emailAddress": "me@home"}})
	if err := os.WriteFile(defaultConfig, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	profileConfig := filepath.Join(ClaudeConfigDirFor(l.data, "p-work"), ".claude.json")
	if err := os.MkdirAll(filepath.Dir(profileConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	own := map[string]any{"mine": map[string]any{"type": "stdio", "command": "x"}}
	raw, _ = json.Marshal(map[string]any{"mcpServers": own, "oauthAccount": map[string]any{"emailAddress": "me@work"}})
	if err := os.WriteFile(profileConfig, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	l.planIn(Claude{}, "p-work")

	var doc map[string]any
	raw, err := os.ReadFile(profileConfig)
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("the profile's .claude.json: %v", err)
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if !reflect.DeepEqual(servers["docs"], shared["docs"]) || !reflect.DeepEqual(servers["mine"], own["mine"]) {
		t.Fatalf("mcpServers = %#v", servers)
	}
	if account, _ := doc["oauthAccount"].(map[string]any); account["emailAddress"] != "me@work" {
		t.Fatalf("the profile's account was touched: %#v", doc["oauthAccount"])
	}
	if entry := claudeProjectEntry(t, profileConfig, l.cwd); entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("the working directory is not trusted in the profile: %#v", entry)
	}
	if _, account := ClaudeSignIn(l.data, "p-work"); account != "me@work" {
		t.Fatalf("ClaudeSignIn for the profile = %q", account)
	}
	if _, account := ClaudeSignIn(l.data, ""); account != "me@home" {
		t.Fatalf("ClaudeSignIn for the default profile = %q", account)
	}
}

func TestProfileVerifiesInItsOwnDirectories(t *testing.T) {
	l := newLab(t)
	id := "0b8e7f3c-1111-4222-8333-944455556666"
	transcript := filepath.Join(ClaudeConfigDirFor(l.data, "p-work"), "projects", "-work", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := l.profileReq("p-work")
	req.CLISession = id
	if ok, err := (Claude{}).VerifyCLISession(context.Background(), req); !ok || err != nil {
		t.Fatalf("the profile's transcript was not found: %v, %v", ok, err)
	}
	req.Profile = ""
	if ok, _ := (Claude{}).VerifyCLISession(context.Background(), req); ok {
		t.Fatal("the default profile found another profile's transcript")
	}
}

func TestProfileRefusesWhatCannotBeOne(t *testing.T) {
	l := newLab(t)
	for _, bad := range []string{"../escape", "UPPER", "a/b", "-leading"} {
		if _, err := (Claude{}).Plan(context.Background(), l.profileReq(bad)); err == nil {
			t.Errorf("profile %q was planned", bad)
		}
	}
}

func TestSignInReadersForCodexAndOpenCode(t *testing.T) {
	l := newLab(t)
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@work"}`))
	auth := `{"auth_mode":"chatgpt","tokens":{"id_token":"h.` + claims + `.s"}}`
	codexAuth := filepath.Join(CodexHomeFor(l.data, "p-work"), "auth.json")
	openCodeAuth := filepath.Join(OpenCodeDataDirFor(l.data, "p-work"), "auth.json")
	for path, data := range map[string]string{
		codexAuth:    auth,
		openCodeAuth: `{"openrouter":{"type":"api"},"anthropic":{"type":"oauth"}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if ok, account := CodexSignIn(l.data, "p-work"); !ok || account != "me@work" {
		t.Fatalf("CodexSignIn = %v, %q", ok, account)
	}
	if ok, _ := CodexSignIn(l.data, ""); ok {
		t.Fatal("the default profile reads as signed in to Codex")
	}
	if got := OpenCodeProviders(l.data, "p-work"); !reflect.DeepEqual(got, []string{"anthropic", "openrouter"}) {
		t.Fatalf("OpenCodeProviders = %v", got)
	}
}
