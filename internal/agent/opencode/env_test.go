//go:build linux

package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The serve process's environment is the chat's without what would point
// OpenCode elsewhere, plus the layout, the password and the switches that keep
// the server headless.
func TestTheServerEnvironmentIsTheChatsOnTheLayout(t *testing.T) {
	layout := NewLayout(t.TempDir())
	base := []string{"PATH=/bin", "HOME=/home/x", "OPENCODE_CONFIG=/elsewhere.json", "OPENCODE_DB=/other.db",
		"XDG_DATA_HOME=/home/x/.local/share", BinaryEnv + "=/opt/opencode", `OPENCODE_AUTH_CONTENT={"p":{}}`}
	env := processEnv(base, layout, "acct", "secret", []byte(`{"share":"disabled"}`))
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("%s is set twice: %q", k, env)
		}
		got[k] = v
	}
	want := map[string]string{
		"PATH": "/bin", "HOME": "/home/x", "OPENCODE_AUTH_CONTENT": `{"p":{}}`,
		"OPENCODE_DB":              filepath.Join(layout.Root, "opencode.db"),
		"XDG_DATA_HOME":            layout.AccountHome("acct"),
		"XDG_STATE_HOME":           filepath.Join(layout.AccountHome("acct"), "state"),
		"XDG_CONFIG_HOME":          layout.ConfigHome(),
		"XDG_CACHE_HOME":           layout.CacheHome(),
		"OPENCODE_SERVER_USERNAME": "opencode", "OPENCODE_SERVER_PASSWORD": "secret",
		"OPENCODE_CONFIG_CONTENT":     `{"share":"disabled"}`,
		"OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_DISABLE_MODELS_FETCH": "1", "OPENCODE_DISABLE_EXTERNAL_SKILLS": "1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s=%q, want %q", k, got[k], v)
		}
	}
	for _, k := range []string{"OPENCODE_CONFIG", BinaryEnv} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was passed on", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("environment %q", env)
	}
	if !filepath.IsAbs(got["OPENCODE_DB"]) {
		t.Errorf("OPENCODE_DB %q is not absolute", got["OPENCODE_DB"])
	}
}

// The config allows what OpenCode would ask about, denies its question tool,
// and never adds a "*" that would lift plan mode's edit deny.
func TestTheConfigAllowsEverythingButQuestions(t *testing.T) {
	raw, err := configContent(agent.StartSpec{Model: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Share      string            `json:"share"`
		Autoupdate *bool             `json:"autoupdate"`
		Model      string            `json:"model"`
		Permission map[string]string `json:"permission"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Share != "disabled" || doc.Autoupdate == nil || *doc.Autoupdate || doc.Model != "fake/model" {
		t.Fatalf("config %s", raw)
	}
	want := map[string]string{"external_directory": "allow", "doom_loop": "allow", "read": "allow", "question": "deny"}
	if len(doc.Permission) != len(want) {
		t.Fatalf("permission %v", doc.Permission)
	}
	for k, v := range want {
		if doc.Permission[k] != v {
			t.Fatalf("permission %v", doc.Permission)
		}
	}
}

// An account's data home links the shared folders in by relative links, so
// the data folder can move; what OpenCode had put in a real folder there is
// moved into the shared one, and preparing again changes nothing.
func TestPrepareAccountLinksTheSharedFoldersRelatively(t *testing.T) {
	layout := NewLayout(t.TempDir())
	own := filepath.Join(layout.AccountHome("a"), "opencode", "snapshot")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "kept"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := layout.PrepareAccount("a"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range sharedDirs {
		link := filepath.Join(layout.AccountHome("a"), "opencode", name)
		target, err := os.Readlink(link)
		if err != nil || target != filepath.Join("..", "..", "..", name) {
			t.Fatalf("%s -> %q (%v)", link, target, err)
		}
	}
	if _, err := os.Stat(filepath.Join(layout.Root, "snapshot", "kept")); err != nil {
		t.Fatalf("the account's own snapshot was not moved into the shared one: %v", err)
	}
	if err := layout.PrepareAccount("../escape"); err == nil {
		t.Fatal("an account id with a path in it was taken")
	}
}
