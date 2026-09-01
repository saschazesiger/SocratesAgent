package claude

import (
	"context"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// Discover returns the curated list. No Claude Code command lists models -
// `claude models` and `--list-models` both fail - so a hardcoded list is the
// only honest option, and Static says so out loud in the picker and the admin
// card. It is not a whitelist: POST /api/chats accepts any non-empty id for a
// static catalogue, so a new alias works the day it ships and a wrong one
// comes back as a clean run error from Claude itself.
func Discover(ctx context.Context, bin string) (harness.Catalog, error) {
	efforts := []string{"low", "medium", "high"}
	return harness.Catalog{
		Static: true,
		Models: []harness.Model{
			{ID: "opus", Label: "Opus", Hint: "the hardest work", Efforts: efforts, DefaultEffort: "medium"},
			{ID: "sonnet", Label: "Sonnet", Hint: "everyday coding", Efforts: efforts, DefaultEffort: "medium", Default: true},
			{ID: "haiku", Label: "Haiku", Hint: "fast and cheap", Efforts: efforts, DefaultEffort: "low"},
			{ID: "fable", Label: "Fable", Efforts: efforts, DefaultEffort: "medium"},
		},
	}, nil
}
