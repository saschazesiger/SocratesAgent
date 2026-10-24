package accounts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// The environment of an account's CLI is built from nothing: what a CLI
// needs of the service's passes, the agent's folder is the account's, and
// none of the service's own variables or other keys get through.
func TestTheEnvironmentIsBuiltPerAccount(t *testing.T) {
	data := t.TempDir()
	home := t.TempDir()
	base := []string{
		"PATH=/usr/bin:/bin", "HOME=" + home, "LANG=en_US.UTF-8", "LC_ALL=C.UTF-8", "TZ=Europe/Zurich",
		"HTTPS_PROXY=http://proxy:3128", "SOCRATES_CODEX_BIN=/opt/codex",
		"INVOCATION_ID=abc", "NOTIFY_SOCKET=/run/systemd/notify", "JOURNAL_STREAM=8:1", "WATCHDOG_USEC=1",
		"ANTHROPIC_API_KEY=sk-other", "SOCRATES_CLAUDE_API_KEY=sk-other", "OPENAI_API_KEY=sk-other",
		"OPENCODE_AUTH_CONTENT={}", "CLAUDE_CONFIG_DIR=/root/.claude", "CODEX_HOME=/root/.codex",
		"SOCRATES_DATA_DIR=/var/lib/socrates", "SOCRATES_FAKE_AGENT=1",
	}
	for _, tc := range []struct {
		agent, folderVar string
	}{
		{Claude, "CLAUDE_CONFIG_DIR"}, {Codex, "CODEX_HOME"}, {OpenCode, ""}, {Fake, ""},
	} {
		a := &store.Account{ID: "a-0123456789ab", Agent: tc.agent, Kind: Subscription}
		env := envMap(Env(data, a, base))
		for _, k := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TZ", "HTTPS_PROXY", "SOCRATES_CODEX_BIN"} {
			if env[k] == "" {
				t.Errorf("%s: %s did not pass", tc.agent, k)
			}
		}
		for _, k := range []string{"INVOCATION_ID", "NOTIFY_SOCKET", "JOURNAL_STREAM", "WATCHDOG_USEC", "ANTHROPIC_API_KEY",
			"SOCRATES_CLAUDE_API_KEY", "OPENAI_API_KEY", "OPENCODE_AUTH_CONTENT", "SOCRATES_DATA_DIR", "SOCRATES_FAKE_AGENT"} {
			if _, ok := env[k]; ok {
				t.Errorf("%s: %s reached the CLI", tc.agent, k)
			}
		}
		for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
			want := ""
			if k == tc.folderVar {
				want = filepath.Join(data, "accounts", a.ID)
			}
			if env[k] != want {
				t.Errorf("%s: %s = %q, want %q", tc.agent, k, env[k], want)
			}
		}
	}
}

// An OpenCode chat's tools find the person's gh login and git configuration,
// which OpenCode's own XDG_CONFIG_HOME would hide.
func TestAnOpenCodeChatKeepsTheToolsConfiguration(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".config/gh", ".config/git"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".config/git/config"), []byte("[user]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &store.Account{ID: "a-0123456789ab", Agent: OpenCode, Kind: Subscription}
	env := envMap(Env(t.TempDir(), a, []string{"HOME=" + home}))
	if env["GH_CONFIG_DIR"] != filepath.Join(home, ".config/gh") || env["GIT_CONFIG_GLOBAL"] != filepath.Join(home, ".config/git/config") {
		t.Fatalf("env %v", env)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if env := envMap(Env(t.TempDir(), a, []string{"HOME=" + home})); env["GIT_CONFIG_GLOBAL"] != "" {
		t.Fatalf("GIT_CONFIG_GLOBAL would hide ~/.gitconfig: %v", env)
	}
}

func TestTheKeyReachesEachAgentItsOwnWay(t *testing.T) {
	if got := SecretEnv(Claude, "", "sk-ant"); len(got) != 1 || got[0] != "SOCRATES_CLAUDE_API_KEY=sk-ant" {
		t.Fatalf("claude %v", got)
	}
	got := SecretEnv(OpenCode, "openrouter", "sk-or")
	if len(got) != 1 || !strings.HasPrefix(got[0], "OPENCODE_AUTH_CONTENT=") {
		t.Fatalf("opencode %v", got)
	}
	var auth map[string]map[string]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got[0], "OPENCODE_AUTH_CONTENT=")), &auth); err != nil ||
		auth["openrouter"]["type"] != "api" || auth["openrouter"]["key"] != "sk-or" {
		t.Fatalf("opencode auth %v %v", auth, err)
	}
	if got := SecretEnv(Codex, "", "sk-codex"); got != nil {
		t.Fatalf("codex keeps its key in its own auth.json, not the environment: %v", got)
	}
}

func TestAKeyIsSealedForItsAccount(t *testing.T) {
	data := t.TempDir()
	k, err := OpenKeyring(data)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := k.Seal("a-000000000001", "sk-secret")
	if err != nil || strings.Contains(string(sealed), "sk-secret") {
		t.Fatalf("sealed %q %v", sealed, err)
	}
	if got, err := k.Open("a-000000000001", sealed); err != nil || got != "sk-secret" {
		t.Fatalf("opened %q %v", got, err)
	}
	if _, err := k.Open("a-000000000002", sealed); err == nil {
		t.Fatal("a key opened on another account")
	}
	again, err := OpenKeyring(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.Open("a-000000000001", sealed); err != nil || got != "sk-secret" {
		t.Fatalf("a keyring opened again does not open the key: %q %v", got, err)
	}
}
