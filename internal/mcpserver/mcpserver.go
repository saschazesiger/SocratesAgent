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
// (Endpoint). A call knows the session it comes from (Session): a chat, or a
// cell of a test run with its dimensions, parameters and evidence folder.
//
// Beside its tools a server may have accounts that Admin keeps (Accounts),
// a pool of them the test runner leases from before a cell starts (Pool),
// state it ends with a session (Ender), what Admin shows about its use
// (Reporter), and checks of a test catalog's area (AreaChecker). Adding a
// server is one package that implements Server and one line in
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
	// instructions, and part of a test cell's prompt.
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

// Session is who calls a tool: a chat, or a test session.
type Session struct {
	// ID is the chat's id (a test session is a chat too).
	ID string
	// Title is the chat's title, for Admin's view of who uses what.
	Title string
	// Workdir is the chat's folder.
	Workdir string
	// Test is the cell a test session works; nil for a chat.
	Test *TestCell
}

// Holder is who holds a lease for the session: a cell's attempt, which the
// runner leased for before its session existed, or the chat.
func (s Session) Holder() string {
	if s.Test != nil && s.Test.Attempt != "" {
		return s.Test.Attempt
	}
	return s.ID
}

// TestCell is the cell of a test run a session works (docs/design/TEST-MODE.md).
type TestCell struct {
	Run     string
	Test    string
	Cell    string // the cell key, web-browser/header-navigation@pixel.es
	Attempt string // the attempt's id, the holder of its leases
	N       int    // the attempt's number
	Repo    string // the repository's root
	// Dims is the cell's value per dimension (device: pixel).
	Dims map[string]string
	// Params is the cell's merged parameters (consent: accept).
	Params map[string]string
	// Evidence is the attempt's evidence folder: actions.jsonl and the files
	// it names, links.json, logs/.
	Evidence string
	// Prompted is the MCP servers the cell's area names, whose instructions
	// its prompt holds already.
	Prompted []string
}

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
// account: a chat when it starts using one, a test cell before its session
// starts (the runner waits for a free one, no tokens spent).
type Pool interface {
	// PoolTitle is how a waiting cell names what it waits for.
	PoolTitle() string
	// Lease takes a free account for holder (the one it holds already, if
	// any); a zero Lease and why when none is free.
	Lease(holder string) (Lease, string)
	// Restore takes the account for holder again after a restart, whether
	// or not it is free; false when the account is gone.
	Restore(holder, account string) bool
	// Release gives back what holder holds.
	Release(holder string)
}

// Ender is a server that keeps state for a session: End closes it (a test
// cell's attempt ended, the chat was deleted) and says what it closed, ""
// for nothing.
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

// AreaChecker is a server that checks the area of a test catalog that names
// it: the values of its dimensions (device: [iphone, pixel]), its
// parameters. It returns the problems, in words for the catalog's author.
type AreaChecker interface {
	CheckArea(dims map[string][]string, params map[string]string) []string
}

// ChatsOnly is a server for chats alone: a test session never has it on,
// and the Tests page does not offer it for the cells.
type ChatsOnly interface {
	ChatsOnly()
}

// NameRE is a server name every harness takes as it is.
var NameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
