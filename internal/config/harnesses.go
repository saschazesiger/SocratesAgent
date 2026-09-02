package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file is the option catalogue: one struct per harness, every field a
// setting the admin dashboard exposes, and nothing else. The launchers turn
// these into argv, environment and generated config files; what a field means
// on the command line is written next to it so the two can never drift apart.
//
// Two rules hold for every struct here.
//
// A field whose sensible default is "on" is only ever set to true in
// Default(). Normalize never puts a boolean back, because the dashboard sends
// the whole document and a switch that was turned off must stay off; the
// server decodes a stored document into Default() rather than into a zero
// value, so an older document that never heard of a field still gets its
// default.
//
// A field the user types free JSON into is kept as a string. It is validated
// where it is saved, so a typo is a save-time error rather than a launch that
// fails an hour later - and a settings document can never become unparseable
// because one textarea did.

// Harness ids. The set is closed: these four are what a session can be.
const (
	HarnessShell    = "shell"
	HarnessClaude   = "claude"
	HarnessCodex    = "codex"
	HarnessOpenCode = "opencode"
)

// KnownHarnesses is every harness, in the order the picker offers them.
var KnownHarnesses = []string{HarnessShell, HarnessClaude, HarnessCodex, HarnessOpenCode}

// IsHarness reports whether id names one of them.
func IsHarness(id string) bool {
	for _, h := range KnownHarnesses {
		if id == h {
			return true
		}
	}
	return false
}

// HarnessSettings is what the user decides about the four programs a session
// can be. Everything else about them - how they are attached to, what their
// screen looks like - is the app's business, not a setting.
type HarnessSettings struct {
	Shell    ShellOptions    `json:"shell"`
	Claude   ClaudeOptions   `json:"claude"`
	Codex    CodexOptions    `json:"codex"`
	OpenCode OpenCodeOptions `json:"opencode"`
}

// Common is the handful of settings every harness has: whether it is offered
// at all, where its program lives, and the two raw escape hatches for the flag
// and the variable this app did not think of.
type Common struct {
	Enabled bool `json:"enabled"`
	// Binary overrides where the program is found. Empty means "look it up on
	// PATH", which is what a normal installation wants.
	Binary string `json:"binary,omitempty"`
	// ExtraArgs is appended to the command line verbatim, and ExtraEnv to the
	// environment as "KEY=VALUE". Both are start-only: a running session is
	// not relaunched because a setting changed.
	ExtraArgs []string `json:"extra_args,omitempty"`
	ExtraEnv  []string `json:"extra_env,omitempty"`
	// Models is the person's own short list: the models the new-session sheet
	// offers for this harness, in this order, each with the effort it starts
	// on. Empty means "offer everything the harness reports", which is what a
	// fresh installation wants and what a four-hundred-entry list does not.
	Models []ModelPick `json:"models,omitempty"`
}

// ModelPick is one entry of that short list. The id is in the harness's own
// naming, picked from the discovered list or typed in - a typed one is not
// checked against anything here, because the discovery may simply be older
// than the model.
type ModelPick struct {
	ID     string `json:"id"`
	Effort string `json:"effort,omitempty"` // an effort level, "" = the model's own
}

// ShellOptions configures a plain login shell. There is no model and no
// permission story: it is the user's own shell, with their own rights.
type ShellOptions struct {
	Common
	// Login starts the shell as a login shell (-l), so the profile that sets
	// up PATH and the prompt is read.
	Login bool `json:"login"`
}

// ClaudeOptions is the Claude Code launch surface. Every field is start-only.
type ClaudeOptions struct {
	Common

	// Model and effort.
	DefaultModel      string `json:"default_model"`       // --model
	DefaultEffort     string `json:"default_effort"`      // --effort
	Advisor           string `json:"advisor"`             // --advisor (undocumented; advanced)
	Autocompact       string `json:"autocompact"`         // --autocompact: "auto" or 100k…1M
	MaxThinkingTokens int    `json:"max_thinking_tokens"` // env MAX_THINKING_TOKENS, 0 = unset

	// Permissions and sandbox.
	PermissionMode    string   `json:"permission_mode"`     // --permission-mode, "unset" = omitted
	SkipPermissions   string   `json:"skip_permissions"`    // off | allow | force
	AllowedTools      []string `json:"allowed_tools"`       // --allowedTools
	DisallowedTools   []string `json:"disallowed_tools"`    // --disallowedTools
	Tools             string   `json:"tools"`               // --tools
	AddDirs           []string `json:"add_dirs"`            // --add-dir, repeatable
	Restricted        bool     `json:"restricted"`          // --restricted
	SafeMode          bool     `json:"safe_mode"`           // --safe-mode
	Bare              bool     `json:"bare"`                // --bare (forces API-key auth)
	SettingSources    []string `json:"setting_sources"`     // --setting-sources
	CleanupPeriodDays int      `json:"cleanup_period_days"` // settings cleanupPeriodDays

	// Remote control.
	RemoteControl       bool   `json:"remote_control"`        // --remote-control
	RemoteControlName   string `json:"remote_control_name"`   // --remote-control <name>
	RemoteControlPrefix string `json:"remote_control_prefix"` // env CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX

	// Session and prompt.
	ResumeMode                   string `json:"resume_mode"`                     // continue | fork
	Agent                        string `json:"agent"`                           // --agent
	AppendSystemPrompt           string `json:"append_system_prompt"`            // --append-system-prompt
	SystemPromptSnapshot         string `json:"system_prompt_snapshot"`          // on | off
	ExcludeDynamicPromptSections bool   `json:"exclude_dynamic_prompt_sections"` // --exclude-dynamic-system-prompt-sections
	DisableSlashCommands         bool   `json:"disable_slash_commands"`          // --disable-slash-commands

	// Extensions.
	MCPConfig       []string `json:"mcp_config"`        // --mcp-config
	StrictMCPConfig bool     `json:"strict_mcp_config"` // --strict-mcp-config
	PluginDirs      []string `json:"plugin_dirs"`       // --plugin-dir, repeatable

	// Theme and terminal. The defaults are what makes Claude Code look and
	// behave like a web terminal rather than a desktop one.
	PinLightTheme        bool `json:"pin_light_theme"`        // writes "theme":"light"
	DisableTerminalTitle bool `json:"disable_terminal_title"` // env CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1
	DisableMouse         bool `json:"disable_mouse"`          // env CLAUDE_CODE_DISABLE_MOUSE=1
	NoFlicker            bool `json:"no_flicker"`             // env CLAUDE_CODE_NO_FLICKER=1
	ForceSyncOutput      bool `json:"force_sync_output"`      // env CLAUDE_CODE_FORCE_SYNC_OUTPUT=1

	// Diagnostics.
	Verbose     bool   `json:"verbose"`      // --verbose
	DebugFilter string `json:"debug_filter"` // -d <filter>
	DebugFile   bool   `json:"debug_file"`   // --debug-file <session dir>/claude-debug.log

	// Advanced. SettingsOverrides is deep-merged into the generated settings
	// file; it is JSON in a string, checked where it is saved.
	SettingsOverrides string `json:"settings_overrides"`
}

// Claude Code permission modes, as the flag spells them.
var ClaudePermissionModes = []string{"unset", "manual", "acceptEdits", "auto", "plan", "dontAsk", "bypassPermissions"}

// Claude Code's dangerous-skip choices. "allow" offers the escape hatch to the
// session, "force" takes it without asking - both behind a confirmation.
const (
	SkipPermissionsOff   = "off"
	SkipPermissionsAllow = "allow"
	SkipPermissionsForce = "force"
)

// Resume modes shared by Claude Code and OpenCode: carry the thread on, or
// branch it so the original stays as it was.
const (
	ResumeContinue = "continue"
	ResumeFork     = "fork"
)

// CodexOptions is the Codex launch surface. Every field is start-only, and
// because Socrates always launches Codex with --strict-config, a wrong value
// here is a loud failure at start rather than a quiet one later.
type CodexOptions struct {
	Common

	// Model and effort.
	DefaultModel          string `json:"default_model"`           // -m
	DefaultEffort         string `json:"default_effort"`          // -c model_reasoning_effort
	ModelReasoningSummary string `json:"model_reasoning_summary"` // auto | concise | detailed | none
	ModelVerbosity        string `json:"model_verbosity"`         // low | medium | high
	Personality           string `json:"personality"`             // none | friendly | pragmatic
	ReviewModel           string `json:"review_model"`            // -c review_model

	// Permissions and sandbox.
	Sandbox       string   `json:"sandbox"`        // -s
	Approval      string   `json:"approval"`       // -a, or -c approval_policy for on-failure
	NetworkAccess bool     `json:"network_access"` // -c sandbox_workspace_write.network_access
	WritableRoots []string `json:"writable_roots"` // -c sandbox_workspace_write.writable_roots
	AddDirs       []string `json:"add_dirs"`       // --add-dir
	ApproveForMe  bool     `json:"approve_for_me"` // --approve-for-me
	Bypass        bool     `json:"bypass"`         // --dangerously-bypass-approvals-and-sandbox
	// TrustWorkdir writes the mandatory projects.<cwd>.trust_level override.
	// With it off, Codex opens on its trust picker and the session blocks.
	TrustWorkdir bool `json:"trust_workdir"`

	// Remote control. RemoteAuthTokenEnv is the *name* of an environment
	// variable, not the token itself.
	RemoteAddr         string `json:"remote_addr"`           // --remote
	RemoteAuthTokenEnv string `json:"remote_auth_token_env"` // --remote-auth-token-env

	// Theme and terminal.
	TUITheme                   string `json:"tui_theme"`                    // -c tui.theme
	NoAltScreen                bool   `json:"no_alt_screen"`                // --no-alt-screen, keeps scrollback
	DisableKeyboardEnhancement bool   `json:"disable_keyboard_enhancement"` // env CODEX_TUI_DISABLE_KEYBOARD_ENHANCEMENT=1

	// Tools and features.
	WebSearch             bool     `json:"web_search"`               // --search
	FeaturesEnable        []string `json:"features_enable"`          // --enable
	FeaturesDisable       []string `json:"features_disable"`         // --disable
	HideAgentReasoning    bool     `json:"hide_agent_reasoning"`     // -c
	ShowRawAgentReasoning bool     `json:"show_raw_agent_reasoning"` // -c

	// Advanced: one -c per entry, "key=value".
	ConfigOverrides []string `json:"config_overrides"`
}

// Codex sandbox policies and approval policies, as the flags spell them. The
// approval values Codex accepts but Socrates does not offer (untrusted,
// granular) are deliberately absent: they need a UI this app does not have.
var (
	CodexSandboxes = []string{"read-only", "workspace-write", "danger-full-access"}
	CodexApprovals = []string{"on-request", "on-failure", "never"}
	// CodexEfforts is a closed list because Codex does not validate the value
	// itself: a typo would reach the model as configuration and be ignored.
	CodexEfforts = []string{"minimal", "low", "medium", "high", "xhigh"}
)

// OpenCodeOptions is the OpenCode launch surface. Every field is start-only.
type OpenCodeOptions struct {
	Common

	// Model and agent. A model id may itself contain slashes; only the first
	// separates the provider from it.
	DefaultModel string `json:"default_model"` // -m provider/model
	SmallModel   string `json:"small_model"`   // config small_model
	DefaultAgent string `json:"default_agent"` // --agent

	// Permissions. PermissionJSON is JSON in a string, checked where it is
	// saved, and reaches OpenCode as OPENCODE_PERMISSION.
	Auto           bool   `json:"auto"` // --auto, approves everything not denied
	PermissionJSON string `json:"permission_json"`

	// Providers: an allowlist and a blocklist of provider ids.
	EnabledProviders  []string `json:"enabled_providers"`
	DisabledProviders []string `json:"disabled_providers"`

	// Isolation. DisableModelsFetch is what lets OpenCode start with no
	// network at all, which is why it is on by default.
	Pure                 bool `json:"pure"`                   // --pure
	DisableProjectConfig bool `json:"disable_project_config"` // env OPENCODE_DISABLE_PROJECT_CONFIG=1
	DisableModelsFetch   bool `json:"disable_models_fetch"`   // env OPENCODE_DISABLE_MODELS_FETCH=1

	// Theme and terminal. Mini is line-oriented instead of alt-screen, which
	// is easier to read on a phone; no_replay and replay_limit only mean
	// anything while it is on.
	TUITheme     string `json:"tui_theme"`
	Mini         bool   `json:"mini"`          // --mini
	NoReplay     bool   `json:"no_replay"`     // --no-replay (mini only)
	ReplayLimit  int    `json:"replay_limit"`  // --replay-limit (mini only)
	DisableMouse bool   `json:"disable_mouse"` // env OPENCODE_DISABLE_MOUSE=1
	Mouse        bool   `json:"mouse"`         // tui.json mouse
	// Attention is off because a server harness has no business trying to
	// make a desktop notification sound.
	Attention bool `json:"attention"` // tui.json attention.enabled

	// Session.
	ResumeMode string `json:"resume_mode"` // continue | fork
	Share      string `json:"share"`       // manual | auto | disabled

	// Diagnostics. --print-logs is deliberately not offered: it writes the
	// log into the very pane the user is reading.
	LogLevel string `json:"log_level"` // DEBUG | INFO | WARN | ERROR, "" = OpenCode's own

	// Advanced: JSON in a string, deep-merged into the generated files.
	ConfigContent string `json:"config_content"` // OPENCODE_CONFIG_CONTENT
	TUIConfig     string `json:"tui_config"`     // tui.json
}

// OpenCode's closed lists.
var (
	OpenCodeShareModes = []string{"manual", "auto", "disabled"}
	OpenCodeLogLevels  = []string{"DEBUG", "INFO", "WARN", "ERROR"}
)

// DefaultHarnesses is the launch surface of a fresh installation: everything
// offered, nothing overridden, and the handful of switches that make the four
// programs behave like a web terminal already on.
func DefaultHarnesses() HarnessSettings {
	return HarnessSettings{
		Shell: ShellOptions{
			Common: Common{Enabled: true},
			Login:  true,
		},
		Claude: ClaudeOptions{
			Common:               Common{Enabled: true},
			PermissionMode:       "unset",
			SkipPermissions:      SkipPermissionsOff,
			CleanupPeriodDays:    90,
			ResumeMode:           ResumeContinue,
			SystemPromptSnapshot: "off",
			PinLightTheme:        true,
			DisableTerminalTitle: true,
			NoFlicker:            true,
		},
		Codex: CodexOptions{
			Common:       Common{Enabled: true},
			Sandbox:      "workspace-write",
			Approval:     "on-request",
			TrustWorkdir: true,
			TUITheme:     "light-gray",
			NoAltScreen:  true,
		},
		OpenCode: OpenCodeOptions{
			Common:             Common{Enabled: true},
			DisableModelsFetch: true,
			TUITheme:           "github",
			Mouse:              true,
			ResumeMode:         ResumeContinue,
			Share:              "disabled",
		},
	}
}

// Entry returns the settings every harness shares, and whether the id names a
// harness at all. It is what discovery and the picker ask: is this program
// offered, where does it live, and which models did the user shortlist.
func (h HarnessSettings) Entry(id string) (Common, bool) {
	switch id {
	case HarnessShell:
		return h.Shell.Common, true
	case HarnessClaude:
		return h.Claude.Common, true
	case HarnessCodex:
		return h.Codex.Common, true
	case HarnessOpenCode:
		return h.OpenCode.Common, true
	}
	return Common{}, false
}

// normalize tidies the parts a person types into. It never changes a switch:
// see the note at the top of this file.
func (c *Common) normalize() {
	c.Binary = strings.TrimSpace(c.Binary)
	c.ExtraArgs = trimList(c.ExtraArgs)
	c.ExtraEnv = trimEnv(c.ExtraEnv)
	c.Models = normalizePicks(c.Models)
}

func (h *HarnessSettings) normalize() {
	d := DefaultHarnesses()

	h.Shell.Common.normalize()

	c := &h.Claude
	c.Common.normalize()
	c.DefaultModel = strings.TrimSpace(c.DefaultModel)
	c.DefaultEffort = NormalizeEffort(c.DefaultEffort)
	c.Advisor = strings.TrimSpace(c.Advisor)
	c.Autocompact = strings.TrimSpace(c.Autocompact)
	if c.MaxThinkingTokens < 0 {
		c.MaxThinkingTokens = 0
	}
	c.PermissionMode = oneOf(c.PermissionMode, ClaudePermissionModes, "unset")
	c.SkipPermissions = oneOf(c.SkipPermissions, []string{SkipPermissionsOff, SkipPermissionsAllow, SkipPermissionsForce}, SkipPermissionsOff)
	c.AllowedTools = trimList(c.AllowedTools)
	c.DisallowedTools = trimList(c.DisallowedTools)
	c.Tools = strings.TrimSpace(c.Tools)
	c.AddDirs = trimList(c.AddDirs)
	c.SettingSources = trimList(c.SettingSources)
	if c.CleanupPeriodDays < 1 || c.CleanupPeriodDays > 3650 {
		c.CleanupPeriodDays = d.Claude.CleanupPeriodDays
	}
	c.RemoteControlName = strings.TrimSpace(c.RemoteControlName)
	c.RemoteControlPrefix = strings.TrimSpace(c.RemoteControlPrefix)
	c.ResumeMode = oneOf(c.ResumeMode, []string{ResumeContinue, ResumeFork}, ResumeContinue)
	c.Agent = strings.TrimSpace(c.Agent)
	c.SystemPromptSnapshot = oneOf(c.SystemPromptSnapshot, []string{"on", "off"}, "off")
	c.MCPConfig = trimList(c.MCPConfig)
	c.PluginDirs = trimList(c.PluginDirs)
	c.DebugFilter = strings.TrimSpace(c.DebugFilter)
	c.SettingsOverrides = strings.TrimSpace(c.SettingsOverrides)

	x := &h.Codex
	x.Common.normalize()
	x.DefaultModel = strings.TrimSpace(x.DefaultModel)
	x.DefaultEffort = oneOf(x.DefaultEffort, CodexEfforts, "")
	x.ModelReasoningSummary = oneOf(x.ModelReasoningSummary, []string{"auto", "concise", "detailed", "none"}, "")
	x.ModelVerbosity = oneOf(x.ModelVerbosity, []string{"low", "medium", "high"}, "")
	x.Personality = oneOf(x.Personality, []string{"none", "friendly", "pragmatic"}, "")
	x.ReviewModel = strings.TrimSpace(x.ReviewModel)
	x.Sandbox = oneOf(x.Sandbox, CodexSandboxes, d.Codex.Sandbox)
	x.Approval = oneOf(x.Approval, CodexApprovals, d.Codex.Approval)
	x.WritableRoots = trimList(x.WritableRoots)
	x.AddDirs = trimList(x.AddDirs)
	x.RemoteAddr = strings.TrimSpace(x.RemoteAddr)
	x.RemoteAuthTokenEnv = strings.TrimSpace(x.RemoteAuthTokenEnv)
	if x.TUITheme = strings.TrimSpace(x.TUITheme); x.TUITheme == "" {
		x.TUITheme = d.Codex.TUITheme
	}
	x.FeaturesEnable = trimList(x.FeaturesEnable)
	x.FeaturesDisable = trimList(x.FeaturesDisable)
	x.ConfigOverrides = trimList(x.ConfigOverrides)

	o := &h.OpenCode
	o.Common.normalize()
	o.DefaultModel = strings.TrimSpace(o.DefaultModel)
	o.SmallModel = strings.TrimSpace(o.SmallModel)
	o.DefaultAgent = strings.TrimSpace(o.DefaultAgent)
	o.PermissionJSON = strings.TrimSpace(o.PermissionJSON)
	o.EnabledProviders = trimList(o.EnabledProviders)
	o.DisabledProviders = trimList(o.DisabledProviders)
	if o.TUITheme = strings.TrimSpace(o.TUITheme); o.TUITheme == "" {
		o.TUITheme = d.OpenCode.TUITheme
	}
	if o.ReplayLimit < 0 {
		o.ReplayLimit = 0
	}
	o.ResumeMode = oneOf(o.ResumeMode, []string{ResumeContinue, ResumeFork}, ResumeContinue)
	o.Share = oneOf(o.Share, OpenCodeShareModes, d.OpenCode.Share)
	o.LogLevel = oneOfFold(o.LogLevel, OpenCodeLogLevels, "")
	o.ConfigContent = strings.TrimSpace(o.ConfigContent)
	o.TUIConfig = strings.TrimSpace(o.TUIConfig)
}

// oneOf keeps a value that is on the list and replaces anything else with the
// fallback. A closed list in the dashboard is only closed if the server says
// so too: the API takes whatever is sent to it.
func oneOf(value string, allowed []string, fallback string) string {
	value = strings.TrimSpace(value)
	for _, a := range allowed {
		if value == a {
			return value
		}
	}
	return fallback
}

// oneOfFold is oneOf for a list that is written in capitals, where typing it
// in lower case is a spelling and not a different value.
func oneOfFold(value string, allowed []string, fallback string) string {
	value = strings.TrimSpace(value)
	for _, a := range allowed {
		if strings.EqualFold(value, a) {
			return a
		}
	}
	return fallback
}

// trimList drops the blanks a list editor leaves behind, and returns nil for a
// list that held nothing else, so an empty setting is absent from the stored
// document rather than an empty array in it.
func trimList(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// trimEnv is trimList for "KEY=VALUE" entries, and it drops the ones that are
// not: a variable with no name cannot be set, and a launch is the wrong place
// to find that out. The value keeps its spaces - they may well be the point.
func trimEnv(in []string) []string {
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		key, _, ok := strings.Cut(v, "=")
		if !ok || !validEnvKey(key) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// validEnvKey is the portable shape of an environment variable name.
func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// normalizePicks trims every id, drops the empty ones and the repeats (the
// first occurrence wins, so the order a person arranged survives), and maps
// every effort onto a level the harnesses understand.
func normalizePicks(in []ModelPick) []ModelPick {
	var out []ModelPick
	seen := map[string]bool{}
	for _, p := range in {
		id := strings.TrimSpace(p.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, ModelPick{ID: id, Effort: NormalizeEffort(p.Effort)})
	}
	return out
}

// validate checks the fields a person types free-form text into and Normalize
// cannot repair. They are checked where the document is saved so that a typo
// is an error next to the field that has it, rather than a session that fails
// to launch an hour later with a message from a program the person never saw.
func (h HarnessSettings) validate() error {
	for _, f := range []struct {
		name  string
		value string
	}{
		{"harnesses.claude.settings_overrides", h.Claude.SettingsOverrides},
		{"harnesses.opencode.permission_json", h.OpenCode.PermissionJSON},
		{"harnesses.opencode.config_content", h.OpenCode.ConfigContent},
		{"harnesses.opencode.tui_config", h.OpenCode.TUIConfig},
	} {
		if f.value == "" {
			continue
		}
		if !json.Valid([]byte(f.value)) {
			return fmt.Errorf("%s is not valid JSON", f.name)
		}
	}
	// Every config override reaches Codex as one -c argument, and Codex is
	// launched with --strict-config: an entry that is not an assignment is a
	// session that refuses to start.
	for _, o := range h.Codex.ConfigOverrides {
		key, _, ok := strings.Cut(o, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf("harnesses.codex.config_overrides: %q is not a key=value setting", o)
		}
	}
	return nil
}
