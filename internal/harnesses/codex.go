package harnesses

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// Codex is the Codex TUI launcher.
//
// Three things about it are not options and a change that drops one of them is
// a defect:
//
//   - the trust override for the working directory, because a fresh directory
//     otherwise opens on a blocking "do you trust this directory?" picker that
//     eats the first keystroke - and dynamic working directories are fresh
//     every single time;
//   - `--strict-config`, because without it an unknown -c key is ignored in
//     silence, and a dashboard whose settings might quietly do nothing is not
//     a dashboard worth having;
//   - `CODEX_INTERNAL_ORIGINATOR_OVERRIDE=socrates`, because it stamps the
//     rollout file that is how the session id is found again.
type Codex struct{}

func (Codex) Kind() Kind            { return KindCodex }
func (Codex) Label() string         { return "Codex" }
func (Codex) DefaultBinary() string { return "codex" }
func (Codex) VersionArgs() []string { return []string{"--version"} }

// Plan builds a fresh session. Codex has no way of being told what its session
// id should be - CODEX_SESSION_ID is something it exports, not something it
// reads - so the id is discovered afterwards by watching for the rollout file.
func (c Codex) Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	return c.plan(req, nil)
}

// ResumePlan inserts `resume <uuid>` before the identical option block, which
// `codex resume` accepts unchanged. --last is deliberately not used: it is
// filtered by working directory and picks "the most recent", which is a race
// as soon as two sessions share a directory.
func (c Codex) ResumePlan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	id := strings.TrimSpace(req.CLISession)
	if id == "" {
		return LaunchPlan{}, ErrNoResume
	}
	return c.plan(req, []string{"resume", id})
}

func (c Codex) plan(req PlanRequest, lead []string) (LaunchPlan, error) {
	opts := req.Settings.Harnesses.Codex
	bin, err := resolveBinary(opts.Binary, c.DefaultBinary())
	if err != nil {
		return LaunchPlan{}, err
	}

	argv := append([]string{bin}, lead...)
	argv = append(argv, "--strict-config", "-C", req.Cwd)
	argv = flag(argv, "-m", pick(req.Model, opts.DefaultModel))
	if effort := oneOf(pick(req.Effort, opts.DefaultEffort), config.CodexEfforts); effort != "" {
		argv = append(argv, "-c", tomlAssign("model_reasoning_effort", effort))
	}
	argv = flag(argv, "-s", oneOf(opts.Sandbox, config.CodexSandboxes))
	switch opts.Approval {
	case "on-request", "never":
		argv = append(argv, "-a", opts.Approval)
	case "on-failure":
		// The flag rejects on-failure; the configuration key takes it.
		argv = append(argv, "-c", tomlAssign("approval_policy", "on-failure"))
	}
	if opts.TrustWorkdir {
		argv = append(argv, "-c", trustLevelOverride(req.Cwd))
	}
	argv = append(argv, "-c", tomlAssign("tui.theme", opts.TUITheme))
	argv = switchFlag(argv, "--no-alt-screen", opts.NoAltScreen)
	argv = repeated(argv, "--add-dir", opts.AddDirs)
	argv = switchFlag(argv, "--search", opts.WebSearch)
	argv = switchFlag(argv, "--approve-for-me", opts.ApproveForMe)
	if opts.NetworkAccess {
		argv = append(argv, "-c", "sandbox_workspace_write.network_access=true")
	}
	if len(opts.WritableRoots) > 0 {
		argv = append(argv, "-c", "sandbox_workspace_write.writable_roots="+tomlStringArray(opts.WritableRoots))
	}
	if opts.HideAgentReasoning {
		argv = append(argv, "-c", "hide_agent_reasoning=true")
	}
	if opts.ShowRawAgentReasoning {
		argv = append(argv, "-c", "show_raw_agent_reasoning=true")
	}
	if summary := oneOf(opts.ModelReasoningSummary, []string{"auto", "concise", "detailed", "none"}); summary != "" {
		argv = append(argv, "-c", tomlAssign("model_reasoning_summary", summary))
	}
	if verbosity := oneOf(opts.ModelVerbosity, []string{"low", "medium", "high"}); verbosity != "" {
		argv = append(argv, "-c", tomlAssign("model_verbosity", verbosity))
	}
	if personality := oneOf(opts.Personality, []string{"none", "friendly", "pragmatic"}); personality != "" {
		argv = append(argv, "-c", tomlAssign("personality", personality))
	}
	if review := strings.TrimSpace(opts.ReviewModel); review != "" {
		argv = append(argv, "-c", tomlAssign("review_model", review))
	}
	argv = repeated(argv, "--enable", opts.FeaturesEnable)
	argv = repeated(argv, "--disable", opts.FeaturesDisable)
	argv = flag(argv, "--remote", opts.RemoteAddr)
	argv = flag(argv, "--remote-auth-token-env", opts.RemoteAuthTokenEnv)
	argv = switchFlag(argv, "--dangerously-bypass-approvals-and-sandbox", opts.Bypass)
	argv = repeated(argv, "-c", opts.ConfigOverrides)
	argv = append(argv, opts.ExtraArgs...)

	env := baseEnv(req)
	if opts.DisableKeyboardEnhancement {
		env["CODEX_TUI_DISABLE_KEYBOARD_ENHANCEMENT"] = "1"
	}
	addExtraEnv(env, opts.ExtraEnv)
	// The originator is what tells a rollout file written by Socrates apart
	// from one the user started by hand, and it is how the session id is found
	// again. It is set after the raw list on purpose: §C.11 lists it as always
	// applied and not user-visible, and an extra_env entry that overwrote it
	// would leave the watcher matching nothing at all, in silence, for fifteen
	// minutes.
	env["CODEX_INTERNAL_ORIGINATOR_OVERRIDE"] = codexOriginator

	return LaunchPlan{
		Argv:       argv,
		Env:        env,
		Cwd:        req.Cwd,
		CLISession: strings.TrimSpace(req.CLISession),
		Discover:   DiscoverCodexRollout,
	}, nil
}

// trustLevelOverride is the mandatory trust key for one working directory.
//
// It is written as a whole inline table - `projects={"<path>"={trust_level=
// "trusted"}}` - rather than as the dotted key `projects."<path>".trust_level`,
// because Codex 0.152.0 splits a dotted override on every `.` without
// respecting the quotes around the path. Verified against the binary: with
// --strict-config, `projects."/a.d".trust_level="trusted"` is rejected as
// `unknown configuration field`, while `projects."/a b".trust_level` is
// accepted - so it is the dot, and every default Socrates workspace lives
// under ~/.socrates, which has one. The table form is accepted by
// --strict-config with dots and spaces in the path, and a Codex TUI started
// with it in a directory it had never seen opens with no trust picker.
//
// The path is quoted the way TOML quotes a basic string, which is what JSON
// does too, character for character.
func trustLevelOverride(cwd string) string {
	return "projects={" + tomlString(cwd) + "={trust_level=" + tomlString("trusted") + "}}"
}

// tomlAssign is `key="value"`, for the string-valued -c overrides.
func tomlAssign(key, value string) string { return key + "=" + tomlString(value) }

// tomlString quotes a value as a TOML basic string.
//
// json.Marshal does that job: it escapes the quote, the backslash and every
// control character below 0x20 as escapes TOML also accepts, and leaves UTF-8
// alone. The one place the two disagree is DEL, which TOML requires escaped
// and JSON does not, so that one character is escaped here.
func tomlString(value string) string {
	quoted, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return strings.ReplaceAll(string(quoted), "\u007f", `\u007F`)
}

func tomlStringArray(values []string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, tomlString(v))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// codexHome is where Codex keeps its rollout files and its index.
func codexHome() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// DiscoverModels asks the binary for its catalogue; see codex_models.go.
func (Codex) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return codexModels(ctx, bin)
}
