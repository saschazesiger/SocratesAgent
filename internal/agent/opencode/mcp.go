//go:build !windows

package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// folderMCP is the chat's folder MCP servers (StartSpec.MCPServers) in
// OpenCode's shape, with every server an OpenCode config file of the folder
// or a folder above it defines turned off (HARNESS-POLICY.md, "MCP servers of
// a folder"): OpenCode reads those files itself and merges OPENCODE_CONFIG_CONTENT
// into them key by key, so a server is only gone when it is turned off by
// name. A name that is turned off but defined nowhere is ignored by OpenCode.
func folderMCP(spec agent.StartSpec) map[string]MCPServer {
	servers := map[string]MCPServer{}
	off := false
	for _, name := range projectMCP(spec.Workdir) {
		servers[name] = MCPServer{Enabled: &off}
	}
	for _, s := range spec.MCPServers {
		switch s.Transport {
		case agent.MCPStdio:
			servers[s.Name] = MCPServer{Type: "local", Command: append([]string{s.Command}, s.Args...), Environment: s.Env}
		default:
			// "remote" tries streamable HTTP, then SSE.
			servers[s.Name] = MCPServer{Type: "remote", URL: s.URL, Headers: s.Headers}
		}
	}
	return servers
}

// projectConfigs are the files OpenCode reads its project config from, in a
// folder and in every folder above it.
var projectConfigs = []string{"opencode.json", "opencode.jsonc",
	filepath.Join(".opencode", "opencode.json"), filepath.Join(".opencode", "opencode.jsonc")}

// projectMCP is the names of the MCP servers the OpenCode config files of
// workdir and the folders above it define. OpenCode stops at the repository's
// root; going on to the file system's root only turns off names nobody
// defines.
func projectMCP(workdir string) []string {
	if workdir == "" {
		return nil
	}
	seen := map[string]bool{}
	dir := filepath.Clean(workdir)
	for {
		for _, name := range projectConfigs {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			var doc struct {
				MCP map[string]json.RawMessage `json:"mcp"`
			}
			if json.Unmarshal(stripJSONC(data), &doc) != nil {
				continue
			}
			for server := range doc.MCP {
				seen[server] = true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// stripJSONC makes JSON of OpenCode's JSONC: comments out, and a comma
// before a closing bracket.
func stripJSONC(in []byte) []byte {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case c == '"':
			start := i
			for i++; i < len(in) && in[i] != '"'; i++ {
				if in[i] == '\\' {
					i++
				}
			}
			out = append(out, in[start:min(i+1, len(in))]...)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
			i--
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && (in[i] != '*' || in[i+1] != '/') {
				i++
			}
			i++
		case c == '}' || c == ']':
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}
