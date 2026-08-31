// Package config holds the runtime settings of the application. Settings are
// stored as a single JSON document in the database so that everything can be
// changed from the admin dashboard without restarting the server.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
  bullet lists, and never mention the internal tool names.`

// Settings is the full configuration document.
type Settings struct {
	OpenRouter OpenRouterSettings `json:"openrouter"`
	Voice      VoiceSettings      `json:"voice"`
	Agent      AgentSettings      `json:"agent"`
	Tunnel     TunnelSettings     `json:"tunnel"`
	// Tools are the programs Socrates knows how to run in a terminal.
	Tools []Tool `json:"tools"`
	// Backends is the shape settings had before Socrates drove its tools
	// interactively. It is read once, migrated into Tools and then dropped.
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

// Tool is one program Socrates can run in a terminal session. There is nothing
// special about the coding agents: Claude Code, Codex and OpenCode are three
// entries below, and a fourth program is added by filling in the same fields.
type Tool struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Description tells Socrates when to reach for this tool. It goes into the
	// system prompt as written.
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

	// Driving is free text that goes into the system prompt: how to hand this
	// program a task, how to tell that it has finished, which keys answer its
	// questions. It is the whole extension mechanism - a new tool needs an
	// entry here, not a change to the code.
	Driving string `json:"driving"`
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

// CommandLine assembles the argv this tool is started with.
func (t Tool) CommandLine() (string, []string) {
	args := append([]string{}, t.Args...)
	if t.SkipPermissions {
		args = append(args, t.SkipArgs...)
	} else {
		args = append(args, t.AskArgs...)
	}
	if flag := strings.TrimSpace(t.ModelFlag); flag != "" && strings.TrimSpace(t.Model) != "" {
		args = append(args, flag, strings.TrimSpace(t.Model))
	}
	return t.Command, args
}

// Idle is the quiet window that means this tool has finished its turn.
func (t Tool) Idle() time.Duration {
	if t.IdleSeconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(t.IdleSeconds) * time.Second
}

// Timeout is the longest a single turn of this tool may take.
func (t Tool) Timeout() time.Duration {
	if t.TimeoutSeconds <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(t.TimeoutSeconds) * time.Second
}

// sandboxEnv tells Claude Code that it is already confined, which is what it
// wants to hear before it will skip permission prompts as root.
const sandboxEnv = "IS_SANDBOX=1"

// DefaultTools returns the three coding agents Socrates ships with. They are
// ordinary entries: nothing in the code treats them differently.
func DefaultTools() []Tool {
	return []Tool{
		{
			ID:      "claude",
			Name:    "Claude Code",
			Enabled: true,
			Description: "Best for writing, refactoring and debugging code in an existing " +
				"project, for multi step engineering tasks and for careful file edits.",
			Command:         "claude",
			ModelFlag:       "--model",
			SkipPermissions: true,
			SkipArgs:        []string{"--dangerously-skip-permissions"},
			// Claude Code refuses to skip permissions when it runs as root
			// unless it is told it is already sandboxed, which is exactly the
			// case here: Socrates often runs in a container.
			Env: []string{sandboxEnv},
			Driving: "Starts an interactive session in the working directory. The first time it " +
				"runs in a new folder it asks whether the folder is trusted: choose the " +
				"\"Yes, I trust this folder\" line with the down arrow and press enter. " +
				"It can look ready a moment before it accepts keys, so check that what you " +
				"typed actually appeared on the screen. " +
				"Type the brief into the prompt box at the bottom and press enter to send it. " +
				"It is working while a spinner line is visible and finished when the prompt box " +
				"is empty again. Press escape to interrupt it, and type /exit then enter to quit.",
			IdleSeconds:    5,
			TimeoutSeconds: 1800,
		},
		{
			ID:      "codex",
			Name:    "Codex",
			Enabled: true,
			Description: "Best for research, investigation and analysis: exploring an unfamiliar " +
				"codebase, gathering facts, comparing options and writing up findings.",
			Command:         "codex",
			Args:            []string{"--no-alt-screen"},
			ModelFlag:       "-m",
			SkipPermissions: true,
			SkipArgs:        []string{"--dangerously-bypass-approvals-and-sandbox"},
			AskArgs:         []string{"--ask-for-approval", "on-request", "--sandbox", "workspace-write"},
			Driving: "Starts an interactive session. On the way up it may show a numbered menu " +
				"twice: an update notice, where \"Skip\" is the right answer, and a question about " +
				"whether the directory is trusted, where \"Yes, continue\" is. Answer each by typing " +
				"the number and pressing enter, and look at the screen again afterwards - the second " +
				"menu only appears once the first is gone. It runs inline rather than on its own " +
				"screen, so an answered menu stays visible further up: judge what it wants from the " +
				"bottom of the screen, not from anything scrolled above it. Then type the brief at the prompt marked " +
				"with a single angle bracket and press enter. It answers below the prompt and is " +
				"finished when it stops printing. Press escape twice to interrupt it, and type /quit " +
				"then enter to leave.",
			IdleSeconds:    5,
			TimeoutSeconds: 1800,
		},
		{
			ID:      "opencode",
			Name:    "OpenCode",
			Enabled: false,
			Description: "Open source coding agent. Useful as an alternative implementer or for " +
				"quick scripted tasks.",
			Command:         "opencode",
			ModelFlag:       "-m",
			SkipPermissions: true,
			SkipArgs:        []string{"--auto"},
			Driving: "Starts an interactive session. Type the brief and press enter. Its answer " +
				"appears above the input box, followed by a line with the model name and how " +
				"long the turn took, which means it has finished. Press ctrl+c to interrupt " +
				"and type /exit then enter to quit.",
			IdleSeconds:    5,
			TimeoutSeconds: 1800,
		},
	}
}

// migrateBackend turns a pre terminal delegate agent into a tool.
func migrateBackend(b legacyBackend) Tool {
	tool := Tool{
		ID:              Slug(b.ID),
		Name:            b.Name,
		Enabled:         b.Enabled,
		Description:     b.Description,
		Command:         b.Command,
		Args:            b.ExtraArgs,
		Model:           b.Model,
		SkipPermissions: b.Approval != "ask",
		TimeoutSeconds:  b.TimeoutSeconds,
	}
	// Take everything else from the matching default, so an upgrade keeps the
	// user's own choices and gains the interactive settings.
	for _, d := range DefaultTools() {
		if d.ID != tool.ID && d.Command != tool.Command {
			continue
		}
		tool.ModelFlag = d.ModelFlag
		tool.SkipArgs = d.SkipArgs
		tool.AskArgs = d.AskArgs
		tool.Driving = d.Driving
		tool.ReadyPattern = d.ReadyPattern
		tool.IdleSeconds = d.IdleSeconds
		if len(tool.Args) == 0 {
			tool.Args = d.Args
		}
		break
	}
	return tool
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
		Tools: DefaultTools(),
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
	s.migrateBackends()
	if len(s.Tools) == 0 {
		s.Tools = d.Tools
	}
	seen := map[string]bool{}
	for i := range s.Tools {
		t := &s.Tools[i]
		t.ID = Slug(t.ID)
		if t.ID == "" {
			t.ID = Slug(t.Name)
		}
		if t.ID == "" {
			t.ID = "tool"
		}
		// Two tools with the same id would make the orchestrator's choice
		// ambiguous, so later duplicates are given a suffix.
		if seen[t.ID] {
			for n := 2; ; n++ {
				candidate := fmt.Sprintf("%s-%d", t.ID, n)
				if !seen[candidate] {
					t.ID = candidate
					break
				}
			}
		}
		seen[t.ID] = true

		if strings.TrimSpace(t.Name) == "" {
			t.Name = t.ID
		}
		if strings.TrimSpace(t.Command) == "" {
			t.Command = t.ID
		}
		if t.IdleSeconds <= 0 {
			t.IdleSeconds = 5
		}
		if t.IdleSeconds > 300 {
			t.IdleSeconds = 300
		}
		if t.TimeoutSeconds <= 0 {
			t.TimeoutSeconds = 1800
		}
		if t.Cols < 0 {
			t.Cols = 0
		}
		if t.Rows < 0 {
			t.Rows = 0
		}
		if t.Args == nil {
			t.Args = []string{}
		}
		if t.SkipArgs == nil {
			t.SkipArgs = []string{}
		}
		if t.AskArgs == nil {
			t.AskArgs = []string{}
		}
		if t.Env == nil {
			// A settings file written before tools had an environment gets the
			// default for this program, so an upgrade does not leave Claude
			// Code refusing to start as root. An empty list, which is what the
			// dashboard sends once the field has been cleared, is left alone.
			t.Env = defaultEnvFor(*t)
		}
		// A tool that cannot skip permissions has nothing to skip.
		if len(t.SkipArgs) == 0 && len(t.AskArgs) == 0 {
			t.SkipPermissions = false
		}
	}
}

// defaultEnvFor is the environment a tool gets when its settings file predates
// the field. It only recognises the programs Socrates ships with; anything else
// starts out with nothing extra.
func defaultEnvFor(t Tool) []string {
	for _, d := range DefaultTools() {
		if d.ID == t.ID || d.Command == t.Command {
			return append([]string{}, d.Env...)
		}
	}
	return []string{}
}

// migrateBackends folds a settings document written by an older version into
// the tool list, so upgrading keeps the agents the user had configured.
func (s *Settings) migrateBackends() {
	if len(s.Backends) == 0 {
		return
	}
	if len(s.Tools) == 0 {
		for _, b := range s.Backends {
			s.Tools = append(s.Tools, migrateBackend(b))
		}
	}
	s.Backends = nil
}

// Tool looks up a tool by id.
func (s *Settings) Tool(id string) (Tool, bool) {
	for _, t := range s.Tools {
		if t.ID == id {
			return t, true
		}
	}
	return Tool{}, false
}

// EnabledTools returns every tool Socrates may start.
func (s *Settings) EnabledTools() []Tool {
	out := make([]Tool, 0, len(s.Tools))
	for _, t := range s.Tools {
		if t.Enabled {
			out = append(out, t)
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
