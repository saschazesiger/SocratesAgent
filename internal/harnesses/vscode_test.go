package harnesses

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVSCodeUsesPrivateSocketAndSessionData(t *testing.T) {
	l := newLab(t)
	req := l.req()
	req.DataDir = "/tmp/soc-test"
	plan, err := (VSCode{}).Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !has(plan.Argv, "--socket", VSCodeSocket(req.DataDir, req.SessionID)) || !has(plan.Argv, "--socket-mode", "0600") || !has(plan.Argv, "--auth", "none") {
		t.Fatalf("unexpected listener: %v", plan.Argv)
	}
	if !has(plan.Argv, "-u", "CODE_SERVER_PARENT_PID") {
		t.Fatal("parent editor must not be inherited")
	}
	if value, ok := plan.Env["VSCODE_IPC_HOOK_CLI"]; !ok || value != "" {
		t.Fatal("parent IPC must be cleared")
	}
	if plan.Argv[len(plan.Argv)-1] != req.Cwd {
		t.Fatalf("unexpected workspace: %#v", plan)
	}
	// What the editor remembers goes with the session.
	if !has(plan.Argv, "--user-data-dir", VSCodeDataDir(req.DataDir, req.SessionID)) {
		t.Fatalf("user data is not the session's: %v", plan.Argv)
	}
	if len(plan.Files) == 0 || plan.Files[0].Mode != 0600 {
		t.Fatal("missing isolated config")
	}
}

// The very first editor gets the defaults, every later one the settings the
// person kept, and a resume leaves what the editor has alone.
func TestVSCodeProfileSeed(t *testing.T) {
	l := newLab(t)
	req := l.req()
	req.DataDir = t.TempDir()
	settings := filepath.Join(VSCodeDataDir(req.DataDir, req.SessionID), "User", "settings.json")
	plan, err := (VSCode{}).Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 2 || plan.Files[1].Path != settings || string(plan.Files[1].Data) != vscodeDefaults {
		t.Fatalf("the first editor should be given the defaults: %#v", plan.Files)
	}
	writeAll(t, map[string]string{
		filepath.Join(VSCodeProfileDir(req.DataDir), "User", "settings.json"):            `{"a":1}`,
		filepath.Join(VSCodeProfileDir(req.DataDir), "User", "snippets", "go.json"):      `{}`,
		filepath.Join(VSCodeProfileDir(req.DataDir), "User", "workspaceStorage", "x.db"): `nope`,
	})
	plan, err = (VSCode{}).Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range plan.Files[1:] {
		got[f.Path] = string(f.Data)
	}
	snippet := filepath.Join(VSCodeDataDir(req.DataDir, req.SessionID), "User", "snippets", "go.json")
	if len(got) != 2 || got[settings] != `{"a":1}` || got[snippet] != `{}` {
		t.Fatalf("the kept settings, and only those, should be given: %#v", got)
	}
	writeAll(t, map[string]string{settings: `{"b":2}`})
	plan, err = (VSCode{}).Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if f.Path == settings {
			t.Fatal("a resume must not overwrite the editor's own settings")
		}
	}
}

// What an editor changed goes back to the profile; what it was only given
// does not, so two editors open at once do not undo each other.
func TestSyncVSCodeProfile(t *testing.T) {
	data := t.TempDir()
	profile := filepath.Join(VSCodeProfileDir(data), "User", "settings.json")
	writeAll(t, map[string]string{profile: `{"old":1}`})
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(profile, old, old)
	stale := filepath.Join(VSCodeDataDir(data, "a"), "User", "settings.json")
	writeAll(t, map[string]string{stale: `{"old":1}`})
	if err := SyncVSCodeProfile(data, "a"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(profile); !info.ModTime().Equal(old) {
		t.Fatal("an unchanged file must not be written back")
	}
	changed := filepath.Join(VSCodeDataDir(data, "b"), "User", "keybindings.json")
	writeAll(t, map[string]string{
		filepath.Join(VSCodeDataDir(data, "b"), "User", "settings.json"): `{"new":2}`,
		changed: `[]`,
		filepath.Join(VSCodeDataDir(data, "b"), "User", "History", "h"): `x`,
	})
	if err := SyncVSCodeProfile(data, "b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(profile); string(got) != `{"new":2}` {
		t.Fatalf("a changed setting must be kept: %s", got)
	}
	if _, err := os.Stat(filepath.Join(VSCodeProfileDir(data), "User", "keybindings.json")); err != nil {
		t.Fatal("keybindings must be kept")
	}
	if _, err := os.Stat(filepath.Join(VSCodeProfileDir(data), "User", "History")); err == nil {
		t.Fatal("history must not be kept")
	}
	// Editor a was opened before b changed the settings, and is closed after:
	// it changed nothing, and b's change stays.
	_ = os.Chtimes(stale, old, old)
	if err := SyncVSCodeProfile(data, "a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(profile); string(got) != `{"new":2}` {
		t.Fatalf("an editor that changed nothing undid another's change: %s", got)
	}
}

// The editors of the folders from before are retired: the settings somebody
// changed are carried over, the rest goes.
func TestMigrateVSCodeFolders(t *testing.T) {
	data := t.TempDir()
	root := VSCodeProfileDir(data)
	writeAll(t, map[string]string{
		filepath.Join(root, "00112233aabbccdd", "User", "settings.json"):         vscodeFolderDefaults,
		filepath.Join(root, "00112233aabbccde", "User", "settings.json"):         `{"mine":true}`,
		filepath.Join(root, "00112233aabbccde", "User", "workspaceStorage", "x"): `x`,
	})
	migrateVSCodeFolders(root)
	if got, _ := os.ReadFile(filepath.Join(root, "User", "settings.json")); string(got) != `{"mine":true}` {
		t.Fatalf("the changed settings were not carried over: %q", got)
	}
	for _, dir := range []string{"00112233aabbccdd", "00112233aabbccde"} {
		if _, err := os.Stat(filepath.Join(root, dir)); err == nil {
			t.Fatalf("%s should be gone", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "User", "workspaceStorage")); err == nil {
		t.Fatal("workspace state must not be carried over")
	}
}

func writeAll(t *testing.T, files map[string]string) {
	t.Helper()
	for path, data := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
