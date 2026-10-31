// Package mcpserver is Socrates' own MCP servers: components built into the
// binary that a chat switches on like any MCP server of Admin or of its
// folder's .mcp.json (HARNESS-POLICY.md, "MCP servers built into Socrates").
//
// A server is a Server registered in a Registry. It runs inside the Socrates
// server process, so what it holds (a device session, a lease of an account)
// outlives the agent's CLI, which is parked when idle and started again. The
// agent reaches it through `socrates mcp-server <name>` (ServeStdio), a small
// MCP server on stdio that every harness starts as it starts any other, and
// that hands every call over the server's tools socket to the Registry
// (Endpoint). A call knows the chat it comes from (Session).
//
// Beside its tools a server may have accounts that Admin keeps (Accounts),
// a pool of them a chat leases from (Pool), state it ends with a chat
// (Ender), what Admin shows about its use (Reporter), and a record of a
// chat's actions that its tests take their evidence from (Recorder). Adding
// a server is one package that implements Server and one line in
// internal/mcpserver/builtin.
package mcpserver

import (
	"context"
	"encoding/json"
	"regexp"
)

// Server is one MCP server built into Socrates.
type Server interface {
	// Name is the name chats know it by, as the name of a server of a
	// .mcp.json: letters, digits, - and _.
	Name() string
	// Title is what Admin and the MCP dialogs call it.
	Title() string
	// Description is one sentence on what it does, for Admin.
	Description() string
	// Instructions is what the agent is told about it: initialize's
	// instructions.
	Instructions() string
	// Tools is its tools, in the order they are listed.
	Tools() []Tool
	// Call runs one tool for a session. It never fails: what went wrong is
	// a Result with IsError.
	Call(ctx context.Context, s Session, tool string, args json.RawMessage) Result
}

// Tool is one tool as an agent is told about it.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"inputSchema"`
}

// Content is one part of a tool's result, as MCP has it: a text, or an
// image (Data in base64).
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// Result is what a tool gives back.
type Result struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Text is a result of one text.
func Text(text string) Result { return Result{Content: []Content{{Type: "text", Text: text}}} }

// Error is an error result of one text.
func Error(text string) Result {
	return Result{Content: []Content{{Type: "text", Text: text}}, IsError: true}
}

// Session is the chat that calls a tool.
type Session struct {
	// ID is the chat's id.
	ID string
	// Title is the chat's title, for Admin's view of who uses what.
	Title string
	// Workdir is the chat's folder.
	Workdir string
}

// Holder is who holds a lease for the session: the chat.
func (s Session) Holder() string { return s.ID }

// Field is one value of an account, as Admin asks for it.
type Field struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Hint        string `json:"hint,omitempty"`
}

// The states of an account's check.
const (
	AccountValid   = "valid"
	AccountInvalid = "invalid"
	AccountUnknown = "unknown"
)

// Check is what a server found out about an account.
type Check struct {
	State string `json:"state"`
	// Detail is one line for Admin: the plan, what was refused, why it
	// could not be checked.
	Detail string `json:"detail,omitempty"`
	// At is when, in Unix milliseconds.
	At int64 `json:"at,omitempty"`
}

// Account is one account of a server as Admin keeps it.
type Account struct {
	ID     string            `json:"id"`
	Values map[string]string `json:"values"`
	Check  *Check            `json:"check,omitempty"`
}

// Accounts is a server with accounts that Admin keeps: the fields one has,
// what it is called, and how it is checked.
type Accounts interface {
	AccountFields() []Field
	// AccountLabel is what Admin and the pool call an account (its user).
	AccountLabel(values map[string]string) string
	// CheckAccount asks the service whether the account works.
	CheckAccount(ctx context.Context, values map[string]string) Check
}

// Lease is an account of a pool held for someone.
type Lease struct {
	// Account is the account's id (Account.ID).
	Account string `json:"account"`
	// Label is what the account is called (Accounts.AccountLabel).
	Label string `json:"label"`
}

// Pool is a server whose accounts are leased, one holder at a time per
// account: a chat when it starts using one.
type Pool interface {
	// Lease takes a free account for holder (the one it holds already, if
	// any); a zero Lease and why when none is free.
	Lease(holder string) (Lease, string)
	// Release gives back what holder holds.
	Release(holder string)
}

// Ender is a server that keeps state for a session: End closes it (the chat
// was deleted) and says what it closed, "" for nothing.
type Ender interface {
	End(ctx context.Context, session string) string
}

// Use is one thing of a server in use, for Admin.
type Use struct {
	// Account is the id of the account in use, "" for none.
	Account string `json:"account,omitempty"`
	// Session is the chat that uses it.
	Session string `json:"session,omitempty"`
	// What says what it is, in a few words (iPhone 15 · Safari, running).
	What string `json:"what"`
}

// Reporter is a server that tells Admin what it has in use.
type Reporter interface {
	Uses() []Use
}

// Recorder is a server that records what it does for a chat as evidence: a
// folder with actions.jsonl (one JSON line per action, its screenshot named
// in "screenshot") and the files the lines name. A chat's tests attach the
// screenshots recorded while a step was worked to that step
// (docs/design/CHAT-TESTS.md).
type Recorder interface {
	// EvidenceDir is the chat's folder; it need not exist.
	EvidenceDir(session string) string
}

// NameRE is a server name every harness takes as it is.
var NameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
