package harnesses

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// openCodeModels asks the binary what it can run.
//
// `opencode models --json` is tried first, because a machine-readable answer
// is worth one extra process; 1.17.13 has no such flag and prints its usage
// instead, so anything that is not JSON falls through to the plain listing,
// which is one `provider/model` per line and is the id OpenCode's own -m takes.
func openCodeModels(ctx context.Context, bin string) (Catalog, error) {
	if bin == "" {
		bin = "opencode"
	}
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()

	if out, err := exec.CommandContext(ctx, bin, "models", "--json").Output(); err == nil {
		if cat, ok := parseOpenCodeJSON(out); ok {
			return cat, nil
		}
	}
	out, err := exec.CommandContext(ctx, bin, "models").Output()
	if err != nil {
		return Catalog{}, fmt.Errorf("opencode would not list its models: %w", err)
	}
	return parseOpenCodeList(string(out)), nil
}

// parseOpenCodeJSON accepts either a bare array of ids or an array of objects
// carrying one, and says so rather than erroring: "this is not JSON" is the
// expected answer from a version without the flag.
func parseOpenCodeJSON(raw []byte) (Catalog, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || (trimmed[0] != '[' && trimmed[0] != '{') {
		return Catalog{}, false
	}
	var ids []string
	if json.Unmarshal(raw, &ids) == nil {
		return catalogFromIDs(ids), true
	}
	var entries []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		ProviderID string `json:"providerID"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return Catalog{}, false
	}
	models := make([]string, 0, len(entries))
	for _, e := range entries {
		switch {
		case e.ProviderID != "" && e.ID != "":
			models = append(models, e.ProviderID+"/"+e.ID)
		case e.ID != "":
			models = append(models, e.ID)
		}
	}
	return catalogFromIDs(models), true
}

func parseOpenCodeList(out string) Catalog {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "/") || strings.ContainsAny(line, " \t") {
			continue
		}
		ids = append(ids, line)
	}
	return catalogFromIDs(ids)
}

// catalogFromIDs shapes the list. The id is kept whole - it is what -m takes -
// and the provider, everything before the first slash, becomes the group the
// picker sorts under. A model id may itself contain slashes, so only the first
// one separates them.
func catalogFromIDs(ids []string) Catalog {
	cat := Catalog{Models: make([]Model, 0, len(ids))}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		provider, name, ok := strings.Cut(id, "/")
		if !ok {
			provider, name = "", id
		}
		cat.Models = append(cat.Models, Model{ID: id, Label: name, Group: provider})
	}
	// A picker that reshuffles between two page loads is one nobody trusts,
	// and the listing's own order is the provider catalogue's, not ours.
	sort.SliceStable(cat.Models, func(i, j int) bool {
		if cat.Models[i].Group != cat.Models[j].Group {
			return cat.Models[i].Group < cat.Models[j].Group
		}
		return cat.Models[i].ID < cat.Models[j].ID
	})
	return cat
}
