// Package config holds the runtime settings of the application. Settings are
// stored as a single JSON document in the database so that everything can be
// changed from the admin dashboard without restarting the server.
package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Defaults that are used on a fresh installation.
const (
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	DefaultChatModel         = "anthropic/claude-sonnet-4.5"
	DefaultTranscribeModel   = "google/gemini-2.5-flash"
	DefaultTitleModel        = "google/gemini-2.5-flash-lite"
	DefaultMaxIterations     = 24
)

// DefaultSystemPrompt is the instruction set of the top level agent. It is
// user editable in the admin dashboard.
const DefaultSystemPrompt = `You are Socrates, a top level orchestration agent.

You talk to the user in a natural, concise way, and you never do the work
yourself. Everything that counts as real work - reading a codebase, writing or
changing code, running a build or a test suite, researching an answer, fixing
what broke - belongs to one of the skills listed below: a coding agent you
start in a real terminal on the user's machine and drive the way a person
would. You decide what should happen, who should do it, and whether it is
really done. You are never the one who does it.

How you work:
- Choose the skill that fits the job, and choose the model and the reasoning
  effort it starts with. Both are real decisions. Every model below carries a
  sentence about when it is the right one: take a cheap model at low effort for
  a small chore, a strong model at high effort for work that is hard, subtle or
  expensive to get wrong, and read those sentences instead of always reaching
  for the same entry.
- Start the skill in a terminal session and drive it the way a person does:
  type the brief, press enter, watch the screen, answer whatever it asks, and
  wait until it is done. A skill is an ordinary program someone wrote the
  manual for, not a special case - read that manual before you touch the
  program.
- Read the screen before you type. If you cannot tell what a program wants,
  look at the screen again rather than guessing at a keypress.
- Give the agent a complete, self contained brief: it cannot see this
  conversation. Say what to do, where, what the finished result has to look
  like, and every constraint the user gave you, spelled out rather than
  referred to.
- Verify by delegating the verification. Ask the agent to run the tests, the
  build or the linter and to show you what came back, then read that output off
  the screen yourself. Never accept "it all passes" without it, and never go
  and run the tests yourself to find out.
- Keep going until the job is really done. If something is missing or wrong,
  say so in the terminal session and let the agent fix it.
- The shell (shell_run) is there for orchestration mechanics only: seeing
  whether a process is still alive, listing a directory so your brief can name
  the right paths, checking that a repository is where you think it is. It is
  never where the task gets done - no builds, no test runs, no edits, no
  reading through code to work an answer out. The moment you are tempted, open
  a terminal session and hand it over instead.
- The only things you answer without delegating are questions about this
  conversation and trivia about your own state: what you asked for, what is
  running, what the agent reported back. Anything that needs looking at the
  project itself goes to an agent, however small it seems.
- If no skill is enabled, or the one this job needs is not, say so and ask the
  user to enable it in the admin dashboard. Stepping in yourself is not the
  fallback.
- If something important is ambiguous, ask for it in your reply and end your
  turn. You have no way to interrupt yourself and wait: the person reads what
  you wrote and answers with their next message, which continues this
  conversation. Ask one clear question, name the concrete choices in a sentence,
  and do not start guessing work in the same turn.
- The final message you write is what the user sees and possibly hears. Make it
  self contained, friendly and to the point. Prefer short paragraphs over long
  bullet lists, and never mention the internal tool names.`

// Settings is the full configuration document.
type Settings struct {
	OpenRouter OpenRouterSettings `json:"openrouter"`
	Voice      VoiceSettings      `json:"voice"`
	Agent      AgentSettings      `json:"agent"`
	Tunnel     TunnelSettings     `json:"tunnel"`
	// Skills is what the user has decided about the programs Socrates ships
	// with: whether each one may be used, and what it should be used for.
	// Everything else about a skill - the command, its arguments, its manual -
	// is predefined by the app and lives in Presets.
	Skills []SkillSetting `json:"skills"`
	// Tools is the shape skills had before they were called skills. It is read
	// once, migrated into Skills and then dropped.
	Tools []legacyEntry `json:"tools,omitempty"`
	// Backends is the shape settings had before Socrates drove its programs
	// interactively. It is read once, migrated into Skills and then dropped.
	Backends []legacyEntry `json:"backends,omitempty"`
}

// Tunnel modes.
const (
	TunnelQuick = "quick" // free *.trycloudflare.com URL, no account needed
	TunnelToken = "token" // named tunnel, driven by a Zero Trust tunnel token
)

// TunnelSettings configures the managed Cloudflare tunnel. Socrates keeps
// listening locally either way; the tunnel simply publishes that local address.
type TunnelSettings struct {
	Enabled   bool     `json:"enabled"`
	Mode      string   `json:"mode"`
	Token     string   `json:"token"`
	Hostname  string   `json:"hostname"`
	Command   string   `json:"command"`
	ExtraArgs []string `json:"extra_args"`
}

// OpenRouterSettings configures access to the OpenRouter API, which powers the
// top level agent and (by default) speech to text.
type OpenRouterSettings struct {
	APIKey          string `json:"api_key"`
	BaseURL         string `json:"base_url"`
	ChatModel       string `json:"chat_model"`
	TranscribeModel string `json:"transcribe_model"`
	TitleModel      string `json:"title_model"`
}

// The languages Socrates speaks. There are two of them and there is no third
// option: no detection, no "whatever the browser thinks". One choice in the
// dashboard decides what every side of the conversation does.
const (
	LanguageEN = "en"
	LanguageDE = "de"

	// DefaultLanguage is what a fresh installation speaks.
	DefaultLanguage = LanguageEN
)

// spokenLanguage is one language the voice pipeline knows by name.
type spokenLanguage struct {
	// Name is the English name of the language, which is what goes into an
	// instruction to a model.
	Name string
	// Tag is the BCP 47 tag the browser's speech synthesiser matches voices
	// against.
	Tag string
}

var spokenLanguages = map[string]spokenLanguage{
	LanguageEN: {Name: "English", Tag: "en-US"},
	LanguageDE: {Name: "German", Tag: "de-DE"},
}

// LanguageName is the English name of a language, which is the form a model
// understands in an instruction: "English" or "German".
func LanguageName(code string) string { return spokenLanguages[NormalizeLanguage(code)].Name }

// LanguageTag is the BCP 47 tag the browser matches voices against.
func LanguageTag(code string) string { return spokenLanguages[NormalizeLanguage(code)].Tag }

// NormalizeLanguage maps whatever is in the settings document onto one of the
// two languages. A regional tag such as "de-CH" counts as its base language,
// and anything else - including an empty field and the "auto" that older
// versions wrote - becomes the default.
func NormalizeLanguage(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(c, "-_"); i > 0 {
		c = c[:i]
	}
	if _, ok := spokenLanguages[c]; ok {
		return c
	}
	return DefaultLanguage
}

// VoiceSettings configures speech to text and text to speech.
//
// OpenRouter itself only exposes chat completions, so transcription happens by
// sending the recorded audio to a multimodal chat model. Speech synthesis is
// done in the browser by default and can optionally be routed to any
// OpenAI compatible /audio/speech endpoint.
type VoiceSettings struct {
	// Language is the language Socrates speaks: "en" or "de". One setting
	// drives all three sides of it - which language the transcript is written
	// in, which voice reads the answer out loud, and which language the agent
	// writes that answer in - because a conversation where those three
	// disagree is worse than any of them being wrong alone.
	Language string `json:"language"`

	STTProvider string `json:"stt_provider"` // "openrouter" | "endpoint"
	STTBaseURL  string `json:"stt_base_url"`
	STTAPIKey   string `json:"stt_api_key"`
	STTModel    string `json:"stt_model"`
	STTPrompt   string `json:"stt_prompt"`

	TTSProvider string  `json:"tts_provider"` // "browser" | "endpoint"
	TTSBaseURL  string  `json:"tts_base_url"`
	TTSAPIKey   string  `json:"tts_api_key"`
	TTSModel    string  `json:"tts_model"`
	TTSVoice    string  `json:"tts_voice"`
	TTSRate     float64 `json:"tts_rate"`
	// TTSLanguage is where the language used to live, back when it only
	// applied to playback. It is read once, folded into Language and dropped.
	TTSLanguage string `json:"tts_language,omitempty"`

	SpeakInAutoMode bool `json:"speak_in_auto_mode"`
	SpeakInChatMode bool `json:"speak_in_chat_mode"`
}

// AgentSettings configures the orchestration loop.
type AgentSettings struct {
	SystemPrompt  string  `json:"system_prompt"`
	MaxIterations int     `json:"max_iterations"`
	Temperature   float64 `json:"temperature"`
	WorkspaceRoot string  `json:"workspace_root"`
}

// The reasoning effort levels Socrates offers. They are the three every
// harness that has an effort mechanism at all understands: Claude Code accepts
// low, medium, high, xhigh and max, Codex accepts those plus minimal and
// ultra, and OpenCode names its effort "variants" low, medium and high. The
// closed set is therefore the intersection, so that a level chosen in the
// dashboard can never be one the program refuses.
//
// EffortDefault - the empty string - means "do not say anything about it" and
// leaves the program on whatever it would have chosen for itself.
const (
	EffortDefault = ""
	EffortLow     = "low"
	EffortMedium  = "medium"
	EffortHigh    = "high"
)

// NormalizeEffort maps whatever is in the settings document onto one of the
// levels above. Anything else - a typo, a level only one program knows, an
// empty field - becomes the program's own default, which is always safe.
func NormalizeEffort(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case EffortLow:
		return EffortLow
	case EffortMedium:
		return EffortMedium
	case EffortHigh:
		return EffortHigh
	default:
		return EffortDefault
	}
}

// ModelChoice is one model a skill may be started with: the model's own name
// in the program's naming - "sonnet", "gpt-5.6-sol", "opencode/big-pickle",
// never an OpenRouter id - how hard it should think, and what the user says it
// is the right choice for.
//
// UseWhen is the whole point of the list. The orchestrator reads it and picks,
// the same way it picks a skill by its description, so it is written for a
// reader: "the hardest engineering work, when it is worth being slow".
type ModelChoice struct {
	ID      string `json:"id"`
	Effort  string `json:"effort,omitempty"`
	UseWhen string `json:"use_when,omitempty"`
}

// Label names a choice the way a line of prose would: the id, and the effort
// when there is one to mention.
func (m ModelChoice) Label() string {
	id := strings.TrimSpace(m.ID)
	if effort := NormalizeEffort(m.Effort); effort != "" {
		if id == "" {
			return "effort " + effort
		}
		return id + " (effort " + effort + ")"
	}
	return id
}

// Empty reports whether this choice says nothing at all, in which case the
// program is started exactly the way it starts itself.
func (m ModelChoice) Empty() bool {
	return strings.TrimSpace(m.ID) == "" && NormalizeEffort(m.Effort) == ""
}

// maxModelsPerSkill caps the list. Every entry is written into the system
// prompt and into the terminal_open description on every single model call, so
// a list that grew without a limit would be paid for in tokens forever. Twelve
// is far more than the three or four a person actually curates.
const maxModelsPerSkill = 12

// normalizeModels turns whatever the settings document says about a skill's
// models into a list that can be offered to the orchestrator: no empty ids, no
// two entries under the same id - the id is how a model is asked for, so it
// has to identify exactly one entry - and no effort outside the closed set.
func normalizeModels(in []ModelChoice) []ModelChoice {
	out := make([]ModelChoice, 0, len(in))
	seen := map[string]bool{}
	for _, m := range in {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, ModelChoice{
			ID:      id,
			Effort:  NormalizeEffort(m.Effort),
			UseWhen: strings.TrimSpace(m.UseWhen),
		})
		if len(out) == maxModelsPerSkill {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sameModels reports whether two lists say the same thing, which is how a
// stored copy of the shipped list is recognised.
func sameModels(a, b []ModelChoice) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Skill is one program Socrates knows how to operate: how it is started, and
// a manual in plain prose for driving it the way a person would. Skills are
// predefined by the app - see Presets - and this is the runtime shape the
// engine consumes, a preset with the user's choices applied.
type Skill struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Description tells Socrates when to reach for this skill. It goes into
	// the system prompt as written, and it is the one part of a skill the
	// dashboard lets the user rewrite.
	Description string `json:"description"`

	// Command and Args are how the program is started.
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// Env is extra environment for the program, as KEY=VALUE lines. It is the
	// declarative form of writing "KEY=VALUE program" in a shell, and some
	// programs need it: Claude Code refuses --dangerously-skip-permissions as
	// root unless IS_SANDBOX=1 is set.
	Env []string `json:"env"`

	// Models is the list of models this skill may be started with, in the
	// program's own naming. It is the user's decision - see
	// SkillSetting.Models - and the preset's list is what a fresh installation
	// gets. The first entry is the default: it is what the program is started
	// with when the orchestrator opens a terminal without naming a model.
	Models []ModelChoice `json:"models"`
	// ModelArgs and EffortArgs are how a chosen model and a chosen effort
	// actually reach the program, as argv fragments with a placeholder for the
	// value: "{model}" in ModelArgs, "{effort}" in EffortArgs. They are part of
	// the preset, never of the settings document, because how a program is
	// invoked belongs to the app - see the shipped presets for the exact,
	// verified form each program takes.
	//
	// An empty EffortArgs is a statement, not an omission: this program has no
	// way of being told how hard to think at launch, so the effort recorded
	// against one of its models is not applied on the command line. Applying
	// says so in words.
	ModelArgs  []string `json:"model_args"`
	EffortArgs []string `json:"effort_args"`
	// Applying explains, in prose, exactly how the model and the effort reach
	// this program. It is shown in the dashboard next to the model list, so
	// that the mechanism is visible rather than magic, and it goes into the
	// system prompt, because for a program with no launch-time effort flag it
	// is also the instruction for setting the effort by hand.
	Applying string `json:"applying"`

	// SkipPermissions runs the program in its own unattended mode, which is
	// what the coding agents call "dangerously skip permissions" or "yolo".
	// SkipArgs is added when it is on, AskArgs when it is off.
	SkipPermissions bool     `json:"skip_permissions"`
	SkipArgs        []string `json:"skip_permission_args"`
	AskArgs         []string `json:"ask_permission_args"`

	// The manual. Every field below is free text - markdown is fine - and each
	// one is rendered under its own heading in the system prompt. Sections
	// left empty are simply not mentioned.
	//
	// Startup: the screens that come after launch, the exact keys that get
	// past them, and what "ready" looks like.
	Startup string `json:"startup"`
	// GivingTasks: where to type, how to submit, how to write more than one
	// line, which slash commands are worth knowing.
	GivingTasks string `json:"giving_tasks"`
	// ReadingState: how to tell working from idle from waiting for an answer.
	ReadingState string `json:"reading_state"`
	// Answering: every dialog the program shows and the keys that answer it.
	Answering string `json:"answering"`
	// Exiting: how to interrupt it and how to quit cleanly.
	Exiting string `json:"exiting"`
	// Notes: pitfalls, and anything that did not fit above.
	Notes string `json:"notes"`

	// InteractiveOnly keeps the program inside a terminal session, which is
	// the point of the whole app: the user is watching that terminal and
	// wants to read along and take the keyboard.
	InteractiveOnly bool `json:"interactive_only"`
	// HeadlessForms names the program's non-interactive invocations, so the
	// orchestrator can be told exactly what not to reach for.
	HeadlessForms string `json:"headless_forms"`
	// HeadlessUsage is how to use the program without a terminal. It only
	// reaches the model when InteractiveOnly is off.
	HeadlessUsage string `json:"headless_usage"`

	// ReadyPattern is a regular expression that means "the program has
	// finished starting and will accept input". Empty means: wait for it to
	// stop printing instead.
	ReadyPattern string `json:"ready_pattern"`
	// BusyPattern is a regular expression that means "this program is still
	// working on the last thing you gave it". Coding agents say so in plain
	// words - "esc to interrupt" - and that sentence is worth far more than
	// silence: a program that is thinking, waiting on the network or drawing
	// the same spinner frame twice looks idle by the byte and is not. While
	// this matches, terminal_wait keeps waiting and Socrates is not allowed to
	// report a result.
	//
	BusyPattern string `json:"busy_pattern"`
	// HoldReplyWhileBusy stops the orchestrator from answering the user while
	// this program is still working.
	HoldReplyWhileBusy bool `json:"hold_reply_while_busy"`
	// IdleSeconds is how long the program has to stay quiet before Socrates
	// treats its turn as finished.
	IdleSeconds    int `json:"idle_seconds"`
	TimeoutSeconds int `json:"timeout_seconds"`
	// Cols and Rows size the window this program gets. Zero means the default.
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Interactive reports whether this skill may only be driven in a terminal
// session.
func (s Skill) Interactive() bool { return s.InteractiveOnly }

// HoldsReply reports whether Socrates has to keep waiting instead of
// answering while this program is busy.
func (s Skill) HoldsReply() bool { return s.HoldReplyWhileBusy }

// Busy compiles the busy pattern, if there is a usable one. A pattern that
// does not compile is treated as no pattern at all: a bad pattern must not
// stop a program from ever being driven.
func (s Skill) Busy() *regexp.Regexp {
	pattern := s.BusyText()
	if pattern == "" {
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		warnOnce("busy "+pattern, "config: skill %s has an unusable busy pattern %q: %v", s.ID, pattern, err)
		return nil
	}
	return re
}

// BusyText is the busy pattern as written, or the empty string when this skill
// has none.
func (s Skill) BusyText() string { return strings.TrimSpace(s.BusyPattern) }

// warnOnce logs a settings problem the first time it is seen. Busy patterns
// are compiled on every wait, and one typo must not fill the log.
var warned sync.Map

func warnOnce(key, format string, args ...any) {
	if _, seen := warned.LoadOrStore(key, true); seen {
		return
	}
	log.Printf(format, args...)
}

// SkillSetting is everything the settings document stores about a skill: it
// names one of the shipped presets and records the decisions that are the
// user's to make. Everything else comes from the preset, so an upgrade that
// improves a manual, a pattern or a command line reaches every installation.
type SkillSetting struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// Description replaces the preset's "when should Socrates use this?" text.
	// Empty means the preset's own wording, which is the normal case.
	Description string `json:"description,omitempty"`
	// Models replaces the preset's list of models this skill may be started
	// with. Empty means the shipped list, exactly like an empty Description
	// means the shipped wording - so a better list in a later version reaches
	// an installation that never touched it.
	//
	// How a model and an effort from this list are handed to the program is
	// not stored here and is not the user's to change: that is Skill.ModelArgs,
	// Skill.EffortArgs and Skill.Applying, and it ships with the app.
	Models []ModelChoice `json:"models,omitempty"`
}

// UnmarshalJSON also reads the shapes this key had in earlier versions, where
// a skill was stored whole and could be one the user had written themselves.
// The extra fields are ignored except for the two that say which preset an
// entry meant; Normalize then drops anything that is not a shipped skill.
func (sk *SkillSetting) UnmarshalJSON(b []byte) error {
	var w struct {
		ID          string        `json:"id"`
		Enabled     bool          `json:"enabled"`
		Description string        `json:"description"`
		Models      []ModelChoice `json:"models"`
		// Preset and Command only appear in a document written before skills
		// became predefined.
		Preset  string `json:"preset"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*sk = SkillSetting{ID: w.ID, Enabled: w.Enabled, Description: w.Description, Models: w.Models}
	if id := presetIDFor(w.Preset, w.ID, w.Command); id != "" {
		sk.ID = id
	}
	return nil
}

// legacyEntry is the shape the skill list had when it was called "tools", and
// before that "backends". Only the fields that survive are read: which program
// it was, whether it was on, and what the user said it was for.
type legacyEntry struct {
	ID          string `json:"id"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
	Command     string `json:"command"`
}

// DefaultModel is the choice a skill is started with when nobody names one:
// the first entry of its list, because that is the one a reader of the
// dashboard sees at the top and reads as "normally this one". A skill with no
// models configured returns the zero choice, which starts the program exactly
// the way it starts itself.
func (s Skill) DefaultModel() ModelChoice {
	for _, m := range s.Models {
		if strings.TrimSpace(m.ID) != "" {
			return m
		}
	}
	return ModelChoice{}
}

// ModelByID looks up one of the configured models. An empty id asks for the
// default, which is what an orchestrator that did not choose gets.
func (s Skill) ModelByID(id string) (ModelChoice, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return s.DefaultModel(), true
	}
	for _, m := range s.Models {
		if strings.EqualFold(strings.TrimSpace(m.ID), id) {
			return m, true
		}
	}
	return ModelChoice{}, false
}

// ModelIDs lists the configured model ids, in order.
func (s Skill) ModelIDs() []string {
	out := make([]string, 0, len(s.Models))
	for _, m := range s.Models {
		if id := strings.TrimSpace(m.ID); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// LaunchArgs is the extra argv that starts this program on one particular
// model at one particular effort. It is the whole of the application
// mechanism: the preset says which flags carry the two values, this fills them
// in, and nothing else in the app needs to know how any single program spells
// it.
//
// A value the program has no mechanism for contributes nothing rather than
// being smuggled in some other way - see EffortArgs.
func (s Skill) LaunchArgs(m ModelChoice) []string {
	var args []string
	if id := strings.TrimSpace(m.ID); id != "" {
		args = append(args, fillPlaceholder(s.ModelArgs, "{model}", id)...)
	}
	if effort := NormalizeEffort(m.Effort); effort != "" {
		args = append(args, fillPlaceholder(s.EffortArgs, "{effort}", effort)...)
	}
	return args
}

// fillPlaceholder copies an argv template with the placeholder replaced. The
// substitution is per element and never re-splits, so a value with a space in
// it stays one argument.
func fillPlaceholder(template []string, placeholder, value string) []string {
	if len(template) == 0 {
		return nil
	}
	out := make([]string, 0, len(template))
	for _, part := range template {
		out = append(out, strings.ReplaceAll(part, placeholder, value))
	}
	return out
}

// CommandLine assembles the argv this skill is started with for one choice of
// model. The zero ModelChoice is the honest way to say "no preference": the
// program is then started without a word about models and picks for itself.
func (s Skill) CommandLine(model ModelChoice) (string, []string) {
	args := append([]string{}, s.Args...)
	if s.SkipPermissions {
		args = append(args, s.SkipArgs...)
	} else {
		args = append(args, s.AskArgs...)
	}
	args = append(args, s.LaunchArgs(model)...)
	return s.Command, args
}

// Idle is the quiet window that means this program has finished its turn.
func (s Skill) Idle() time.Duration {
	if s.IdleSeconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(s.IdleSeconds) * time.Second
}

// Timeout is the longest a single turn of this program may take.
func (s Skill) Timeout() time.Duration {
	if s.TimeoutSeconds <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(s.TimeoutSeconds) * time.Second
}

// sandboxEnv tells Claude Code that it is already confined, which is what it
// wants to hear before it will skip permission prompts as root.
const sandboxEnv = "IS_SANDBOX=1"

// Presets are the skills Socrates ships with. Their manuals were written
// against the versions named in them - Claude Code 2.1.251, codex-cli 0.146.0,
// opencode 1.17.13 - by driving each program in a terminal and writing down
// what it actually did.
//
// These are the skills, not a starting point for them: an installation gets
// whatever this list says, and a preset added in a later version arrives on
// upgrade with the enabled flag it ships with.
func Presets() []Skill {
	presetsMu.RLock()
	defer presetsMu.RUnlock()
	if presetOverride != nil {
		return append([]Skill(nil), presetOverride...)
	}
	return shippedPresets()
}

// presetOverride lets a test pretend the app ships a different set of skills.
var (
	presetsMu      sync.RWMutex
	presetOverride []Skill
)

// SwapPresets replaces the shipped catalogue with skills of your own and
// returns a function that puts the real one back. Skills are predefined by the
// app, so this is how a test exercises a program Socrates does not ship.
// Nothing in the running server calls it.
func SwapPresets(skills []Skill) func() {
	presetsMu.Lock()
	previous := presetOverride
	presetOverride = append([]Skill(nil), skills...)
	presetsMu.Unlock()
	return func() {
		presetsMu.Lock()
		presetOverride = previous
		presetsMu.Unlock()
	}
}

func shippedPresets() []Skill {
	return []Skill{
		{
			ID:      "claude",
			Name:    "Claude Code",
			Enabled: true,
			Description: "Best for writing, refactoring and debugging code in an existing project, for multi step " +
				"engineering tasks and for careful file edits.",
			Command: "claude",
			// Remote Control lets a phone app drive this session from outside;
			// there is no flag to turn it off, but the settings key is honoured
			// per invocation, and --settings takes the JSON inline.
			Args: []string{"--settings", `{"disableRemoteControl":true}`},
			Env:  []string{sandboxEnv},
			// Both halves are real flags of claude 2.1.251, and both were read
			// off `claude --help`: "--model <model>" takes an alias (fable,
			// opus, sonnet, haiku) or a full model id, and "--effort <level>"
			// takes low, medium, high, xhigh or max. A level it does not know
			// is not fatal - it prints "Unknown --effort value ... ignoring it
			// and using the default effort" and carries on - but Socrates only
			// ever passes one of its own three, so that never happens.
			ModelArgs:  []string{"--model", "{model}"},
			EffortArgs: []string{"--effort", "{effort}"},
			Applying: "The model is passed as `--model <id>`, which takes an alias - fable, opus, sonnet or " +
				"haiku - or a full model id such as `claude-sonnet-4-5`. The effort is passed as " +
				"`--effort <level>`. Both are start-up flags; inside a running session the same two are " +
				"changed with `/model` and `/effort`, and the current effort is the `◐ medium` part of " +
				"the footer.",
			Models: []ModelChoice{
				{ID: "sonnet", Effort: EffortMedium, UseWhen: "The everyday choice, and what you get if you do not " +
					"pick: fast enough to keep a conversation going and strong enough for ordinary refactoring, " +
					"bug fixing and multi file edits."},
				{ID: "opus", Effort: EffortHigh, UseWhen: "The hardest work, when being slow is worth it: a " +
					"subtle bug nobody has located yet, a design that has to be got right the first time, a " +
					"refactor across many files with a lot that can go wrong."},
				{ID: "haiku", Effort: EffortLow, UseWhen: "Small mechanical chores where the answer is obvious " +
					"and only the typing takes time: renaming things, adding a test that mirrors an existing " +
					"one, applying the same edit to a list of files."},
			},
			SkipPermissions: true,
			SkipArgs:        []string{"--dangerously-skip-permissions"},
			AskArgs:         []string{"--permission-mode", "manual"},
			Startup: "The prompt and the highlighted row of a dialog both start with the glyph \u276f (U+276F), not " +
				"with a plain \">\".\n" +
				"The first screen in a directory it has not seen before is the workspace trust dialog: " +
				"\"Quick safety check: Is this a project you created or one you trust?\" with two choices, " +
				"\"No, exit\" on top and \"Yes, I trust this folder\" below it. \"No, exit\" is highlighted, so " +
				"a plain enter quits the program. Press the down arrow once, then enter. This dialog has " +
				"no numbers.\n" +
				"On a machine that has never run Claude Code before it asks for a theme first (a numbered " +
				"list of seven) and then for a login method (1 = Claude subscription, 2 = Anthropic " +
				"Console, 3 = Bedrock, Foundry or Vertex). Both are answered by typing the number and " +
				"pressing enter, but the login itself opens a browser, so if you land on that screen stop " +
				"and tell the user instead of guessing.\n" +
				"It is ready when the input box shows a bare \"\u276f \" prompt with nothing after it and the " +
				"footer no longer contains \"esc to interrupt\": it carries the mode line instead, for " +
				"example \"\u23f8 manual mode on \u00b7 ? for shortcuts \u25d0 medium \u00b7 /effort\", or \"\u23f5\u23f5 auto mode on " +
				"(shift+tab to cycle) \u25d0 medium \u00b7 /effort\" in auto mode. It can look ready a moment before " +
				"it accepts keys, so check that what you typed actually appeared on the screen.",
			GivingTasks: "Type the brief into the \"\u276f \" prompt at the bottom, which always has focus when the " +
				"program is idle, and press enter to send it.\n" +
				"For a line break without sending, end the line with a backslash and press enter, or " +
				"press ctrl+j.\n" +
				"Typing \"/\" opens the slash command list with the best match highlighted; enter picks it. " +
				"The ones worth knowing: /exit quits, /clear starts a fresh conversation, /resume opens " +
				"the session picker. Shift+tab cycles the permission mode.",
			ReadingState: "Working: the status line shows a spinner glyph, a randomised present participle and the " +
				"elapsed time, like \"Grooving... (6s \u00b7 247 tokens)\". The verb is different every time, so " +
				"never match on the word. The footer is the reliable signal: while it is busy it ends in " +
				"\"esc to interrupt\".\n" +
				"Idle: \"esc to interrupt\" is gone and a summary line stands above the prompt in the shape " +
				"\"<verb> for <N>s \u00b7 done <H:MM AM|PM>\", which is the regular expression `\u00b7 done " +
				"\\d{1,2}:\\d{2}\\s*(AM|PM)`, with an empty \"\u276f \" prompt underneath it.\n" +
				"Waiting for an answer: a boxed dialog with a numbered list. \"Do you want to proceed?\" " +
				"together with \"Esc to cancel \u00b7 Tab to amend \u00b7 ctrl+e to explain\" is a permission prompt; " +
				"\"Enter to select \u00b7 \u2191/\u2193 to navigate \u00b7 Esc to cancel\" is a question it wants answered.",
			Answering: "A permission prompt looks like this:\n" +
				"  Do you want to proceed?\n" +
				"  \u276f 1. Yes\n" +
				"    2. Yes, and always allow access to <path> from this project\n" +
				"    3. Yes, and switch to auto mode\n" +
				"    4. No\n" +
				"Answer it by typing the digit 1 to 4, or by moving with the up and down arrows and " +
				"pressing enter. Option 1 is highlighted by default and approves this one action only.\n" +
				"A question dialog is the other shape, with the footer \"Enter to select \u00b7 \u2191/\u2193 to navigate " +
				"\u00b7 Esc to cancel\": numbered choices, then \"Type something.\" and \"Chat about this\". Type " +
				"the digit you want, or arrow to it and press enter; \"Type something.\" lets you write a " +
				"free answer instead. A bare enter accepts the highlighted first option, so never press " +
				"enter merely to acknowledge the dialog. Afterwards the transcript shows a line like " +
				"\"User answered Claude's questions: | <question> -> <answer>\".\n" +
				"Not every command asks. A risk classifier decides per action, so \"echo hi\" runs silently " +
				"even in manual mode while \"rm -rf ...\" raises the dialog. Silence does not mean " +
				"permissions are switched off.",
			Exiting: "Escape interrupts whatever it is doing, and also cancels an open dialog.\n" +
				"Ctrl+c once prints \"Press Ctrl-C again to exit\"; a second ctrl+c exits.\n" +
				"Ctrl+d pressed twice within 800 ms also exits.\n" +
				"The tidy way out is to type /exit and press enter, after which the process ends within a " +
				"second or two.",
			Notes: "On a subscription plan a fresh interactive session starts in \"auto\" mode, which approves " +
				"most actions by itself, not in ask-every-time. That is why the permission-asking variant " +
				"passes \"--permission-mode manual\" explicitly; without it you will see far fewer prompts " +
				"than you expect.\n" +
				"\"--dangerously-skip-permissions\" is refused outright when the process runs as root " +
				"unless IS_SANDBOX=1 is in the environment, which is why that variable is on the " +
				"environment list.\n" +
				"\"--continue\" resumes the most recent conversation in this working directory and " +
				"\"--resume <id-or-title>\" a specific one. Both are start-up flags, not something you type " +
				"inside the session.\n" +
				"\"--model\" takes an alias - fable, opus, sonnet or haiku - or a full model id.\n" +
				"Every session here starts with \"--settings {\\\"disableRemoteControl\\\":true}\", because Remote " +
				"Control would otherwise let a second party drive the same session from a phone. It has no " +
				"flag of its own, only that settings key.\n" +
				"The animated first-run screens draw words by moving the cursor rather than by printing " +
				"spaces, so words can look glued together in the screen you read back. Judge those " +
				"screens by their choices, not by their prose.",
			InteractiveOnly:    true,
			HoldReplyWhileBusy: true,
			HeadlessForms: "`-p` / `--print`, `--output-format json`, `--output-format stream-json`, `--input-format " +
				"stream-json`, `--bare`, `--json-schema`, a prompt piped in on stdin (`cat brief.md | " +
				"claude -p ...`), and the background session commands `--bg`, `claude attach`, `claude " +
				"agents`, `claude logs` and `claude stop`.",
			BusyPattern:    "esc to interrupt",
			IdleSeconds:    5,
			TimeoutSeconds: 1800,
		},
		{
			ID:      "codex",
			Name:    "Codex",
			Enabled: true,
			Description: "Best for research, investigation and analysis: exploring an unfamiliar codebase, " +
				"gathering facts, comparing options and writing up findings.",
			Command: "codex",
			Args:    []string{"--no-alt-screen"},
			// Codex has a flag for the model and no flag at all for the effort:
			// the effort is an ordinary config key, `model_reasoning_effort`,
			// and `-c key=value` overrides any config key for this one run. The
			// value is parsed as TOML, which is why the level is written with
			// its quotes - they are part of the argument, not shell quoting,
			// and Socrates starts the program without a shell. Proof that the
			// override lands: `codex -c model="gpt-5.4" doctor` reports
			// "model gpt-5.4 · openai" where a plain `codex doctor` reports the
			// configured default.
			ModelArgs:  []string{"-m", "{model}"},
			EffortArgs: []string{"-c", `model_reasoning_effort="{effort}"`},
			Applying: "The model is passed as `-m <slug>`, using the slugs codex itself lists - gpt-5.6-sol, " +
				"gpt-5.6-terra, gpt-5.6-luna, gpt-5.5, gpt-5.4, gpt-5.4-mini. The effort has no flag: it is " +
				"the config key `model_reasoning_effort`, overridden for this run with " +
				"`-c model_reasoning_effort=\"<level>\"`. Both are visible in the footer of the running " +
				"session, which reads \"<model> <reasoning> · <cwd>\", and the effort can also be changed " +
				"from inside with `/model`.",
			Models: []ModelChoice{
				{ID: "gpt-5.6-terra", Effort: EffortMedium, UseWhen: "The everyday choice, and what you get if " +
					"you do not pick: a balanced agentic model for ordinary reading, searching and writing up."},
				{ID: "gpt-5.6-sol", Effort: EffortHigh, UseWhen: "The frontier model thinking hard. Worth the " +
					"wait for an investigation that has to be right: tracing a bug through an unfamiliar " +
					"codebase, comparing designs, anything where a plausible wrong answer is expensive."},
				{ID: "gpt-5.6-luna", Effort: EffortLow, UseWhen: "Fast and cheap, for small lookups: what does " +
					"this function do, where is this configured, does this repository even have tests."},
			},
			SkipPermissions: true,
			SkipArgs:        []string{"--dangerously-bypass-approvals-and-sandbox"},
			AskArgs:         []string{"--ask-for-approval", "on-request", "--sandbox", "workspace-write"},
			Startup: "Up to two numbered screens can appear before the composer, in this order:\n" +
				"1. An update notice, \"Update available! 0.146.0 -> ...\", with \"1. Update now\" (which " +
				"runs the upgrade there and then), \"2. Skip\" and \"3. Skip until next version\". Send 2.\n" +
				"2. The directory trust question, \"Do you trust the contents of this directory?\", with " +
				"\"1. Yes, continue\" and \"2. No, quit\". Send 1. It comes once per directory and it comes " +
				"even when the program was started with \"--dangerously-bypass-approvals-and-sandbox\".\n" +
				"On these dialogs the digit alone selects and confirms, so do not follow it with enter, " +
				"and look at the screen again afterwards: the second dialog only appears once the first " +
				"one is gone.\n" +
				"Ready looks like a box with \"OpenAI Codex (v0.146.0)\", the model and the directory, then " +
				"the composer line starting with \"\u203a\", then a footer of the shape \"<model> <reasoning> \u00b7 " +
				"<cwd>\". The placeholder inside the composer rotates between suggestions like \"Explain " +
				"this codebase\" - that is normal, not a task it is working on.\n" +
				"It is started with \"--no-alt-screen\", so it prints inline like a shell and answered " +
				"dialogs stay visible further up. Judge what it wants from the bottom of the screen, " +
				"never from something scrolled above.",
			GivingTasks: "Type the brief at the \"\u203a\" composer and press enter; the composer clears back to its " +
				"placeholder once the message is sent. Ctrl+j or alt+enter inserts a newline instead of " +
				"sending. \"@\" starts a file reference and a line beginning with \"!\" runs a shell command " +
				"inline.",
			ReadingState: "Working: a status line above the composer containing \"Working\" with an elapsed time and " +
				"a hint ending in \"to interrupt\"; the model and directory footer stays where it is.\n" +
				"Idle: the composer line and the \"<model> <reasoning> \u00b7 <cwd>\" footer with nothing " +
				"moving. As a rule: a line matching `^\u203a ` that does not also match the dialog pattern " +
				"below, together with that footer line.\n" +
				"Waiting for an answer: a numbered dialog whose focused option is prefixed with \"\u203a \" and " +
				"which ends in \"Press enter to confirm\" or \"Press enter to continue\". Detect it with `^\u203a " +
				"\\d+\\.\\s`.\n" +
				"Check in that order: a numbered dialog first, then busy, then ready.\n" +
				"A dismissed update notice leaves a cosmetic banner behind, without numbered options and " +
				"without a \"Press enter\" line. That is not a dialog and needs no answer.",
			Answering: "Any numbered dialog is answered by pressing the digit, which selects and confirms in one " +
				"keystroke; arrow keys plus enter work too.\n" +
				"Command approval - \"Would you like to run the following command?\" with the options \"Yes, " +
				"just this once\", \"Yes, and don't ask again for this command in this session\", \"Yes, and " +
				"don't ask again for commands that start with `...`\", \"No, continue without running it\" " +
				"and \"No, and tell Codex what to do differently\".\n" +
				"File edits - \"Would you like to make the following edits?\", which also offers \"Yes, and " +
				"don't ask again for these files\".\n" +
				"Network access - \"Would you like to grant these permissions?\" with \"Yes, and allow this " +
				"host for this conversation\", \"Yes, and allow these permissions for this session\" and " +
				"\"No, and block this host in the future\".\n" +
				"MCP tool call - \"Approve app tool call?\" with \"Run the tool and continue.\", \"Allow for " +
				"this session\", \"Allow and don't ask me again\" and \"Cancel\".\n" +
				"Typing /permissions opens \"Update Model Permissions\": \"1. Ask for approval (current)\", " +
				"\"2. Approve for me\", \"3. Full Access\".",
			Exiting: "Escape during a turn interrupts that turn.\n" +
				"Type /quit and press enter to leave cleanly. Ctrl+c on an empty composer exits " +
				"immediately, while ctrl+c with text in the composer only clears the text. Ctrl+d exits " +
				"as well.",
			Notes: "\"--full-auto\" does not exist in 0.146.0; it exits with \"unexpected argument\". The " +
				"unattended flag is \"--dangerously-bypass-approvals-and-sandbox\".\n" +
				"The directory trust dialog appears even in that unattended mode.\n" +
				"When authentication is missing or expired there is no login screen: the program simply " +
				"produces nothing for a long time, or a turn silently vanishes back to the idle composer. " +
				"\"codex doctor\" reports the real reason.\n" +
				"Sessions are resumed from the command line: \"codex resume --last\" for the most recent " +
				"one, \"codex resume\" for a picker, \"codex resume <id>\" for a specific one; there are also " +
				"fork, archive and delete.\n" +
				"\"-C\" sets the working directory, \"-m\" the model, \"--add-dir\" grants access to further " +
				"directories.",
			InteractiveOnly:    true,
			HoldReplyWhileBusy: true,
			HeadlessForms: "`codex exec` (alias `e`), `codex exec --json`, `codex exec -o` / " +
				"`--output-last-message`, `codex exec resume`, `codex exec review`, `codex review`, " +
				"`codex mcp-server` and `codex app-server`.",
			// The word "Working" on its own is unusable: the directory trust
			// dialog says "Working with untrusted contents comes with higher
			// risk", and with --no-alt-screen the transcript keeps every line
			// the model ever printed, "Working tree is clean" included. The
			// live status line reads "Working (12s * Esc to interrupt)", so
			// the interrupt hint identifies it on its own.
			BusyPattern:    `(?i)esc to interrupt`,
			IdleSeconds:    5,
			TimeoutSeconds: 1800,
		},
		{
			ID:      "opencode",
			Name:    "OpenCode",
			Enabled: false,
			Description: "Open source coding agent. Useful as an alternative implementer, or as a second opinion " +
				"when another agent is stuck.",
			Command: "opencode",
			// The model is a flag; the effort is not, and there is no way to
			// pretend otherwise. `-m` is parsed by splitting the string at the
			// first slash into a provider and a model id and nothing else, so a
			// third segment or a suffix is not a level, it is a model that does
			// not exist. What opencode calls a "variant" *is* the reasoning
			// effort - `opencode models openai --verbose` prints
			// "variants": {"low": {"reasoningEffort": "low"}, ...} - and a
			// variant is chosen inside the running program or pinned per agent
			// in the config file, never on the command line. EffortArgs is
			// therefore deliberately empty: a level recorded against one of
			// these models is not applied at launch, and Applying says how to
			// apply it by hand.
			ModelArgs: []string{"-m", "{model}"},
			Applying: "The model is passed as `-m provider/model`, exactly as `opencode models` prints the " +
				"ids. The effort has no flag: opencode calls the effort levels \"variants\", and a variant " +
				"is only selectable inside the running program - type `/variants`, pick the level with the " +
				"arrow keys, press enter. So set it there if it matters, right after start-up and before " +
				"the brief; if the model has no variants it answers \"No variants available\", which is " +
				"nothing to worry about. The line above the footer shows the current one, as in " +
				"\"Build · GPT-5.6 Sol OpenAI · medium\".",
			Models: []ModelChoice{
				{ID: "opencode/big-pickle", UseWhen: "The everyday choice, and what you get if you do not " +
					"pick. It is OpenCode's own hosted model, so it works without a provider account - which " +
					"is what a machine with nothing configured falls back to anyway. It has no effort " +
					"variants."},
				{ID: "opencode/nemotron-3-ultra-free", Effort: EffortHigh, UseWhen: "A very large model with " +
					"a million token window, for a job that has to see a lot of code at once."},
				{ID: "opencode/nemotron-3.5-lightning-free", Effort: EffortLow, UseWhen: "The quick one, for " +
					"small chores where the answer is obvious."},
			},
			SkipPermissions: true,
			SkipArgs:        []string{"--auto"},
			Startup: "There is no start-up dialog and no blocking login screen: it boots straight into its " +
				"interface. It is ready when the prompt box shows the placeholder \"Ask anything...\" and " +
				"the footer reads \"tab agents   ctrl+p commands\", with no \"esc interrupt\" hint and no " +
				"spinner on screen.\n" +
				"The line above the footer names the current agent, then the model and its provider, and " +
				"the reasoning effort when one is set: \"Build \u00b7 Big Pickle OpenCode Zen\", or \"Build \u00b7 " +
				"GPT-5.6 Sol OpenAI \u00b7 medium\". With no provider configured it quietly falls back to its " +
				"own free tier rather than asking anyone to log in, so read that line if it matters which " +
				"model is answering.",
			GivingTasks: "Type the brief into the boxed prompt at the bottom and press enter. Shift+enter, " +
				"ctrl+enter, alt+enter and ctrl+j insert a newline instead of sending. A message sent " +
				"while a turn is running is marked QUEUED and runs afterwards.\n" +
				"Tab cycles the agent forward (build, plan, and any custom ones) and shift+tab backwards; " +
				"the current one is the first word of the status line. Type /models and press enter for " +
				"the model picker: type to filter, arrows to move, enter to select. Ctrl+p opens the " +
				"command palette.",
			ReadingState: "Working: the footer gains \"esc interrupt\", which is the single most reliable busy " +
				"signal. You will also see a braille spinner with \"Thinking: ...\", lines such as \"~ " +
				"Writing command...\", and a block progress bar that animates continuously.\n" +
				"Idle: \"esc interrupt\" is gone, the \"Ask anything...\" placeholder and the \"tab agents / " +
				"ctrl+p commands\" footer are back, and the screen stops changing. An idle session does " +
				"not repaint at all, so two identical reads a second apart confirm it.\n" +
				"Waiting for an answer: the permission dialog described below.\n" +
				"Do not compare raw screens to decide whether a turn has ended - the spinner and the " +
				"progress bar make every capture different while it works.",
			Answering: "The permission dialog looks like this:\n" +
				"  Permission required\n" +
				"    Access external directory /tmp\n" +
				"  Patterns\n" +
				"  - /tmp/*\n" +
				"    Allow once   Allow always   Reject      ctrl+f fullscreen   select   enter confirm\n" +
				"Recognise it by \"Permission required\" together with \"Allow once\", \"Allow always\" and " +
				"\"Reject\" on one line. Move between the three choices with the left and right arrow keys " +
				"and press enter. \"Allow once\" is highlighted by default, so a bare enter approves this " +
				"one request. Ctrl+f expands the dialog when you need to read a long command or diff " +
				"before deciding.\n" +
				"By default only a few things ask at all: touching a path outside the project directory, " +
				"and a repeated \"doom loop\" pattern. Bash commands, file edits and web fetches inside the " +
				"project run without a prompt, and reading a .env file is refused outright. The absence " +
				"of a dialog therefore does not mean it did nothing.",
			Exiting: "Escape interrupts the running turn while \"esc interrupt\" is showing.\n" +
				"To quit: two ctrl+c in quick succession - a single one does nothing - or ctrl+d, or " +
				"ctrl+x followed by q.\n" +
				"/exit and /quit are not commands in this version. Typing one just sends it to the model " +
				"as an ordinary message and the program keeps running.",
			Notes: "\"--auto\" approves anything that would otherwise ask, while explicit deny rules stay in " +
				"force. Without it, it asks and you answer the dialog on screen.\n" +
				"\"-c\" continues the last session in this directory, \"-s <id>\" a specific one, \"--fork\" " +
				"branches instead of appending. \"--prompt\" preloads a first message and \"--agent\" picks " +
				"the starting agent.\n" +
				"\"-m\" takes a \"provider/model\" id; \"opencode models\" prints the exact ids.\n" +
				"On start-up it sends colour and capability queries to the terminal and expects no reply. " +
				"That is harmless; do not wait for one.",
			InteractiveOnly:    true,
			HoldReplyWhileBusy: true,
			HeadlessForms: "`opencode run` (including `--format json`, `-f` / `--file`, `-c`, `-s` and `--agent`), " +
				"`opencode serve`, `opencode web`, `opencode attach <url>` and `opencode acp`.",
			BusyPattern:    "esc interrupt",
			IdleSeconds:    5,
			TimeoutSeconds: 1800,
		},
	}
}

// PresetByID looks up a shipped preset.
func PresetByID(id string) (Skill, bool) {
	for _, p := range Presets() {
		if p.ID == id {
			return p, true
		}
	}
	return Skill{}, false
}

// presetIDFor guesses which shipped skill an older entry corresponds to: by
// the preset it named, by its id, and finally by the program it ran.
func presetIDFor(preset, id, command string) string {
	presets := Presets()
	for _, want := range []string{preset, id} {
		for _, p := range presets {
			if want != "" && p.ID == want {
				return p.ID
			}
		}
	}
	// The command may be an absolute path - /opt/bin/claude is still Claude
	// Code - so only the program name is compared.
	name := filepath.Base(strings.TrimSpace(command))
	for _, p := range presets {
		if name != "" && name != "." && p.Command == name {
			return p.ID
		}
	}
	return ""
}

// defaultSkillSettings is what a fresh installation decides: every shipped
// skill, on or off the way the preset says, with the preset's own description.
func defaultSkillSettings() []SkillSetting {
	presets := Presets()
	out := make([]SkillSetting, 0, len(presets))
	for _, p := range presets {
		out = append(out, SkillSetting{ID: p.ID, Enabled: p.Enabled})
	}
	return out
}

// Default returns a fresh settings document, seeded from the environment where
// that is useful for container deployments.
func Default() Settings {
	s := Settings{
		OpenRouter: OpenRouterSettings{
			APIKey:          os.Getenv("OPENROUTER_API_KEY"),
			BaseURL:         DefaultOpenRouterBaseURL,
			ChatModel:       DefaultChatModel,
			TranscribeModel: DefaultTranscribeModel,
			TitleModel:      DefaultTitleModel,
		},
		Voice: VoiceSettings{
			Language:        DefaultLanguage,
			STTProvider:     "openrouter",
			STTPrompt:       "Transcribe the spoken audio verbatim. Reply with the transcript only, no commentary, no quotes.",
			TTSProvider:     "browser",
			TTSBaseURL:      "https://api.openai.com/v1",
			TTSModel:        "gpt-4o-mini-tts",
			TTSVoice:        "alloy",
			TTSRate:         1,
			SpeakInAutoMode: true,
		},
		Agent: AgentSettings{
			SystemPrompt:  DefaultSystemPrompt,
			MaxIterations: DefaultMaxIterations,
			Temperature:   0.3,
			WorkspaceRoot: DefaultWorkspaceRoot(),
		},
		Tunnel: TunnelSettings{
			Enabled: false,
			Mode:    TunnelQuick,
			Command: "cloudflared",
		},
		Skills: defaultSkillSettings(),
	}
	return s
}

// Normalize fills in empty fields with their defaults so that a partially
// written settings document from the admin UI never breaks the server.
func (s *Settings) Normalize() {
	d := Default()
	if strings.TrimSpace(s.OpenRouter.BaseURL) == "" {
		s.OpenRouter.BaseURL = d.OpenRouter.BaseURL
	}
	s.OpenRouter.BaseURL = strings.TrimRight(strings.TrimSpace(s.OpenRouter.BaseURL), "/")
	if strings.TrimSpace(s.OpenRouter.ChatModel) == "" {
		s.OpenRouter.ChatModel = d.OpenRouter.ChatModel
	}
	if strings.TrimSpace(s.OpenRouter.TranscribeModel) == "" {
		s.OpenRouter.TranscribeModel = d.OpenRouter.TranscribeModel
	}
	if strings.TrimSpace(s.OpenRouter.TitleModel) == "" {
		s.OpenRouter.TitleModel = s.OpenRouter.ChatModel
	}
	// An older settings document carried the language on the playback side
	// only. Reading it here is what keeps an existing installation's choice
	// alive now that one setting covers the whole conversation.
	if strings.TrimSpace(s.Voice.Language) == "" {
		s.Voice.Language = s.Voice.TTSLanguage
	}
	s.Voice.Language = NormalizeLanguage(s.Voice.Language)
	s.Voice.TTSLanguage = ""
	if s.Voice.STTProvider != "endpoint" {
		s.Voice.STTProvider = "openrouter"
	}
	if strings.TrimSpace(s.Voice.STTPrompt) == "" {
		s.Voice.STTPrompt = d.Voice.STTPrompt
	}
	if s.Voice.TTSProvider != "endpoint" {
		s.Voice.TTSProvider = "browser"
	}
	s.Voice.TTSBaseURL = strings.TrimRight(strings.TrimSpace(s.Voice.TTSBaseURL), "/")
	s.Voice.STTBaseURL = strings.TrimRight(strings.TrimSpace(s.Voice.STTBaseURL), "/")
	if s.Voice.TTSRate <= 0 {
		s.Voice.TTSRate = 1
	}
	if strings.TrimSpace(s.Agent.SystemPrompt) == "" {
		s.Agent.SystemPrompt = d.Agent.SystemPrompt
	}
	s.adoptCurrentPrompt()
	if s.Agent.MaxIterations <= 0 {
		s.Agent.MaxIterations = d.Agent.MaxIterations
	}
	if s.Agent.MaxIterations > 200 {
		s.Agent.MaxIterations = 200
	}
	if s.Agent.Temperature < 0 {
		s.Agent.Temperature = 0
	}
	if strings.TrimSpace(s.Agent.WorkspaceRoot) == "" {
		s.Agent.WorkspaceRoot = d.Agent.WorkspaceRoot
	}
	if s.Tunnel.Mode != TunnelToken {
		s.Tunnel.Mode = TunnelQuick
	}
	if strings.TrimSpace(s.Tunnel.Command) == "" {
		s.Tunnel.Command = d.Tunnel.Command
	}
	s.Tunnel.Token = strings.TrimSpace(s.Tunnel.Token)
	s.Tunnel.Hostname = strings.TrimSpace(strings.TrimPrefix(
		strings.TrimPrefix(s.Tunnel.Hostname, "https://"), "http://"))
	s.Tunnel.Hostname = strings.TrimSuffix(s.Tunnel.Hostname, "/")
	if s.Tunnel.Mode == TunnelToken && s.Tunnel.Token == "" {
		// A named tunnel without its token can never connect.
		s.Tunnel.Enabled = false
	}
	s.normalizeSkills()
}

// normalizeSkills turns whatever the settings document says about skills into
// exactly one entry per shipped skill, in the order they are shipped. A
// document from an older version is folded in on the way: its programs are
// matched to the skill they plainly are, its enabled flags and its own
// descriptions are kept, and everything else it stored - commands, arguments,
// manuals, patterns, timings - is dropped, because the app defines those now.
func (s *Settings) normalizeSkills() {
	if s.Skills == nil {
		for _, e := range s.Tools {
			s.Skills = append(s.Skills, e.setting())
		}
	}
	if s.Skills == nil {
		for _, e := range s.Backends {
			s.Skills = append(s.Skills, e.setting())
		}
	}
	s.Tools = nil
	s.Backends = nil

	stored := map[string]SkillSetting{}
	dropped := []string{}
	for _, sk := range s.Skills {
		id := Slug(sk.ID)
		if _, ok := PresetByID(id); !ok {
			// A skill of the user's own, from the version where skills could
			// be written in the dashboard. There is nothing left to run it
			// with, so it goes.
			if id != "" {
				dropped = append(dropped, id)
			}
			continue
		}
		if _, seen := stored[id]; seen {
			continue
		}
		sk.ID = id
		sk.Description = strings.TrimSpace(sk.Description)
		sk.Models = normalizeModels(sk.Models)
		stored[id] = sk
	}
	if len(dropped) > 0 {
		warnOnce("dropped skills "+strings.Join(dropped, ","),
			"config: skills are predefined by the app now, so the stored settings for %s were "+
				"dropped - they were programs added by hand and nothing runs them any more",
			strings.Join(dropped, ", "))
	}

	presets := Presets()
	out := make([]SkillSetting, 0, len(presets))
	for _, p := range presets {
		sk, ok := stored[p.ID]
		if !ok {
			// A skill this installation has never seen, because it was set up
			// before the app shipped it. It arrives with its own default.
			out = append(out, SkillSetting{ID: p.ID, Enabled: p.Enabled})
			continue
		}
		if sk.Description == strings.TrimSpace(p.Description) {
			// Storing a copy of the shipped wording would freeze it, and the
			// point of predefined skills is that improvements arrive.
			sk.Description = ""
		}
		if sameModels(sk.Models, normalizeModels(p.Models)) {
			// Same reasoning for the model list: a dashboard that saves the
			// form untouched must not pin this installation to the models a
			// long past release thought were current.
			sk.Models = nil
		}
		out = append(out, sk)
	}
	s.Skills = out
}

// setting is the part of an older entry that still means something.
func (e legacyEntry) setting() SkillSetting {
	id := presetIDFor("", e.ID, e.Command)
	if id == "" {
		// Not a program this app ships. Keeping its id means the one line in
		// the log that says what was dropped can name it.
		id = Slug(e.ID)
	}
	return SkillSetting{
		ID:          id,
		Enabled:     e.Enabled,
		Description: e.Description,
	}
}

// retiredDefaultPrompts are the system prompts Socrates has shipped with in
// the past, verbatim. Each one is wrong about the app it now runs: the early
// ones describe tools that no longer exist - delegate_to_agent, ask_user - and
// the later one tells the orchestrator to sit down and do the work itself,
// which is exactly what it must not do. The dashboard stored a copy of
// whichever one was current the first time anyone opened it, and leaving such
// a copy in place teaches every model the wrong job.
//
// They are matched whole rather than by tool name on purpose: a prompt that
// merely mentions ask_user is one somebody wrote, and rewriting it would throw
// away their work. Only an exact copy of something this project shipped is
// replaced.
var retiredDefaultPrompts = []string{
	// The first release: work was handed to coding agents through a tool.
	`You are Socrates, a top level orchestration agent.

You talk to the user in a natural, concise way and you get work done by
delegating to specialised coding agents that run on the user's machine.

Rules of engagement:
- Think about what the user actually wants before acting.
- If a task matches one of the available agents (see the list below), delegate
  it with the delegate_to_agent tool. Give the agent a complete, self contained
  brief: it cannot see this conversation.
- You may delegate several times, refine the brief, and delegate again until the
  job is really done. Never stop half way.
- Answer trivial questions yourself instead of delegating.
- If something important is ambiguous, call ask_user with 2-4 concrete options
  instead of guessing. Keep the options short, they may be read out loud.
- The final message you write is what the user sees and possibly hears. Make it
  self contained, friendly and to the point. Prefer short paragraphs over long
  bullet lists, and never mention the internal tool names.`,
	// The terminal driven rewrite, before questions moved into the reply.
	`You are Socrates, a top level orchestration agent.

You talk to the user in a natural, concise way, and you get work done at a real
terminal on the user's machine - the same terminal a person would use.

How you work:
- You have an interactive shell. Run anything in it: git, ls, npm, a build, a
  test suite. Read the output and decide what to do next.
- For real engineering work, start one of the coding agents listed below inside
  a terminal session and drive it the way a person does: type the brief, press
  enter, watch the screen, answer whatever it asks, and wait until it is done.
  Every one of them is an ordinary program you launch, not a special case.
- Read the screen before you type. If you cannot tell what a program wants,
  look at the screen again rather than guessing at a keypress.
- Give a coding agent a complete, self contained brief: it cannot see this
  conversation.
- Keep going until the job is really done. Check the agent's work - read the
  files it changed, run the tests - instead of trusting its summary.
- Answer trivial questions yourself instead of starting anything.
- If something important is ambiguous, call ask_user with 2-4 concrete options
  instead of guessing. Keep the options short, they may be read out loud.
- The final message you write is what the user sees and possibly hears. Make it
  self contained, friendly and to the point. Prefer short paragraphs over long
  bullet lists, and never mention the internal tool names.`,
	// The same, once the coding agents became configurable skills.
	`You are Socrates, a top level orchestration agent.

You talk to the user in a natural, concise way, and you get work done at a real
terminal on the user's machine - the same terminal a person would use.

How you work:
- You have an interactive shell. Run anything in it: git, ls, npm, a build, a
  test suite. Read the output and decide what to do next.
- For real engineering work, start one of the skills listed below inside a
  terminal session and drive it the way a person does: type the brief, press
  enter, watch the screen, answer whatever it asks, and wait until it is done.
  A skill is an ordinary program someone wrote the manual for, not a special
  case - read that manual before you touch the program.
- Read the screen before you type. If you cannot tell what a program wants,
  look at the screen again rather than guessing at a keypress.
- Give a coding agent a complete, self contained brief: it cannot see this
  conversation.
- Keep going until the job is really done. Check the agent's work - read the
  files it changed, run the tests - instead of trusting its summary.
- Answer trivial questions yourself instead of starting anything.
- If something important is ambiguous, call ask_user with 2-4 concrete options
  instead of guessing. Keep the options short, they may be read out loud.
- The final message you write is what the user sees and possibly hears. Make it
  self contained, friendly and to the point. Prefer short paragraphs over long
  bullet lists, and never mention the internal tool names.`,
	// The last one before Socrates became purely an orchestrator: it still told
	// the model to run the build and the test suite itself.
	`You are Socrates, a top level orchestration agent.

You talk to the user in a natural, concise way, and you get work done at a real
terminal on the user's machine - the same terminal a person would use.

How you work:
- You have an interactive shell. Run anything in it: git, ls, npm, a build, a
  test suite. Read the output and decide what to do next.
- For real engineering work, start one of the skills listed below inside a
  terminal session and drive it the way a person does: type the brief, press
  enter, watch the screen, answer whatever it asks, and wait until it is done.
  A skill is an ordinary program someone wrote the manual for, not a special
  case - read that manual before you touch the program.
- Read the screen before you type. If you cannot tell what a program wants,
  look at the screen again rather than guessing at a keypress.
- Give a coding agent a complete, self contained brief: it cannot see this
  conversation.
- Keep going until the job is really done. Check the agent's work - read the
  files it changed, run the tests - instead of trusting its summary.
- Answer trivial questions yourself instead of starting anything.
- If something important is ambiguous, ask for it in your reply and end your
  turn. You have no way to interrupt yourself and wait: the person reads what
  you wrote and answers with their next message, which continues this
  conversation. Ask one clear question, name the concrete choices in a sentence,
  and do not start guessing work in the same turn.
- The final message you write is what the user sees and possibly hears. Make it
  self contained, friendly and to the point. Prefer short paragraphs over long
  bullet lists, and never mention the internal tool names.`,
}

// promptMigrationOnce keeps the log line to one, however often settings are
// read and normalized.
var promptMigrationOnce sync.Once

// samePrompt compares two prompts the way a person would read them, so a copy
// that picked up a trailing newline or CRLF line endings on its way through a
// text area still counts as the same document.
func samePrompt(a, b string) bool {
	return normalizePrompt(a) == normalizePrompt(b)
}

func normalizePrompt(in string) string {
	lines := strings.Split(strings.ReplaceAll(in, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// adoptCurrentPrompt replaces a stored copy of a retired default prompt with
// the current one. Anything else - a prompt somebody edited, however lightly -
// is left exactly as they wrote it.
func (s *Settings) adoptCurrentPrompt() {
	stored := s.Agent.SystemPrompt
	if strings.TrimSpace(stored) == "" || samePrompt(stored, DefaultSystemPrompt) {
		return
	}
	for i, retired := range retiredDefaultPrompts {
		if !samePrompt(stored, retired) {
			continue
		}
		s.Agent.SystemPrompt = DefaultSystemPrompt
		promptMigrationOnce.Do(func() {
			log.Printf("config: the saved system prompt was version %d of the shipped default, which "+
				"no longer describes how this app works - replaced it with the current default", i+1)
		})
		return
	}
}

// resolve applies a stored decision to the shipped skill it belongs to.
func resolve(preset Skill, sk SkillSetting) Skill {
	preset.Enabled = sk.Enabled
	if d := strings.TrimSpace(sk.Description); d != "" {
		preset.Description = d
	}
	// The list is normalized here as well as in Normalize, because SkillList
	// is also read from a settings document nobody has normalized yet - a test,
	// or an older row in the database - and an entry with an empty id would
	// otherwise reach the orchestrator as a model it can ask for by no name.
	if models := normalizeModels(sk.Models); len(models) > 0 {
		preset.Models = models
	} else {
		preset.Models = normalizeModels(preset.Models)
	}
	return preset
}

// SkillList is every skill the app ships, in order, with the user's decisions
// applied. A preset the settings document says nothing about keeps its own
// defaults, which is what a settings file written before it existed does.
func (s *Settings) SkillList() []Skill {
	out := make([]Skill, 0, len(s.Skills))
	for _, p := range Presets() {
		found := false
		for _, sk := range s.Skills {
			if sk.ID == p.ID {
				out = append(out, resolve(p, sk))
				found = true
				break
			}
		}
		if !found {
			out = append(out, resolve(p, SkillSetting{ID: p.ID, Enabled: p.Enabled}))
		}
	}
	return out
}

// Skill looks up a skill by id.
func (s *Settings) Skill(id string) (Skill, bool) {
	for _, sk := range s.SkillList() {
		if sk.ID == id {
			return sk, true
		}
	}
	return Skill{}, false
}

// EnabledSkills returns every program Socrates may start.
func (s *Settings) EnabledSkills() []Skill {
	out := make([]Skill, 0, len(s.Skills))
	for _, sk := range s.SkillList() {
		if sk.Enabled {
			out = append(out, sk)
		}
	}
	return out
}

// PublicURL is the address the tunnel publishes, when it is known upfront.
func (t TunnelSettings) PublicURL() string {
	if t.Hostname == "" {
		return ""
	}
	return "https://" + t.Hostname
}

// Slug turns arbitrary text into a safe identifier.
func Slug(in string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(in)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteRune('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// DataDir is the directory that holds the database and the workspaces.
func DataDir() string {
	if v := strings.TrimSpace(os.Getenv("SOCRATES_DATA_DIR")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".socrates"
	}
	return filepath.Join(home, ".socrates")
}

// DefaultWorkspaceRoot is where delegate agents get their working directories.
func DefaultWorkspaceRoot() string {
	if v := strings.TrimSpace(os.Getenv("SOCRATES_WORKSPACE_ROOT")); v != "" {
		return v
	}
	return filepath.Join(DataDir(), "workspaces")
}
