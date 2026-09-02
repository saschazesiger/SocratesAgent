package harnesses

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// This file answers one question per option: the dashboard has some seventy of
// them, and every one is a promise that setting it changes what the program is
// started with. A promise nothing checks is a promise that quietly stops being
// true - a dropped `--plugin-dir` or a lost `OPENCODE_DISABLE_PROJECT_CONFIG`
// would pass every other test in this package.
//
// The tables are kept honest by reflection: a field of the option struct with
// no entry here fails the test, so an option added to the catalogue cannot be
// added without saying where it goes.

// optionCase is one option, what it is set to, and what that must produce.
type optionCase struct {
	// field is the name of the field in the option struct.
	field string
	// set puts the option into its interesting state.
	set func(o *config.HarnessSettings)
	// want is what the launch plan must then carry. A nil want means the
	// option deliberately does not reach a launch plan at all, and why is in
	// the note.
	want func(t *testing.T, p LaunchPlan)
	note string
	// resume asks for the plan to be built by ResumePlan instead.
	resume bool
}

// argvHas is a want that looks for a flag with a value.
func argvHas(flag, value string) func(*testing.T, LaunchPlan) {
	return func(t *testing.T, p LaunchPlan) {
		t.Helper()
		if !has(p.Argv, flag, value) {
			t.Errorf("argv has no %s %s: %v", flag, value, p.Argv)
		}
	}
}

// argvCarries is a want for a flag with no value.
func argvCarries(flag string) func(*testing.T, LaunchPlan) {
	return func(t *testing.T, p LaunchPlan) {
		t.Helper()
		if !carries(p.Argv, flag) {
			t.Errorf("argv has no %s: %v", flag, p.Argv)
		}
	}
}

// envIs is a want for an environment variable.
func envIs(key, value string) func(*testing.T, LaunchPlan) {
	return func(t *testing.T, p LaunchPlan) {
		t.Helper()
		if p.Env[key] != value {
			t.Errorf("%s = %q, want %q", key, p.Env[key], value)
		}
	}
}

// fileHas is a want for a key of the one generated JSON file.
func fileHas(path string, value any) func(*testing.T, LaunchPlan) {
	return func(t *testing.T, p LaunchPlan) {
		t.Helper()
		if len(p.Files) == 0 {
			t.Fatalf("no generated file to look in")
		}
		var doc map[string]any
		if err := json.Unmarshal(p.Files[0].Data, &doc); err != nil {
			t.Fatalf("the generated file is not JSON: %v", err)
		}
		if got := dig(doc, path); !reflect.DeepEqual(got, value) {
			t.Errorf("%s in %s = %#v, want %#v", path, p.Files[0].Path, got, value)
		}
	}
}

// inlineHas is fileHas for OpenCode's inline configuration document.
func inlineHas(path string, value any) func(*testing.T, LaunchPlan) {
	return func(t *testing.T, p LaunchPlan) {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal([]byte(p.Env["OPENCODE_CONFIG_CONTENT"]), &doc); err != nil {
			t.Fatalf("OPENCODE_CONFIG_CONTENT is not JSON: %v", err)
		}
		if got := dig(doc, path); !reflect.DeepEqual(got, value) {
			t.Errorf("%s in the inline config = %#v, want %#v", path, got, value)
		}
	}
}

// dig walks a dotted path into a decoded JSON document.
func dig(doc map[string]any, path string) any {
	var current any = doc
	for _, part := range strings.Split(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = obj[part]
	}
	return current
}

// runOptionCases builds one plan per case and checks it, then reports any
// field of the option struct the table forgot.
func runOptionCases(t *testing.T, h Harness, options any, cases []optionCase) {
	t.Helper()
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.field] = true
		t.Run(c.field, func(t *testing.T) {
			l := newLab(t)
			c.set(&l.settings.Harnesses)
			req := l.req()
			req.CLISession = "a-stored-session-id"
			var (
				plan LaunchPlan
				err  error
			)
			if c.resume {
				plan, err = h.ResumePlan(context.Background(), req)
			} else {
				plan, err = h.Plan(context.Background(), req)
			}
			if err != nil {
				t.Fatalf("planning with %s set: %v", c.field, err)
			}
			if c.want == nil {
				if c.note == "" {
					t.Fatalf("%s has no expectation and no reason", c.field)
				}
				return
			}
			c.want(t, plan)
		})
	}
	for _, name := range fieldNames(reflect.TypeOf(options)) {
		if !covered[name] {
			t.Errorf("%T.%s is in the option catalogue and in no case here: either it reaches a launch plan and this table must say how, or it does not and the table must say why", options, name)
		}
	}
}

// fieldNames flattens an option struct, walking into the embedded Common.
func fieldNames(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			out = append(out, fieldNames(field.Type)...)
			continue
		}
		out = append(out, field.Name)
	}
	return out
}

func TestShellOptionsReachTheLaunch(t *testing.T) {
	runOptionCases(t, Shell{}, config.ShellOptions{}, []optionCase{
		{field: "Enabled", note: "whether the picker offers the harness at all, which is the catalogue's business and not the launcher's",
			set: func(o *config.HarnessSettings) { o.Shell.Enabled = false }},
		{field: "Binary", note: "an override is resolved to argv[0]; TestEnvAlwaysCarriesWhite pins that argv[0] is the resolved absolute path",
			set: func(o *config.HarnessSettings) { o.Shell.Binary = "/bin/sh" }},
		{field: "Models", note: "a shell has no model step",
			set: func(o *config.HarnessSettings) { o.Shell.Models = []config.ModelPick{{ID: "x"}} }},
		{field: "ExtraArgs", set: func(o *config.HarnessSettings) { o.Shell.ExtraArgs = []string{"-x"} },
			want: argvCarries("-x")},
		{field: "ExtraEnv", set: func(o *config.HarnessSettings) { o.Shell.ExtraEnv = []string{"PS1=> "} },
			want: envIs("PS1", "> ")},
		{field: "Login", set: func(o *config.HarnessSettings) { o.Shell.Login = true }, want: argvCarries("-l")},
	})
}

func TestClaudeOptionsReachTheLaunch(t *testing.T) {
	claude := func(f func(*config.ClaudeOptions)) func(*config.HarnessSettings) {
		return func(o *config.HarnessSettings) { f(&o.Claude) }
	}
	runOptionCases(t, Claude{}, config.ClaudeOptions{}, []optionCase{
		{field: "Enabled", note: "the picker, not the launcher", set: claude(func(c *config.ClaudeOptions) { c.Enabled = false })},
		{field: "Binary", note: "resolved to argv[0]", set: claude(func(c *config.ClaudeOptions) { c.Binary = "claude" })},
		{field: "Models", note: "the new-session sheet's short list", set: claude(func(c *config.ClaudeOptions) { c.Models = []config.ModelPick{{ID: "opus"}} })},
		{field: "ExtraArgs", set: claude(func(c *config.ClaudeOptions) { c.ExtraArgs = []string{"--ax-screen-reader"} }), want: argvCarries("--ax-screen-reader")},
		{field: "ExtraEnv", set: claude(func(c *config.ClaudeOptions) { c.ExtraEnv = []string{"ANTHROPIC_PROFILE=work"} }), want: envIs("ANTHROPIC_PROFILE", "work")},

		{field: "DefaultModel", set: claude(func(c *config.ClaudeOptions) { c.DefaultModel = "opus" }), want: argvHas("--model", "opus")},
		{field: "DefaultEffort", set: claude(func(c *config.ClaudeOptions) { c.DefaultEffort = "high" }), want: argvHas("--effort", "high")},
		{field: "Advisor", set: claude(func(c *config.ClaudeOptions) { c.Advisor = "opus" }), want: argvHas("--advisor", "opus")},
		{field: "Autocompact", set: claude(func(c *config.ClaudeOptions) { c.Autocompact = "200k" }), want: argvHas("--autocompact", "200k")},
		{field: "MaxThinkingTokens", set: claude(func(c *config.ClaudeOptions) { c.MaxThinkingTokens = 4096 }), want: envIs("MAX_THINKING_TOKENS", "4096")},

		{field: "PermissionMode", set: claude(func(c *config.ClaudeOptions) { c.PermissionMode = "plan" }), want: argvHas("--permission-mode", "plan")},
		{field: "SkipPermissions", set: claude(func(c *config.ClaudeOptions) { c.SkipPermissions = config.SkipPermissionsAllow }), want: argvCarries("--allow-dangerously-skip-permissions")},
		{field: "AllowedTools", set: claude(func(c *config.ClaudeOptions) { c.AllowedTools = []string{"Edit", "Bash(git *)"} }), want: argvHas("--allowedTools", "Edit,Bash(git *)")},
		{field: "DisallowedTools", set: claude(func(c *config.ClaudeOptions) { c.DisallowedTools = []string{"WebFetch"} }), want: argvHas("--disallowedTools", "WebFetch")},
		{field: "Tools", set: claude(func(c *config.ClaudeOptions) { c.Tools = "default,-Bash" }), want: argvHas("--tools", "default,-Bash")},
		{field: "AddDirs", set: claude(func(c *config.ClaudeOptions) { c.AddDirs = []string{"/srv/one", "/srv/two"} }),
			want: func(t *testing.T, p LaunchPlan) {
				argvHas("--add-dir", "/srv/one")(t, p)
				argvHas("--add-dir", "/srv/two")(t, p)
				fileHas("permissions.additionalDirectories", []any{"/srv/one", "/srv/two"})(t, p)
			}},
		{field: "Restricted", set: claude(func(c *config.ClaudeOptions) { c.Restricted = true }), want: argvCarries("--restricted")},
		{field: "SafeMode", set: claude(func(c *config.ClaudeOptions) { c.SafeMode = true }), want: argvCarries("--safe-mode")},
		{field: "Bare", set: claude(func(c *config.ClaudeOptions) { c.Bare = true }), want: argvCarries("--bare")},
		{field: "SettingSources", set: claude(func(c *config.ClaudeOptions) { c.SettingSources = []string{"user", "project"} }), want: argvHas("--setting-sources", "user,project")},
		{field: "CleanupPeriodDays", set: claude(func(c *config.ClaudeOptions) { c.CleanupPeriodDays = 365 }), want: fileHas("cleanupPeriodDays", float64(365))},

		{field: "RemoteControl", set: claude(func(c *config.ClaudeOptions) { c.RemoteControl = true }), want: argvCarries("--remote-control")},
		{field: "RemoteControlName", set: claude(func(c *config.ClaudeOptions) {
			c.RemoteControl, c.RemoteControlName = true, "phone"
		}), want: argvHas("--remote-control", "phone")},
		{field: "RemoteControlPrefix", set: claude(func(c *config.ClaudeOptions) { c.RemoteControlPrefix = "socrates" }),
			want: envIs("CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX", "socrates")},

		{field: "ResumeMode", resume: true, set: claude(func(c *config.ClaudeOptions) { c.ResumeMode = config.ResumeFork }), want: argvCarries("--fork-session")},
		{field: "Agent", set: claude(func(c *config.ClaudeOptions) { c.Agent = "reviewer" }), want: argvHas("--agent", "reviewer")},
		{field: "AppendSystemPrompt", set: claude(func(c *config.ClaudeOptions) { c.AppendSystemPrompt = "be brief" }), want: argvHas("--append-system-prompt", "be brief")},
		{field: "SystemPromptSnapshot", set: claude(func(c *config.ClaudeOptions) {
			c.AppendSystemPrompt, c.SystemPromptSnapshot = "be brief", "on"
		}), want: argvHas("--system-prompt-snapshot", "on")},
		{field: "ExcludeDynamicPromptSections", set: claude(func(c *config.ClaudeOptions) { c.ExcludeDynamicPromptSections = true }),
			want: argvCarries("--exclude-dynamic-system-prompt-sections")},
		{field: "DisableSlashCommands", set: claude(func(c *config.ClaudeOptions) { c.DisableSlashCommands = true }), want: argvCarries("--disable-slash-commands")},

		{field: "MCPConfig", set: claude(func(c *config.ClaudeOptions) { c.MCPConfig = []string{"/srv/mcp.json"} }), want: argvHas("--mcp-config", "/srv/mcp.json")},
		{field: "StrictMCPConfig", set: claude(func(c *config.ClaudeOptions) { c.StrictMCPConfig = true }), want: argvCarries("--strict-mcp-config")},
		{field: "PluginDirs", set: claude(func(c *config.ClaudeOptions) { c.PluginDirs = []string{"/srv/plugins"} }), want: argvHas("--plugin-dir", "/srv/plugins")},

		{field: "PinLightTheme", note: "writes one key into the global configuration; TestClaudeThemePinKeepsTheRestOfTheFile checks both switch positions",
			set: claude(func(c *config.ClaudeOptions) { c.PinLightTheme = true })},
		{field: "DisableTerminalTitle", set: claude(func(c *config.ClaudeOptions) { c.DisableTerminalTitle = true }), want: envIs("CLAUDE_CODE_DISABLE_TERMINAL_TITLE", "1")},
		{field: "DisableMouse", set: claude(func(c *config.ClaudeOptions) { c.DisableMouse = true }), want: envIs("CLAUDE_CODE_DISABLE_MOUSE", "1")},
		{field: "NoFlicker", set: claude(func(c *config.ClaudeOptions) { c.NoFlicker = true }), want: envIs("CLAUDE_CODE_NO_FLICKER", "1")},
		{field: "ForceSyncOutput", set: claude(func(c *config.ClaudeOptions) { c.ForceSyncOutput = true }), want: envIs("CLAUDE_CODE_FORCE_SYNC_OUTPUT", "1")},

		{field: "Verbose", set: claude(func(c *config.ClaudeOptions) { c.Verbose = true }), want: argvCarries("--verbose")},
		{field: "DebugFilter", set: claude(func(c *config.ClaudeOptions) { c.DebugFilter = "api,hooks" }), want: argvHas("-d", "api,hooks")},
		{field: "DebugFile", set: claude(func(c *config.ClaudeOptions) { c.DebugFile = true }),
			want: func(t *testing.T, p LaunchPlan) {
				if path := valueOf(p.Argv, "--debug-file"); !strings.HasSuffix(path, claudeDebugFile) {
					t.Errorf("--debug-file = %q", path)
				}
			}},
		{field: "SettingsOverrides", set: claude(func(c *config.ClaudeOptions) { c.SettingsOverrides = `{"editorMode":"vim"}` }),
			want: fileHas("editorMode", "vim")},
	})
}

func TestCodexOptionsReachTheLaunch(t *testing.T) {
	codex := func(f func(*config.CodexOptions)) func(*config.HarnessSettings) {
		return func(o *config.HarnessSettings) { f(&o.Codex) }
	}
	runOptionCases(t, Codex{}, config.CodexOptions{}, []optionCase{
		{field: "Enabled", note: "the picker, not the launcher", set: codex(func(c *config.CodexOptions) { c.Enabled = false })},
		{field: "Binary", note: "resolved to argv[0]", set: codex(func(c *config.CodexOptions) { c.Binary = "codex" })},
		{field: "Models", note: "the new-session sheet's short list", set: codex(func(c *config.CodexOptions) { c.Models = []config.ModelPick{{ID: "gpt-5.6-sol"}} })},
		{field: "ExtraArgs", set: codex(func(c *config.CodexOptions) { c.ExtraArgs = []string{"--oss"} }), want: argvCarries("--oss")},
		{field: "ExtraEnv", set: codex(func(c *config.CodexOptions) { c.ExtraEnv = []string{"OPENAI_BASE_URL=https://example.test"} }),
			want: envIs("OPENAI_BASE_URL", "https://example.test")},

		{field: "DefaultModel", set: codex(func(c *config.CodexOptions) { c.DefaultModel = "gpt-5.6-terra" }), want: argvHas("-m", "gpt-5.6-terra")},
		{field: "DefaultEffort", set: codex(func(c *config.CodexOptions) { c.DefaultEffort = "xhigh" }), want: argvHas("-c", `model_reasoning_effort="xhigh"`)},
		{field: "ModelReasoningSummary", set: codex(func(c *config.CodexOptions) { c.ModelReasoningSummary = "detailed" }), want: argvHas("-c", `model_reasoning_summary="detailed"`)},
		{field: "ModelVerbosity", set: codex(func(c *config.CodexOptions) { c.ModelVerbosity = "high" }), want: argvHas("-c", `model_verbosity="high"`)},
		{field: "Personality", set: codex(func(c *config.CodexOptions) { c.Personality = "pragmatic" }), want: argvHas("-c", `personality="pragmatic"`)},
		{field: "ReviewModel", set: codex(func(c *config.CodexOptions) { c.ReviewModel = "gpt-5.4" }), want: argvHas("-c", `review_model="gpt-5.4"`)},

		{field: "Sandbox", set: codex(func(c *config.CodexOptions) { c.Sandbox = "read-only" }), want: argvHas("-s", "read-only")},
		{field: "Approval", set: codex(func(c *config.CodexOptions) { c.Approval = "never" }), want: argvHas("-a", "never")},
		{field: "NetworkAccess", set: codex(func(c *config.CodexOptions) { c.NetworkAccess = true }), want: argvHas("-c", "sandbox_workspace_write.network_access=true")},
		{field: "WritableRoots", set: codex(func(c *config.CodexOptions) { c.WritableRoots = []string{"/srv/a", "/srv/b"} }),
			want: argvHas("-c", `sandbox_workspace_write.writable_roots=["/srv/a","/srv/b"]`)},
		{field: "AddDirs", set: codex(func(c *config.CodexOptions) { c.AddDirs = []string{"/srv/one"} }), want: argvHas("--add-dir", "/srv/one")},
		{field: "ApproveForMe", set: codex(func(c *config.CodexOptions) { c.ApproveForMe = true }), want: argvCarries("--approve-for-me")},
		{field: "Bypass", set: codex(func(c *config.CodexOptions) { c.Bypass = true }), want: argvCarries("--dangerously-bypass-approvals-and-sandbox")},
		{field: "TrustWorkdir", set: codex(func(c *config.CodexOptions) { c.TrustWorkdir = true }),
			want: func(t *testing.T, p LaunchPlan) { argvHas("-c", trustLevelOverride(p.Cwd))(t, p) }},

		{field: "RemoteAddr", set: codex(func(c *config.CodexOptions) { c.RemoteAddr = "ws://127.0.0.1:9000" }), want: argvHas("--remote", "ws://127.0.0.1:9000")},
		{field: "RemoteAuthTokenEnv", set: codex(func(c *config.CodexOptions) { c.RemoteAuthTokenEnv = "CODEX_TOKEN" }), want: argvHas("--remote-auth-token-env", "CODEX_TOKEN")},

		{field: "TUITheme", set: codex(func(c *config.CodexOptions) { c.TUITheme = "gruvbox-light" }), want: argvHas("-c", `tui.theme="gruvbox-light"`)},
		{field: "NoAltScreen", set: codex(func(c *config.CodexOptions) { c.NoAltScreen = true }), want: argvCarries("--no-alt-screen")},
		{field: "DisableKeyboardEnhancement", set: codex(func(c *config.CodexOptions) { c.DisableKeyboardEnhancement = true }),
			want: envIs("CODEX_TUI_DISABLE_KEYBOARD_ENHANCEMENT", "1")},

		{field: "WebSearch", set: codex(func(c *config.CodexOptions) { c.WebSearch = true }), want: argvCarries("--search")},
		{field: "FeaturesEnable", set: codex(func(c *config.CodexOptions) { c.FeaturesEnable = []string{"memories"} }), want: argvHas("--enable", "memories")},
		{field: "FeaturesDisable", set: codex(func(c *config.CodexOptions) { c.FeaturesDisable = []string{"apps"} }), want: argvHas("--disable", "apps")},
		{field: "HideAgentReasoning", set: codex(func(c *config.CodexOptions) { c.HideAgentReasoning = true }), want: argvHas("-c", "hide_agent_reasoning=true")},
		{field: "ShowRawAgentReasoning", set: codex(func(c *config.CodexOptions) { c.ShowRawAgentReasoning = true }), want: argvHas("-c", "show_raw_agent_reasoning=true")},
		{field: "ConfigOverrides", set: codex(func(c *config.CodexOptions) { c.ConfigOverrides = []string{`history.persistence="none"`} }),
			want: argvHas("-c", `history.persistence="none"`)},
	})
}

func TestOpenCodeOptionsReachTheLaunch(t *testing.T) {
	oc := func(f func(*config.OpenCodeOptions)) func(*config.HarnessSettings) {
		return func(o *config.HarnessSettings) { f(&o.OpenCode) }
	}
	runOptionCases(t, OpenCode{}, config.OpenCodeOptions{}, []optionCase{
		{field: "Enabled", note: "the picker, not the launcher", set: oc(func(c *config.OpenCodeOptions) { c.Enabled = false })},
		{field: "Binary", note: "resolved to argv[0]", set: oc(func(c *config.OpenCodeOptions) { c.Binary = "opencode" })},
		{field: "Models", note: "the new-session sheet's short list", set: oc(func(c *config.OpenCodeOptions) { c.Models = []config.ModelPick{{ID: "opencode/big-pickle"}} })},
		{field: "ExtraArgs", set: oc(func(c *config.OpenCodeOptions) { c.ExtraArgs = []string{"--mdns"} }), want: argvCarries("--mdns")},
		{field: "ExtraEnv", set: oc(func(c *config.OpenCodeOptions) { c.ExtraEnv = []string{"OPENROUTER_API_KEY=k"} }), want: envIs("OPENROUTER_API_KEY", "k")},

		{field: "DefaultModel", set: oc(func(c *config.OpenCodeOptions) { c.DefaultModel = "anthropic/claude-sonnet-4-5" }),
			want: func(t *testing.T, p LaunchPlan) {
				argvHas("-m", "anthropic/claude-sonnet-4-5")(t, p)
				inlineHas("model", "anthropic/claude-sonnet-4-5")(t, p)
			}},
		{field: "SmallModel", set: oc(func(c *config.OpenCodeOptions) { c.SmallModel = "anthropic/claude-haiku-4-5" }),
			want: inlineHas("small_model", "anthropic/claude-haiku-4-5")},
		{field: "DefaultAgent", set: oc(func(c *config.OpenCodeOptions) { c.DefaultAgent = "plan" }), want: argvHas("--agent", "plan")},

		{field: "Auto", set: oc(func(c *config.OpenCodeOptions) { c.Auto = true }), want: argvCarries("--auto")},
		{field: "PermissionJSON", set: oc(func(c *config.OpenCodeOptions) { c.PermissionJSON = `{"webfetch":"deny"}` }),
			want: func(t *testing.T, p LaunchPlan) {
				envIs("OPENCODE_PERMISSION", `{"webfetch":"deny"}`)(t, p)
				inlineHas("permission.webfetch", "deny")(t, p)
			}},

		{field: "EnabledProviders", set: oc(func(c *config.OpenCodeOptions) { c.EnabledProviders = []string{"anthropic"} }),
			want: inlineHas("enabled_providers", []any{"anthropic"})},
		{field: "DisabledProviders", set: oc(func(c *config.OpenCodeOptions) { c.DisabledProviders = []string{"openai"} }),
			want: inlineHas("disabled_providers", []any{"openai"})},

		{field: "Pure", set: oc(func(c *config.OpenCodeOptions) { c.Pure = true }), want: argvCarries("--pure")},
		{field: "DisableProjectConfig", set: oc(func(c *config.OpenCodeOptions) { c.DisableProjectConfig = true }),
			want: envIs("OPENCODE_DISABLE_PROJECT_CONFIG", "1")},
		{field: "DisableModelsFetch", set: oc(func(c *config.OpenCodeOptions) { c.DisableModelsFetch = true }),
			want: envIs("OPENCODE_DISABLE_MODELS_FETCH", "1")},

		{field: "TUITheme", set: oc(func(c *config.OpenCodeOptions) { c.TUITheme = "solarized" }), want: fileHas("theme", "solarized")},
		{field: "Mini", set: oc(func(c *config.OpenCodeOptions) { c.Mini = true }), want: argvCarries("--mini")},
		{field: "NoReplay", set: oc(func(c *config.OpenCodeOptions) { c.Mini, c.NoReplay = true, true }), want: argvCarries("--no-replay")},
		{field: "ReplayLimit", set: oc(func(c *config.OpenCodeOptions) { c.Mini, c.ReplayLimit = true, 25 }), want: argvHas("--replay-limit", "25")},
		{field: "DisableMouse", set: oc(func(c *config.OpenCodeOptions) { c.DisableMouse = true }), want: envIs("OPENCODE_DISABLE_MOUSE", "1")},
		{field: "Mouse", set: oc(func(c *config.OpenCodeOptions) { c.Mouse = true }), want: fileHas("mouse", true)},
		{field: "Attention", set: oc(func(c *config.OpenCodeOptions) { c.Attention = true }), want: fileHas("attention.enabled", true)},

		{field: "ResumeMode", resume: true, set: oc(func(c *config.OpenCodeOptions) { c.ResumeMode = config.ResumeFork }), want: argvCarries("--fork")},
		{field: "Share", set: oc(func(c *config.OpenCodeOptions) { c.Share = "manual" }), want: inlineHas("share", "manual")},
		{field: "LogLevel", set: oc(func(c *config.OpenCodeOptions) { c.LogLevel = "WARN" }), want: argvHas("--log-level", "WARN")},

		{field: "ConfigContent", set: oc(func(c *config.OpenCodeOptions) { c.ConfigContent = `{"snapshot":false}` }), want: inlineHas("snapshot", false)},
		{field: "TUIConfig", set: oc(func(c *config.OpenCodeOptions) { c.TUIConfig = `{"leader_timeout":900}` }), want: fileHas("leader_timeout", float64(900))},
	})
}

// The handful of variables that are the launch rather than a preference are
// applied after the raw list, so that a typo - or a paste - in extra_env
// cannot quietly disarm the session-id discovery or the server's password.
func TestExtraEnvCannotOverrideTheMandatoryVariables(t *testing.T) {
	l := newLab(t)
	l.settings.Harnesses.Codex.ExtraEnv = []string{"CODEX_INTERNAL_ORIGINATOR_OVERRIDE=someone-else"}
	l.settings.Harnesses.OpenCode.ExtraEnv = []string{
		"OPENCODE_SERVER_PASSWORD=hunter2",
		"OPENCODE_SERVER_USERNAME=root",
		"OPENCODE_TUI_CONFIG=/dev/null",
		"OPENCODE_CONFIG_CONTENT={}",
	}

	codex := l.plan(Codex{})
	if codex.Env["CODEX_INTERNAL_ORIGINATOR_OVERRIDE"] != codexOriginator {
		t.Errorf("extra_env replaced the originator with %q, and the watcher would match nothing",
			codex.Env["CODEX_INTERNAL_ORIGINATOR_OVERRIDE"])
	}

	oc := l.plan(OpenCode{})
	if oc.Env["OPENCODE_SERVER_PASSWORD"] == "hunter2" {
		t.Error("extra_env replaced the generated server password")
	}
	if oc.Env["OPENCODE_SERVER_USERNAME"] != openCodeUser {
		t.Errorf("username = %q", oc.Env["OPENCODE_SERVER_USERNAME"])
	}
	if oc.Env["OPENCODE_TUI_CONFIG"] != oc.Files[0].Path {
		t.Errorf("OPENCODE_TUI_CONFIG = %q, want the generated file", oc.Env["OPENCODE_TUI_CONFIG"])
	}
	if oc.Env["OPENCODE_CONFIG_CONTENT"] == "{}" {
		t.Error("extra_env replaced the generated inline configuration")
	}
}
