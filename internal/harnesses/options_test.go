package harnesses

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// This file answers one question per option: every setting the dashboard has
// is a promise that setting it changes what the program is started with, and a
// promise nothing checks is a promise that quietly stops being true.
//
// The tables are kept honest by reflection: a field of the option struct with
// no entry here fails the test, so an option added to the catalogue cannot be
// added without saying where it goes.
//
// The other half of a launch is no longer here. Since 2026-09-02 the argv is
// fixed policy rather than configuration, and what it must contain - and must
// never contain - is pinned by TestFixedPolicyArgv at the bottom of this file
// rather than by a row per switch.

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
		{field: "DefaultModel", set: claude(func(c *config.ClaudeOptions) { c.DefaultModel = "opus" }), want: argvHas("--model", "opus")},
		{field: "DefaultEffort", set: claude(func(c *config.ClaudeOptions) { c.DefaultEffort = "high" }), want: argvHas("--effort", "high")},
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
		{field: "DefaultModel", set: codex(func(c *config.CodexOptions) { c.DefaultModel = "gpt-5.6-terra" }), want: argvHas("-m", "gpt-5.6-terra")},
		{field: "DefaultEffort", set: codex(func(c *config.CodexOptions) { c.DefaultEffort = "xhigh" }), want: argvHas("-c", `model_reasoning_effort="xhigh"`)},
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
		{field: "DefaultModel", set: oc(func(c *config.OpenCodeOptions) { c.DefaultModel = "anthropic/claude-sonnet-4-5" }),
			want: func(t *testing.T, p LaunchPlan) {
				argvHas("-m", "anthropic/claude-sonnet-4-5")(t, p)
				inlineHas("model", "anthropic/claude-sonnet-4-5")(t, p)
			}},
	})
}

// TestFixedPolicyArgv is the other half of the catalogue: what every session
// gets whatever the settings say, and what no session may ever get.
//
// The flags were verified against the shipped binaries - Claude Code 2.1.258,
// codex-cli 0.152.1, OpenCode 1.17.13 - and the sources are in
// docs/design/HARNESS-POLICY.md. A change to one of these lines is a change to
// what the product promises, not a refactor.
func TestFixedPolicyArgv(t *testing.T) {
	for _, c := range []struct {
		name string
		h    Harness
		// must is a flag the argv always carries.
		must []string
		// never is a flag no argv may carry, whatever is configured.
		never []string
		env   map[string]string
		// noEnv are variables that must not be set at all.
		noEnv []string
	}{
		{
			name: "claude",
			h:    Claude{},
			must: []string{"--dangerously-skip-permissions", "--settings", "--session-id"},
			// Remote Control is off in every session, and the permission mode
			// is not a second lever on the first: bypassing is said once.
			never: []string{"--remote-control", "--permission-mode", "--allow-dangerously-skip-permissions",
				"--restricted", "--safe-mode", "--bare", "--verbose", "--fork-session"},
			env: map[string]string{
				"CLAUDE_CODE_TMUX_TRUECOLOR":         "1",
				"CLAUDE_CODE_DISABLE_TERMINAL_TITLE": "1",
				"CLAUDE_CODE_NO_FLICKER":             "1",
			},
			noEnv: []string{"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX", "MAX_THINKING_TOKENS",
				"CLAUDE_CODE_DISABLE_MOUSE", "CLAUDE_CODE_FORCE_SYNC_OUTPUT"},
		},
		{
			name: "codex",
			h:    Codex{},
			must: []string{"--strict-config", "--no-alt-screen", "--dangerously-bypass-approvals-and-sandbox"},
			// -s and -a are what the bypass replaces; passing either beside it
			// is a command line Codex refuses.
			never: []string{"-s", "-a", "--remote", "--remote-auth-token-env", "--approve-for-me", "--search", "--yolo"},
			env:   map[string]string{"CODEX_INTERNAL_ORIGINATOR_OVERRIDE": codexOriginator},
			noEnv: []string{"CODEX_TUI_DISABLE_KEYBOARD_ENHANCEMENT"},
		},
		{
			name:  "opencode",
			h:     OpenCode{},
			must:  []string{"--port", "--hostname"},
			never: []string{"--auto", "--pure", "--mini", "--print-logs", "--fork", "--log-level"},
			env: map[string]string{
				"OPENCODE_DISABLE_AUTOUPDATE":     "1",
				"OPENCODE_DISABLE_TERMINAL_TITLE": "1",
				"OPENCODE_DISABLE_MODELS_FETCH":   "1",
				"OPENCODE_SERVER_USERNAME":        openCodeUser,
			},
			noEnv: []string{"OPENCODE_DISABLE_MOUSE", "OPENCODE_DISABLE_PROJECT_CONFIG"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newLab(t).plan(c.h)
			for _, flag := range c.must {
				if !carries(p.Argv, flag) {
					t.Errorf("argv has no %s: %v", flag, p.Argv)
				}
			}
			for _, flag := range c.never {
				if carries(p.Argv, flag) {
					t.Errorf("argv carries %s, which no session may ever be started with: %v", flag, p.Argv)
				}
			}
			for key, want := range c.env {
				if p.Env[key] != want {
					t.Errorf("%s = %q, want %q", key, p.Env[key], want)
				}
			}
			for _, key := range c.noEnv {
				if _, ok := p.Env[key]; ok {
					t.Errorf("%s is set to %q and should not be set at all", key, p.Env[key])
				}
			}
		})
	}
}

// The Claude Code settings file is where the two things a flag cannot say are
// said: that the bypass dialog is not to be shown, and that Remote Control is
// off. Neither is a setting.
func TestClaudeSettingsCarryTheFixedPolicy(t *testing.T) {
	p := newLab(t).plan(Claude{})
	fileHas("skipDangerousModePermissionPrompt", true)(t, p)
	fileHas("disableRemoteControl", true)(t, p)
	fileHas("cleanupPeriodDays", float64(claudeTranscriptDays))(t, p)
}

// OpenCode is told to allow everything twice - once in the variable that is
// merged over every configuration file it found, and once in the generated
// configuration that is merged last - because a prompt in a pane nobody is
// watching is a session that has stopped.
func TestOpenCodeAllowsEveryPermission(t *testing.T) {
	p := newLab(t).plan(OpenCode{})
	var fromEnv map[string]any
	if err := json.Unmarshal([]byte(p.Env["OPENCODE_PERMISSION"]), &fromEnv); err != nil {
		t.Fatalf("OPENCODE_PERMISSION is not JSON: %v", err)
	}
	for key, want := range openCodePermission {
		if fromEnv[key] != want {
			t.Errorf("OPENCODE_PERMISSION[%q] = %#v, want %#v", key, fromEnv[key], want)
		}
		inlineHas("permission."+key, want)(t, p)
	}
	if fromEnv["*"] != "allow" {
		t.Errorf("the wildcard permission is %#v", fromEnv["*"])
	}
	// Nothing a session does is published anywhere.
	inlineHas("share", "disabled")(t, p)
}
