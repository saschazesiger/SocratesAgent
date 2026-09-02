package harnesses

// Catalog is what a harness can be run on, as its own CLI reports it.
type Catalog struct {
	Models []Model `json:"models"`
	Static bool    `json:"static"` // true when the list is curated, not discovered
	Notes  string  `json:"notes,omitempty"`
}

// Model is one entry of that list, in the harness's own naming - "sonnet",
// "gpt-5.6-sol", "opencode/big-pickle" - never an OpenRouter id.
type Model struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Hint          string   `json:"hint,omitempty"`
	Group         string   `json:"group,omitempty"`   // provider, for opencode
	Efforts       []string `json:"efforts,omitempty"` // subset of low/medium/high, empty = no effort
	DefaultEffort string   `json:"default_effort,omitempty"`
	Default       bool     `json:"default,omitempty"`
}

// EffortOrder is every reasoning-effort level any of the harnesses names, from
// least to most. It is an ordering, not a whitelist: a model offers whatever
// its CLI reports for it, and this only decides how those are lined up.
var EffortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// OrderEfforts lines a model's reported levels up in EffortOrder, drops the
// repeats, and keeps anything it has never heard of at the end in the order
// it arrived - a level the CLI names is a level the CLI takes. It is applied
// before a Catalog leaves DiscoverModels.
func OrderEfforts(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, want := range EffortOrder {
		for _, got := range in {
			if got == want && !seen[got] {
				out = append(out, got)
				seen[got] = true
			}
		}
	}
	for _, got := range in {
		if got != "" && !seen[got] {
			out = append(out, got)
			seen[got] = true
		}
	}
	return out
}
