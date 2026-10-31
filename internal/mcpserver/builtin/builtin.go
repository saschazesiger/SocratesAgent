// Package builtin is the list of the MCP servers built into Socrates. A new
// server is a package that implements mcpserver.Server and a line in
// servers.
package builtin

import (
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/browserstack"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/tests"
)

// Deps is what the built-in servers need of the Socrates server besides
// its data directory and the accounts Admin keeps.
type Deps struct {
	// Tests is the chats' tests, which Socrates Tests writes.
	Tests tests.Deps
}

// servers makes every built-in server; accounts is how a server reads the
// accounts Admin keeps for it. Socrates Tests takes its evidence from every
// other server that records a chat's actions.
func servers(dataDir string, accounts func(server string) []mcpserver.Account, d Deps) []mcpserver.Server {
	list := []mcpserver.Server{
		browserstack.New(browserstack.Deps{DataDir: dataDir, Accounts: func() []mcpserver.Account { return accounts(browserstack.Name) }}),
	}
	var recorders []tests.NamedRecorder
	for _, s := range list {
		if r, ok := s.(mcpserver.Recorder); ok {
			recorders = append(recorders, tests.NamedRecorder{Name: s.Name(), Recorder: r})
		}
	}
	d.Tests.Recorders = func() []tests.NamedRecorder { return recorders }
	return append(list, tests.New(d.Tests))
}

// Register adds every built-in server to r, running in dataDir.
func Register(r *mcpserver.Registry, dataDir string, d Deps) {
	for _, s := range servers(dataDir, r.Accounts, d) {
		r.Register(s)
	}
}

// inert is the servers as their names are known before any runs.
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
}
