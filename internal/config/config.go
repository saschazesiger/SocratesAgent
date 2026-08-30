// Package config holds the runtime settings of the application. Settings are
// stored as a single JSON document in the database so that everything can be
// changed from the admin dashboard without restarting the server.
package config

import (
	"os"
	"path/filepath"
	"strings"
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
  bullet lists, and never mention the internal tool names.`

// Settings is the full configuration document.
type Settings struct {
	OpenRouter OpenRouterSettings `json:"openrouter"`
	Voice      VoiceSettings      `json:"voice"`
	Agent      AgentSettings      `json:"agent"`
	Tunnel     TunnelSettings     `json:"tunnel"`
	Backends   []Backend          `json:"backends"`
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

// VoiceSettings configures speech to text and text to speech.
//
// OpenRouter itself only exposes chat completions, so transcription happens by
// sending the recorded audio to a multimodal chat model. Speech synthesis is
// done in the browser by default and can optionally be routed to any
// OpenAI compatible /audio/speech endpoint.
type VoiceSettings struct {
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
	TTSLanguage string  `json:"tts_language"`

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

// Backend describes one delegate agent CLI.
type Backend struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // claude | codex | opencode | custom
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"` // when should the orchestrator use it

	Command   string   `json:"command"`
	ExtraArgs []string `json:"extra_args"`
	Model     string   `json:"model"`

	// Approval is "auto" (the agent runs unattended) or "ask" (permission
	// requests are surfaced in the web UI). "ask" is fully interactive for the
	// claude backend and maps to a restrictive sandbox for the others.
	Approval string `json:"approval"`
	// Sandbox is used by the codex backend: read-only | workspace-write |
	// danger-full-access.
	Sandbox        string `json:"sandbox"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Kinds that ship with a dedicated event parser.
const (
	KindClaude   = "claude"
	KindCodex    = "codex"
	KindOpenCode = "opencode"
	KindCustom   = "custom"
)

// DefaultBackends returns the three agents that Socrates knows out of the box.
func DefaultBackends() []Backend {
	return []Backend{
		{
			ID:      "claude",
			Kind:    KindClaude,
			Name:    "Claude Code",
			Enabled: true,
			Description: "Best for writing, refactoring and debugging code in an existing " +
				"project, for multi step engineering tasks and for careful file edits.",
			Command:        "claude",
			Approval:       "auto",
			TimeoutSeconds: 1800,
		},
		{
			ID:      "codex",
			Kind:    KindCodex,
			Name:    "Codex",
			Enabled: true,
			Description: "Best for research, investigation and analysis: exploring an unfamiliar " +
				"codebase, gathering facts, comparing options and writing up findings.",
			Command:        "codex",
			Approval:       "auto",
			Sandbox:        "workspace-write",
			TimeoutSeconds: 1800,
		},
		{
			ID:      "opencode",
			Kind:    KindOpenCode,
			Name:    "OpenCode",
			Enabled: false,
			Description: "Open source coding agent. Useful as an alternative implementer or for " +
				"quick scripted tasks.",
			Command:        "opencode",
			Approval:       "auto",
			TimeoutSeconds: 1800,
		},
	}
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
		Backends: DefaultBackends(),
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
	if s.Backends == nil {
		s.Backends = d.Backends
	}
	for i := range s.Backends {
		b := &s.Backends[i]
		b.ID = Slug(b.ID)
		if b.ID == "" {
			b.ID = Slug(b.Name)
		}
		if b.ID == "" {
			b.ID = "agent"
		}
		switch b.Kind {
		case KindClaude, KindCodex, KindOpenCode, KindCustom:
		default:
			b.Kind = KindCustom
		}
		if strings.TrimSpace(b.Name) == "" {
			b.Name = b.ID
		}
		if strings.TrimSpace(b.Command) == "" {
			b.Command = b.ID
		}
		if b.Approval != "ask" {
			b.Approval = "auto"
		}
		switch b.Sandbox {
		case "read-only", "workspace-write", "danger-full-access":
		default:
			b.Sandbox = "workspace-write"
		}
		if b.TimeoutSeconds <= 0 {
			b.TimeoutSeconds = 1800
		}
	}
}

// Backend looks up an enabled backend by id.
func (s *Settings) Backend(id string) (Backend, bool) {
	for _, b := range s.Backends {
		if b.ID == id {
			return b, true
		}
	}
	return Backend{}, false
}

// EnabledBackends returns every backend the orchestrator may delegate to.
func (s *Settings) EnabledBackends() []Backend {
	out := make([]Backend, 0, len(s.Backends))
	for _, b := range s.Backends {
		if b.Enabled {
			out = append(out, b)
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
