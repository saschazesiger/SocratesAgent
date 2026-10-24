package agent

import "encoding/json"

// CommandInfo is one entry of an agent's command palette: what the composer
// offers after "/" (REBUILD.md §7.1). Name is what Adapter.Command is called
// with; Args says what may follow it, empty for nothing.
type CommandInfo struct {
	Name        string `json:"name"`
	Args        string `json:"args,omitempty"`
	Description string `json:"description,omitempty"`
}

// AgentInfo is the part of an agent.info event's content every adapter
// writes. An adapter may add fields of its own beside these (Claude: account,
// models, tools, permission mode).
type AgentInfo struct {
	Agent   string `json:"agent"`
	Version string `json:"version,omitempty"`
	// Commands is the palette: the agent's slash commands as this chat's CLI
	// reported them (custom commands and skills included), plus the ones the
	// adapter itself implements.
	Commands []CommandInfo `json:"commands,omitempty"`
}

// Palette is the chat's command palette: the commands of the newest
// agent.info event, nil before there is one.
func (p *Projection) Palette() []CommandInfo {
	if len(p.Agent) == 0 {
		return nil
	}
	var info AgentInfo
	if json.Unmarshal(p.Agent, &info) != nil {
		return nil
	}
	return info.Commands
}
