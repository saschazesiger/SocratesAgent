package claude

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// mcpConfigFile is where a chat's folder MCP servers are written for
// --mcp-config, in its state directory: a file rather than the JSON on the
// command line, which every process on the machine could read, secrets and
// all.
const mcpConfigFile = "mcp.json"

// writeMCPConfig writes the chat's folder MCP servers (StartSpec.MCPServers)
// for --mcp-config and says where, or removes what an earlier start left and
// says nothing when there are none. --strict-mcp-config stays on: the CLI
// takes these and ask_user, and not its own .mcp.json, settings or the
// account's claude.ai connectors (HARNESS-POLICY.md).
func writeMCPConfig(spec agent.StartSpec) (string, error) {
	if spec.StateDir == "" {
		if len(spec.MCPServers) == 0 {
			return "", nil
		}
		data, err := mcpConfig(spec.MCPServers)
		return string(data), err
	}
	path := filepath.Join(spec.StateDir, mcpConfigFile)
	if len(spec.MCPServers) == 0 {
		_ = os.Remove(path)
		return "", nil
	}
	data, err := mcpConfig(spec.MCPServers)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(spec.StateDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	// An older file keeps its mode through WriteFile.
	return path, os.Chmod(path, 0o600)
}

// mcpConfig is {"mcpServers": …} in Claude Code's own shape.
func mcpConfig(servers []agent.MCPServer) ([]byte, error) {
	out := map[string]any{}
	for _, s := range servers {
		entry := map[string]any{"type": s.Transport}
		if s.Transport == agent.MCPStdio {
			entry["command"] = s.Command
			entry["args"] = nonNil(s.Args)
			if len(s.Env) > 0 {
				entry["env"] = s.Env
			}
		} else {
			entry["url"] = s.URL
			if len(s.Headers) > 0 {
				entry["headers"] = s.Headers
			}
		}
		out[s.Name] = entry
	}
	return json.Marshal(map[string]any{"mcpServers": out})
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
