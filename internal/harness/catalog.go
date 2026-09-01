package harness

// Catalog is what an agent can be run on, as its own CLI reports it.
type Catalog struct {
	Models []Model `json:"models"`
	Static bool    `json:"static"` // true when the list is curated, not discovered
	Notes  string  `json:"notes,omitempty"`
}

// Model is one entry of that list, in the agent's own naming - "sonnet",
// "gpt-5.6-sol", "opencode|big-pickle" - never an OpenRouter id.
type Model struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Hint          string   `json:"hint,omitempty"`
	Group         string   `json:"group,omitempty"`   // provider, for opencode
	Efforts       []string `json:"efforts,omitempty"` // subset of low/medium/high, empty = no effort
	DefaultEffort string   `json:"default_effort,omitempty"`
	Default       bool     `json:"default,omitempty"`
}

// FilterEfforts keeps only the three levels every agent with an effort
// mechanism understands, in a fixed order. It is applied before a Catalog
// leaves Discover, so a level offered in the picker can never be one the CLI
// refuses.
func FilterEfforts(in []string) []string {
	var out []string
	for _, want := range []string{"low", "medium", "high"} {
		for _, got := range in {
			if got == want {
				out = append(out, want)
				break
			}
		}
	}
	return out
}
