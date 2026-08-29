package backends

import (
	"context"
	"runtime"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

func TestRunCustomCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	var events []Event
	res, err := Run(context.Background(), Request{
		Backend: config.Backend{
			ID: "echo", Kind: config.KindCustom, Name: "Echo", Command: "sh",
			ExtraArgs: []string{"-c", "printf 'first\nsecond\n'"}, TimeoutSeconds: 30,
		},
		Prompt:  "ignored",
		Workdir: t.TempDir(),
	}, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "first\nsecond" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(events) != 2 {
		t.Fatalf("expected two log events, got %d: %#v", len(events), events)
	}
}

func TestRunPromptPlaceholder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	res, err := Run(context.Background(), Request{
		Backend: config.Backend{
			ID: "echo", Kind: config.KindCustom, Name: "Echo", Command: "sh",
			ExtraArgs: []string{"-c", "printf '%s' \"{{prompt}}\""}, TimeoutSeconds: 30,
		},
		Prompt:  "hello world",
		Workdir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "hello world" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestRunMissingCommand(t *testing.T) {
	_, err := Run(context.Background(), Request{
		Backend: config.Backend{ID: "nope", Kind: config.KindCustom, Name: "Nope",
			Command: "socrates-does-not-exist", TimeoutSeconds: 10},
		Workdir: t.TempDir(),
	}, nil)
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestBuildClaudeArgs(t *testing.T) {
	args, stdin, _ := build(Request{
		Backend: config.Backend{Kind: config.KindClaude, Command: "claude", Approval: "auto", Model: "sonnet"},
		Prompt:  "do it",
	})
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	for _, want := range []string{"-p", "stream-json", "--verbose", "bypassPermissions", "sonnet"} {
		if !contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if stdin != "do it" {
		t.Errorf("prompt should go to stdin, got %q", stdin)
	}
}

func TestBuildClaudeAskArgs(t *testing.T) {
	args, _, _ := build(Request{
		Backend:   config.Backend{Kind: config.KindClaude, Command: "claude", Approval: "ask"},
		Prompt:    "do it",
		SelfPath:  "/usr/bin/socrates",
		BridgeURL: "http://127.0.0.1:1/api/bridge/permission",
	})
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	if !contains(joined, "--permission-prompt-tool") || !contains(joined, "mcp__socrates__permission_prompt") {
		t.Errorf("ask mode should wire the bridge: %q", joined)
	}
	if contains(joined, "bypassPermissions") {
		t.Errorf("ask mode must not bypass permissions: %q", joined)
	}
}

func TestBuildCodexAskUsesReadOnlySandbox(t *testing.T) {
	args, _, _ := build(Request{
		Backend: config.Backend{Kind: config.KindCodex, Command: "codex", Approval: "ask", Sandbox: "workspace-write"},
	})
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	if !contains(joined, "read-only") {
		t.Errorf("expected a read-only sandbox, got %q", joined)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
