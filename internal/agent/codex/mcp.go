//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// mcpOverrides is the mcp_servers part of every thread's config
// (HARNESS-POLICY.md, "MCP servers of a folder"): the chat's folder MCP
// servers (StartSpec.MCPServers) in codex's shape, and every server a
// project layer of the working directory defines (its .codex/config.toml,
// which codex loads because the folder is trusted) turned off, since a
// folder's MCP servers come from its .mcp.json alone. A thread's config is
// merged into codex's own, table by table: a server is only gone when it is
// turned off by name, which is why the project's names are asked for first
// (config/read). Servers of the account's own config.toml stay. Nil is
// nothing to say.
func (a *adapter) mcpOverrides(ctx context.Context) map[string]any {
	servers := map[string]any{}
	for _, name := range a.projectMCP(ctx) {
		servers[name] = map[string]any{"enabled": false}
	}
	var skipped []string
	for _, s := range a.spec.MCPServers {
		entry, ok := codexMCP(s)
		if !ok {
			skipped = append(skipped, s.Name)
			continue
		}
		servers[s.Name] = entry
	}
	if len(skipped) > 0 {
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeMCP, Text: fmt.Sprintf(
			"Codex cannot reach an MCP server over SSE, so %s from this folder's %s was left out.",
			strings.Join(skipped, ", "), agent.FolderMCPFile)})
	}
	if len(servers) == 0 {
		return nil
	}
	return servers
}

// codexMCP is a server as a [mcp_servers.<name>] table: a command, or a
// streamable HTTP endpoint. Codex has no SSE transport.
func codexMCP(s agent.MCPServer) (map[string]any, bool) {
	switch s.Transport {
	case agent.MCPStdio:
		entry := map[string]any{"command": s.Command, "args": nonNil(s.Args)}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		return entry, true
	case agent.MCPHTTP:
		entry := map[string]any{"url": s.URL}
		if len(s.Headers) > 0 {
			entry["http_headers"] = s.Headers
		}
		return entry, true
	}
	return nil, false
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// configLayers is the part of config/read's answer projectMCP reads.
type configLayers struct {
	Layers []struct {
		Name struct {
			Type string `json:"type"`
		} `json:"name"`
		Config struct {
			MCPServers map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	} `json:"layers"`
}

// projectMCP is the names of the MCP servers the project layers of the
// working directory define, as codex itself sees them from there. A codex
// that cannot say leaves them be, which is logged.
func (a *adapter) projectMCP(ctx context.Context) []string {
	if a.spec.Workdir == "" {
		return nil
	}
	var res configLayers
	if err := a.rpc.call(ctx, "config/read", map[string]any{"cwd": a.spec.Workdir, "includeLayers": true}, &res); err != nil {
		log.Printf("codex: the project's MCP servers are unknown, so they stay: %v", err)
		return nil
	}
	var names []string
	for _, l := range res.Layers {
		if l.Name.Type != "project" {
			continue
		}
		for name := range l.Config.MCPServers {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
