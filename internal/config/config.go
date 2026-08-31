// Package config holds the runtime settings of the application. Settings are
// stored as a single JSON document in the database so that everything can be
// changed from the admin dashboard without restarting the server.
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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

	// Internet access defaults.
	DefaultTavilyBaseURL     = "https://api.tavily.com"
	DefaultJinaSearchBaseURL = "https://s.jina.ai"
	DefaultJinaReaderBaseURL = "https://r.jina.ai"
	DefaultSearchResults     = 5
	DefaultFetchChars        = 12000
	MaxFetchChars            = 40000
)

// Search providers and fetch engines.
const (
	SearchOpenRouter = "openrouter"
	SearchTavily     = "tavily"
	SearchJina       = "jina"

	FetchLocal = "local"
	FetchJina  = "jina"
)

// DefaultSystemPrompt is the instruction set of the top level agent. It is
// user editable in the admin dashboard.
const DefaultSystemPrompt = `You are Socrates, a top level orchestration agent.

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
  bullet lists, and never mention the internal tool names.`

// InternetPrompt is appended to the system prompt when the internet tools are
// switched on. It only makes sense while those tools exist, which is why it
// lives beside the prompt rather than inside it.
const InternetPrompt = `You can also reach the open internet.

- Use web_search whenever the answer depends on something current or on a fact
  you are not sure of: a release version, a price, an API that may have changed,
  today's news, a library's documentation. Searching is cheaper than guessing.
- Use web_fetch to read a page in full: a URL the user gave you, or the most
  promising result of a search. Search gives you snippets, web_fetch gives you
  the page.
- Cite the URL you used in your answer, so the user can check it.
- Do not open a terminal session or reach for curl to look something up. These
  two tools are the way you read the web.`

// Settings is the full configuration document.
type Settings struct {
	OpenRouter OpenRouterSettings `json:"openrouter"`
	Voice      VoiceSettings      `json:"voice"`
	Agent      AgentSettings      `json:"agent"`
	Internet   InternetSettings   `json:"internet"`
	Tunnel     TunnelSettings     `json:"tunnel"`
	// Skills are the programs Socrates knows how to operate, each with its
	// own manual for driving it.
	Skills []Skill `json:"skills"`
	// Tools is the shape skills had before they were called skills. It is read
	// once, migrated into Skills and then dropped.
	Tools []legacyTool `json:"tools,omitempty"`
	// Backends is the shape settings had before Socrates drove its programs
	// interactively. It is read once, migrated into Skills and then dropped.
	Backends []legacyBackend `json:"backends,omitempty"`
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

// InternetSettings configures the two tools that let Socrates read the web:
// a search and a fetch. Both are off until someone turns them on, because an
// agent that can reach the open internet is a different thing from one that
// can only reach this machine.
type InternetSettings struct {
	Enabled bool `json:"enabled"`

	// SearchProvider is "openrouter", "tavily" or "jina". OpenRouter needs no
	// second account - it bills the search to the key that is already there.
	SearchProvider string `json:"search_provider"`
	TavilyAPIKey   string `json:"tavily_api_key"`
	// JinaAPIKey is optional: Jina answers without a key at a lower rate limit.
	JinaAPIKey string `json:"jina_api_key"`
	// SearchModel is the model that runs the OpenRouter web plugin. Empty
	// means the ordinary chat model.
	SearchModel string `json:"search_model"`
	MaxResults  int    `json:"max_results"`

	// FetchEngine is "local" (fetch and extract here) or "jina" (Jina Reader,
	// which also copes with PDFs and pages that need JavaScript).
	FetchEngine string `json:"fetch_engine"`

	// SearchBaseURL and FetchBaseURL are advanced overrides, meant for tests
	// and for a self hosted mirror of one of these APIs. Empty means the
	// provider's own address.
	SearchBaseURL string `json:"search_base_url"`
	FetchBaseURL  string `json:"fetch_base_url"`
}

// SearchEndpoint is the base URL of the configured search provider.
func (i InternetSettings) SearchEndpoint() string {
	if v := strings.TrimRight(strings.TrimSpace(i.SearchBaseURL), "/"); v != "" {
		return v
	}
	if i.SearchProvider == SearchJina {
		return DefaultJinaSearchBaseURL
	}
	return DefaultTavilyBaseURL
}

// FetchEndpoint is the base URL of the Jina Reader.
func (i InternetSettings) FetchEndpoint() string {
	if v := strings.TrimRight(strings.TrimSpace(i.FetchBaseURL), "/"); v != "" {
		return v
	}
	return DefaultJinaReaderBaseURL
}

// AgentSettings configures the orchestration loop.
type AgentSettings struct {
	SystemPrompt  string  `json:"system_prompt"`
	MaxIterations int     `json:"max_iterations"`
	Temperature   float64 `json:"temperature"`
	WorkspaceRoot string  `json:"workspace_root"`
}

// Skill is one program Socrates knows how to operate. A skill is the whole
// extension mechanism: it says how the program is started and it carries a
// manual, in plain prose written by whoever added it, for driving the program
// the way a person would. Claude Code, Codex and OpenCode are three ordinary
// skills that happen to ship with the app; a fourth program is added by
// filling in the same fields in the dashboard, no code change needed.
type Skill struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Preset is the id of the shipped preset this skill was made from, and is
	// empty for a skill someone wrote themselves. It is what lets the
	// dashboard offer "reset to preset" and re-add a preset that was removed.
	Preset string `json:"preset"`
	// Description tells Socrates when to reach for this skill. It goes into
	// the system prompt as written.
	Description string `json:"description"`

	// Command and Args are how the program is started.
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// Env is extra environment for the program, as KEY=VALUE lines. It is the
	// declarative form of writing "KEY=VALUE program" in a shell, and some
	// programs need it: Claude Code refuses --dangerously-skip-permissions as
	// root unless IS_SANDBOX=1 is set.
	Env []string `json:"env"`
	// Model is passed after ModelFlag when both are set. These are the
	// program's own model names, not OpenRouter ids.
	Model     string `json:"model"`
	ModelFlag string `json:"model_flag"`

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
	// the default and the point of the whole app: the user is watching that
	// terminal and wants to read along and take the keyboard. Nil counts as
	// true, so a settings document written before this field existed does not
	// silently open the headless door.
	InteractiveOnly *bool `json:"interactive_only"`
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
	// IdleSeconds is how long the program has to stay quiet before Socrates
	// treats its turn as finished.
	IdleSeconds    int `json:"idle_seconds"`
	TimeoutSeconds int `json:"timeout_seconds"`
	// Cols and Rows size the window this program gets. Zero means the default.
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Interactive reports whether this skill may only be driven in a terminal
// session. An unset field means yes, which is the safe answer.
func (s Skill) Interactive() bool { return s.InteractiveOnly == nil || *s.InteractiveOnly }

// legacyTool is the shape a skill had while it was still called a tool. Its
// single free text "driving" field became the Notes section of the manual.
type legacyTool struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	Description     string   `json:"description"`
	Command         string   `json:"command"`
	Args            []string `json:"args"`
	Env             []string `json:"env"`
	Model           string   `json:"model"`
	ModelFlag       string   `json:"model_flag"`
	SkipPermissions bool     `json:"skip_permissions"`
	SkipArgs        []string `json:"skip_permission_args"`
	AskArgs         []string `json:"ask_permission_args"`
	Driving         string   `json:"driving"`
	ReadyPattern    string   `json:"ready_pattern"`
	IdleSeconds     int      `json:"idle_seconds"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
	Cols            int      `json:"cols"`
	Rows            int      `json:"rows"`
}

// legacyBackend is the pre terminal shape of a delegate agent, kept only long
// enough to migrate an existing installation.
type legacyBackend struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	Description    string   `json:"description"`
	Command        string   `json:"command"`
	ExtraArgs      []string `json:"extra_args"`
	Model          string   `json:"model"`
	Approval       string   `json:"approval"`
	Sandbox        string   `json:"sandbox"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// CommandLine assembles the argv this skill is started with.
func (s Skill) CommandLine() (string, []string) {
	args := append([]string{}, s.Args...)
	if s.SkipPermissions {
		args = append(args, s.SkipArgs...)
	} else {
		args = append(args, s.AskArgs...)
	}
	if flag := strings.TrimSpace(s.ModelFlag); flag != "" && strings.TrimSpace(s.Model) != "" {
		args = append(args, flag, strings.TrimSpace(s.Model))
	}
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

// alwaysTrue is the interactive-only default in a form a pointer field can
// hold.
func alwaysTrue() *bool { yes := true; return &yes }

// Presets are the skills Socrates ships with. Their manuals were written
// against the versions named in them - Claude Code 2.1.251, codex-cli 0.146.0,
// opencode 1.17.13 - by driving each program in a terminal and writing down
// what it actually did.
//
// A preset is an ordinary skill: nothing in the code treats these three
// differently, and an installation that already has skills is never given a
// new one behind the user's back. The dashboard offers them instead.
func Presets() []Skill {
	return []Skill{
		{
			ID:      "claude",
			Name:    "Claude Code",
			Enabled: true,
			Preset:  "claude",
			Description: "Best for writing, refactoring and debugging code in an existing project, for multi step " +
				"engineering tasks and for careful file edits.",
			Command:         "claude",
			Env:             []string{sandboxEnv},
			ModelFlag:       "--model",
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
				"The animated first-run screens draw words by moving the cursor rather than by printing " +
				"spaces, so words can look glued together in the screen you read back. Judge those " +
				"screens by their choices, not by their prose.",
			InteractiveOnly: alwaysTrue(),
			HeadlessForms: "`-p` / `--print`, `--output-format json`, `--output-format stream-json`, `--input-format " +
				"stream-json`, `--bare`, `--json-schema`, a prompt piped in on stdin (`cat brief.md | " +
				"claude -p ...`), and the background session commands `--bg`, `claude attach`, `claude " +
				"agents`, `claude logs` and `claude stop`.",
			IdleSeconds:    5,
			TimeoutSeconds: 1800,
		},
		{
			ID:      "codex",
			Name:    "Codex",
			Enabled: true,
			Preset:  "codex",
			Description: "Best for research, investigation and analysis: exploring an unfamiliar codebase, " +
				"gathering facts, comparing options and writing up findings.",
			Command:         "codex",
			Args:            []string{"--no-alt-screen"},
			ModelFlag:       "-m",
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
			InteractiveOnly: alwaysTrue(),
			HeadlessForms: "`codex exec` (alias `e`), `codex exec --json`, `codex exec -o` / " +
				"`--output-last-message`, `codex exec resume`, `codex exec review`, `codex review`, " +
				"`codex mcp-server` and `codex app-server`.",
			IdleSeconds:    5,
			TimeoutSeconds: 1800,
		},
		{
			ID:      "opencode",
			Name:    "OpenCode",
			Enabled: false,
			Preset:  "opencode",
			Description: "Open source coding agent. Useful as an alternative implementer, or as a second opinion " +
				"when another agent is stuck.",
			Command:         "opencode",
			ModelFlag:       "-m",
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
			InteractiveOnly: alwaysTrue(),
			HeadlessForms: "`opencode run` (including `--format json`, `-f` / `--file`, `-c`, `-s` and `--agent`), " +
				"`opencode serve`, `opencode web`, `opencode attach <url>` and `opencode acp`.",
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

// fillFromPreset copies the manual sections a migrated skill has no answer for
// out of the preset it came from. An upgrade this way gains the verified
// manual without losing anything the user wrote.
func fillFromPreset(s *Skill) {
	preset, ok := PresetByID(s.Preset)
	if !ok {
		return
	}
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&s.Description, preset.Description},
		{&s.Startup, preset.Startup},
		{&s.GivingTasks, preset.GivingTasks},
		{&s.ReadingState, preset.ReadingState},
		{&s.Answering, preset.Answering},
		{&s.Exiting, preset.Exiting},
		{&s.Notes, preset.Notes},
		{&s.HeadlessForms, preset.HeadlessForms},
	} {
		if strings.TrimSpace(*f.dst) == "" {
			*f.dst = f.src
		}
	}
}

// presetIDFor guesses which preset an older entry corresponds to, by id first
// and by the command it runs second.
func presetIDFor(id, command string) string {
	presets := Presets()
	for _, p := range presets {
		if p.ID == id {
			return p.ID
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

// migrateTool turns a pre skill tool into a skill. Its one free text field was
// everything the model knew about driving the program, so it becomes the notes
// section and the rest of the manual is taken from the matching preset.
func migrateTool(t legacyTool) Skill {
	s := Skill{
		ID:              t.ID,
		Name:            t.Name,
		Enabled:         t.Enabled,
		Preset:          presetIDFor(t.ID, t.Command),
		Description:     t.Description,
		Command:         t.Command,
		Args:            t.Args,
		Env:             t.Env,
		Model:           t.Model,
		ModelFlag:       t.ModelFlag,
		SkipPermissions: t.SkipPermissions,
		SkipArgs:        t.SkipArgs,
		AskArgs:         t.AskArgs,
		Notes:           t.Driving,
		InteractiveOnly: alwaysTrue(),
		ReadyPattern:    t.ReadyPattern,
		IdleSeconds:     t.IdleSeconds,
		TimeoutSeconds:  t.TimeoutSeconds,
		Cols:            t.Cols,
		Rows:            t.Rows,
	}
	fillFromPreset(&s)
	return s
}

// migrateBackend turns a pre terminal delegate agent into a skill.
func migrateBackend(b legacyBackend) Skill {
	s := Skill{
		ID:              Slug(b.ID),
		Name:            b.Name,
		Enabled:         b.Enabled,
		Description:     b.Description,
		Command:         b.Command,
		Args:            b.ExtraArgs,
		Model:           b.Model,
		SkipPermissions: b.Approval != "ask",
		TimeoutSeconds:  b.TimeoutSeconds,
		InteractiveOnly: alwaysTrue(),
	}
	s.Preset = presetIDFor(s.ID, s.Command)
	// Take everything else from the matching preset, so an upgrade keeps the
	// user's own choices and gains the interactive settings.
	if preset, ok := PresetByID(s.Preset); ok {
		s.Env = preset.Env
		s.ModelFlag = preset.ModelFlag
		s.SkipArgs = preset.SkipArgs
		s.AskArgs = preset.AskArgs
		s.ReadyPattern = preset.ReadyPattern
		s.IdleSeconds = preset.IdleSeconds
		if len(s.Args) == 0 {
			s.Args = preset.Args
		}
	}
	fillFromPreset(&s)
	return s
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
		Internet: InternetSettings{
			Enabled:        false,
			SearchProvider: SearchOpenRouter,
			MaxResults:     DefaultSearchResults,
			FetchEngine:    FetchLocal,
		},
		Tunnel: TunnelSettings{
			Enabled: false,
			Mode:    TunnelQuick,
			Command: "cloudflared",
		},
		Skills: Presets(),
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
	switch s.Internet.SearchProvider {
	case SearchTavily, SearchJina:
	default:
		s.Internet.SearchProvider = SearchOpenRouter
	}
	if s.Internet.FetchEngine != FetchJina {
		s.Internet.FetchEngine = FetchLocal
	}
	if s.Internet.MaxResults <= 0 {
		s.Internet.MaxResults = DefaultSearchResults
	}
	if s.Internet.MaxResults > 10 {
		s.Internet.MaxResults = 10
	}
	s.Internet.TavilyAPIKey = strings.TrimSpace(s.Internet.TavilyAPIKey)
	s.Internet.JinaAPIKey = strings.TrimSpace(s.Internet.JinaAPIKey)
	s.Internet.SearchModel = strings.TrimSpace(s.Internet.SearchModel)
	s.Internet.SearchBaseURL = strings.TrimRight(strings.TrimSpace(s.Internet.SearchBaseURL), "/")
	s.Internet.FetchBaseURL = strings.TrimRight(strings.TrimSpace(s.Internet.FetchBaseURL), "/")
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
	s.migrateSkills()
	if s.Skills == nil {
		// Only a settings document that has never heard of skills gets the
		// presets. An empty list is a decision - someone removed everything
		// and saved - and it is left alone.
		s.Skills = d.Skills
	}
	seen := map[string]bool{}
	for i := range s.Skills {
		sk := &s.Skills[i]
		sk.ID = Slug(sk.ID)
		if sk.ID == "" {
			sk.ID = Slug(sk.Name)
		}
		if sk.ID == "" {
			sk.ID = "skill"
		}
		// Two skills with the same id would make the orchestrator's choice
		// ambiguous, so later duplicates are given a suffix.
		if seen[sk.ID] {
			for n := 2; ; n++ {
				candidate := fmt.Sprintf("%s-%d", sk.ID, n)
				if !seen[candidate] {
					sk.ID = candidate
					break
				}
			}
		}
		seen[sk.ID] = true

		if strings.TrimSpace(sk.Name) == "" {
			sk.Name = sk.ID
		}
		if strings.TrimSpace(sk.Command) == "" {
			sk.Command = sk.ID
		}
		if sk.InteractiveOnly == nil {
			// A skill that does not say otherwise is driven in a terminal.
			sk.InteractiveOnly = alwaysTrue()
		}
		if sk.IdleSeconds <= 0 {
			sk.IdleSeconds = 5
		}
		if sk.IdleSeconds > 300 {
			sk.IdleSeconds = 300
		}
		if sk.TimeoutSeconds <= 0 {
			sk.TimeoutSeconds = 1800
		}
		if sk.Cols < 0 {
			sk.Cols = 0
		}
		if sk.Rows < 0 {
			sk.Rows = 0
		}
		if sk.Args == nil {
			sk.Args = []string{}
		}
		if sk.SkipArgs == nil {
			sk.SkipArgs = []string{}
		}
		if sk.AskArgs == nil {
			sk.AskArgs = []string{}
		}
		if sk.Env == nil {
			// A settings file written before skills had an environment gets
			// the default for this program, so an upgrade does not leave
			// Claude Code refusing to start as root. An empty list, which is
			// what the dashboard sends once the field has been cleared, is
			// left alone.
			sk.Env = defaultEnvFor(*sk)
		}
		// A program that cannot skip permissions has nothing to skip.
		if len(sk.SkipArgs) == 0 && len(sk.AskArgs) == 0 {
			sk.SkipPermissions = false
		}
	}
}

// staleDefaultMarkers name tools that the shipped prompt used to describe and
// that no longer exist. A saved prompt mentioning one of them is not something
// a user wrote: it is a copy of an old default that the dashboard stored the
// first time anyone opened it, and leaving it in place teaches every model to
// reach for tools that are gone.
var staleDefaultMarkers = []string{"delegate_to_agent", "ask_user"}

// promptMigrationOnce keeps the log line to one, however often settings are
// read and normalized.
var promptMigrationOnce sync.Once

// adoptCurrentPrompt replaces a stored copy of a long dead default prompt with
// the current one. A prompt somebody actually edited never mentions those
// tools, so nothing hand written is thrown away.
func (s *Settings) adoptCurrentPrompt() {
	stored := s.Agent.SystemPrompt
	if strings.TrimSpace(stored) == "" || stored == DefaultSystemPrompt {
		return
	}
	for _, marker := range staleDefaultMarkers {
		if strings.Contains(stored, marker) {
			s.Agent.SystemPrompt = DefaultSystemPrompt
			promptMigrationOnce.Do(func() {
				log.Printf("config: the saved system prompt still described %s, which no longer exists - "+
					"replaced it with the current default", marker)
			})
			return
		}
	}
}

// defaultEnvFor is the environment a skill gets when its settings file
// predates the field. It only recognises the programs Socrates ships with;
// anything else starts out with nothing extra.
func defaultEnvFor(s Skill) []string {
	if id := presetIDFor(s.ID, s.Command); id != "" {
		if p, ok := PresetByID(id); ok {
			return append([]string{}, p.Env...)
		}
	}
	return []string{}
}

// migrateSkills folds a settings document written by an older version into the
// skill list, so upgrading keeps the programs the user had configured. Only a
// document with no skills key at all is filled in: a list that exists is the
// user's, empty or not, and new presets are offered in the dashboard rather
// than added behind their back.
func (s *Settings) migrateSkills() {
	if s.Skills == nil {
		for _, t := range s.Tools {
			s.Skills = append(s.Skills, migrateTool(t))
		}
	}
	if s.Skills == nil {
		for _, b := range s.Backends {
			s.Skills = append(s.Skills, migrateBackend(b))
		}
	}
	s.Tools = nil
	s.Backends = nil
}

// Skill looks up a skill by id.
func (s *Settings) Skill(id string) (Skill, bool) {
	for _, sk := range s.Skills {
		if sk.ID == id {
			return sk, true
		}
	}
	return Skill{}, false
}

// EnabledSkills returns every program Socrates may start.
func (s *Settings) EnabledSkills() []Skill {
	out := make([]Skill, 0, len(s.Skills))
	for _, sk := range s.Skills {
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
