// Package config holds the runtime settings of the application. Settings are
// stored as a single JSON document in the database so that everything can be
// changed from the admin dashboard without restarting the server.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/googletts"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
)

// Defaults that are used on a fresh installation.
const (
	// Where OpenRouter lives. The address belongs to the client that talks to
	// it, so it is named there and only borrowed here - two copies of a URL
	// are two things to keep in step.
	DefaultOpenRouterBaseURL = openrouter.DefaultBaseURL
	DefaultTranscribeModel   = "google/gemini-2.5-flash"
	DefaultTitleModel        = "google/gemini-2.5-flash-lite"

	// DefaultStatusModel describes a screen out loud: a short answer, several
	// times a session, on a phone that is waiting for it. Fast and cheap wins,
	// and it is the family that already transcribes here.
	DefaultStatusModel = "google/gemini-2.5-flash"
	// DefaultAgentModel drives the terminal. It has to read a TUI - a menu, a
	// permission prompt, a half painted spinner - and answer with keystrokes,
	// which is the one job in this app where a cheap model is a false economy.
	DefaultAgentModel = "anthropic/claude-sonnet-5"

	// DefaultAgentMaxSteps is how many look-decide-type rounds one operator run
	// may take, and MinAgentMaxSteps/MaxAgentMaxSteps are what the dashboard
	// may set it to: one step is a legitimate "press Enter for me", and past
	// forty a run that is going nowhere is going nowhere expensively.
	DefaultAgentMaxSteps = 12
	MinAgentMaxSteps     = 1
	MaxAgentMaxSteps     = 40
)

// Settings is the full configuration document.
type Settings struct {
	OpenRouter OpenRouterSettings `json:"openrouter"`
	Voice      VoiceSettings      `json:"voice"`
	Tunnel     TunnelSettings     `json:"tunnel"`
	Workspace  WorkspaceSettings  `json:"workspace"`
	Terminal   TerminalSettings   `json:"terminal"`
	Harnesses  HarnessSettings    `json:"harnesses"`
	Agent      AgentSettings      `json:"agent"`
}

// OpenRouterSettings configures access to the OpenRouter API. Since Socrates
// became a harness, OpenRouter answers nothing: it transcribes what was spoken
// and it writes chat titles. The coding agents bring their own credentials.
type OpenRouterSettings struct {
	APIKey string `json:"api_key"`
	// BaseURL is not a choice the dashboard offers. It exists so that the app
	// can be pointed at a stand in for OpenRouter in its own tests, and it is
	// filled with the real address on every settings document that does not
	// carry one.
	BaseURL         string `json:"base_url"`
	TranscribeModel string `json:"transcribe_model"`
	TitleModel      string `json:"title_model"`
	// StatusModel reads a screen and says what it means; AgentModel reads the
	// same screen and decides what to type. They are two settings rather than
	// one because the first wants to be cheap and the second wants to be
	// right, and nothing in the app validates either id: there is no
	// catalogue endpoint left, so both are free text and a model that does not
	// exist is only found out when OpenRouter refuses it.
	StatusModel string `json:"status_model"`
	AgentModel  string `json:"agent_model"`
}

// AgentSettings is the one policy lever the operator loop has, and the bound
// on how long it may keep going.
//
// There is deliberately no blocklist of dangerous commands here: the loop
// types into a terminal that already runs as this user, and a model that
// wanted to could spell `rm -rf` across two actions. What is real is the
// switch below, which keeps the operator away from a bare shell on a shared
// deployment, and a run that is bounded, cancellable and visible step by step.
type AgentSettings struct {
	// AllowShell decides whether the operator may drive a Shell session at
	// all. It is on by default, because a Socrates on a laptop is a Socrates
	// with one user.
	AllowShell bool `json:"allow_shell"`
	// MaxSteps is how many rounds one run may take before it stops by itself.
	MaxSteps int `json:"max_steps"`
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
}

var spokenLanguages = map[string]spokenLanguage{
	LanguageEN: {Name: "English"},
	LanguageDE: {Name: "German"},
}

// LanguageName is the English name of a language, which is the form a model
// understands in an instruction: "English" or "German".
func LanguageName(code string) string { return spokenLanguages[NormalizeLanguage(code)].Name }

// NormalizeLanguage maps whatever is in the settings document onto one of the
// two languages. A regional tag such as "de-CH" counts as its base language,
// and anything else - an empty field included - becomes the default.
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

// VoiceSettings configures speech to text and text to speech. The recording
// always goes to the transcription model in OpenRouterSettings. Reading an
// answer back is Google Cloud Text-to-Speech, which needs one credential of
// its own - an API key - and picks a voice from the language below.
type VoiceSettings struct {
	// Language is the language Socrates speaks: "en" or "de". One setting
	// drives all three sides of it - which language the transcript is written
	// in, which voice reads the answer out loud, and which language the agent
	// writes that answer in - because a conversation where those three
	// disagree is worse than any of them being wrong alone.
	Language string `json:"language"`

	// STTPrompt is sent with the recording when an audio capable chat model
	// does the transcribing. A dedicated transcription model is given the
	// language instead, which is what it understands.
	STTPrompt string `json:"stt_prompt"`

	// GoogleAPIKey is the credential for Google Cloud Text-to-Speech. It is
	// stored and handed to the dashboard exactly like the OpenRouter key: the
	// dashboard is already behind the password, and a key it cannot read back
	// is a key nobody can check.
	GoogleAPIKey string `json:"google_api_key"`

	// GoogleVoiceEN and GoogleVoiceDE are the voice names per language. They
	// default to Standard voices, which is the tier with four million
	// characters a month free; a WaveNet, Neural2 or Studio name works here
	// and is billed from the first character.
	GoogleVoiceEN string `json:"google_voice_en"`
	GoogleVoiceDE string `json:"google_voice_de"`

	// TTSRate is how fast the answer is read, where 1 is the pace the voice
	// was trained at. It reaches Google as speakingRate, which it accepts
	// between 0.25 and 4.
	TTSRate float64 `json:"tts_rate"`
}

// GoogleVoice is the voice name for a language, falling back to the shipped
// Standard voice when the dashboard left the field empty.
func (v VoiceSettings) GoogleVoice(language string) string {
	name := v.GoogleVoiceEN
	if NormalizeLanguage(language) == LanguageDE {
		name = v.GoogleVoiceDE
	}
	if strings.TrimSpace(name) == "" {
		return googletts.DefaultVoice(language)
	}
	return strings.TrimSpace(name)
}

// WorkspaceSettings decides where a session may work. Root is where a
// "dynamic" session gets its own fresh directory; Presets are the places the
// new-session sheet offers by name; AllowCustom decides whether a path may be
// typed in at all, because on a machine that is published through a tunnel
// "anywhere on the disk" is a decision and not a default.
type WorkspaceSettings struct {
	Root           string      `json:"root"`
	DefaultHarness string      `json:"default_harness"`
	Presets        []PresetDir `json:"presets"`
	AllowCustom    bool        `json:"allow_custom"`
}

// PresetDir is one quick pick: a label a person recognises and the path it
// stands for.
type PresetDir struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// TerminalSettings is how the terminal itself behaves - the tmux side of it
// and the xterm.js side, in one place, because to the person changing them
// they are one thing.
//
// There is deliberately no fixed size here. Under the "manual" policy Socrates
// sizes the window to the viewer that owns it, so a fixed size would be a
// setting with nothing to apply it to; the fallback when nobody is connecting
// is a constant.
type TerminalSettings struct {
	// WindowSize is tmux's policy when several browsers watch one session:
	// "manual" (Socrates decides), "latest" (the last one to act) or
	// "largest".
	WindowSize string `json:"window_size"`
	// HistoryLimit is how many lines tmux keeps per pane, Scrollback how many
	// the browser keeps. They are two buffers, and only the first survives a
	// reload.
	HistoryLimit int  `json:"history_limit"`
	Mouse        bool `json:"mouse"`
	ExtendedKeys bool `json:"extended_keys"`
	Scrollback   int  `json:"scrollback"`
	FontSize     int  `json:"font_size"`
	// WebGL renders with the GPU where there is one; the browser falls back on
	// its own when there is not.
	WebGL bool `json:"webgl"`
}

// The tmux window-size policies Socrates offers.
var WindowSizePolicies = []string{"manual", "latest", "largest"}

// The reasoning effort levels. Each agent names its own - Claude Code takes
// low, medium, high, xhigh and max, Codex reports a list per model, OpenCode
// names its effort "variants" per model - and the picker offers exactly what
// the chosen model reports. This list is only what a settings document may
// carry at all; whether a model takes a level is that model's answer.
//
// EffortDefault - the empty string - means "do not say anything about it" and
// leaves the program on whatever it would have chosen for itself.
const (
	EffortDefault = ""
	EffortMinimal = "minimal"
	EffortLow     = "low"
	EffortMedium  = "medium"
	EffortHigh    = "high"
	EffortXHigh   = "xhigh"
	EffortMax     = "max"
	EffortUltra   = "ultra"
)

// KnownEfforts is every level above, from least to most.
var KnownEfforts = []string{EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra}

// NormalizeEffort maps whatever is in the settings document onto one of the
// known levels. Anything else - a typo, an empty field - becomes the
// program's own default, which is always safe.
func NormalizeEffort(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	for _, known := range KnownEfforts {
		if level == known {
			return known
		}
	}
	return EffortDefault
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

// Default returns a fresh settings document, seeded from the environment where
// that is useful for container deployments.
func Default() Settings {
	return Settings{
		OpenRouter: OpenRouterSettings{
			APIKey:          os.Getenv("OPENROUTER_API_KEY"),
			BaseURL:         DefaultOpenRouterBaseURL,
			TranscribeModel: DefaultTranscribeModel,
			TitleModel:      DefaultTitleModel,
			StatusModel:     DefaultStatusModel,
			AgentModel:      DefaultAgentModel,
		},
		Voice: VoiceSettings{
			Language:      DefaultLanguage,
			STTPrompt:     "Transcribe the spoken audio verbatim. Reply with the transcript only, no commentary, no quotes.",
			GoogleAPIKey:  os.Getenv("GOOGLE_TTS_API_KEY"),
			GoogleVoiceEN: googletts.DefaultVoiceEN,
			GoogleVoiceDE: googletts.DefaultVoiceDE,
			TTSRate:       1,
		},
		Tunnel: TunnelSettings{
			Enabled: false,
			Mode:    TunnelQuick,
			Command: "cloudflared",
		},
		Workspace: WorkspaceSettings{
			Root:           DefaultWorkspaceRoot(),
			DefaultHarness: HarnessClaude,
			AllowCustom:    true,
		},
		Terminal: TerminalSettings{
			WindowSize:   "manual",
			HistoryLimit: 20000,
			Mouse:        true,
			Scrollback:   2000,
			FontSize:     14,
			WebGL:        true,
		},
		Harnesses: DefaultHarnesses(),
		Agent: AgentSettings{
			AllowShell: true,
			MaxSteps:   DefaultAgentMaxSteps,
		},
	}
}

// Normalize fills in empty fields with their defaults so that a partially
// written settings document from the admin UI never breaks the server.
func (s *Settings) Normalize() {
	d := Default()
	if strings.TrimSpace(s.OpenRouter.BaseURL) == "" {
		s.OpenRouter.BaseURL = d.OpenRouter.BaseURL
	}
	s.OpenRouter.BaseURL = strings.TrimRight(strings.TrimSpace(s.OpenRouter.BaseURL), "/")
	if strings.TrimSpace(s.OpenRouter.TranscribeModel) == "" {
		s.OpenRouter.TranscribeModel = d.OpenRouter.TranscribeModel
	}
	// There is no chat model left to fall back to: OpenRouter writes titles and
	// nothing else, so an empty field takes the shipped title model.
	if strings.TrimSpace(s.OpenRouter.TitleModel) == "" {
		s.OpenRouter.TitleModel = d.OpenRouter.TitleModel
	}
	// The two models the Status button and the operator loop use. An empty
	// field is a form that was never filled in, not a request for no model:
	// without one the button would answer "pick a model" on a fresh install,
	// which is a setup step nobody asked for.
	if strings.TrimSpace(s.OpenRouter.StatusModel) == "" {
		s.OpenRouter.StatusModel = d.OpenRouter.StatusModel
	}
	if strings.TrimSpace(s.OpenRouter.AgentModel) == "" {
		s.OpenRouter.AgentModel = d.OpenRouter.AgentModel
	}
	s.normalizeAgent(d)
	s.Voice.Language = NormalizeLanguage(s.Voice.Language)
	if strings.TrimSpace(s.Voice.STTPrompt) == "" {
		s.Voice.STTPrompt = d.Voice.STTPrompt
	}
	s.Voice.GoogleAPIKey = strings.TrimSpace(s.Voice.GoogleAPIKey)
	if strings.TrimSpace(s.Voice.GoogleVoiceEN) == "" {
		s.Voice.GoogleVoiceEN = d.Voice.GoogleVoiceEN
	}
	if strings.TrimSpace(s.Voice.GoogleVoiceDE) == "" {
		s.Voice.GoogleVoiceDE = d.Voice.GoogleVoiceDE
	}
	s.Voice.GoogleVoiceEN = strings.TrimSpace(s.Voice.GoogleVoiceEN)
	s.Voice.GoogleVoiceDE = strings.TrimSpace(s.Voice.GoogleVoiceDE)
	// A rate of zero is a field the admin form left behind, and a negative one
	// is nothing at all; outside Google's range it would be a refused request.
	s.Voice.TTSRate = googletts.ClampRate(s.Voice.TTSRate)
	s.normalizeWorkspace(d)
	s.normalizeTerminal(d)
	s.Harnesses.normalize()
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
}

// normalizeAgent bounds the operator loop. A zero is the number field of a
// form that was left empty rather than a run that may take no steps, so it
// takes the shipped default; anything above the ceiling is brought down to it.
func (s *Settings) normalizeAgent(d Settings) {
	switch {
	case s.Agent.MaxSteps <= 0:
		s.Agent.MaxSteps = d.Agent.MaxSteps
	case s.Agent.MaxSteps < MinAgentMaxSteps:
		s.Agent.MaxSteps = MinAgentMaxSteps
	case s.Agent.MaxSteps > MaxAgentMaxSteps:
		s.Agent.MaxSteps = MaxAgentMaxSteps
	}
}

// normalizeWorkspace keeps the two facts a session is created from usable: a
// root it can always fall back to, and a default harness that exists. A preset
// with no path is dropped, and one with no label is named after its path, so
// the picker never shows a blank row.
func (s *Settings) normalizeWorkspace(d Settings) {
	if strings.TrimSpace(s.Workspace.Root) == "" {
		s.Workspace.Root = d.Workspace.Root
	}
	s.Workspace.Root = strings.TrimSpace(s.Workspace.Root)
	if !IsHarness(s.Workspace.DefaultHarness) {
		s.Workspace.DefaultHarness = d.Workspace.DefaultHarness
	}
	var presets []PresetDir
	seen := map[string]bool{}
	for _, p := range s.Workspace.Presets {
		path := strings.TrimSpace(p.Path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		label := strings.TrimSpace(p.Label)
		if label == "" {
			label = filepath.Base(path)
		}
		presets = append(presets, PresetDir{Label: label, Path: path})
	}
	s.Workspace.Presets = presets
}

// normalizeTerminal replaces the sizes an empty form leaves behind. A zero
// scrollback or a zero font size is a field that was never filled in, not a
// terminal somebody wants.
func (s *Settings) normalizeTerminal(d Settings) {
	s.Terminal.WindowSize = oneOf(s.Terminal.WindowSize, WindowSizePolicies, d.Terminal.WindowSize)
	if s.Terminal.HistoryLimit < 100 {
		s.Terminal.HistoryLimit = d.Terminal.HistoryLimit
	}
	if s.Terminal.Scrollback < 100 {
		s.Terminal.Scrollback = d.Terminal.Scrollback
	}
	if s.Terminal.FontSize < 8 || s.Terminal.FontSize > 40 {
		s.Terminal.FontSize = d.Terminal.FontSize
	}
}

// Validate reports what is wrong with a settings document that Normalize
// cannot put right on its own. Normalize is total by design - it always
// produces a usable document - so this is where a person's typing is refused
// instead of quietly discarded, and it is what the dashboard answers 400 with.
//
// Call it after Normalize: the two together are what saving a document means.
func (s *Settings) Validate() error {
	return s.Harnesses.validate()
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

// DefaultWorkspaceRoot is where a session with a dynamic working directory
// gets one.
func DefaultWorkspaceRoot() string {
	if v := strings.TrimSpace(os.Getenv("SOCRATES_WORKSPACE_ROOT")); v != "" {
		return v
	}
	return filepath.Join(DataDir(), "workspaces")
}
