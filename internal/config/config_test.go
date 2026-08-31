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
		t.Errorf("expected one entry per shipped skill, got %d", len(s.Skills))
	}
	if s.Voice.TTSProvider != "browser" || s.Voice.STTProvider != "openrouter" {
		t.Errorf("voice defaults = %#v", s.Voice)
	}
}

func TestSkillCommandLine(t *testing.T) {
	skill := Skill{
		Command: "claude", Args: []string{"--verbose"},
		ModelArgs:       []string{"--model", "{model}"},
		EffortArgs:      []string{"--effort", "{effort}"},
		SkipPermissions: true,
		SkipArgs:        []string{"--dangerously-skip-permissions"},
		AskArgs:         []string{"--ask"},
		Models:          []ModelChoice{{ID: "sonnet", Effort: EffortMedium}},
	}
	command, args := skill.CommandLine(skill.DefaultModel())
	if command != "claude" {
		t.Errorf("command = %q", command)
	}
	want := []string{"--verbose", "--dangerously-skip-permissions", "--model", "sonnet", "--effort", "medium"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}

	skill.SkipPermissions = false
	if _, args := skill.CommandLine(skill.DefaultModel()); args[1] != "--ask" {
		t.Errorf("with permissions on, the ask arguments should be used: %v", args)
	}

	// No choice at all means no word about models: the program starts exactly
	// the way it starts itself.
	if _, args := skill.CommandLine(ModelChoice{}); len(args) != 2 {
		t.Errorf("an empty choice should add no flag: %v", args)
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

// Remote Control would let a phone drive the same session from outside, and it
// has no CLI switch - only this settings key, which has to reach the program as
// one argv element, not as something a shell would have to unpick.
func TestClaudeStartsWithRemoteControlOff(t *testing.T) {
	claude, ok := PresetByID("claude")
	if !ok {
		t.Fatal("no claude preset")
	}
	_, args := claude.CommandLine(claude.DefaultModel())
	found := false
	for i, arg := range args {
		if arg != "--settings" {
			continue
		}
		if i+1 >= len(args) {
			t.Fatalf("--settings came without its JSON: %#v", args)
		}
		var settings map[string]any
		if err := json.Unmarshal([]byte(args[i+1]), &settings); err != nil {
			t.Fatalf("the --settings argument is not JSON (%v): %q", err, args[i+1])
		}
		if settings["disableRemoteControl"] != true {
			t.Errorf("--settings = %q, want disableRemoteControl true", args[i+1])
		}
		found = true
	}
	if !found {
		t.Errorf("claude args = %#v, want --settings with disableRemoteControl", args)
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

// Socrates used to have a web search and a page reader of its own, and every
// installation from that era has an "internet" object sitting in its settings
// document. Those keys mean nothing now, and a settings document must load
// around them rather than fail on them.
func TestSettingsFromTheInternetEraStillLoad(t *testing.T) {
	stored := []byte(`{
		"openrouter": {"chat_model": "some/model"},
		"internet": {
			"enabled": true,
			"search_provider": "tavily",
			"tavily_api_key": "tvly-key",
			"fetch_engine": "jina",
			"max_results": 7
		},
		"agent": {"max_iterations": 12}
	}`)
	var s Settings
	if err := json.Unmarshal(stored, &s); err != nil {
		t.Fatalf("a settings document with an internet key no longer loads: %v", err)
	}
	s.Normalize()
	if s.OpenRouter.ChatModel != "some/model" {
		t.Errorf("chat model = %q", s.OpenRouter.ChatModel)
	}
	if s.Agent.MaxIterations != 12 {
		t.Errorf("max iterations = %d", s.Agent.MaxIterations)
	}
}

// An installation that has been running a while stores a copy of whatever the
// default prompt said the day it first opened the dashboard. Those copies name
// tools that no longer exist, and a model told about a tool it does not have
// spends its turn trying to call it. Each one is matched whole, against the
// real text this project shipped.
func TestNormalizeReplacesAStaleShippedPrompt(t *testing.T) {
	if len(retiredDefaultPrompts) < 4 {
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
	if !strings.Contains(retiredDefaultPrompts[2], "skills listed below") ||
		!strings.Contains(retiredDefaultPrompts[2], "ask_user") {
		t.Error("the third retired prompt is not the one written after skills arrived")
	}
	if !strings.Contains(retiredDefaultPrompts[3], "You have an interactive shell") ||
		strings.Contains(retiredDefaultPrompts[3], "ask_user") {
		t.Error("the fourth retired prompt is not the one that still did the work itself")
	}
	// The whole point of the list is prompts that no longer fit the app: they
	// name tools that are gone, or they tell the orchestrator to do the work
	// itself. One that does neither is either a mistake or a prompt still in
	// use, and either way it would take away something a user might have meant.
	for i, retired := range retiredDefaultPrompts {
		namesADeadTool := strings.Contains(retired, "ask_user") ||
			strings.Contains(retired, "delegate_to_agent")
		doesTheWorkItself := strings.Contains(retired, "You have an interactive shell")
		if !namesADeadTool && !doesTheWorkItself {
			t.Errorf("retired prompt %d is neither wrong about the tools nor about the job, "+
				"so it does not belong on this list", i+1)
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

// The rule the whole app rests on: Socrates orchestrates and never does the
// work. A default prompt that has lost that sentence quietly turns it back
// into an agent that codes, which is the one thing it must not be.
func TestDefaultPromptIsOrchestratorOnly(t *testing.T) {
	for _, want := range []string{
		"you never do the work",
		"Verify by delegating the verification",
		"orchestration mechanics only",
		"shell_run",
		"reasoning\n  effort",
		"admin dashboard",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("the default prompt no longer says %q", want)
		}
	}
	if strings.Contains(DefaultSystemPrompt, "You have an interactive shell") {
		t.Error("the default prompt still offers the shell as the way to get work done")
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
