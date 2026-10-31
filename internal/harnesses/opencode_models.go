package harnesses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// openCodeModels asks the binary what it can run, once for every environment
// in envs - one per OpenCode account, each with its own auth.json - and
// merges the answers: a model one account's providers offer is on the list.
// No environment at all is the service's own.
//
// `opencode models --verbose` is the listing the TUI's model dialog is drawn
// from: every model of every connected provider, with its name, its context
// window, what it costs, whether it calls tools, and its variants - the
// reasoning levels ctrl+t cycles through, which are the efforts here. A build
// that does not know the flag falls through to the plain listing, one
// `provider/model` per line.
//
// The TUI's own state comes along: the models it used last (model.json's
// recent, the first of which is OpenCode's own default when nothing else
// names one), its favourites, and the variant it last ran each model at.
func openCodeModels(ctx context.Context, bin string, envs [][]string) (Catalog, error) {
	return openCodeModelsIn(ctx, bin, "", envs)
}

// openCodeModelsIn is openCodeModels asked in dir: OpenCode reads the
// project config of the folder it runs in, whose providers add models. An
// empty dir is this process's own working directory.
func openCodeModelsIn(ctx context.Context, bin, dir string, envs [][]string) (Catalog, error) {
	if bin == "" {
		bin = "opencode"
	}
	if len(envs) == 0 {
		envs = [][]string{nil}
	}
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout*time.Duration(len(envs)))
	defer cancel()

	var (
		lists    [][]openCodeModel
		states   []openCodeState
		names    map[string]string
		firstErr error
	)
	for _, env := range envs {
		list, err := listOpenCodeModels(ctx, bin, dir, env)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		lists = append(lists, list)
		states = append(states, readOpenCodeState(env))
		if names == nil {
			names = readProviderNames(env)
		}
	}
	if len(lists) == 0 {
		return Catalog{}, firstErr
	}
	return mergeOpenCode(lists, states, names), nil
}

// listOpenCodeModels is one environment's listing, in dir.
func listOpenCodeModels(ctx context.Context, bin, dir string, env []string) ([]openCodeModel, error) {
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		if env != nil {
			cmd.Env = env
		}
		return cmd.Output()
	}
	if out, err := run("models", "--verbose"); err == nil {
		if list, ok := parseOpenCodeVerbose(out); ok {
			return list, nil
		}
	}
	if out, err := run("models", "--json"); err == nil {
		if cat, ok := parseOpenCodeJSON(out); ok {
			return modelsOf(cat), nil
		}
	}
	out, err := run("models")
	if err != nil {
		return nil, fmt.Errorf("opencode would not list its models: %w", err)
	}
	return modelsOf(parseOpenCodeList(string(out))), nil
}

// openCodeModel is one model of the verbose listing, and only the fields a
// picker uses.
type openCodeModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Cost       struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
	Limit struct {
		Context int64 `json:"context"`
	} `json:"limit"`
	Capabilities *struct {
		Reasoning bool `json:"reasoning"`
		ToolCall  bool `json:"toolcall"`
		Input     struct {
			Image bool `json:"image"`
			PDF   bool `json:"pdf"`
		} `json:"input"`
	} `json:"capabilities"`
	Variants map[string]json.RawMessage `json:"variants"`

	// full is provider/model, what -m and a prompt's model take.
	full string
}

// modelsOf turns an id-only catalogue back into listing entries, for the
// older builds' fallbacks.
func modelsOf(cat Catalog) []openCodeModel {
	out := make([]openCodeModel, 0, len(cat.Models))
	for _, m := range cat.Models {
		provider, id, _ := strings.Cut(m.ID, "/")
		out = append(out, openCodeModel{ID: id, ProviderID: provider, full: m.ID})
	}
	return out
}

// parseOpenCodeVerbose reads `opencode models --verbose`: for every model a
// line with its provider/model id and then the model as an indented JSON
// object. Only the objects are read - each carries its own provider and id -
// so a line of anything else between them does no harm. Not one object is
// "not this format", and the caller tries the next one.
func parseOpenCodeVerbose(raw []byte) ([]openCodeModel, bool) {
	var out []openCodeModel
	rest := raw
	for {
		start := bytes.IndexByte(rest, '{')
		if start < 0 {
			break
		}
		dec := json.NewDecoder(bytes.NewReader(rest[start:]))
		var m openCodeModel
		if err := dec.Decode(&m); err != nil {
			// Not an object after all: step over the brace and look on.
			rest = rest[start+1:]
			continue
		}
		rest = rest[start+int(dec.InputOffset()):]
		if m.ID == "" || m.ProviderID == "" {
			continue
		}
		m.full = m.ProviderID + "/" + m.ID
		out = append(out, m)
	}
	return out, len(out) > 0
}

// openCodeState is what the TUI keeps about models in model.json.
type openCodeState struct {
	Recent   []openCodeRef     `json:"recent"`
	Favorite []openCodeRef     `json:"favorite"`
	Variant  map[string]string `json:"variant"`
}

type openCodeRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

func (r openCodeRef) id() string {
	if r.ProviderID == "" || r.ModelID == "" {
		return ""
	}
	return r.ProviderID + "/" + r.ModelID
}

// readOpenCodeState reads the TUI's model.json of an environment: in
// XDG_STATE_HOME/opencode, or ~/.local/state/opencode. A missing or broken
// file is a TUI that was never used there.
func readOpenCodeState(env []string) openCodeState {
	var st openCodeState
	dir := xdgDir(env, "XDG_STATE_HOME", ".local/state")
	if dir == "" {
		return st
	}
	raw, err := os.ReadFile(filepath.Join(dir, "opencode", "model.json"))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	return st
}

// readProviderNames is the providers' own names ("OpenRouter", "xAI") from
// the models.dev catalogue OpenCode keeps in its cache. The listing names a
// provider only by its id; the id is what is shown when the cache has none.
func readProviderNames(env []string) map[string]string {
	names := map[string]string{}
	dir := xdgDir(env, "XDG_CACHE_HOME", ".cache")
	if dir == "" {
		return names
	}
	raw, err := os.ReadFile(filepath.Join(dir, "opencode", "models.json"))
	if err != nil {
		return names
	}
	var doc map[string]struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return names
	}
	for id, p := range doc {
		if p.Name != "" {
			names[id] = p.Name
		}
	}
	return names
}

// xdgDir is an XDG directory as a process with env would find it: the
// variable when it is set, else fallback under HOME. A nil env is this
// process's own.
func xdgDir(env []string, key, fallback string) string {
	if env == nil {
		env = os.Environ()
	}
	value, home := "", ""
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case key:
			value = v
		case "HOME":
			home = v
		}
	}
	if value != "" {
		return value
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, fallback)
}

// mergeOpenCode is the catalogue of every account's listing and TUI state.
//
// A model that cannot call tools is left out: an agent that cannot read a
// file or run a command is not one a coding chat can use, and the image and
// video models are most of what that drops. A deprecated one is left out as
// the TUI leaves it out.
//
// The picker's order is the provider and then the name; Recent and
// Favorites say which of them come first. The default is the model OpenCode
// itself would start on: the most recent one the TUI used that is still on
// the list.
func mergeOpenCode(lists [][]openCodeModel, states []openCodeState, names map[string]string) Catalog {
	seen := map[string]bool{}
	var models []openCodeModel
	for _, list := range lists {
		for _, m := range list {
			if seen[m.full] || m.Status == "deprecated" {
				continue
			}
			if m.Capabilities != nil && !m.Capabilities.ToolCall {
				continue
			}
			seen[m.full] = true
			models = append(models, m)
		}
	}

	variant := map[string]string{}
	var recent, favorites []string
	inRecent, inFavorites := map[string]bool{}, map[string]bool{}
	add := func(into *[]string, in map[string]bool, id string) {
		if id != "" && seen[id] && !in[id] {
			in[id] = true
			*into = append(*into, id)
		}
	}
	for _, st := range states {
		for _, r := range st.Recent {
			add(&recent, inRecent, r.id())
		}
		for _, r := range st.Favorite {
			add(&favorites, inFavorites, r.id())
		}
		for id, v := range st.Variant {
			if _, ok := variant[id]; !ok && v != "" && v != "default" {
				variant[id] = v
			}
		}
	}

	cat := Catalog{Models: make([]Model, 0, len(models)), Recent: recent, Favorites: favorites}
	for _, m := range models {
		efforts := make([]string, 0, len(m.Variants))
		for v := range m.Variants {
			efforts = append(efforts, v)
		}
		sort.Strings(efforts)
		efforts = OrderEfforts(efforts)
		def := variant[m.full]
		if !contains(efforts, def) {
			def = ""
		}
		group := names[m.ProviderID]
		if group == "" {
			group = m.ProviderID
		}
		label := m.Name
		if label == "" {
			label = m.ID
		}
		cat.Models = append(cat.Models, Model{
			ID: m.full, Label: label, Group: group, Hint: openCodeHint(m),
			Efforts: efforts, DefaultEffort: def,
			Default: len(recent) > 0 && m.full == recent[0],
		})
	}
	// A picker that reshuffles between two page loads is one nobody trusts,
	// and the listing's own order is the provider catalogue's, not ours.
	sort.SliceStable(cat.Models, func(i, j int) bool {
		a, b := cat.Models[i], cat.Models[j]
		if ga, gb := strings.ToLower(a.Group), strings.ToLower(b.Group); ga != gb {
			return ga < gb
		}
		if la, lb := strings.ToLower(a.Label), strings.ToLower(b.Label); la != lb {
			return la < lb
		}
		return a.ID < b.ID
	})
	return cat
}

func contains(list []string, v string) bool {
	for _, one := range list {
		if one == v {
			return true
		}
	}
	return false
}

// openCodeHint is the line under a model's name: how much it reads at once,
// whether it is free, and what it takes besides text. The id is not on it -
// the provider is the line's group, and the picker searches the id anyway.
func openCodeHint(m openCodeModel) string {
	var parts []string
	if c := contextWords(m.Limit.Context); c != "" {
		parts = append(parts, c+" context")
	}
	switch {
	case m.Capabilities == nil:
	case m.Cost.Input == 0 && m.Cost.Output == 0:
		parts = append(parts, "free")
	default:
		parts = append(parts, fmt.Sprintf("$%s/$%s per 1M", price(m.Cost.Input), price(m.Cost.Output)))
	}
	if m.Capabilities != nil && m.Capabilities.Input.Image {
		parts = append(parts, "images")
	}
	return strings.Join(parts, " · ")
}

// contextWords is a context window the way a person says it: 200k, 1M.
func contextWords(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n >= 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1_000_000)) + "M"
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

func price(v float64) string { return trimZero(fmt.Sprintf("%.2f", v)) }

func trimZero(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
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
