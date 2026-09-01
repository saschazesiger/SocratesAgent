package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// discoverTimeout is the whole budget for "start a server, ask it what it can
// run, stop it". A cold OpenCode start loads its provider catalogue over the
// network, so it is generous.
const discoverTimeout = 30 * time.Second

// modelsPoll is how often GET /api/model is asked again while it is empty.
// Providers resolve their credentials in the background: for the first second
// or so after the server is listening and healthy, /api/model answers 200 with
// an empty list, and a discovery that asked once would report a machine with
// working credentials as having no models at all. Measured at 1.0-1.4 s on a
// warm cache against opencode 1.17.13. The polling runs inside discoverTimeout
// rather than a shorter budget of its own, so a slow machine is given the same
// thirty seconds as everything else here; an install with genuinely nothing
// connected waits them out once and then answers honestly.
const modelsPoll = 200 * time.Millisecond

// Discover asks a short-lived `opencode serve` which models it has a connected
// provider for.
//
// GET /api/model is the right question: it lists only providers that actually
// resolved credentials at boot, which is what the picker should offer. The
// fuller GET /config/providers is never called - it returns the plaintext API
// key.
func Discover(ctx context.Context, bin string) (harness.Catalog, error) {
	if bin == "" {
		found, err := exec.LookPath("opencode")
		if err != nil {
			return harness.Catalog{}, fmt.Errorf("opencode: %w", err)
		}
		bin = found
	}
	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()

	srv, cli, err := startServer(ctx, bin, "", nil, nil)
	if err != nil {
		return harness.Catalog{}, err
	}
	defer srv.stop()

	entries, err := connectedModels(ctx, cli)
	if err != nil {
		return harness.Catalog{}, err
	}

	models := make([]harness.Model, 0, len(entries))
	for _, e := range entries {
		if e.ID == "" || e.ProviderID == "" {
			continue
		}
		label := e.Name
		if label == "" {
			label = e.ID
		}
		models = append(models, harness.Model{
			// A pipe, not a slash: model ids contain slashes.
			ID:      e.ProviderID + "|" + e.ID,
			Label:   label,
			Group:   e.ProviderID,
			Efforts: harness.FilterEfforts(variantNames(e.Variants)),
		})
	}
	// A picker that reshuffles between two page loads is a picker nobody
	// trusts, and /api/model's order is the provider catalogue's, not ours.
	sort.Slice(models, func(i, j int) bool {
		if models[i].Group != models[j].Group {
			return models[i].Group < models[j].Group
		}
		return models[i].ID < models[j].ID
	})
	return harness.Catalog{Models: models}, nil
}

// connectedModels asks for the model list until it is not empty any more, or
// until the discovery budget is up - at which point an empty list is the
// answer, because an install with no credentials really does have no models.
func connectedModels(ctx context.Context, cli *client) ([]modelEntry, error) {
	for {
		entries, err := cli.models(ctx)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return entries, nil
		}
		select {
		case <-time.After(modelsPoll):
		case <-ctx.Done():
			return entries, nil
		}
	}
}

// variantNames reads a model's reasoning-effort presets. GET /api/model
// reports "no variants" as an empty array and the catalogue reports them as a
// map keyed by name, so both shapes are accepted (F-13); inside an array, an
// entry may be the name itself or an object carrying it.
func variantNames(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var asMap map[string]json.RawMessage
	if json.Unmarshal(raw, &asMap) == nil {
		out := make([]string, 0, len(asMap))
		for name := range asMap {
			out = append(out, name)
		}
		sort.Strings(out)
		return out
	}
	var asList []json.RawMessage
	if json.Unmarshal(raw, &asList) != nil {
		return nil
	}
	var out []string
	for _, item := range asList {
		var s string
		if json.Unmarshal(item, &s) == nil {
			out = append(out, s)
			continue
		}
		var obj struct {
			Name            string `json:"name"`
			ReasoningEffort string `json:"reasoningEffort"`
		}
		if json.Unmarshal(item, &obj) == nil {
			switch {
			case obj.ReasoningEffort != "":
				out = append(out, obj.ReasoningEffort)
			case obj.Name != "":
				out = append(out, obj.Name)
			}
		}
	}
	return out
}
