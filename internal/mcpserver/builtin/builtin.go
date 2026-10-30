// Package builtin is the list of the MCP servers built into Socrates. A new
// server is a package that implements mcpserver.Server and a line in
// servers.
package builtin

import (
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/browserstack"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/tests"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// Deps is what the built-in servers need of the Socrates server besides
// its data directory and the accounts Admin keeps.
type Deps struct {
	Tests tests.Deps
}

// servers makes every built-in server; accounts is how a server reads the
// accounts Admin keeps for it.
func servers(dataDir string, accounts func(server string) []mcpserver.Account, d Deps) []mcpserver.Server {
	return []mcpserver.Server{
		browserstack.New(browserstack.Deps{DataDir: dataDir, Accounts: func() []mcpserver.Account { return accounts(browserstack.Name) }}),
		tests.New(d.Tests),
	}
}

// Register adds every built-in server to r, running in dataDir.
func Register(r *mcpserver.Registry, dataDir string, d Deps) {
	for _, s := range servers(dataDir, r.Accounts, d) {
		r.Register(s)
	}
}

// inert is the servers as the catalog check sees them: names, instructions
// and checks, nothing running.
var inert = servers("", func(string) []mcpserver.Account { return nil }, Deps{})

// Names is the names of the built-in servers.
func Names() []string {
	var out []string
	for _, s := range inert {
		out = append(out, s.Name())
	}
	return out
}

func init() {
	// No server of Admin may take a built-in server's name.
	config.BuiltinMCPNames = Names()
	// A test catalog's area may name a built-in server; the server checks
	// the area that does, and its instructions join the cells' prompt.
	catalog.BuiltinMCP = func(name string) *catalog.BuiltinServer {
		for _, s := range inert {
			if s.Name() != name {
				continue
			}
			b := &catalog.BuiltinServer{Instructions: s.Instructions()}
			if c, ok := s.(mcpserver.AreaChecker); ok {
				b.Check = c.CheckArea
			}
			return b
		}
		return nil
	}
}
