package codex

import "github.com/saschazesiger/SocratesAgent/internal/agent"

// Commands is Codex's palette: the TUI's slash commands that have an
// app-server equivalent. /model is the chat's model picker (SetModel), and a
// message that starts with "!" runs as a shell command without the palette.
var Commands = []agent.CommandInfo{
	{Name: "compact", Description: "Summarize the conversation to free up context"},
	{Name: "review", Args: "[base <branch> | commit <sha> | instructions]",
		Description: "Review the uncommitted changes, a branch, a commit, or what you describe"},
	{Name: "plan", Args: "[off]", Description: "Plan before changing anything (Plan mode), or stop planning"},
	{Name: "shell", Args: "<command>", Description: "Run a shell command in the working directory (same as a line starting with !)"},
}
