//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// Call is one request codex sent the adapter, as a Handler sees it.
type Call struct {
	Method string
	Params json.RawMessage
	// ThreadID and TurnID are codex's; Turn is the journal turn it belongs
	// to (empty outside a turn).
	ThreadID string
	TurnID   string
	Turn     string
	// Model is the model the request's thread runs on, which says whether
	// it is a code-mode model (REBUILD.md §5.3). PartID is the part of the
	// item the request belongs to (a dynamic tool call, a request_user_input
	// call), when the adapter has made one.
	Model  string
	PartID string

	a *adapter
}

// Ask puts a question to the person (an `ask` event with the ask_user
// payload, AGENT-HOST.md §2) and waits for the answer. It returns
// agent.ErrAskGone when the wait ends without one: the turn ended or was
// interrupted, codex withdrew the request, or the app-server stopped. The
// host then delivers the answer, when it comes, as a user message.
func (c *Call) Ask(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	return c.a.ask(ctx, c.Turn, payload)
}

// Defer puts a question whose answer is to arrive as the next user message
// (`ask{via: "message"}`), for a call that returns at once by design (code
// mode, REBUILD.md §5.3). It returns the ask's id.
func (c *Call) Defer(payload json.RawMessage) string {
	id := newID("ask_")
	c.a.out.Emit(agent.Event{Type: agent.TypeAsk, AskID: id, TurnID: c.Turn, Payload: payload, Via: agent.ViaMessage})
	return id
}

// Notice says something in the chat, in a line of its own.
func (c *Call) Notice(code, text string) {
	c.a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: code, Text: text})
}

// Handler answers one kind of server request. Its result is the JSON-RPC
// result; an error is sent as a JSON-RPC error. It runs on a goroutine of its
// own and may block (on Ask) for as long as the person takes.
type Handler func(ctx context.Context, c *Call) (any, error)

// Tool is a dynamic tool: offered to the model on thread/start (codex keeps
// it with the thread and restores it on resume) and run by Handle when the
// model calls it (`item/tool/call`).
type Tool struct {
	// Spec is the DynamicToolSpec: {"type":"function","name",
	// "description","inputSchema"}.
	Spec   json.RawMessage
	Name   string
	Handle Handler
}

var (
	hooksMu  sync.Mutex
	handlers = map[string]Handler{}
	tools    = map[string]Tool{}
)

// HandleRequest makes h the handler of a server request method, in place of
// the built-in one. WP6 sets "item/tool/requestUserInput" (Plan mode's
// request_user_input) and "mcpServer/elicitation/request" this way.
func HandleRequest(method string, h Handler) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	handlers[method] = h
}

// RegisterTool adds a dynamic tool to every thread started from now on.
func RegisterTool(t Tool) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	tools[t.Name] = t
}

func dynamicToolSpecs() []json.RawMessage {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	out := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Spec)
	}
	return out
}

func handlerFor(method string, params json.RawMessage) Handler {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	if method == "item/tool/call" {
		var p struct {
			Tool string `json:"tool"`
		}
		_ = json.Unmarshal(params, &p)
		if t, ok := tools[p.Tool]; ok {
			return t.Handle
		}
	}
	if h, ok := handlers[method]; ok {
		return h
	}
	return builtin[method]
}

// call is a server request being handled, which ends early when codex
// withdraws it, its turn ends, or the app-server goes.
type call struct {
	turn   *turn
	cancel context.CancelFunc
}

// serverRequest answers a request from codex on a goroutine of its own, so
// that a handler waiting for the person never holds up the stream.
func (a *adapter) serverRequest(fr *frame) {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		CallID   string `json:"callId"`
		ItemID   string `json:"itemId"`
	}
	_ = json.Unmarshal(fr.Params, &p)
	ctx, cancel := context.WithCancel(context.Background())
	key := string(fr.ID)
	c := &Call{Method: fr.Method, Params: fr.Params, ThreadID: p.ThreadID, TurnID: p.TurnID, a: a}
	a.mu.Lock()
	c.Model = a.model
	if c.Model == "" {
		c.Model = a.threadModel
	}
	t, ag := a.turnFor(p.ThreadID, p.TurnID)
	if ag != nil && ag.model != "" {
		c.Model = ag.model
	}
	if t != nil && !t.ended {
		c.Turn = t.id
		item := p.CallID
		if item == "" {
			item = p.ItemID
		}
		thread := p.ThreadID
		if thread == "" {
			thread = a.thread
		}
		if pt := t.parts[thread+"/"+item]; pt != nil && item != "" {
			c.PartID = pt.id
		}
	} else {
		t = nil
	}
	a.calls[key] = &call{turn: t, cancel: cancel}
	ending := a.ending
	a.mu.Unlock()
	if ending {
		cancel()
	}
	h := handlerFor(fr.Method, fr.Params)
	go func() {
		defer func() {
			cancel()
			a.mu.Lock()
			delete(a.calls, key)
			a.mu.Unlock()
		}()
		if h == nil {
			c.Notice("codex_request", "Codex asked for "+fr.Method+", which Socrates does not answer; it was declined.")
			_ = a.rpc.refuse(fr.ID, -32601, "socrates does not handle "+fr.Method)
			return
		}
		res, err := h(ctx, c)
		if err != nil {
			_ = a.rpc.refuse(fr.ID, -32000, err.Error())
			return
		}
		_ = a.rpc.reply(fr.ID, res)
	}()
}

// waitingAsk is an ask whose call waits for the answer, for as long as ctx
// lasts.
type waitingAsk struct {
	ctx    context.Context
	answer chan json.RawMessage
}

// ask is Call.Ask.
func (a *adapter) ask(ctx context.Context, turnID string, payload json.RawMessage) (json.RawMessage, error) {
	id := newID("ask_")
	w := &waitingAsk{ctx: ctx, answer: make(chan json.RawMessage, 1)}
	a.mu.Lock()
	a.asks[id] = w
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.asks, id)
		a.mu.Unlock()
	}()
	a.out.Emit(agent.Event{Type: agent.TypeAsk, AskID: id, TurnID: turnID, Payload: payload})
	select {
	case answer := <-w.answer:
		return answer, nil
	case <-ctx.Done():
		return nil, agent.ErrAskGone
	}
}

// builtin is how the adapter answers codex's requests under the always-allow
// policy (decision §1). Approvals should not come with approvalPolicy
// "never"; when one does, it is accepted. Questions to the person have
// handlers of their own (askuser.go); without one they are declined, with a
// notice, so that no turn waits for ever.
var builtin = map[string]Handler{
	"item/commandExecution/requestApproval": accept(map[string]any{"decision": "accept"}),
	"item/fileChange/requestApproval":       accept(map[string]any{"decision": "accept"}),
	"execCommandApproval":                   accept(map[string]any{"decision": "approved"}),
	"applyPatchApproval":                    accept(map[string]any{"decision": "approved"}),
	"item/permissions/requestApproval":      grantPermissions,
	"mcpServer/elicitation/request":         elicitation,
	"item/tool/requestUserInput":            declineUserInput,
	"item/tool/call":                        unknownTool,
	"currentTime/read": func(context.Context, *Call) (any, error) {
		return map[string]any{"currentTimeAt": time.Now().Unix()}, nil
	},
}

func accept(result any) Handler {
	return func(context.Context, *Call) (any, error) { return result, nil }
}

// grantPermissions grants what was asked for, for the session.
func grantPermissions(_ context.Context, c *Call) (any, error) {
	var p struct {
		Permissions map[string]json.RawMessage `json:"permissions"`
	}
	_ = json.Unmarshal(c.Params, &p)
	granted := map[string]json.RawMessage{}
	for k, v := range p.Permissions {
		if string(v) != "null" {
			granted[k] = v
		}
	}
	return map[string]any{"permissions": granted, "scope": "session"}, nil
}

// elicitation accepts an MCP tool-call approval (that is a permission, and
// everything is allowed) and declines a form or a URL meant for the person.
func elicitation(_ context.Context, c *Call) (any, error) {
	var p struct {
		Meta struct {
			Kind string `json:"codex_approval_kind"`
		} `json:"_meta"`
		ServerName string `json:"serverName"`
		Message    string `json:"message"`
	}
	_ = json.Unmarshal(c.Params, &p)
	if p.Meta.Kind != "" {
		return map[string]any{"action": "accept", "content": map[string]any{}, "_meta": nil}, nil
	}
	c.Notice("elicitation_declined", "The MCP server "+p.ServerName+" asked a question Socrates cannot show yet, so it was declined: "+p.Message)
	return map[string]any{"action": "decline", "content": nil, "_meta": nil}, nil
}

func declineUserInput(_ context.Context, c *Call) (any, error) {
	c.Notice("question_declined", "Codex asked a question Socrates cannot show yet, so it was not answered.")
	return nil, errors.New("socrates cannot show this question")
}

func unknownTool(_ context.Context, c *Call) (any, error) {
	var p struct {
		Tool string `json:"tool"`
	}
	_ = json.Unmarshal(c.Params, &p)
	return map[string]any{"contentItems": []any{map[string]any{"type": "inputText",
		"text": "The tool " + p.Tool + " is not available."}}, "success": false}, nil
}
