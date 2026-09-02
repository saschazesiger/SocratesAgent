package config

import "testing"

func TestNormalizeFillsDefaults(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.OpenRouter.BaseURL != DefaultOpenRouterBaseURL {
		t.Errorf("base url = %q", s.OpenRouter.BaseURL)
	}
	// OpenRouter transcribes and writes titles and does nothing else, so the
	// two models it still has are the two that must always be filled in.
	if s.OpenRouter.TranscribeModel != DefaultTranscribeModel {
		t.Errorf("transcribe model = %q", s.OpenRouter.TranscribeModel)
	}
	if s.OpenRouter.TitleModel != DefaultTitleModel {
		t.Errorf("title model = %q", s.OpenRouter.TitleModel)
	}
	if s.Workspace.Root == "" {
		t.Error("workspace root was left empty")
	}
	if s.Workspace.DefaultHarness != HarnessClaude || !IsHarness(s.Workspace.DefaultHarness) {
		t.Errorf("default harness = %q", s.Workspace.DefaultHarness)
	}
	// A terminal with no scrollback and no font size is a form that was never
	// filled in, not a terminal anybody wants.
	if s.Terminal.WindowSize != "manual" || s.Terminal.Scrollback == 0 || s.Terminal.FontSize == 0 || s.Terminal.HistoryLimit == 0 {
		t.Errorf("terminal defaults = %#v", s.Terminal)
	}
	// Reading an answer out loud takes no configuration at all now, so the
	// voice section is a language and a pace and nothing else.
	if s.Voice.Language != DefaultLanguage || s.Voice.TTSRate != 1 {
		t.Errorf("voice defaults = %#v", s.Voice)
	}
}

// A fresh installation offers all four session kinds. Whether one of them is
// actually on this machine is not a setting - discovery reports that - so the
// default is on and the dashboard is where it is switched off.
func TestHarnessesDefaultToEnabled(t *testing.T) {
	s := Default()
	for _, id := range KnownHarnesses {
		entry, ok := s.Harnesses.Entry(id)
		if !ok {
			t.Fatalf("%s is not a known harness", id)
		}
		if !entry.Enabled {
			t.Errorf("%s is off by default", id)
		}
		if entry.Binary != "" || len(entry.ExtraArgs) != 0 || len(entry.ExtraEnv) != 0 {
			t.Errorf("%s ships with an override: %#v", id, entry)
		}
	}
	if _, ok := s.Harnesses.Entry("invented"); ok {
		t.Error("a harness nobody ships was reported as known")
	}
	// The switches that make these programs behave like a web terminal are on
	// out of the box; nothing else has to be configured to get a usable screen.
	if !s.Harnesses.Shell.Login || !s.Harnesses.Claude.PinLightTheme ||
		!s.Harnesses.Codex.TrustWorkdir || !s.Harnesses.Codex.NoAltScreen ||
		!s.Harnesses.OpenCode.DisableModelsFetch {
		t.Errorf("a terminal default is off: %#v", s.Harnesses)
	}
}

// Normalize only tidies the harness section - it does not fill it in. An
// upgraded installation keeps its switches anyway, because the server decodes
// its stored document into config.Default() rather than into a zero value.
func TestHarnessNormalizeTidiesWithoutFillingIn(t *testing.T) {
	var s Settings
	s.Normalize()
	if s.Harnesses.Claude.Enabled {
		t.Error("Normalize turned a switch on by itself")
	}
	s.Harnesses.Claude.Binary = "  /usr/local/bin/claude  "
	s.Harnesses.Claude.ExtraArgs = []string{" --debug ", "  "}
	s.Harnesses.Claude.ExtraEnv = []string{" FOO=bar baz ", "=orphan", "not an assignment", "1BAD=x"}
	s.Normalize()
	if s.Harnesses.Claude.Binary != "/usr/local/bin/claude" {
		t.Errorf("binary was not trimmed: %q", s.Harnesses.Claude.Binary)
	}
	if len(s.Harnesses.Claude.ExtraArgs) != 1 || s.Harnesses.Claude.ExtraArgs[0] != "--debug" {
		t.Errorf("extra args were not trimmed: %#v", s.Harnesses.Claude.ExtraArgs)
	}
	// A variable with no usable name cannot be set, and a launch is the wrong
	// place to find that out. The value keeps its space: it may be the point.
	if len(s.Harnesses.Claude.ExtraEnv) != 1 || s.Harnesses.Claude.ExtraEnv[0] != "FOO=bar baz" {
		t.Errorf("extra env = %#v", s.Harnesses.Claude.ExtraEnv)
	}
}

// The closed lists in the dashboard are only closed if the server says so too:
// the API takes whatever is sent to it.
func TestHarnessClosedListsFallBack(t *testing.T) {
	s := Default()
	s.Harnesses.Claude.PermissionMode = "whatever"
	s.Harnesses.Claude.SkipPermissions = "yes please"
	s.Harnesses.Claude.CleanupPeriodDays = 0
	s.Harnesses.Codex.Sandbox = "off"
	s.Harnesses.Codex.Approval = "untrusted"
	s.Harnesses.Codex.DefaultEffort = "ludicrous"
	s.Harnesses.OpenCode.Share = "everywhere"
	s.Harnesses.OpenCode.LogLevel = "debug"
	s.Terminal.WindowSize = "whatever"
	s.Normalize()
	if s.Harnesses.Claude.PermissionMode != "unset" || s.Harnesses.Claude.SkipPermissions != SkipPermissionsOff {
		t.Errorf("claude permissions = %#v", s.Harnesses.Claude)
	}
	if s.Harnesses.Claude.CleanupPeriodDays != 90 {
		t.Errorf("cleanup period = %d", s.Harnesses.Claude.CleanupPeriodDays)
	}
	if s.Harnesses.Codex.Sandbox != "workspace-write" || s.Harnesses.Codex.Approval != "on-request" {
		t.Errorf("codex sandbox = %#v", s.Harnesses.Codex)
	}
	// Codex does not validate the effort itself, so a typo would reach the
	// model as configuration and be quietly ignored.
	if s.Harnesses.Codex.DefaultEffort != "" {
		t.Errorf("codex effort = %q", s.Harnesses.Codex.DefaultEffort)
	}
	if s.Harnesses.OpenCode.Share != "disabled" {
		t.Errorf("share = %q", s.Harnesses.OpenCode.Share)
	}
	// A level written in lower case is a spelling, not a different value.
	if s.Harnesses.OpenCode.LogLevel != "DEBUG" {
		t.Errorf("log level = %q", s.Harnesses.OpenCode.LogLevel)
	}
	if s.Terminal.WindowSize != "manual" {
		t.Errorf("window size = %q", s.Terminal.WindowSize)
	}
}

// A preset with no path is not a place; one with no label is named after the
// directory it points at, so the picker never shows a blank row.
func TestWorkspacePresetsAreTidied(t *testing.T) {
	s := Default()
	s.Workspace.Presets = []PresetDir{
		{Path: " /srv/socrates "},
		{Label: "  ", Path: ""},
		{Label: "Repo", Path: "/srv/repo"},
		{Label: "Again", Path: "/srv/repo"},
	}
	s.Normalize()
	if len(s.Workspace.Presets) != 2 {
		t.Fatalf("presets = %#v", s.Workspace.Presets)
	}
	if s.Workspace.Presets[0] != (PresetDir{Label: "socrates", Path: "/srv/socrates"}) {
		t.Errorf("first preset = %#v", s.Workspace.Presets[0])
	}
	if s.Workspace.Presets[1].Label != "Repo" {
		t.Errorf("second preset = %#v", s.Workspace.Presets[1])
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
func TestNormalizeDefaultsTheLanguage(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.Voice.Language != DefaultLanguage {
		t.Fatalf("language = %q", s.Voice.Language)
	}
	if LanguageName(s.Voice.Language) != "English" {
		t.Fatalf("default = %q", LanguageName(s.Voice.Language))
	}
}

// There are two languages and no third state, so everything else has to land
// on one of them.
func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"de":       LanguageDE,
		"DE":       LanguageDE,
		" de-CH ":  LanguageDE,
		"de_DE":    LanguageDE,
		"en":       LanguageEN,
		"en-GB":    LanguageEN,
		"":         DefaultLanguage,
		"fr":       DefaultLanguage,
		"deutsch":  DefaultLanguage,
		"-":        DefaultLanguage,
		"nonsense": DefaultLanguage,
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Errorf("NormalizeLanguage(%q) = %q, want %q", in, got, want)
		}
		// Every language a settings document can hold has a name, because it
		// goes out to a model unchecked.
		if LanguageName(in) == "" {
			t.Errorf("%q has no name", in)
		}
	}
}

// The model short list keeps the order a person arranged, drops repeats and
// blanks, and maps every effort onto a level the harnesses understand.
func TestModelPicksAreNormalized(t *testing.T) {
	var s Settings
	s.Normalize()
	s.Harnesses.OpenCode.Models = []ModelPick{
		{ID: " openai|gpt-5.6 ", Effort: "HIGH"},
		{ID: "", Effort: "low"},
		{ID: "opencode|big-pickle", Effort: "bogus"},
		{ID: "openai|gpt-5.6", Effort: "low"},
	}
	s.Normalize()
	got := s.Harnesses.OpenCode.Models
	if len(got) != 2 {
		t.Fatalf("picks = %#v", got)
	}
	if got[0] != (ModelPick{ID: "openai|gpt-5.6", Effort: EffortHigh}) {
		t.Errorf("first pick = %#v", got[0])
	}
	if got[1] != (ModelPick{ID: "opencode|big-pickle", Effort: EffortDefault}) {
		t.Errorf("second pick = %#v", got[1])
	}
	if s.Harnesses.Claude.Models != nil {
		t.Errorf("an empty list became %#v", s.Harnesses.Claude.Models)
	}
}

// The four textarea fields are JSON a person types, and Normalize cannot
// repair them. Saving is where a typo has to be refused, so that it is an
// error next to the field rather than a session that fails to launch an hour
// later with a message from a program the person never saw.
func TestValidateRefusesWhatNormalizeCannotRepair(t *testing.T) {
	base := func() Settings {
		s := Default()
		s.Normalize()
		return s
	}
	fresh := base()
	if err := fresh.Validate(); err != nil {
		t.Fatalf("a fresh installation does not validate: %v", err)
	}
	cases := map[string]func(*Settings){
		"claude.settings_overrides": func(s *Settings) { s.Harnesses.Claude.SettingsOverrides = "{not json" },
		"opencode.permission_json":  func(s *Settings) { s.Harnesses.OpenCode.PermissionJSON = `{"bash":}` },
		"opencode.config_content":   func(s *Settings) { s.Harnesses.OpenCode.ConfigContent = "nope" },
		"opencode.tui_config":       func(s *Settings) { s.Harnesses.OpenCode.TUIConfig = "[" },
		// Every override reaches Codex as one -c argument, and Codex is
		// launched with --strict-config: an entry that is not an assignment is
		// a session that refuses to start.
		"codex.config_overrides": func(s *Settings) {
			s.Harnesses.Codex.ConfigOverrides = []string{"tui.theme=\"light-gray\"", "not-an-assignment"}
		},
	}
	for name, break_ := range cases {
		s := base()
		break_(&s)
		s.Normalize()
		if err := s.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// Valid JSON and a proper assignment go through untouched.
	s := base()
	s.Harnesses.Claude.SettingsOverrides = `{"cleanupPeriodDays": 5}`
	s.Harnesses.OpenCode.PermissionJSON = `{"*":"ask"}`
	s.Harnesses.Codex.ConfigOverrides = []string{`tui.theme="light-gray"`}
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Errorf("a valid document was refused: %v", err)
	}
}

// The two fields WP1 left to WP8's controls. Both are free text in the
// dashboard, so both are refused here rather than at launch, where the message
// would arrive in a pane that is already gone.
func TestAutocompactAndSettingSourcesAreValidated(t *testing.T) {
	for _, ok := range []string{"", "auto", "AUTO", "100k", "200K", "1M", "0.5M"} {
		s := Default()
		s.Harnesses.Claude.Autocompact = ok
		s.Normalize()
		if err := s.Validate(); err != nil {
			t.Errorf("autocompact %q was refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"lots", "99k", "2M", "200", "200 k", "-1k"} {
		s := Default()
		s.Harnesses.Claude.Autocompact = bad
		s.Normalize()
		if err := s.Validate(); err == nil {
			t.Errorf("autocompact %q was accepted", bad)
		}
	}

	s := Default()
	s.Harnesses.Claude.SettingSources = []string{"user", "project", "local"}
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Errorf("the three real setting sources were refused: %v", err)
	}
	s.Harnesses.Claude.SettingSources = []string{"user", "global"}
	s.Normalize()
	if err := s.Validate(); err == nil {
		t.Error("an invented setting source was accepted")
	}
}

// The Status button and the operator loop each pick their own model, and
// neither may be left without one: an empty field is a form nobody filled in,
// and a button that answers "pick a model first" is a setup step nobody asked
// for.
func TestAssistModelsAndAgentBoundsAreDefaulted(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.OpenRouter.StatusModel != DefaultStatusModel {
		t.Errorf("status model = %q", s.OpenRouter.StatusModel)
	}
	if s.OpenRouter.AgentModel != DefaultAgentModel {
		t.Errorf("agent model = %q", s.OpenRouter.AgentModel)
	}
	// Zero steps is an empty number field, not a run that may take none.
	if s.Agent.MaxSteps != DefaultAgentMaxSteps {
		t.Errorf("max steps = %d", s.Agent.MaxSteps)
	}
	// A fresh installation is one person on one machine, so the operator may
	// drive a shell until somebody says otherwise.
	if !Default().Agent.AllowShell {
		t.Error("the agent may not drive a shell out of the box")
	}

	// A chosen model is kept, and the ceiling is a ceiling.
	s = Settings{}
	s.OpenRouter.StatusModel, s.OpenRouter.AgentModel = "someone/cheap", "someone/careful"
	s.Agent.MaxSteps = 400
	s.Normalize()
	if s.OpenRouter.StatusModel != "someone/cheap" || s.OpenRouter.AgentModel != "someone/careful" {
		t.Errorf("a chosen model was replaced: %#v", s.OpenRouter)
	}
	if s.Agent.MaxSteps != MaxAgentMaxSteps {
		t.Errorf("max steps = %d, want the ceiling", s.Agent.MaxSteps)
	}

	// And "one step, then stop" is a legitimate thing to ask for.
	s = Settings{}
	s.Agent.MaxSteps = 1
	s.Normalize()
	if s.Agent.MaxSteps != 1 {
		t.Errorf("max steps = %d", s.Agent.MaxSteps)
	}
}
