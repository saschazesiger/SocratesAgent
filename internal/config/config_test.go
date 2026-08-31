package config

import (
	"encoding/json"
	"regexp"
	"strings"
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
	if len(s.Skills) != len(Presets()) {
		t.Errorf("expected the shipped presets, got %d skills", len(s.Skills))
	}
	if s.Voice.TTSProvider != "browser" || s.Voice.STTProvider != "openrouter" {
		t.Errorf("voice defaults = %#v", s.Voice)
	}
}

func TestNormalizeSanitisesSkills(t *testing.T) {
	s := Settings{Skills: []Skill{{Name: "My Agent!", IdleSeconds: -4, TimeoutSeconds: -1}}}
	s.Normalize()
	skill := s.Skills[0]
	if skill.ID != "my-agent" {
		t.Errorf("id = %q", skill.ID)
	}
	if skill.Command != "my-agent" {
		t.Errorf("a skill without a command should fall back to its id, got %q", skill.Command)
	}
	if skill.IdleSeconds <= 0 || skill.TimeoutSeconds <= 0 {
		t.Errorf("idle = %d, timeout = %d", skill.IdleSeconds, skill.TimeoutSeconds)
	}
	if skill.SkipPermissions {
		t.Error("a skill with no skip arguments cannot skip permissions")
	}
}

// A skill that does not say how it may be used is interactive only, because
// that is the whole point of the app: the user watches the terminal.
func TestNormalizeDefaultsToInteractiveOnly(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"skills":[{"id":"htop","command":"htop"}]}`), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	if !s.Skills[0].Interactive() {
		t.Error("a skill without interactive_only should count as interactive only")
	}
	if s.Skills[0].InteractiveOnly == nil {
		t.Error("normalisation should write the field out, so the dashboard sees a real value")
	}

	var off Settings
	if err := json.Unmarshal([]byte(`{"skills":[{"id":"htop","command":"htop","interactive_only":false}]}`), &off); err != nil {
		t.Fatal(err)
	}
	off.Normalize()
	if off.Skills[0].Interactive() {
		t.Error("an explicit false should survive normalisation")
	}
}

func TestNormalizeMakesSkillIDsUnique(t *testing.T) {
	s := Settings{Skills: []Skill{
		{ID: "claude", Name: "One", Command: "claude"},
		{ID: "claude", Name: "Two", Command: "claude"},
		{Name: "Claude", Command: "claude"},
	}}
	s.Normalize()
	seen := map[string]bool{}
	for _, skill := range s.Skills {
		if seen[skill.ID] {
			t.Fatalf("duplicate skill id %q survived normalisation", skill.ID)
		}
		seen[skill.ID] = true
	}
}

func TestSkillCommandLine(t *testing.T) {
	skill := Skill{
		Command: "claude", Args: []string{"--verbose"},
		Model: "sonnet", ModelFlag: "--model",
		SkipPermissions: true,
		SkipArgs:        []string{"--dangerously-skip-permissions"},
		AskArgs:         []string{"--ask"},
	}
	command, args := skill.CommandLine()
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

	skill.SkipPermissions = false
	if _, args := skill.CommandLine(); args[1] != "--ask" {
		t.Errorf("with permissions on, the ask arguments should be used: %v", args)
	}

	skill.Model = ""
	if _, args := skill.CommandLine(); len(args) != 2 {
		t.Errorf("an empty model should add no flag: %v", args)
	}
}

// An installation from before skills existed keeps its tools, and the one free
// text field it had becomes the notes section of the new manual.
func TestNormalizeMigratesOldTools(t *testing.T) {
	var s Settings
	raw := `{"tools":[
		{"id":"claude","name":"Claude Code","enabled":true,"command":"claude","description":"my own words",
		 "driving":"press enter twice","skip_permissions":true,"skip_permission_args":["--dangerously-skip-permissions"],
		 "timeout_seconds":900},
		{"id":"mine","name":"Mine","enabled":false,"command":"mine","driving":"just type"}
	]}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	if s.Tools != nil {
		t.Error("the legacy list should be cleared after migration")
	}
	if len(s.Skills) != 2 {
		t.Fatalf("migrated %d skills, want 2", len(s.Skills))
	}
	claude, ok := s.Skill("claude")
	if !ok {
		t.Fatal("claude did not survive the migration")
	}
	if claude.Notes != "press enter twice" {
		t.Errorf("driving should have become the notes section, got %q", claude.Notes)
	}
	if claude.Description != "my own words" || claude.TimeoutSeconds != 900 || !claude.Enabled {
		t.Errorf("the user's own settings were lost: %#v", claude)
	}
	if claude.Preset != "claude" {
		t.Errorf("a tool whose id matches a preset should be marked as coming from it, got %q", claude.Preset)
	}
	if claude.Startup == "" || claude.Answering == "" {
		t.Error("the empty manual sections should have been filled from the preset")
	}
	if !claude.Interactive() {
		t.Error("a migrated tool has to stay interactive only")
	}
	mine, _ := s.Skill("mine")
	if mine.Preset != "" {
		t.Errorf("a program of the user's own is not a preset, got %q", mine.Preset)
	}
	if mine.Startup != "" {
		t.Errorf("nothing should be invented for an unknown program, got %q", mine.Startup)
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
	if len(s.Skills) != 2 {
		t.Fatalf("migrated %d skills, want 2", len(s.Skills))
	}
	claude, ok := s.Skill("claude")
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
	if len(claude.SkipArgs) == 0 || claude.Startup == "" || claude.Answering == "" {
		t.Error("the migrated skill did not pick up how to drive the program")
	}
	codex, _ := s.Skill("codex")
	if codex.SkipPermissions {
		t.Error("an agent that asked for approval should keep asking")
	}
}

// A list that already has skills is the user's. New presets are offered in the
// dashboard rather than appearing behind their back.
func TestNormalizeDoesNotAddPresetsToAnExistingList(t *testing.T) {
	s := Settings{Skills: []Skill{{ID: "mine", Name: "Mine", Command: "mine"}}}
	s.Normalize()
	if len(s.Skills) != 1 || s.Skills[0].ID != "mine" {
		t.Fatalf("skills = %#v", s.Skills)
	}
}

// Removing every skill is a decision, not a blank slate: the presets must not
// come back on the next save.
func TestNormalizeKeepsAnEmptiedSkillList(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"skills":[]}`), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	if len(s.Skills) != 0 {
		t.Fatalf("an empty list was refilled: %#v", s.Skills)
	}
	// And it has to survive being written out and read back, which is how the
	// list actually reaches the next start.
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"skills":[]`) {
		t.Fatalf("an emptied list did not serialise as []: %s", raw)
	}
	var back Settings
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.Normalize()
	if len(back.Skills) != 0 {
		t.Fatalf("the presets came back after a round trip: %#v", back.Skills)
	}

	// A document that has never heard of skills is the other case, and it does
	// get them.
	var fresh Settings
	if err := json.Unmarshal([]byte(`{}`), &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Normalize()
	if len(fresh.Skills) != len(Presets()) {
		t.Fatalf("a settings file without a skills key should be seeded, got %#v", fresh.Skills)
	}
}

// A program installed somewhere other than the PATH is still that program, so
// an absolute path has to find its preset.
func TestMigrationRecognisesAnAbsolutePathCommand(t *testing.T) {
	var s Settings
	raw := `{"tools":[{"id":"implementer","name":"Implementer","enabled":true,
		"command":"/opt/bin/claude","driving":"press enter"}]}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	skill := s.Skills[0]
	if skill.Preset != "claude" {
		t.Errorf("preset = %q, want claude", skill.Preset)
	}
	if skill.Startup == "" || skill.Answering == "" {
		t.Error("a renamed Claude Code did not pick up the manual")
	}
	if len(skill.Env) != 1 || skill.Env[0] != sandboxEnv {
		t.Errorf("env = %#v, want %q", skill.Env, sandboxEnv)
	}
}

// Every pattern a preset hands the orchestrator has to be something Go can
// actually compile, or the model is sent to look for a match that can never
// happen.
func TestPresetPatternsCompile(t *testing.T) {
	pattern := regexp.MustCompile("`([^`\n]+)`")
	for _, p := range Presets() {
		for _, section := range []string{p.Startup, p.GivingTasks, p.ReadingState, p.Answering, p.Exiting, p.Notes} {
			for _, m := range pattern.FindAllStringSubmatch(section, -1) {
				candidate := m[1]
				// Only the quoted pieces that are meant as expressions: a flag
				// or a command name is not one.
				if !strings.ContainsAny(candidate, `\^$[](){}?*+|`) {
					continue
				}
				if _, err := regexp.Compile(candidate); err != nil {
					t.Errorf("%s offers a pattern Go cannot compile: %s (%v)", p.ID, candidate, err)
				}
			}
		}
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

func TestEnabledSkills(t *testing.T) {
	s := Default()
	s.Skills[0].Enabled = false
	for _, skill := range s.EnabledSkills() {
		if !skill.Enabled {
			t.Fatalf("disabled skill leaked: %#v", skill)
		}
	}
	if _, ok := s.Skill("claude"); !ok {
		t.Error("Skill() should find disabled skills too")
	}
	if _, ok := s.Skill("ghost"); ok {
		t.Error("unknown id should not resolve")
	}
}

// The presets are the app's answer to "how do I drive this program". A gap in
// one of them is a gap in what the orchestrator knows.
func TestPresetsAreDrivable(t *testing.T) {
	want := map[string]bool{"claude": false, "codex": false, "opencode": false}
	for _, p := range Presets() {
		if _, ok := want[p.ID]; !ok {
			t.Errorf("unexpected preset id %q - ids are stable, an installation refers to them", p.ID)
			continue
		}
		want[p.ID] = true
		if p.Preset != p.ID {
			t.Errorf("%s should point at itself as its preset, got %q", p.ID, p.Preset)
		}
		for name, section := range map[string]string{
			"description":    p.Description,
			"startup":        p.Startup,
			"giving tasks":   p.GivingTasks,
			"reading state":  p.ReadingState,
			"answering":      p.Answering,
			"exiting":        p.Exiting,
			"notes":          p.Notes,
			"headless forms": p.HeadlessForms,
		} {
			if strings.TrimSpace(section) == "" {
				t.Errorf("%s has no %s section", p.ID, name)
			}
		}
		if !p.Interactive() {
			t.Errorf("%s should be interactive only", p.ID)
		}
		if !p.SkipPermissions || len(p.SkipArgs) == 0 {
			t.Errorf("%s should run unattended by default", p.ID)
		}
		if p.Idle() <= 0 || p.Timeout() <= 0 {
			t.Errorf("%s has no usable timing: %#v", p.ID, p)
		}
		if _, ok := PresetByID(p.ID); !ok {
			t.Errorf("PresetByID does not find %q", p.ID)
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("the %s preset is missing", id)
		}
	}
}

// The exact flags matter: they were verified against the installed versions,
// and getting one wrong means the program refuses to start.
func TestPresetFlags(t *testing.T) {
	claude, _ := PresetByID("claude")
	if got := strings.Join(claude.SkipArgs, " "); got != "--dangerously-skip-permissions" {
		t.Errorf("claude skip args = %q", got)
	}
	if got := strings.Join(claude.AskArgs, " "); got != "--permission-mode manual" {
		t.Errorf("claude ask args = %q - without it a session starts in auto mode", got)
	}
	codex, _ := PresetByID("codex")
	if got := strings.Join(codex.SkipArgs, " "); got != "--dangerously-bypass-approvals-and-sandbox" {
		t.Errorf("codex skip args = %q - --full-auto does not exist in 0.146.0", got)
	}
	if got := strings.Join(codex.Args, " "); got != "--no-alt-screen" {
		t.Errorf("codex base args = %q", got)
	}
	opencode, _ := PresetByID("opencode")
	if got := strings.Join(opencode.SkipArgs, " "); got != "--auto" {
		t.Errorf("opencode skip args = %q", got)
	}
	if len(opencode.AskArgs) != 0 {
		t.Errorf("opencode asks by default, with no flag at all: %v", opencode.AskArgs)
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
	claude, ok := PresetByID("claude")
	if !ok {
		t.Fatal("no claude preset")
	}
	if len(claude.Env) != 1 || claude.Env[0] != sandboxEnv {
		t.Fatalf("claude env = %#v, want %q - it refuses to skip permissions as root without it", claude.Env, sandboxEnv)
	}
}

func TestNormalizeAddsTheSandboxEnvironmentToAnOlderSettingsFile(t *testing.T) {
	// A settings document written before skills had an environment: env is
	// missing entirely, which unmarshals to nil.
	s := Settings{Skills: []Skill{
		{ID: "claude", Name: "Claude Code", Command: "claude", SkipPermissions: true,
			SkipArgs: []string{"--dangerously-skip-permissions"}},
		{ID: "codex", Name: "Codex", Command: "codex"},
		{ID: "mine", Name: "Mine", Command: "mine"},
	}}
	s.Normalize()
	if got := s.Skills[0].Env; len(got) != 1 || got[0] != sandboxEnv {
		t.Errorf("claude env after upgrade = %#v, want %q", got, sandboxEnv)
	}
	if got := s.Skills[1].Env; len(got) != 0 {
		t.Errorf("codex env = %#v, want none", got)
	}
	if got := s.Skills[2].Env; got == nil || len(got) != 0 {
		t.Errorf("an unknown skill should end up with an empty environment, got %#v", got)
	}
}

func TestNormalizeKeepsAnEmptiedEnvironment(t *testing.T) {
	// The dashboard sends an empty list once the field has been cleared, and
	// that choice has to survive a reload.
	s := Settings{Skills: []Skill{
		{ID: "claude", Name: "Claude Code", Command: "claude", Env: []string{},
			SkipPermissions: true, SkipArgs: []string{"--dangerously-skip-permissions"}},
	}}
	s.Normalize()
	if len(s.Skills[0].Env) != 0 {
		t.Errorf("env = %#v, want it left empty", s.Skills[0].Env)
	}
}

func TestSettingsRoundTripKeepsAnEmptiedEnvironment(t *testing.T) {
	s := Settings{Skills: []Skill{{ID: "claude", Command: "claude", Env: []string{}}}}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Settings
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.Normalize()
	if len(back.Skills[0].Env) != 0 {
		t.Errorf("env = %#v after a round trip, want it left empty", back.Skills[0].Env)
	}
}

func TestNormalizeDefaultsTheLanguage(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.Voice.Language != DefaultLanguage {
		t.Fatalf("language = %q", s.Voice.Language)
	}
	if LanguageName(s.Voice.Language) != "English" || LanguageTag(s.Voice.Language) != "en-US" {
		t.Fatalf("default = %q / %q", LanguageName(s.Voice.Language), LanguageTag(s.Voice.Language))
	}
}

// The language used to live on the playback side only. An installation that
// set it there keeps its choice now that one setting covers the whole
// conversation.
func TestNormalizeMigratesTheOldPlaybackLanguage(t *testing.T) {
	s := Settings{}
	s.Voice.TTSLanguage = "de-DE"
	s.Normalize()
	if s.Voice.Language != LanguageDE {
		t.Fatalf("language = %q", s.Voice.Language)
	}
	if s.Voice.TTSLanguage != "" {
		t.Fatalf("the old field was kept: %q", s.Voice.TTSLanguage)
	}
	if LanguageName(s.Voice.Language) != "German" || LanguageTag(s.Voice.Language) != "de-DE" {
		t.Fatalf("german = %q / %q", LanguageName(s.Voice.Language), LanguageTag(s.Voice.Language))
	}
}

// There are two languages and no third state, so everything else - including
// the "auto" an older version wrote - has to land on one of them.
func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"de":       LanguageDE,
		"DE":       LanguageDE,
		" de-CH ":  LanguageDE,
		"de_DE":    LanguageDE,
		"en":       LanguageEN,
		"en-GB":    LanguageEN,
		"":         DefaultLanguage,
		"auto":     DefaultLanguage,
		"fr":       DefaultLanguage,
		"deutsch":  DefaultLanguage,
		"-":        DefaultLanguage,
		"nonsense": DefaultLanguage,
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Errorf("NormalizeLanguage(%q) = %q, want %q", in, got, want)
		}
		// Every language a settings document can hold has a name and a voice
		// tag, because both go out to a model or a synthesiser unchecked.
		if LanguageName(in) == "" || LanguageTag(in) == "" {
			t.Errorf("%q has no name or tag", in)
		}
	}
}

func TestNormalizeInternetDefaults(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.Internet.Enabled {
		t.Error("internet access is on for a fresh settings document")
	}
	if s.Internet.SearchProvider != SearchOpenRouter {
		t.Errorf("search provider = %q", s.Internet.SearchProvider)
	}
	if s.Internet.FetchEngine != FetchLocal {
		t.Errorf("fetch engine = %q", s.Internet.FetchEngine)
	}
	if s.Internet.MaxResults != DefaultSearchResults {
		t.Errorf("max results = %d", s.Internet.MaxResults)
	}
}

func TestNormalizeInternetClampsAndFallsBack(t *testing.T) {
	s := Settings{}
	s.Internet = InternetSettings{
		SearchProvider: "bing",
		FetchEngine:    "wget",
		MaxResults:     99,
		TavilyAPIKey:   "  tvly-key  ",
		SearchBaseURL:  " https://mirror.example/ ",
	}
	s.Normalize()
	if s.Internet.SearchProvider != SearchOpenRouter {
		t.Errorf("an unknown provider became %q", s.Internet.SearchProvider)
	}
	if s.Internet.FetchEngine != FetchLocal {
		t.Errorf("an unknown engine became %q", s.Internet.FetchEngine)
	}
	if s.Internet.MaxResults != 10 {
		t.Errorf("max results = %d, wanted the cap of 10", s.Internet.MaxResults)
	}
	if s.Internet.TavilyAPIKey != "tvly-key" {
		t.Errorf("the key was not trimmed: %q", s.Internet.TavilyAPIKey)
	}
	if s.Internet.SearchBaseURL != "https://mirror.example" {
		t.Errorf("the base URL was not tidied: %q", s.Internet.SearchBaseURL)
	}

	s.Internet.MaxResults = 0
	s.Internet.SearchProvider = SearchJina
	s.Internet.FetchEngine = FetchJina
	s.Normalize()
	if s.Internet.MaxResults != DefaultSearchResults {
		t.Errorf("zero results became %d", s.Internet.MaxResults)
	}
	if s.Internet.SearchProvider != SearchJina || s.Internet.FetchEngine != FetchJina {
		t.Errorf("a valid choice was overwritten: %q / %q", s.Internet.SearchProvider, s.Internet.FetchEngine)
	}
}

func TestSearchAndFetchEndpointsFallBackToTheProviders(t *testing.T) {
	tavily := InternetSettings{SearchProvider: SearchTavily}
	if tavily.SearchEndpoint() != DefaultTavilyBaseURL {
		t.Errorf("tavily endpoint = %q", tavily.SearchEndpoint())
	}
	jina := InternetSettings{SearchProvider: SearchJina}
	if jina.SearchEndpoint() != DefaultJinaSearchBaseURL {
		t.Errorf("jina endpoint = %q", jina.SearchEndpoint())
	}
	if jina.FetchEndpoint() != DefaultJinaReaderBaseURL {
		t.Errorf("reader endpoint = %q", jina.FetchEndpoint())
	}
	override := InternetSettings{SearchProvider: SearchTavily, SearchBaseURL: "http://127.0.0.1:9999"}
	if override.SearchEndpoint() != "http://127.0.0.1:9999" {
		t.Errorf("the override was ignored: %q", override.SearchEndpoint())
	}
}

// An installation that has been running a while stores a copy of whatever the
// default prompt said the day it first opened the dashboard. Those copies name
// tools that no longer exist, and a model told about a tool it does not have
// spends its turn trying to call it. Each one is matched whole, against the
// real text this project shipped.
func TestNormalizeReplacesAStaleShippedPrompt(t *testing.T) {
	if len(retiredDefaultPrompts) < 3 {
		t.Fatalf("only %d retired prompts are listed", len(retiredDefaultPrompts))
	}
	for i, retired := range retiredDefaultPrompts {
		s := Settings{}
		s.Agent.SystemPrompt = retired
		s.Normalize()
		if s.Agent.SystemPrompt != DefaultSystemPrompt {
			t.Errorf("retired prompt %d survived:\n%s", i+1, s.Agent.SystemPrompt)
		}
	}

	// The same text after a round trip through a textarea: CRLF line endings
	// and a trailing newline. It is still the same document.
	roughed := strings.ReplaceAll(retiredDefaultPrompts[0], "\n", "\r\n") + "\r\n"
	s := Settings{}
	s.Agent.SystemPrompt = roughed
	s.Normalize()
	if s.Agent.SystemPrompt != DefaultSystemPrompt {
		t.Error("a retired prompt with CRLF line endings was not recognised")
	}
}

// The retired prompts have to be the real historical text, or the migration
// silently matches nothing on the installations it exists for.
func TestRetiredPromptsAreTheOnesThatShipped(t *testing.T) {
	if !strings.Contains(retiredDefaultPrompts[0], "delegate_to_agent") {
		t.Error("the first retired prompt is not the delegate_to_agent era one")
	}
	if !strings.Contains(retiredDefaultPrompts[1], "ask_user") ||
		strings.Contains(retiredDefaultPrompts[1], "delegate_to_agent") {
		t.Error("the second retired prompt is not the terminal driven, ask_user era one")
	}
	if !strings.Contains(retiredDefaultPrompts[2], "skills listed below") {
		t.Error("the third retired prompt is not the one written after skills arrived")
	}
	// The whole point of the list is prompts that describe tools that are
	// gone. One that does not is either a mistake or a prompt still in use,
	// and either way it would take away something a user might have meant.
	for i, retired := range retiredDefaultPrompts {
		if !strings.Contains(retired, "ask_user") && !strings.Contains(retired, "delegate_to_agent") {
			t.Errorf("retired prompt %d names no retired tool, so it does not belong on this list", i+1)
		}
	}
	for i, retired := range retiredDefaultPrompts {
		if retired == DefaultSystemPrompt {
			t.Errorf("retired prompt %d is the current default", i+1)
		}
	}
}

// A prompt somebody wrote themselves is theirs, however much it differs from
// the default - and mentioning a dead tool is not enough to take it away. This
// is the case the old substring match got wrong.
func TestNormalizeKeepsAHandWrittenPrompt(t *testing.T) {
	mine := []string{
		"You are Socrates. Answer in haiku and never open a terminal.",
		// Someone documenting their own history, or warning the model off:
		"You are Socrates. Never call ask_user or delegate_to_agent - they are gone.",
		// The retired prompt with one line of their own added is an edit, and
		// edits are kept.
		retiredDefaultPrompts[0] + "\n- Always answer in German.",
	}
	for _, prompt := range mine {
		s := Settings{}
		s.Agent.SystemPrompt = prompt
		s.Normalize()
		if s.Agent.SystemPrompt != prompt {
			t.Errorf("a hand written prompt was replaced:\nwanted %q\ngot    %q", prompt, s.Agent.SystemPrompt)
		}
	}
}

// The current default itself must survive being normalized, however often.
func TestNormalizeLeavesTheCurrentDefaultAlone(t *testing.T) {
	s := Default()
	for i := 0; i < 3; i++ {
		s.Normalize()
	}
	if s.Agent.SystemPrompt != DefaultSystemPrompt {
		t.Error("the current default prompt was rewritten")
	}
	for i, retired := range retiredDefaultPrompts {
		if samePrompt(DefaultSystemPrompt, retired) {
			t.Errorf("the current default equals retired prompt %d, which the migration would loop on", i+1)
		}
	}
}
