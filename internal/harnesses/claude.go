package harnesses

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/saschazesiger/SocratesAgent/internal/config"
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

// claudeDebugFile is where --debug-file writes when the admin turns it on.
const claudeDebugFile = "claude-debug.log"

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
	lead := []string{"--resume", id}
	if req.Settings.Harnesses.Claude.ResumeMode == config.ResumeFork {
		lead = append(lead, "--fork-session")
	}
	return c.plan(req, lead, id)
}

// plan is the shared body: everything but the two flags that decide whether
// this is a new conversation or an old one.
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
	if opts.PermissionMode != "" && opts.PermissionMode != "unset" {
		argv = append(argv, "--permission-mode", opts.PermissionMode)
	}
	switch opts.SkipPermissions {
	case config.SkipPermissionsForce:
		argv = append(argv, "--dangerously-skip-permissions")
	case config.SkipPermissionsAllow:
		argv = append(argv, "--allow-dangerously-skip-permissions")
	}
	argv = flag(argv, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	argv = flag(argv, "--disallowedTools", strings.Join(opts.DisallowedTools, ","))
	argv = flag(argv, "--tools", opts.Tools)
	argv = repeated(argv, "--add-dir", opts.AddDirs)
	argv = append(argv, "--settings", settingsPath)
	argv = flag(argv, "--setting-sources", strings.Join(opts.SettingSources, ","))
	argv = switchFlag(argv, "--restricted", opts.Restricted)
	argv = switchFlag(argv, "--safe-mode", opts.SafeMode)
	argv = switchFlag(argv, "--bare", opts.Bare)
	argv = flag(argv, "--agent", opts.Agent)
	argv = flag(argv, "--advisor", opts.Advisor)
	argv = flag(argv, "--autocompact", opts.Autocompact)
	argv = flag(argv, "--append-system-prompt", opts.AppendSystemPrompt)
	if strings.TrimSpace(opts.AppendSystemPrompt) != "" {
		// The snapshot only decides anything when there is an appended prompt:
		// with it on, a *changed* append on a later launch is ignored until
		// compaction. Passing it unconditionally would flip Claude's own
		// default (on, for the built-in prompt) for every session that never
		// appends anything, which is an unrequested behaviour change with a
		// prompt-cache cost on every resume.
		argv = flag(argv, "--system-prompt-snapshot", oneOf(opts.SystemPromptSnapshot, []string{"on", "off"}))
	}
	argv = switchFlag(argv, "--exclude-dynamic-system-prompt-sections", opts.ExcludeDynamicPromptSections)
	argv = switchFlag(argv, "--disable-slash-commands", opts.DisableSlashCommands)
	argv = repeated(argv, "--mcp-config", opts.MCPConfig)
	argv = switchFlag(argv, "--strict-mcp-config", opts.StrictMCPConfig)
	argv = repeated(argv, "--plugin-dir", opts.PluginDirs)
	if opts.RemoteControl {
		// The name is optional, and an optional value takes the next
		// non-dash argument - which would be the first of extra_args if the
		// admin left the name empty. The session title is a better name than
		// somebody's raw flag, and it is always there.
		name := strings.TrimSpace(opts.RemoteControlName)
		if name == "" {
			name = strings.TrimSpace(req.Title)
		}
		argv = append(argv, "--remote-control")
		if name != "" {
			argv = append(argv, name)
		}
	}
	argv = switchFlag(argv, "--verbose", opts.Verbose)
	argv = flag(argv, "-d", opts.DebugFilter)
	if opts.DebugFile {
		argv = append(argv, "--debug-file", SessionFile(req.DataDir, req.SessionID, claudeDebugFile))
	}
	argv = append(argv, opts.ExtraArgs...)

	env := baseEnv(req)
	// Claude Code does not read OSC 11 at all; COLORFGBG in baseEnv is what
	// decides its palette, and the true-colour hint is what keeps that palette
	// from being approximated to 256 colours inside tmux.
	env["CLAUDE_CODE_TMUX_TRUECOLOR"] = "1"
	if opts.DisableTerminalTitle {
		env["CLAUDE_CODE_DISABLE_TERMINAL_TITLE"] = "1"
	}
	if opts.DisableMouse {
		env["CLAUDE_CODE_DISABLE_MOUSE"] = "1"
	}
	if opts.NoFlicker {
		env["CLAUDE_CODE_NO_FLICKER"] = "1"
	}
	if opts.ForceSyncOutput {
		env["CLAUDE_CODE_FORCE_SYNC_OUTPUT"] = "1"
	}
	if opts.MaxThinkingTokens > 0 {
		env["MAX_THINKING_TOKENS"] = strconv.Itoa(opts.MaxThinkingTokens)
	}
	if prefix := strings.TrimSpace(opts.RemoteControlPrefix); prefix != "" {
		env["CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX"] = prefix
	}
	addExtraEnv(env, opts.ExtraEnv)

	settings, err := claudeSettings(opts, env)
	if err != nil {
		return LaunchPlan{}, err
	}

	if opts.PinLightTheme {
		pinClaudeTheme(claudeGlobalConfigPath())
	}

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
// were verified against the shipped binary are written; anything else a person
// wants goes through settings_overrides, which is theirs and is deep-merged
// last.
//
// askUserQuestionTimeout and autoContinueAtUsageLimit are deliberately absent.
// They are documented but do not appear in the 2.1.258 binary, and a timeout of
// zero might well mean "dismiss every question at once", which would break
// AskUserQuestion in every session Socrates ever starts.
func claudeSettings(opts config.ClaudeOptions, env map[string]string) ([]byte, error) {
	doc := map[string]any{}

	// Without these two an unattended launch into bypass or auto mode stops on
	// a confirmation dialog before the user can type a single character. They
	// are written only for the modes that actually raise those dialogs.
	if opts.PermissionMode == "bypassPermissions" || opts.SkipPermissions != config.SkipPermissionsOff {
		doc["skipDangerousModePermissionPrompt"] = true
	}
	if opts.PermissionMode == "auto" {
		doc["skipAutoPermissionPrompt"] = true
	}
	if opts.CleanupPeriodDays > 0 {
		// A transcript has to outlive the offline stretch it may have to be
		// resumed after, and the default retention is shorter than that.
		doc["cleanupPeriodDays"] = opts.CleanupPeriodDays
	}
	permissions := map[string]any{}
	if opts.PermissionMode != "" && opts.PermissionMode != "unset" {
		permissions["defaultMode"] = opts.PermissionMode
	}
	if len(opts.AddDirs) > 0 {
		permissions["additionalDirectories"] = opts.AddDirs
	}
	if len(permissions) > 0 {
		doc["permissions"] = permissions
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

	doc = mergeJSON(doc, opts.SettingsOverrides)
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

// pinClaudeTheme writes "theme": "light" into the global configuration.
//
// Where that preference lives was verified against the 2.1.258 binary rather
// than guessed: the defaults object the binary builds for its global config -
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
// The file belongs to the user and holds their credentials, so it is read,
// changed by exactly one key and written through a temporary file in the same
// directory; when it does not exist at all, what is written is a file with
// only this key in it, which is what Claude Code itself would grow from. A
// failure is not a launch failure: COLORFGBG is the lever that actually
// decides the palette, and this is the belt to its braces.
//
// It is a side effect of building a plan rather than of starting the pane, and
// Claude Code writes the same file under a lock of its own. A pin that lands
// while another Claude is running can therefore be overwritten by it. That
// costs one session the wrong palette and nothing else, which is why it is
// left as it is rather than being turned into a lock protocol against a file
// format nobody has documented.
func pinClaudeTheme(path string) {
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
	if doc["theme"] == "light" {
		return
	}
	doc["theme"] = "light"
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
