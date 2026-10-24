//go:build !windows

package claude

import (
	"context"
	"encoding/json"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// AskTool is the name the model knows ask_user by in Claude Code: an MCP
// tool of the in-process server askuser.ServerName.
const AskTool = "mcp__" + askuser.ServerName + "__" + askuser.Name

// askGuidance is the system-prompt text every chat gets (REBUILD.md §5.3).
// Claude Code defers MCP tools behind ToolSearch, which the model finds on
// its own; saying so saves it a turn of looking.
var askGuidance = askuser.Guidance(AskTool) +
	" If the tool is not loaded yet, load it with ToolSearch (select:" + AskTool + ") first."

// askServerConfig is the server's sdkMcpServerConfigs entry. An in-process
// server has no idle timeout, only the wall-clock one, which is set as high
// as the CLI takes it (2^31-1 ms, almost 25 days), so that a question waits
// for as long as the person takes (verified 20 minutes, research/03 §4.4).
// disableAutoBackground keeps the CLI from moving a waiting call into the
// background: "meant for a server whose calls wait on a person".
var askServerConfig = json.RawMessage(`{"timeout":2147483647,"disableAutoBackground":true}`)

// askServer is ask_user as the chat's in-process MCP server: the CLI reaches
// it over the control protocol (mcp_message), a call becomes an ask event
// and blocks until Answer brings the person's answer, which the model gets
// in the shape of REBUILD.md §5.2.
type askServer struct {
	a     *Adapter
	mcp   *askuser.MCP
	waits agent.Waits
}

func newAskServer(a *Adapter) *askServer {
	s := &askServer{a: a}
	s.mcp = &askuser.MCP{Ask: s.ask}
	return s
}

func (s *askServer) Name() string            { return askuser.ServerName }
func (s *askServer) Config() json.RawMessage { return askServerConfig }

func (s *askServer) Handle(ctx context.Context, message json.RawMessage) (json.RawMessage, error) {
	return s.mcp.Handle(ctx, message), nil
}

// ask puts one call to the person. Its id is the tool call's, so that a view
// can draw the ask with the tool part it belongs to.
func (s *askServer) ask(ctx context.Context, call askuser.Call) (json.RawMessage, error) {
	id := "ask_" + randomID()
	if call.ToolUseID != "" {
		id = "ask_" + call.ToolUseID
		call.Payload.PartID = call.ToolUseID
	}
	return s.wait(ctx, id, call.ToolUseID, call.Payload.JSON())
}

// wait emits an ask of the running turn and blocks until its answer or the
// end of ctx: the CLI withdrew the call (an interrupt, a timeout) or exited.
// part is the tool part of the call, "" for an elicitation.
func (s *askServer) wait(ctx context.Context, id, part string, payload json.RawMessage) (json.RawMessage, error) {
	s.a.mu.Lock()
	turnID, out := "", s.a.out
	if s.a.st != nil && s.a.st.turn != nil {
		turnID = s.a.st.turn.id
	}
	s.a.mu.Unlock()
	return s.waits.Wait(ctx, out, agent.Event{Type: agent.TypeAsk, AskID: id, TurnID: turnID, Payload: payload}, part)
}

// Answer hands an answer to the call waiting on it, and reports
// agent.ErrAskGone when none is (agent.Waits).
func (s *askServer) Answer(_ context.Context, askID string, answer json.RawMessage) error {
	return s.waits.Answer(askID, answer)
}

// withdraw is an interrupt the CLI acknowledged: it has stopped the turn,
// and with it the main agent's ask_user calls in that turn, even while the
// cancels of those calls are still on their way. A call a subagent made, or
// an elicitation, is left to its cancel, because a subagent in the
// background outlives the turn; a call whose part the turn's end has already
// dropped was not one of those. The caller holds a.mu.
func (s *askServer) withdraw(turnID string) {
	if turnID == "" {
		return
	}
	s.waits.Withdraw(func(turn, part string) bool {
		p := s.a.st.parts[part]
		return turn == turnID && part != "" && (p == nil || p.parent == "")
	})
}

// elicit is an MCP elicitation the CLI forwards (a form a server wants the
// person to fill in), asked as an ask_user payload and answered in the
// elicitation's own shape. A form it cannot show is declined, and the chat
// says so.
func (s *askServer) elicit(ctx context.Context, id string, req controlRequest) map[string]any {
	var message string
	_ = json.Unmarshal(req.Message, &message)
	p, ok := askuser.FromElicitation(req.MCPServerName, message, req.RequestedSchema, req.PropertyOrder)
	if !ok || req.Mode != "" && req.Mode != "form" {
		s.a.mu.Lock()
		out := s.a.out
		s.a.mu.Unlock()
		out.Emit(agent.Event{Type: agent.TypeNotice, Code: "elicitation_declined",
			Text: "The MCP server " + req.MCPServerName + " asked for something Socrates cannot show, so it was declined: " + message})
		return map[string]any{"action": "decline"}
	}
	answer, err := s.wait(ctx, "ask_elicit_"+id, "", p.JSON())
	if err != nil {
		return nil
	}
	action, content := askuser.ElicitationResult(&p, req.RequestedSchema, answer)
	if content == nil {
		return map[string]any{"action": action}
	}
	return map[string]any{"action": action, "content": content}
}
