package agent

import "sync"

// CommandInfo is one entry of an agent's command palette: what the composer
// offers after "/" (REBUILD.md §7.1). Name is what Adapter.Command is called
// with; Args says what may follow it, empty for nothing.
type CommandInfo struct {
	Name        string `json:"name"`
	Args        string `json:"args,omitempty"`
	Description string `json:"description"`
}

var (
	paletteMu sync.Mutex
	palettes  = map[string][]CommandInfo{}
)

// RegisterCommands sets the palette of a harness. An adapter registers it
// next to itself.
func RegisterCommands(harness string, cmds []CommandInfo) {
	paletteMu.Lock()
	defer paletteMu.Unlock()
	palettes[harness] = append([]CommandInfo(nil), cmds...)
}

// Commands is the palette of a harness, nil when it has none.
func Commands(harness string) []CommandInfo {
	paletteMu.Lock()
	defer paletteMu.Unlock()
	return append([]CommandInfo(nil), palettes[harness]...)
}
