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
// Only the binary, the model and the effort come from the settings. The rest
// of the command line is fixed policy - see docs/design/HARNESS-POLICY.md -
// and four parts of it are load-bearing rather than tasteful:
//
//   - the trust override for the working directory, because a fresh directory
//     otherwise opens on a blocking "do you trust this directory?" picker that
//     eats the first keystroke - and dynamic working directories are fresh
//     every single time;
//   - `--strict-config`, because without it an unknown -c key is ignored in
//     silence, and a policy that might quietly do nothing is not a policy;
//   - `--dangerously-bypass-approvals-and-sandbox`, because an approval prompt
//     in a pane nobody is watching is a session that has stopped;
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

// codexTheme is the TUI palette. Codex does not validate a theme name, so this
// is one of its own: light-gray is the light family, which is what a white
// page wants.
const codexTheme = "light-gray"

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
	argv = append(argv, "-c", trustLevelOverride(req.Cwd))
	argv = append(argv, "-c", tomlAssign("tui.theme", codexTheme))
	// Inline rather than the alternate screen, so the pane keeps its
	// scrollback - which is the whole of what a web terminal has.
	argv = append(argv, "--no-alt-screen")
	// Neither -s nor -a is passed with this: the flag replaces both, and Codex
	// refuses a command line that sets a sandbox and then bypasses it.
	// `--yolo` is the same flag under a shorter name; the long spelling is
	// used because it says what it does.
	argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
	// --remote is never passed: a Codex TUI that talks to somebody else's app
	// server is not the session this app started.

	env := baseEnv(req)
	// The originator is what tells a rollout file written by Socrates apart
	// from one the user started by hand, and it is how the session id is found
	// again.
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
