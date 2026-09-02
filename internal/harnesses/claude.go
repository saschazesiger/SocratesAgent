package harnesses

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Claude is the Claude Code launcher.
//
// It is the only one of the four whose session id Socrates gets to choose:
// `--session-id <uuid>` fixes it at creation, so a reboot needs nothing
// discovered and nothing scraped. The price is that an id may never be used
// twice - the binary answers `Error: Session ID <id> is already in use.` - so a
// relaunch either resumes with --resume or starts with a new uuid, never with
// the same --session-id.
type Claude struct{}

func (Claude) Kind() Kind            { return KindClaude }
func (Claude) Label() string         { return "Claude Code" }
func (Claude) DefaultBinary() string { return "claude" }
func (Claude) VersionArgs() []string { return []string{"--version"} }

// claudeEfforts is what --effort takes. It is a closed list on purpose:
// config.NormalizeEffort also admits "minimal" and "ultra", which other
// harnesses name and this flag rejects, and a session that will not start is
// not how a person should find that out.
var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// claudeSettingsFile is the generated settings document, one per session.
const claudeSettingsFile = "claude-settings.json"

// claudeTranscriptDays is how long Claude Code keeps a transcript on disk.
// A conversation has to be resumable after a week away from the machine, and
// Claude Code's own default is shorter than that.
const claudeTranscriptDays = 90

// Plan builds a fresh session, with a uuid of our own choosing.
//
// It mints a new one every time and never looks at req.CLISession, and that is
// the safety rule rather than a simplification. A session id may be used with
// --session-id exactly once - the binary answers `Error: Session ID <id> is
// already in use.` and the pane dies before the user can type - so the only id
// that is safe to start with is one that has never been started with. A
// conversation that is meant to be continued goes through ResumePlan; a
// conversation whose transcript could not be found is a new conversation, and
// this is where that becomes true rather than depending on a caller having
// remembered to clear a field.
func (c Claude) Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	id := uuid.NewString()
	return c.plan(req, []string{"--session-id", id}, id)
}

// ResumePlan is the same launch with --resume in place of --session-id, and
// --fork-session when the dashboard asked for a branch rather than a
// continuation.
//
// --resume is preferred over -c/--continue: -c means "the most recent
// conversation in this directory", which races as soon as two sessions share
// one.
func (c Claude) ResumePlan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	id := strings.TrimSpace(req.CLISession)
	if id == "" {
		return LaunchPlan{}, ErrNoResume
	}
	// --fork-session is deliberately not offered: a resume continues the
	// conversation it names, and a branch of it is a second session the user
	// never asked for.
	return c.plan(req, []string{"--resume", id}, id)
}

// plan is the shared body: everything but the two flags that decide whether
// this is a new conversation or an old one.
//
// The command line is fixed policy, not configuration. Only the binary, the
// model and the effort come from the settings; the rest is what
// docs/design/HARNESS-POLICY.md says every Socrates session gets.
func (c Claude) plan(req PlanRequest, lead []string, id string) (LaunchPlan, error) {
	opts := req.Settings.Harnesses.Claude
	bin, err := resolveBinary(opts.Binary, c.DefaultBinary())
	if err != nil {
		return LaunchPlan{}, err
	}
	settingsPath := SessionFile(req.DataDir, req.SessionID, claudeSettingsFile)

	argv := append([]string{bin}, lead...)
	argv = flag(argv, "--name", req.Title)
	argv = flag(argv, "--model", pick(req.Model, opts.DefaultModel))
	argv = flag(argv, "--effort", oneOf(pick(req.Effort, opts.DefaultEffort), claudeEfforts))
	// Permissions are bypassed, always. A session is a terminal somebody
	// opened on their own machine to do work in; a permission dialog in it is
	// a dialog nobody is watching, on a phone, in a car. --permission-mode
	// bypassPermissions would be the same thing said less plainly, and the
	// settings key below is what stops Claude Code asking about it first.
	argv = append(argv, "--dangerously-skip-permissions")
	argv = append(argv, "--settings", settingsPath)
	// --remote-control is never passed. See claudeSettings and
	// pinClaudeGlobalConfig for the two places it is also turned off, because
	// not passing the flag is not on its own enough.

	env := baseEnv(req)
	// Claude Code does not read OSC 11 at all; COLORFGBG in baseEnv is what
	// decides its palette, and the true-colour hint is what keeps that palette
	// from being approximated to 256 colours inside tmux.
	env["CLAUDE_CODE_TMUX_TRUECOLOR"] = "1"
	// The pane's title belongs to Socrates, and a redraw that blanks the
	// screen between frames is what a phone on a slow link sees as a flash.
	// The mouse is left alone: tmux owns it, and CLAUDE_CODE_DISABLE_MOUSE
	// would take it from the pane as well as from the program.
	env["CLAUDE_CODE_DISABLE_TERMINAL_TITLE"] = "1"
	env["CLAUDE_CODE_NO_FLICKER"] = "1"

	settings, err := claudeSettings(env)
	if err != nil {
		return LaunchPlan{}, err
	}

	pinClaudeGlobalConfig(claudeGlobalConfigPath(), req.Cwd)

	return LaunchPlan{
		Argv:       argv,
		Env:        env,
		Cwd:        req.Cwd,
		Files:      []GenFile{{Path: settingsPath, Mode: 0o600, Data: settings}},
		CLISession: id,
		Discover:   DiscoverPreSet,
	}, nil
}

// claudeSettings builds the one auditable artefact per session. Only keys that
// were verified against the shipped binary are written.
//
// askUserQuestionTimeout and autoContinueAtUsageLimit are deliberately absent.
// They are documented but do not appear in the 2.1.258 binary, and a timeout of
// zero might well mean "dismiss every question at once", which would break
// AskUserQuestion in every session Socrates ever starts.
func claudeSettings(env map[string]string) ([]byte, error) {
	doc := map[string]any{
		// Without this, a launch with --dangerously-skip-permissions stops on
		// a confirmation dialog before the user can type a single character.
		"skipDangerousModePermissionPrompt": true,
		// Remote Control is off in every Socrates session, and the flag not
		// being passed is only half of it: `disableRemoteControl` is the key
		// the binary reports as `Disabled by org policy (disableRemoteControl)`
		// and is what stops the feature being offered inside the pane at all.
		"disableRemoteControl": true,
		// A transcript has to outlive the offline stretch it may have to be
		// resumed after, and Claude Code's own retention is shorter than that.
		"cleanupPeriodDays": claudeTranscriptDays,
	}
	if len(env) > 0 {
		// The env key reaches every subprocess Claude Code starts, which is
		// what makes a tool's shell look like the pane it was started from.
		vars := map[string]any{}
		for k, v := range env {
			vars[k] = v
		}
		doc["env"] = vars
	}
	return json.MarshalIndent(doc, "", "  ")
}

// VerifyCLISession reports whether the transcript of a session id is still on
// disk, which is what --resume needs.
//
// The project directory name is the working directory with its separators
// mangled, and the mangling has non-obvious double-dash cases, so it is not
// reimplemented here: the uuid is unique across every project, and a glob over
// all of them is both shorter and right.
func (Claude) VerifyCLISession(ctx context.Context, req PlanRequest) (bool, error) {
	id := strings.TrimSpace(req.CLISession)
	if id == "" {
		return false, nil
	}
	matches, err := filepath.Glob(filepath.Join(claudeConfigDir(), "projects", "*", id+".jsonl"))
	if err != nil {
		// "Could not tell" is not "gone": the caller tries the resume anyway,
		// and a refusal shows in the terminal, which is honest.
		return false, err
	}
	return len(matches) > 0, nil
}

// DiscoverModels returns the curated list; see claude_models.go.
func (Claude) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return claudeModels(), nil
}

// claudeConfigDir is where Claude Code keeps its transcripts.
func claudeConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// claudeGlobalConfigPath is the global preference file, and its location is
// not simply "inside the config directory": the binary computes it as
// `join(process.env.CLAUDE_CONFIG_DIR || homedir(), ".claude.json")` [BIN], so
// with the variable unset it is a sibling of ~/.claude and with it set it is
// a child of whatever it names.
func claudeGlobalConfigPath() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

// pinClaudeGlobalConfig writes the three preferences that are not
// settings-file keys into Claude Code's global configuration: the light theme,
// Remote Control off at startup, and the working directory trusted.
//
// Where those preferences live was verified against the 2.1.258 binary rather
// than guessed. The defaults object the binary builds for its global config -
// `{numStartups:0, installMethod:undefined, autoUpdates:undefined,
// theme:"dark", preferredNotifChannel:"auto", …, diffTool:"auto",
// autoConnectIde:false, …}` - carries `theme`, and the same object is the one
// the binary logs about as `~/.claude.json` ("Watching ~/.claude.json for
// other processes through the storage interface", "saveConfigWithLock: …
// refusing to write to avoid wiping ~/.claude.json"). So the key is `theme`,
// the file is `$CLAUDE_CONFIG_DIR/.claude.json`, and the shipped default is
// "dark" - which on Socrates' white page is the unreadable case. There is no
// --theme flag in 2.1.258 and `theme` is not a settings.json key.
//
// `remoteControlAtStartup` is the second half of turning Remote Control off.
// Not passing --remote-control is not enough on its own: the binary logs
// `remoteControlAtStartup: true in …` for `project and local` and for
// `legacy_global_config`, so a person who once turned Remote Control on by
// hand has a stored preference that starts it again with no flag at all. It is
// cleared at the top level and inside the entry for this working directory.
//
// `hasTrustDialogAccepted` is what lets the session start at all. A Socrates
// session works in a directory Claude Code has never been opened in - a
// dynamic workspace directory is brand new every time - and in one of those
// the binary opens on a blocking full-screen question ("Accessing workspace:
// … Is this a project you created or one you trust?") whose highlighted answer
// is **No, exit**. Nothing on the command line skips it: there is no
// --trust flag in 2.1.258, --dangerously-skip-permissions is about tool
// permissions and comes after it, and the settings file has no key for it.
// The gate is the per-project attribute in the global config - verified
// against 2.1.258: with `projects[<cwd>].hasTrustDialogAccepted = true` the
// same launch goes straight to the prompt. This is the exact counterpart of
// the `trust_level="trusted"` override Codex is given on its command line
// (§Codex in docs/design/HARNESS-POLICY.md); Claude Code has no command line
// for it, so the entry is written here.
//
// So the entry for this working directory is created when it does not exist,
// rather than only amended when it does. A directory Socrates itself made and
// is about to start a session in is a directory the person asked for; leaving
// the answer to a dialog nobody is watching, on a phone, in a car, is the
// thing this whole product exists not to do.
//
// The file belongs to the user and holds their credentials, so it is read,
// changed by exactly these keys and written through a temporary file in the
// same directory; when it does not exist at all, what is written is a file
// with only them in it, which is what Claude Code itself would grow from. A
// failure is still not a launch failure - the pane opens and says what is
// wrong with it, which is more use than a create that refuses with the same
// sentence - but it is no longer only cosmetic: without the trust entry the
// session opens on the workspace question, so a failure is worth reading in
// the log rather than swallowing.
//
// It is a side effect of building a plan rather than of starting the pane, and
// Claude Code writes the same file under a lock of its own. A write that lands
// while another Claude is running can therefore be overwritten by it, at the
// cost of that session's palette and its trust entry. It is left as it is
// rather than turned into a lock protocol against a file format nobody has
// documented; what the session then shows is Claude Code's own question, not a
// silent failure.
func pinClaudeGlobalConfig(path, cwd string) {
	if path == "" {
		return
	}
	doc := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if json.Unmarshal(raw, &doc) != nil {
			return
		}
	case !os.IsNotExist(err):
		return
	}
	changed := false
	if doc["theme"] != "light" {
		doc["theme"] = "light"
		changed = true
	}
	if doc["remoteControlAtStartup"] != false {
		doc["remoteControlAtStartup"] = false
		changed = true
	}
	if cwd != "" {
		projects, ok := doc["projects"].(map[string]any)
		if !ok {
			// A "projects" that is there but is not an object belongs to a
			// file this code does not understand, and replacing it would
			// throw away whatever it is. Nothing is written in that case.
			if _, present := doc["projects"]; present {
				projects = nil
			} else {
				projects = map[string]any{}
				doc["projects"] = projects
				changed = true
			}
		}
		if projects != nil {
			entry, ok := projects[cwd].(map[string]any)
			if !ok {
				if _, present := projects[cwd]; !present {
					entry = map[string]any{}
					projects[cwd] = entry
					changed = true
				}
			}
			if entry != nil {
				if entry["hasTrustDialogAccepted"] != true {
					entry["hasTrustDialogAccepted"] = true
					changed = true
				}
				if entry["remoteControlAtStartup"] != false {
					entry["remoteControlAtStartup"] = false
					changed = true
				}
			}
		}
	}
	if !changed {
		return
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude.json.*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, path)
}

// pick takes the session's own choice when there is one, and the dashboard's
// default when there is not.
func pick(chosen, fallback string) string {
	if v := strings.TrimSpace(chosen); v != "" {
		return v
	}
	return strings.TrimSpace(fallback)
}

// oneOf passes a value through when it is on the list and drops it when it is
// not, so that a level one CLI names and another rejects never reaches a flag.
func oneOf(value string, allowed []string) string {
	value = strings.TrimSpace(value)
	for _, a := range allowed {
		if value == a {
			return value
		}
	}
	return ""
}
