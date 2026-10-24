package harnesses

import "context"

// The three coding agents run as chats (internal/agent, AGENT-HOST.md): their
// launch, their policy and their sign-in are the adapters' and the accounts'.
// What is left of them here is what the catalogue asks of each: its name, the
// binary it is looked up as, how its version is read, and the models it can
// be started on (claude_models.go, codex_models.go, opencode_models.go). None
// of them is a Terminal: the terminal they ran in before is gone (REBUILD.md
// §1).

// Claude is Claude Code.
type Claude struct{}

func (Claude) Kind() Kind            { return KindClaude }
func (Claude) Label() string         { return "Claude Code" }
func (Claude) DefaultBinary() string { return "claude" }
func (Claude) VersionArgs() []string { return []string{"--version"} }

// DiscoverModels returns the curated list; see claude_models.go.
func (Claude) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return claudeModels(), nil
}

// Codex is the Codex CLI.
type Codex struct{}

func (Codex) Kind() Kind            { return KindCodex }
func (Codex) Label() string         { return "Codex" }
func (Codex) DefaultBinary() string { return "codex" }
func (Codex) VersionArgs() []string { return []string{"--version"} }

// DiscoverModels asks the binary for its catalogue; see codex_models.go.
func (Codex) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return codexModels(ctx, bin)
}

// OpenCode is OpenCode.
type OpenCode struct{}

func (OpenCode) Kind() Kind            { return KindOpenCode }
func (OpenCode) Label() string         { return "OpenCode" }
func (OpenCode) DefaultBinary() string { return "opencode" }
func (OpenCode) VersionArgs() []string { return []string{"--version"} }

// DiscoverModels asks the binary what it can run; see opencode_models.go.
func (OpenCode) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return openCodeModels(ctx, bin)
}
