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

// chatsPrefix is what Claude Code puts before the name of a tool of the
// socrates server.
const chatsPrefix = "mcp__" + askuser.ServerName + "__"

// askGuidance is the system-prompt text every chat gets (REBUILD.md §5.3).
// Claude Code defers MCP tools behind ToolSearch, which the model finds on
// its own; saying so saves it a turn of looking.
var askGuidance = askuser.Guidance(AskTool, chatsPrefix) +
	" If the tools are not loaded yet, load them with ToolSearch (select:" + AskTool + "," + chatsPrefix + askuser.CheckName +
	"," + chatsPrefix + askuser.WithdrawName + ") first."

// todoGuidance is how a chat keeps its task list (agent.TodoGuidance): the
// TaskCreate calls of a new list go out in one message.
var todoGuidance = agent.TodoGuidance("TaskCreate (all entries in one message) and TaskUpdate")

// askServerConfig is the server's sdkMcpServerConfigs entry. ask_user
// returns at once, but an elicitation the server forwards waits for the
// person: an in-process server has no idle timeout, only the wall-clock one,
// which is set as high as the CLI takes it (2^31-1 ms, almost 25 days).
// disableAutoBackground keeps the CLI from moving a call into the
// background.
var askServerConfig = json.RawMessage(`{"timeout":2147483647,"disableAutoBackground":true}`)

// askServer is the chat's in-process MCP server "socrates": the CLI reaches
// it over the control protocol (mcp_message). ask_user puts its question
// as an ask the answer of which goes to the agent as a message, and returns
// at once; check_questions and withdraw_question read and close the
// journal's questions (StartSpec.Questions); the chat tools reach the
// server. An elicitation the CLI forwards waits for its answer (waits).
type askServer struct {
	a     *Adapter
	mcp   *askuser.MCP
	waits agent.Waits
}

func newAskServer(a *Adapter) *askServer {
	s := &askServer{a: a}
	a.mu.Lock()
	chats, questions := a.spec.ChatTools, a.spec.Questions
	a.mu.Unlock()
	s.mcp = &askuser.MCP{Put: s.put, Chats: chats, Questions: questions}
	return s
}

func (s *askServer) Name() string            { return askuser.ServerName }
func (s *askServer) Config() json.RawMessage { return askServerConfig }

func (s *askServer) Handle(ctx context.Context, message json.RawMessage) (json.RawMessage, error) {
	return s.mcp.Handle(ctx, message), nil
}

// put puts one ask_user question into the person's queue: an ask of the
// running turn with via message, drawn at its tool part. A subagent's call
// is refused (askuser.ErrSubagent): the answer would reach only the main
// agent.
func (s *askServer) put(_ context.Context, call askuser.Call) (string, error) {
	id := askuser.NewID()
	s.a.mu.Lock()
	turnID, out := "", s.a.out
	if st := s.a.st; st != nil {
		if st.turn != nil {
			turnID = st.turn.id
		}
		if p := st.parts[call.ToolUseID]; call.ToolUseID != "" && p != nil && p.parent != "" {
			s.a.mu.Unlock()
			return "", askuser.ErrSubagent
		}
	}
	s.a.mu.Unlock()
	call.Payload.PartID = call.ToolUseID
	out.Emit(agent.Event{Type: agent.TypeAsk, AskID: id, TurnID: turnID, Payload: call.Payload.JSON(), Via: agent.ViaMessage})
	return id, nil
}

// wait emits an ask of the running turn and blocks until its answer or the
// end of ctx: the CLI withdrew the call (an interrupt, a timeout) or exited.
func (s *askServer) wait(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	s.a.mu.Lock()
	turnID, out := "", s.a.out
	if s.a.st != nil && s.a.st.turn != nil {
		turnID = s.a.st.turn.id
	}
	s.a.mu.Unlock()
	return s.waits.Wait(ctx, out, agent.Event{Type: agent.TypeAsk, AskID: id, TurnID: turnID, Payload: payload}, "")
}

// Answer hands an answer to the call waiting on it, and reports
// agent.ErrAskGone when none is (agent.Waits).
func (s *askServer) Answer(_ context.Context, askID string, answer json.RawMessage) error {
	return s.waits.Answer(askID, answer)
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
	answer, err := s.wait(ctx, "ask_elicit_"+id, p.JSON())
	if err != nil {
		return nil
	}
	action, content := askuser.ElicitationResult(&p, req.RequestedSchema, answer)
	if content == nil {
		return map[string]any{"action": action}
	}
	return map[string]any{"action": action, "content": content}
}
