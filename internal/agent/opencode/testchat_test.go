//go:build !windows

package opencode

import (
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// OpenCode's socrates server (`socrates mcp-ask`) is told by its
// environment that the chat has the test tools; a chat without them is not.
func TestAChatWithTestsStartsMCPAskWithTheTestTools(t *testing.T) {
	spec := agent.StartSpec{AskCommand: []string{"socrates", "mcp-ask"},
		ChatTools: chattools.Endpoint{Socket: "/s", Token: "c.x", Tests: true}}
	env := mcpServers(spec)[askuser.ServerName].Environment
	if env[chattools.EnvTests] != "1" || env[chattools.EnvSocket] != "/s" {
		t.Fatalf("environment %v", env)
	}
	spec.ChatTools.Tests = false
	if _, ok := mcpServers(spec)[askuser.ServerName].Environment[chattools.EnvTests]; ok {
		t.Fatal("a chat without the test tools has them")
	}
}
