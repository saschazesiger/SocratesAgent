package harnesses

// Catalog is what a harness can be run on, as its own CLI reports it.
type Catalog struct {
	Models []Model `json:"models"`
	Static bool    `json:"static"` // true when the list is curated, not discovered
	Notes  string  `json:"notes,omitempty"`
	// Recent and Favorites are model ids the CLI itself keeps first: the ones
	// OpenCode's TUI used last, newest first, and the ones starred there.
	Recent    []string `json:"recent,omitempty"`
	Favorites []string `json:"favorites,omitempty"`
	// Folder is what a folder's own configuration of the agent says, for a
	// catalogue discovered in that folder (FolderDiscoverer).
	Folder *FolderConfig `json:"folder,omitempty"`
}

// FolderConfig is a folder's own configuration of an agent, as the agent
// resolves it there: OpenCode's opencode.json and .opencode/ in the folder
// and above it.
type FolderConfig struct {
	// Files is the configuration files that make the folder differ.
	Files []string `json:"files"`
	// Model is the model the agent starts on in the folder when nothing else
	// is chosen; empty when the folder's configuration names none, or one the
	// agent does not list.
	Model string `json:"model,omitempty"`
	// Agents is the agents the folder's configuration puts on a model of
	// their own - the subagents a task runs among them - by name.
	Agents []AgentModel `json:"agents,omitempty"`
}

// AgentModel is one of the agent's own agents (OpenCode's build, plan,
// general, explore ...) and the model it runs on.
type AgentModel struct {
	Name  string `json:"name"`
	Model string `json:"model"`
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
var EffortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

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
