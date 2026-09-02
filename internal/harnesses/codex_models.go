package harnesses

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"time"
)

// modelsTimeout is the whole budget for asking a CLI what it can run. Nobody
// is held up by it - the catalogue is cached and refreshed behind requests -
// and the point of the bound is only that a wedged CLI cannot keep a
// subprocess for the life of the server.
const modelsTimeout = 20 * time.Second

// codexModels asks `codex debug models` for the raw catalogue.
//
// It is a plain subprocess that prints JSON on stdout, which replaces the
// app-server JSON-RPC handshake the old adapter used: that existed because the
// adapter needed an app-server anyway, and this package does not.
func codexModels(ctx context.Context, bin string) (Catalog, error) {
	if bin == "" {
		bin = "codex"
	}
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "debug", "models").Output()
	if err != nil {
		return Catalog{}, fmt.Errorf("codex would not list its models: %w", err)
	}
	return parseCodexModels(out)
}

// codexModelEntry is one entry of the catalogue, and only the fields a picker
// needs. The rest of each entry is the model's whole system prompt.
type codexModelEntry struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Default     string `json:"default_reasoning_level"`
	Visibility  string `json:"visibility"`
	Priority    int    `json:"priority"`
	Levels      []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

func parseCodexModels(raw []byte) (Catalog, error) {
	var doc struct {
		Models []codexModelEntry `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Catalog{}, fmt.Errorf("codex's model list is not what this build expects: %w", err)
	}
	entries := make([]codexModelEntry, 0, len(doc.Models))
	for _, m := range doc.Models {
		// "hide" is how the catalogue marks the models that exist for the
		// product's own machinery - a reserve model, the review model - and
		// are not something a person picks.
		if m.Slug == "" || m.Visibility == "hide" {
			continue
		}
		entries = append(entries, m)
	}
	// The catalogue's priority is the order Codex itself offers them in, and a
	// picker that reshuffles between two page loads is one nobody trusts.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Priority < entries[j].Priority })

	cat := Catalog{Models: make([]Model, 0, len(entries))}
	for i, m := range entries {
		label := m.DisplayName
		if label == "" {
			label = m.Slug
		}
		efforts := make([]string, 0, len(m.Levels))
		for _, l := range m.Levels {
			if l.Effort != "" {
				efforts = append(efforts, l.Effort)
			}
		}
		cat.Models = append(cat.Models, Model{
			ID:            m.Slug,
			Label:         label,
			Hint:          m.Description,
			Efforts:       OrderEfforts(efforts),
			DefaultEffort: m.Default,
			Default:       i == 0,
		})
	}
	return cat, nil
}
