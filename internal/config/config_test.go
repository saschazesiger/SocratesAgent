package config

import (
	"encoding/json"
	"testing"
)

func TestNormalizeFillsDefaults(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.OpenRouter.BaseURL != DefaultOpenRouterBaseURL {
		t.Errorf("base url = %q", s.OpenRouter.BaseURL)
	}
	if s.Agent.MaxIterations != DefaultMaxIterations {
		t.Errorf("max iterations = %d", s.Agent.MaxIterations)
	}
	if len(s.Tools) != 3 {
		t.Errorf("expected the three default tools, got %d", len(s.Tools))
	}
	if s.Voice.TTSProvider != "browser" || s.Voice.STTProvider != "openrouter" {
		t.Errorf("voice defaults = %#v", s.Voice)
	}
}

func TestNormalizeSanitisesTools(t *testing.T) {
	s := Settings{Tools: []Tool{{Name: "My Agent!", IdleSeconds: -4, TimeoutSeconds: -1}}}
	s.Normalize()
	tool := s.Tools[0]
	if tool.ID != "my-agent" {
		t.Errorf("id = %q", tool.ID)
	}
	if tool.Command != "my-agent" {
		t.Errorf("a tool without a command should fall back to its id, got %q", tool.Command)
	}
	if tool.IdleSeconds <= 0 || tool.TimeoutSeconds <= 0 {
		t.Errorf("idle = %d, timeout = %d", tool.IdleSeconds, tool.TimeoutSeconds)
	}
	if tool.SkipPermissions {
		t.Error("a tool with no skip arguments cannot skip permissions")
	}
}

func TestNormalizeMakesToolIDsUnique(t *testing.T) {
	s := Settings{Tools: []Tool{
		{ID: "claude", Name: "One", Command: "claude"},
		{ID: "claude", Name: "Two", Command: "claude"},
		{Name: "Claude", Command: "claude"},
	}}
	s.Normalize()
	seen := map[string]bool{}
	for _, tool := range s.Tools {
		if seen[tool.ID] {
			t.Fatalf("duplicate tool id %q survived normalisation", tool.ID)
		}
		seen[tool.ID] = true
	}
}

func TestToolCommandLine(t *testing.T) {
	tool := Tool{
		Command: "claude", Args: []string{"--verbose"},
		Model: "sonnet", ModelFlag: "--model",
		SkipPermissions: true,
		SkipArgs:        []string{"--dangerously-skip-permissions"},
		AskArgs:         []string{"--ask"},
	}
	command, args := tool.CommandLine()
	if command != "claude" {
		t.Errorf("command = %q", command)
	}
	want := []string{"--verbose", "--dangerously-skip-permissions", "--model", "sonnet"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}

	tool.SkipPermissions = false
	if _, args := tool.CommandLine(); args[1] != "--ask" {
		t.Errorf("with permissions on, the ask arguments should be used: %v", args)
	}

	tool.Model = ""
	if _, args := tool.CommandLine(); len(args) != 2 {
		t.Errorf("an empty model should add no flag: %v", args)
	}
}

// An installation from before the terminal rework has to keep its agents.
func TestNormalizeMigratesOldBackends(t *testing.T) {
	s := Settings{Backends: []legacyBackend{
		{ID: "claude", Kind: "claude", Name: "Claude Code", Enabled: true,
			Description: "my own description", Command: "claude", Approval: "auto", TimeoutSeconds: 900},
		{ID: "codex", Kind: "codex", Name: "Codex", Enabled: false,
			Command: "codex", Approval: "ask", TimeoutSeconds: 1800},
	}}
	s.Normalize()
	if s.Backends != nil {
		t.Error("the legacy list should be cleared after migration")
	}
	if len(s.Tools) != 2 {
		t.Fatalf("migrated %d tools, want 2", len(s.Tools))
	}
	claude, ok := s.Tool("claude")
	if !ok {
		t.Fatal("claude did not survive the migration")
	}
	if claude.Description != "my own description" {
		t.Errorf("the user's own description was lost: %q", claude.Description)
	}
	if !claude.Enabled || claude.TimeoutSeconds != 900 {
		t.Errorf("settings were lost: %#v", claude)
	}
	if !claude.SkipPermissions {
		t.Error("an unattended agent should migrate to skipping permissions")
	}
	if len(claude.SkipArgs) == 0 || claude.Driving == "" {
		t.Error("the migrated tool did not pick up how to drive the program")
	}
	codex, _ := s.Tool("codex")
	if codex.SkipPermissions {
		t.Error("an agent that asked for approval should keep asking")
	}
}

func TestNormalizeTrimsBaseURL(t *testing.T) {
	s := Settings{}
	s.OpenRouter.BaseURL = "https://example.com/v1/"
	s.Normalize()
	if s.OpenRouter.BaseURL != "https://example.com/v1" {
		t.Errorf("base url = %q", s.OpenRouter.BaseURL)
	}
}

func TestEnabledTools(t *testing.T) {
	s := Default()
	s.Tools[0].Enabled = false
	for _, tool := range s.EnabledTools() {
		if !tool.Enabled {
			t.Fatalf("disabled tool leaked: %#v", tool)
		}
	}
	if _, ok := s.Tool("claude"); !ok {
		t.Error("Tool() should find disabled tools too")
	}
	if _, ok := s.Tool("ghost"); ok {
		t.Error("unknown id should not resolve")
	}
}

func TestDefaultToolsAreDrivable(t *testing.T) {
	for _, tool := range DefaultTools() {
		if tool.Description == "" {
			t.Errorf("%s has no description, so Socrates cannot decide when to use it", tool.ID)
		}
		if tool.Driving == "" {
			t.Errorf("%s does not say how it should be driven", tool.ID)
		}
		if !tool.SkipPermissions || len(tool.SkipArgs) == 0 {
			t.Errorf("%s should run unattended by default", tool.ID)
		}
		if tool.Idle() <= 0 || tool.Timeout() <= 0 {
			t.Errorf("%s has no usable timing: %#v", tool.ID, tool)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Claude Code":  "claude-code",
		"  Weird__ID ": "weird-id",
		"???":          "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeGetsTheSandboxEnvironment(t *testing.T) {
	tool, ok := func() (Tool, bool) {
		for _, t := range DefaultTools() {
			if t.ID == "claude" {
				return t, true
			}
		}
		return Tool{}, false
	}()
	if !ok {
		t.Fatal("no claude tool in the defaults")
	}
	if len(tool.Env) != 1 || tool.Env[0] != sandboxEnv {
		t.Fatalf("claude env = %#v, want %q - it refuses to skip permissions as root without it", tool.Env, sandboxEnv)
	}
}

func TestNormalizeAddsTheSandboxEnvironmentToAnOlderSettingsFile(t *testing.T) {
	// A settings document written before tools had an environment: env is
	// missing entirely, which unmarshals to nil.
	s := Settings{Tools: []Tool{
		{ID: "claude", Name: "Claude Code", Command: "claude", SkipPermissions: true,
			SkipArgs: []string{"--dangerously-skip-permissions"}},
		{ID: "codex", Name: "Codex", Command: "codex"},
		{ID: "mine", Name: "Mine", Command: "mine"},
	}}
	s.Normalize()
	if got := s.Tools[0].Env; len(got) != 1 || got[0] != sandboxEnv {
		t.Errorf("claude env after upgrade = %#v, want %q", got, sandboxEnv)
	}
	if got := s.Tools[1].Env; len(got) != 0 {
		t.Errorf("codex env = %#v, want none", got)
	}
	if got := s.Tools[2].Env; got == nil || len(got) != 0 {
		t.Errorf("an unknown tool should end up with an empty environment, got %#v", got)
	}
}

func TestNormalizeKeepsAnEmptiedEnvironment(t *testing.T) {
	// The dashboard sends an empty list once the field has been cleared, and
	// that choice has to survive a reload.
	s := Settings{Tools: []Tool{
		{ID: "claude", Name: "Claude Code", Command: "claude", Env: []string{},
			SkipPermissions: true, SkipArgs: []string{"--dangerously-skip-permissions"}},
	}}
	s.Normalize()
	if len(s.Tools[0].Env) != 0 {
		t.Errorf("env = %#v, want it left empty", s.Tools[0].Env)
	}
}

func TestSettingsRoundTripKeepsAnEmptiedEnvironment(t *testing.T) {
	s := Settings{Tools: []Tool{{ID: "claude", Command: "claude", Env: []string{}}}}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Settings
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.Normalize()
	if len(back.Tools[0].Env) != 0 {
		t.Errorf("env = %#v after a round trip, want it left empty", back.Tools[0].Env)
	}
}
