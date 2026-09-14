package harnesses

import (
	"context"
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
	if len(plan.Files) != 1 || plan.Files[0].Mode != 0600 {
		t.Fatal("missing isolated config")
	}
	_, err = (VSCode{}).ResumePlan(context.Background(), req)
	if err != ErrNoResume {
		t.Fatalf("resume = %v", err)
	}
}
