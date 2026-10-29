package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validate checks what one file cannot check on its own: the names that
// point at other parts of the catalog (tools, MCP servers, previous ids),
// the size of each area's prompt, and the warnings about parts no test uses.
func (c *Catalog) validate() {
	used := map[string]bool{}
	previous := map[string]string{}
	lower := map[string]string{}
	for _, a := range c.Areas {
		areaFile := a.ID + "/" + AreaFile
		for _, name := range a.Tools {
			if c.Tools[name] == nil {
				c.errorf(areaFile, a.lines["tools"], "tools: there is no tool %q (a folder tools/%s with a %s)", name, name, ToolFile)
			}
		}
		builtinPrompt := 0
		for _, name := range a.MCP {
			if b := builtin(name); b != nil {
				builtinPrompt += len(b.Instructions)
				if b.Check != nil {
					for _, problem := range b.Check(a.dimValues(), a.Params) {
						c.errorf(areaFile, a.lines["mcp"], "mcp: %s", problem)
					}
				}
				continue
			}
			if !c.mcpServer(name) {
				c.errorf(areaFile, a.lines["mcp"], "mcp: neither Socrates nor the repository's .mcp.json has a server %q", name)
			}
		}
		if len(a.Tests) == 0 {
			c.warnf(areaFile, 0, "the area has no tests")
		}
		prompt := map[string]bool{}
		for _, name := range a.Tools {
			prompt[name] = true
		}
		for _, t := range a.Tests {
			for _, name := range t.Tools {
				if c.Tools[name] == nil {
					c.errorf(t.File, t.lines["tools"], "tools: there is no tool %q (a folder tools/%s with a %s)", name, name, ToolFile)
				}
				prompt[name] = true
			}
			for _, tool := range c.ToolsOf(t) {
				used[tool.Name] = true
			}
			if !t.explicitPriority {
				c.warnf(t.File, 0, "no priority: the test runs as P1 (priority: P0, P1 or P2)")
			}
			if t.Previous != "" {
				if c.tests[t.Previous] != nil {
					c.errorf(t.File, t.lines["previous"], "previous: %s is a test of the catalog; previous names the id a renamed test had", t.Previous)
				}
				if other, dup := previous[t.Previous]; dup {
					c.errorf(t.File, t.lines["previous"], "previous: %s already carries on the history of %s", other, t.Previous)
				}
				previous[t.Previous] = t.ID
			}
			// Ids are paths and unique as such; two that differ only in case
			// would be one file on macOS and Windows.
			if other, dup := lower[strings.ToLower(t.ID)]; dup {
				c.errorf(t.File, 0, "the id differs from %s only in case", other)
			}
			lower[strings.ToLower(t.ID)] = t.ID
		}
		size := len(c.Instructions) + len(a.Instructions) + builtinPrompt
		parts := []string{fmt.Sprintf("%s %s", TestsFile, kib(len(c.Instructions))), fmt.Sprintf("%s %s", AreaFile, kib(len(a.Instructions)))}
		if builtinPrompt > 0 {
			parts = append(parts, fmt.Sprintf("MCP servers %s", kib(builtinPrompt)))
		}
		for _, name := range c.ToolNames() {
			if prompt[name] {
				n := len(c.Tools[name].Instructions)
				size += n
				parts = append(parts, fmt.Sprintf("%s %s", name, kib(n)))
			}
		}
		if size > MaxPromptBytes {
			c.errorf(areaFile, 0, "the instructions of this area's cells are %s (%s), more than the %s a prompt may hold",
				kib(size), strings.Join(parts, ", "), kib(MaxPromptBytes))
		}
	}
	for _, name := range c.ToolNames() {
		if !used[name] {
			c.warnf(ToolsDir+"/"+name+"/"+ToolFile, 0, "no test uses the tool")
		}
	}
}

func kib(n int) string { return fmt.Sprintf("%.1f KiB", float64(n)/1024) }

// BuiltinServer is an MCP server built into Socrates as the catalog sees
// it: the instructions its cells' prompt gets, and the check of an area that
// names it.
type BuiltinServer struct {
	Instructions string
	Check        func(dims map[string][]string, params map[string]string) []string
}

// BuiltinMCP is the MCP server built into Socrates of a name, nil for none;
// internal/mcpserver/builtin sets it.
var BuiltinMCP func(name string) *BuiltinServer

func builtin(name string) *BuiltinServer {
	if BuiltinMCP == nil {
		return nil
	}
	return BuiltinMCP(name)
}

// dimValues is every value of each dimension of the area, extra ones too.
func (a *Area) dimValues() map[string][]string {
	out := map[string][]string{}
	for _, d := range a.Dimensions {
		var ids []string
		for _, v := range append(append([]Value{}, d.Values...), d.Extra...) {
			ids = append(ids, v.ID)
		}
		out[d.Name] = ids
	}
	return out
}

// mcpServer reports whether the repository's .mcp.json has a server called
// name. The file is read once, when an area first names a server.
func (c *Catalog) mcpServer(name string) bool {
	if c.mcp == nil {
		c.mcp = map[string]bool{}
		var f struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if b, err := os.ReadFile(filepath.Join(c.Root, ".mcp.json")); err == nil && json.Unmarshal(b, &f) == nil {
			for k := range f.MCPServers {
				c.mcp[k] = true
			}
		}
	}
	return c.mcp[name]
}
