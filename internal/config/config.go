// Package config holds the runtime settings of the application. Settings are
// stored as a single JSON document in the database so that everything can be
// changed from the admin dashboard without restarting the server.
package config

import (
	"os"
	"path/filepath"
	"strings"

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
)

// Settings is the full configuration document.
type Settings struct {
	OpenRouter OpenRouterSettings `json:"openrouter"`
	Voice      VoiceSettings      `json:"voice"`
	Tunnel     TunnelSettings     `json:"tunnel"`
	Workspace  WorkspaceSettings  `json:"workspace"`
	Terminal   TerminalSettings   `json:"terminal"`
	Harnesses  HarnessSettings    `json:"harnesses"`
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
// always goes to the transcription model in OpenRouterSettings, chosen from
// the catalogue like every other model in the app. Who reads the answer back
// is no longer a decision at all: Piper does, on this machine, with the voice
// of the language below - which is why there is no provider here, no model, no
// voice name and no key.
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

	// TTSRate is how fast the answer is read, where 1 is the pace the voice
	// was trained at. It reaches Piper as the length of every phoneme, so half
	// the rate is a sentence that takes twice as long.
	TTSRate float64 `json:"tts_rate"`

	SpeakInAutoMode bool `json:"speak_in_auto_mode"`
	SpeakInChatMode bool `json:"speak_in_chat_mode"`
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
		},
		Voice: VoiceSettings{
			Language:        DefaultLanguage,
			STTPrompt:       "Transcribe the spoken audio verbatim. Reply with the transcript only, no commentary, no quotes.",
			TTSRate:         1,
			SpeakInAutoMode: true,
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
	s.Voice.Language = NormalizeLanguage(s.Voice.Language)
	if strings.TrimSpace(s.Voice.STTPrompt) == "" {
		s.Voice.STTPrompt = d.Voice.STTPrompt
	}
	// A rate of zero is a field the admin form left behind, and a negative one
	// is nothing at all.
	if s.Voice.TTSRate <= 0 {
		s.Voice.TTSRate = 1
	}
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
