// Package chattools is how the agents of chats in one repository reach each
// other: three tools every agent gets beside ask_user, on the same MCP server
// (askuser.ServerName), whatever the harness and whatever the account.
//
//	list_chats     the other chats in this chat's repository
//	read_chat      the last messages of one of them
//	send_to_chat   a message into one of them, which wakes its agent
//
// A call goes from the agent's host (Claude Code, Codex) or from `socrates
// mcp-ask` (OpenCode) to the server over a Unix socket (Endpoint), with a
// token that names the calling chat; the server does the rest
// (internal/server, chattools.go).
package chattools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// The tools' names. An agent prefixes them with the server's name as it does
// ask_user's (Claude Code: mcp__socrates__send_to_chat; OpenCode:
// socrates_send_to_chat; Codex: the bare name).
const (
	List = "list_chats"
	Read = "read_chat"
	Send = "send_to_chat"
)

// Def is one tool as an agent is told about it.
type Def struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
}

// Tools is the three tools, in the order they are listed.
func Tools() []Def {
	chat := map[string]any{"type": "string", "description": "The id of the chat, as list_chats names it."}
	return []Def{
		{
			Name:  List,
			Title: "List the other chats here",
			Description: "Lists the other chats whose agents work in this repository (this folder, or the same Git " +
				"repository), with their id, title, agent, model and whether they are working or waiting. " +
				"Use read_chat and send_to_chat with an id from it.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:  Read,
			Title: "Read another chat",
			Description: "Reads the last messages of another chat in this repository (list_chats): what the person " +
				"asked there and what its agent answered. Reading does not disturb it.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"chat":  chat,
				"count": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxRead, "description": fmt.Sprintf("How many of the last messages, %d when left out.", DefaultRead)},
			}, "required": []string{"chat"}},
		},
		{
			Name:  Send,
			Title: "Send a message to another chat",
			Description: "Sends a message to the agent of another chat in this repository (list_chats). It arrives " +
				"as a message in that chat, marked as coming from this one, and starts its agent at once, or is " +
				"queued while it works. Its answer, if it gives one, arrives here the same way. Every message " +
				"costs the other chat's usage: write only what it needs, and do not answer a message that " +
				"needs no answer.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"chat": chat,
				"text": map[string]any{"type": "string", "description": "The message, in Markdown."},
			}, "required": []string{"chat", "text"}},
		},
	}
}

// Read's bounds, and how long a message may be.
const (
	DefaultRead = 10
	MaxRead     = 50
	MaxText     = 20000
)

// Guidance is what an agent's instructions say about the tools; tool is how
// it knows list_chats, which names the other two.
func Guidance(tool string) string {
	return "Other chats may work in the same repository, each with an agent of its own. " + tool +
		" lists them; read_chat and send_to_chat read one and send it a message. Use them when the work " +
		"concerns another chat, or when the person asks you to coordinate with one."
}

// Endpoint is where a chat's tools reach the server: its socket, and the
// token that names the chat to it. The zero Endpoint has no tools.
type Endpoint struct {
	Socket string `json:"socket,omitempty"`
	Token  string `json:"token,omitempty"`
}

// Usable is whether the endpoint can be called at all.
func (e Endpoint) Usable() bool { return e.Socket != "" && e.Token != "" }

// The variables `socrates mcp-ask` reads the endpoint from (Env, FromEnv).
const (
	EnvSocket = "SOCRATES_CHAT_TOOLS_SOCKET"
	EnvToken  = "SOCRATES_CHAT_TOOLS_TOKEN"
)

// Env is the endpoint as the environment of `socrates mcp-ask`; nil when it
// is not usable.
func (e Endpoint) Env() map[string]string {
	if !e.Usable() {
		return nil
	}
	return map[string]string{EnvSocket: e.Socket, EnvToken: e.Token}
}

// FromEnv is the endpoint the process environment names.
func FromEnv() Endpoint {
	return Endpoint{Socket: os.Getenv(EnvSocket), Token: os.Getenv(EnvToken)}
}

// Path is the request path on the server's socket.
const Path = "/tool"

// Request is one call, as the server is sent it.
type Request struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Response is what the tool says back: a text for the model, and whether it
// is an error the model should see as one.
type Response struct {
	Text  string `json:"text"`
	Error bool   `json:"error,omitempty"`
}

// callTimeout is how long a call may take: none waits for a person, so
// anything longer is a server that is not answering.
const callTimeout = 30 * time.Second

// Call makes one call and returns what the model is told. It never fails:
// an endpoint that cannot be reached is a text that says so, as an error.
func (e Endpoint) Call(ctx context.Context, tool string, args json.RawMessage) Response {
	if !e.Usable() {
		return Response{Text: "The other chats cannot be reached from here.", Error: true}
	}
	resp, err := e.call(ctx, tool, args)
	if err != nil {
		return Response{Text: "The other chats could not be reached: " + err.Error(), Error: true}
	}
	return resp
}

func (e Endpoint) call(ctx context.Context, tool string, args json.RawMessage) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	body, _ := json.Marshal(Request{Tool: tool, Arguments: args})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://socrates"+Path, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+e.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", e.Socket)
		},
	}}
	res, err := client.Do(req)
	if err != nil {
		return Response{}, errors.New("Socrates is not answering")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return Response{}, err
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil || (out.Text == "" && res.StatusCode != http.StatusOK) {
		return Response{}, fmt.Errorf("Socrates answered %s", res.Status)
	}
	return out, nil
}
