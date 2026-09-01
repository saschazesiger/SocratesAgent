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
	Agent      AgentSettings      `json:"agent"`
	Agents     AgentsSettings     `json:"agents"`
	Tunnel     TunnelSettings     `json:"tunnel"`
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

// AgentSettings is what is left of the orchestrator's own configuration: where
// a chat works when it was not pointed anywhere else.
type AgentSettings struct {
	WorkspaceRoot string `json:"workspace_root"`
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

// AgentsSettings is what the user decides about the three programs Socrates
// can talk to. Everything else about them - how they are launched, what they
// say back - is the app's business, not a setting.
type AgentsSettings struct {
	Claude   AgentEntry `json:"claude"`
	Codex    AgentEntry `json:"codex"`
	OpenCode AgentEntry `json:"opencode"`
}

// AgentEntry is one agent's switch and the two overrides a person may need.
type AgentEntry struct {
	Enabled bool `json:"enabled"`
	// Binary overrides where the program is found. Empty means "look it up on
	// PATH", which is what a normal installation wants.
	Binary string `json:"binary,omitempty"`
	// ExtraArgs is appended to the command line, for the one flag a person
	// turns out to need and this app did not think of.
	ExtraArgs []string `json:"extra_args,omitempty"`
}

// Entry returns the settings for one agent id, and whether it is a known one.
func (a AgentsSettings) Entry(id string) (AgentEntry, bool) {
	switch id {
	case "claude":
		return a.Claude, true
	case "codex":
		return a.Codex, true
	case "opencode":
		return a.OpenCode, true
	}
	return AgentEntry{}, false
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
		Agent: AgentSettings{
			WorkspaceRoot: DefaultWorkspaceRoot(),
		},
		Agents: AgentsSettings{
			Claude:   AgentEntry{Enabled: true},
			Codex:    AgentEntry{Enabled: true},
			OpenCode: AgentEntry{Enabled: true},
		},
		Tunnel: TunnelSettings{
			Enabled: false,
			Mode:    TunnelQuick,
			Command: "cloudflared",
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
	s.Voice.Language = NormalizeLanguage(s.Voice.Language)
	if strings.TrimSpace(s.Voice.STTPrompt) == "" {
		s.Voice.STTPrompt = d.Voice.STTPrompt
	}
	// A rate of zero is what an older document that never had the setting
	// carries, and a negative one is nothing at all.
	if s.Voice.TTSRate <= 0 {
		s.Voice.TTSRate = 1
	}
	if strings.TrimSpace(s.Agent.WorkspaceRoot) == "" {
		s.Agent.WorkspaceRoot = d.Agent.WorkspaceRoot
	}
	s.normalizeAgents()
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

// normalizeAgents does the little there is to do: trim the two strings a
// person can type. Whether an agent is actually there is not a setting - it is
// a fact about the machine, and /api/agents is what reports it.
func (s *Settings) normalizeAgents() {
	for _, e := range []*AgentEntry{&s.Agents.Claude, &s.Agents.Codex, &s.Agents.OpenCode} {
		e.Binary = strings.TrimSpace(e.Binary)
		args := e.ExtraArgs[:0]
		for _, a := range e.ExtraArgs {
			if a = strings.TrimSpace(a); a != "" {
				args = append(args, a)
			}
		}
		if len(args) == 0 {
			e.ExtraArgs = nil
			continue
		}
		e.ExtraArgs = args
	}
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
