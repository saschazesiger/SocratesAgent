package claude

import (
	"context"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// Discover returns the curated list. No Claude Code command lists models -
// `claude models` and `--list-models` both fail, and the docs (checked against
// 2.1.258) offer nothing non-interactive - so a hardcoded list of the aliases
// the docs name is the only honest option, and Static says so out loud in the
// picker and the admin card. It is not a whitelist: POST /api/chats accepts any non-empty id for a
// static catalogue, so a new alias works the day it ships and a wrong one
// comes back as a clean run error from Claude itself.
func Discover(ctx context.Context, bin string) (harness.Catalog, error) {
	// `claude --help` names these five for --effort (checked against 2.1.258).
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	return harness.Catalog{
		Static: true,
		Models: []harness.Model{
			{ID: "opus", Label: "Opus", Hint: "the hardest work", Efforts: efforts, DefaultEffort: "medium"},
			{ID: "sonnet", Label: "Sonnet", Hint: "everyday coding", Efforts: efforts, DefaultEffort: "medium", Default: true},
			{ID: "haiku", Label: "Haiku", Hint: "fast and cheap", Efforts: efforts, DefaultEffort: "low"},
			{ID: "fable", Label: "Fable", Efforts: efforts, DefaultEffort: "medium"},
			{ID: "best", Label: "Best", Hint: "the most capable model your plan has", Efforts: efforts, DefaultEffort: "medium"},
			{ID: "opusplan", Label: "Opus plan", Hint: "Opus to plan, Sonnet to build", Efforts: efforts, DefaultEffort: "medium"},
			{ID: "opus[1m]", Label: "Opus 1M", Hint: "Opus with a 1M context", Efforts: efforts, DefaultEffort: "medium"},
			{ID: "sonnet[1m]", Label: "Sonnet 1M", Hint: "Sonnet with a 1M context", Efforts: efforts, DefaultEffort: "medium"},
		},
	}, nil
}
