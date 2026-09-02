package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// discoverTimeout is the whole budget for "start a server, ask it what it can
// run, stop it". A cold OpenCode start loads its provider catalogue over the
// network, so it is generous.
const discoverTimeout = 30 * time.Second

// modelsPoll is how often the server is asked again while it reports no
// provider at all. Providers resolve their credentials in the background:
// for the first second or so after the server is listening and healthy, the
// list is empty, and a discovery that asked once would report a machine with
// working credentials as having no models at all. Measured at 1.0-1.4 s on a
// warm cache against opencode 1.17.13. The polling runs inside discoverTimeout
// rather than a shorter budget of its own, so a slow machine is given the same
// thirty seconds as everything else here; an install with genuinely nothing
// connected waits them out once and then answers honestly.
const modelsPoll = 200 * time.Millisecond

// Discover asks a short-lived `opencode serve` which models it can run.
//
// GET /config/providers is the right question: it is what OpenCode's own
// model picker is built from - every provider whose credentials resolved,
// with that provider's full model list. GET /api/model is not: against
// 1.17.13 it names only the free "opencode" provider's models, so a machine
// with OpenAI, OpenRouter and two more providers connected was shown thirty
// free models and nothing it had paid for. It stays as the fallback for a
// server without the providers route.
//
// The providers answer carries each provider's resolved API key. The client
// decodes it into a struct with no field for it, so the key stops there:
// nothing in a Catalog has ever seen it.
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

	models, err := connectedModels(ctx, cli)
	if err != nil {
		return harness.Catalog{}, err
	}
	// A picker that reshuffles between two page loads is a picker nobody
	// trusts, and the server's order is the provider catalogue's, not ours.
	sort.SliceStable(models, func(i, j int) bool {
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
func connectedModels(ctx context.Context, cli *client) ([]harness.Model, error) {
	useProviders := true
	for {
		var models []harness.Model
		if useProviders {
			providers, err := cli.providers(ctx)
			switch {
			case err == nil:
				models = fromProviders(providers)
			case isNoRoute(err):
				useProviders = false
				continue
			default:
				return nil, err
			}
		} else {
			entries, err := cli.models(ctx)
			if err != nil {
				return nil, err
			}
			models = fromModelList(entries)
		}
		if len(models) > 0 {
			return models, nil
		}
		select {
		case <-time.After(modelsPoll):
		case <-ctx.Done():
			return models, nil
		}
	}
}

// isNoRoute is true for the 404 an older server answers on a route it does
// not have.
func isNoRoute(err error) bool {
	return err != nil && strings.Contains(err.Error(), "404")
}

// fromProviders flattens the providers answer. A model the catalogue marks as
// anything but active - deprecated, retired - is left out; OpenCode's own
// picker hides those too.
//
// No entry is flagged as the default. The answer's "default" map is one
// model per provider, not one for the install - on a machine with OpenRouter
// connected it names an image model there - and the model OpenCode itself
// starts on is decided elsewhere (opencode.json, then the last one used). A
// picker that starts on nothing and asks is more honest than one that starts
// on the wrong thing.
func fromProviders(providers []providerEntry) []harness.Model {
	var out []harness.Model
	for _, p := range providers {
		if p.ID == "" {
			continue
		}
		for id, e := range p.Models {
			if e.ID == "" {
				e.ID = id
			}
			if e.Status != "" && e.Status != "active" {
				continue
			}
			out = append(out, shape(p.ID, e))
		}
	}
	return out
}

// fromModelList shapes the /api/model answer, for a server without the
// providers route.
func fromModelList(entries []modelEntry) []harness.Model {
	out := make([]harness.Model, 0, len(entries))
	for _, e := range entries {
		if e.ID == "" || e.ProviderID == "" {
			continue
		}
		out = append(out, shape(e.ProviderID, e))
	}
	return out
}

// shape turns one server entry into a catalogue row: the id is
// provider|model (a pipe, not a slash: model ids contain slashes), the hint
// is the id with the context window and the input price when the catalogue
// knows them, the way the OpenRouter pickers read.
func shape(providerID string, e modelEntry) harness.Model {
	label := e.Name
	if label == "" {
		label = e.ID
	}
	bits := []string{e.ID}
	if e.Limit.Context > 0 {
		bits = append(bits, contextWindow(e.Limit.Context))
	}
	if e.Cost.Input > 0 {
		bits = append(bits, inputPrice(e.Cost.Input))
	}
	return harness.Model{
		ID:      providerID + "|" + e.ID,
		Label:   label,
		Hint:    strings.Join(bits, " · "),
		Group:   providerID,
		Efforts: harness.OrderEfforts(variantNames(e.Variants)),
	}
}

func contextWindow(n int64) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%dM ctx", n/1_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM ctx", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk ctx", n/1000)
	}
	return fmt.Sprintf("%d ctx", n)
}

// inputPrice is dollars per million input tokens, as the catalogue quotes it.
func inputPrice(perMillion float64) string {
	switch {
	case perMillion < 1:
		return fmt.Sprintf("$%.2f/M", perMillion)
	case perMillion < 100:
		return fmt.Sprintf("$%.1f/M", perMillion)
	}
	return fmt.Sprintf("$%.0f/M", perMillion)
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
