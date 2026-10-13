package harnesses

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVSCodeUsesPrivateSocketAndPersistentData(t *testing.T) {
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
	if plan.Argv[len(plan.Argv)-1] != req.Cwd || plan.Discover != DiscoverNone {
		t.Fatalf("unexpected workspace: %#v", plan)
	}
	// The editor's memory belongs to the folder, not to the session.
	if !has(plan.Argv, "--user-data-dir", VSCodeDataDir(req.DataDir, req.Cwd)) {
		t.Fatalf("user data is not per folder: %v", plan.Argv)
	}
	if len(plan.Files) == 0 || plan.Files[0].Mode != 0600 {
		t.Fatal("missing isolated config")
	}
	_, err = (VSCode{}).ResumePlan(context.Background(), req)
	if err != ErrNoResume {
		t.Fatalf("resume = %v", err)
	}
}

// A folder's first editor gets the defaults that make it survive a restart,
// and an editor that was running per session before brings its state along.
func TestVSCodeFolderState(t *testing.T) {
	l := newLab(t)
	req := l.req()
	req.DataDir = t.TempDir()
	legacy := SessionFile(req.DataDir, req.SessionID, "vscode-data")
	if err := os.MkdirAll(filepath.Join(legacy, "User"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "User", "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := (VSCode{}).Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	data := VSCodeDataDir(req.DataDir, req.Cwd)
	if _, err := os.Stat(filepath.Join(data, "User", "settings.json")); err != nil {
		t.Fatalf("the session's editor state was not adopted by its folder: %v", err)
	}
	if len(plan.Files) != 1 {
		t.Fatalf("adopted settings must not be overwritten: %d files", len(plan.Files))
	}
	fresh := l.req()
	fresh.DataDir = t.TempDir()
	plan, err = (VSCode{}).Plan(context.Background(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 2 || plan.Files[1].Path != filepath.Join(VSCodeDataDir(fresh.DataDir, fresh.Cwd), "User", "settings.json") {
		t.Fatalf("a new folder should be given the defaults: %#v", plan.Files)
	}
}
