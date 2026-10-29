// Package mcpclient is the client side of the Model Context Protocol that
// Socrates needs to set up MCP servers for its agents: a probe that connects
// to a server the way an agent would and reports what it offers, and an
// OAuth 2.1 client for remote servers that require authorization.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ProtocolVersion is the MCP protocol version the probe asks for. The probe
// accepts whatever version the server answers with.
const ProtocolVersion = "2025-11-25"

// ClientName is the name the probe gives in its client info.
const ClientName = "socrates"

// ClientVersion is the version the probe gives in its client info.
var ClientVersion = "dev"

// Server is how to reach an MCP server.
type Server struct {
	// Transport is "stdio", "http" (streamable HTTP) or "sse" (the legacy
	// HTTP+SSE transport).
	Transport string

	// Command, Args, Env and Dir start a stdio server. Env is added to the
	// environment of Socrates' own process.
	Command string
	Args    []string
	Env     map[string]string
	Dir     string

	// URL and Headers reach an HTTP or SSE server.
	URL     string
	Headers map[string]string
}

// Tool is a tool the server offers.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
}

// Prompt is a prompt the server offers.
type Prompt struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// Resource is a resource the server offers.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// Info is what a probe found.
type Info struct {
	ProtocolVersion string
	ServerName      string
	ServerTitle     string
	ServerVersion   string
	Instructions    string
	// Capabilities are the top-level capability names the server declared
	// (tools, prompts, resources, logging, completions ...), sorted.
	Capabilities []string
	// Tools are every page of tools/list.
	Tools []Tool
	// Prompts and Resources are listed when the server declares them; an
	// error listing them is ignored.
	Prompts   []Prompt
	Resources []Resource
	// Latency is the round trip of initialize.
	Latency time.Duration
	// Stderr is, for a stdio server, the last few KiB the process wrote to
	// its standard error.
	Stderr string
}

// HasCapability is whether the server declared the capability.
func (i *Info) HasCapability(name string) bool {
	return slices.Contains(i.Capabilities, name)
}

// maxPages bounds the pages of one list, against a server whose cursor
// never ends.
const maxPages = 100

// Probe connects to the server (it starts a stdio server's process, or
// reaches an HTTP one), runs initialize, sends notifications/initialized,
// lists the tools (and the prompts and resources when the server declares
// them) and closes the connection again. ctx bounds the whole probe.
//
// The error is plain English for a person setting the server up. An HTTP
// server that needs OAuth gives an *AuthRequiredError. When the probe fails
// after it reached the server, the Info returned with the error holds what
// it found until then, a stdio server's standard error in particular.
func Probe(ctx context.Context, s Server) (*Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t, err := open(ctx, s)
	if err != nil {
		return nil, err
	}
	info := &Info{}
	err = probe(ctx, t, info)
	t.close()
	if st, ok := t.(interface{ stderrTail() string }); ok {
		info.Stderr = st.stderrTail()
	}
	if err != nil {
		return info, err
	}
	return info, nil
}

// open connects the transport the server names.
func open(ctx context.Context, s Server) (transport, error) {
	switch strings.ToLower(strings.TrimSpace(s.Transport)) {
	case "stdio", "":
		if s.Transport == "" && s.Command == "" && s.URL != "" {
			return openHTTP(s)
		}
		return openStdio(s)
	case "http", "streamable-http", "streamable_http", "streamablehttp":
		return openHTTP(s)
	case "sse":
		return openSSE(ctx, s)
	default:
		return nil, fmt.Errorf("unknown transport %q (expected stdio, http or sse)", s.Transport)
	}
}

// probe is the conversation of Probe on an open transport.
func probe(ctx context.Context, t transport, info *Info) error {
	c := &client{t: t}

	var init struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	start := time.Now()
	err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": ClientName, "version": ClientVersion},
	}, &init)
	if err != nil {
		return err
	}
	info.Latency = time.Since(start)
	info.ProtocolVersion = init.ProtocolVersion
	info.ServerName = init.ServerInfo.Name
	info.ServerTitle = init.ServerInfo.Title
	info.ServerVersion = init.ServerInfo.Version
	info.Instructions = init.Instructions
	for name := range init.Capabilities {
		info.Capabilities = append(info.Capabilities, name)
	}
	sort.Strings(info.Capabilities)
	if init.ProtocolVersion != "" {
		t.setProtocolVersion(init.ProtocolVersion)
	}

	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return err
	}

	// tools/list is asked for even when the server does not declare tools;
	// an error then only means that it has none.
	tools, err := listAll[Tool](ctx, c, "tools/list", "tools")
	if err != nil && (info.HasCapability("tools") || ctx.Err() != nil) {
		return err
	}
	for i := range tools {
		if tools[i].Title == "" {
			if title, ok := tools[i].Annotations["title"].(string); ok {
				tools[i].Title = title
			}
		}
	}
	info.Tools = tools

	if info.HasCapability("prompts") {
		info.Prompts, _ = listAll[Prompt](ctx, c, "prompts/list", "prompts")
	}
	if info.HasCapability("resources") {
		info.Resources, _ = listAll[Resource](ctx, c, "resources/list", "resources")
	}
	return ctx.Err()
}

// listAll asks for every page of a list method.
func listAll[T any](ctx context.Context, c *client, method, field string) ([]T, error) {
	var all []T
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		var params map[string]any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		var raw map[string]json.RawMessage
		if err := c.call(ctx, method, params, &raw); err != nil {
			return all, err
		}
		var items []T
		if list, ok := raw[field]; ok && string(list) != "null" {
			if err := json.Unmarshal(list, &items); err != nil {
				return all, fmt.Errorf("the server's answer to %s is not valid: %v", method, err)
			}
		}
		all = append(all, items...)
		cursor = ""
		if next, ok := raw["nextCursor"]; ok {
			_ = json.Unmarshal(next, &cursor)
		}
		if cursor == "" || seen[cursor] {
			return all, nil
		}
		seen[cursor] = true
	}
	return all, nil
}

// message is one JSON-RPC message, in either direction.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// hasID is whether the message carries an id at all.
func (m *message) hasID() bool { return len(m.ID) > 0 && string(m.ID) != "null" }

// isResponse is whether the message answers a request.
func (m *message) isResponse() bool {
	return m.Method == "" && m.hasID() && (m.Result != nil || m.Error != nil)
}

// isRequest is whether the message is a request the other side must answer.
func (m *message) isRequest() bool { return m.Method != "" && m.hasID() }

// idKey is an id in a form that compares equal for 7 and "7".
func idKey(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return string(raw)
}

// parseMessages reads one JSON-RPC message or a batch of them. Anything
// else gives none.
func parseMessages(data []byte) []*message {
	data = trimSpace(data)
	if len(data) == 0 {
		return nil
	}
	switch data[0] {
	case '{':
		var m message
		if json.Unmarshal(data, &m) != nil {
			return nil
		}
		return []*message{&m}
	case '[':
		var batch []*message
		if json.Unmarshal(data, &batch) != nil {
			return nil
		}
		return batch
	}
	return nil
}

// trimSpace trims ASCII white space without copying.
func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r' || b[len(b)-1] == '\n') {
		b = b[:len(b)-1]
	}
	return b
}

// answerFor is the client's answer to a request of the server: an empty
// result for ping, and "method not found" for everything else, since the
// probe declares no client capabilities.
func answerFor(req *message) *message {
	if req.Method == "ping" {
		return &message{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{}`)}
	}
	return &message{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}}
}

// transport carries JSON-RPC messages to a server and back.
type transport interface {
	// roundTrip sends a request and returns the response that has its id.
	roundTrip(ctx context.Context, req *message) (*message, error)
	// send sends a message that gets no response (a notification).
	send(ctx context.Context, msg *message) error
	// setProtocolVersion tells the transport the negotiated version.
	setProtocolVersion(v string)
	// close ends the connection; it does not fail.
	close()
}

// client numbers the requests on a transport.
type client struct {
	t    transport
	next int
}

// call sends a request and decodes its result into out.
func (c *client) call(ctx context.Context, method string, params, out any) error {
	c.next++
	req := &message{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(c.next)), Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	resp, err := c.t.roundTrip(ctx, req)
	if err != nil {
		return explain(ctx, method, err)
	}
	if resp.Error != nil {
		return &wrapped{msg: fmt.Sprintf("the server answered %s with an error: %s (code %d)", method, oneLine(resp.Error.Message, 300), resp.Error.Code)}
	}
	if out != nil {
		result := resp.Result
		if len(result) == 0 || string(result) == "null" {
			result = json.RawMessage(`{}`)
		}
		if err := json.Unmarshal(result, out); err != nil {
			return fmt.Errorf("the server's answer to %s is not valid: %v", method, err)
		}
	}
	return nil
}

// notify sends a notification without parameters.
func (c *client) notify(ctx context.Context, method string) error {
	if err := c.t.send(ctx, &message{JSONRPC: "2.0", Method: method}); err != nil {
		return explain(ctx, method, err)
	}
	return nil
}

// explain turns a cancelled or expired context into a sentence about the
// method that was waiting.
func explain(ctx context.Context, method string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &wrapped{msg: "the server did not answer " + method + " in time", err: context.DeadlineExceeded}
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return &wrapped{msg: "the probe was cancelled while it waited for " + method, err: context.Canceled}
	}
	return err
}

// wrapped is an error with a sentence of its own that still unwraps to the
// error underneath.
type wrapped struct {
	msg string
	err error
}

func (w *wrapped) Error() string { return w.msg }
func (w *wrapped) Unwrap() error { return w.err }

// oneLine shortens a text for an error message: white space folded, at most
// max bytes, cut on a rune boundary.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// utf8Start is whether b starts a UTF-8 sequence (is not a continuation).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
