package agenthost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// MCPAskTool is the name an agent that prefixes MCP tools with their server
// (OpenCode: <server>_<tool>) knows ask_user by.
const MCPAskTool = askuser.ServerName + "_" + askuser.Name

// MCPAskMain is the `socrates mcp-ask --socket <host socket> [--pages
// <dir>]` subcommand, and its exit code: ask_user as an MCP server on stdin
// and stdout, for an agent CLI that runs its MCP servers as processes of its
// own (OpenCode). Every call reaches the chat's agent host over its socket:
// ask_user puts its question (OpAsk with via message) and returns at once,
// check_questions and withdraw_question read and close the chat's questions
// (OpQuestions, OpWithdrawAsk; REBUILD.md §5.3). The chat tools are served beside it when the environment names
// their endpoint (chattools.FromEnv). --pages is the chat's pages folder, which the
// server's instructions name (agent.PagesGuidance).
func MCPAskMain(args []string, version string) int {
	fs := flag.NewFlagSet("mcp-ask", flag.ContinueOnError)
	sock := fs.String("socket", "", "the agent host's socket")
	pages := fs.String("pages", "", "the chat's pages folder")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *sock == "" {
		fmt.Fprintln(os.Stderr, "socrates mcp-ask: -socket is required")
		return 2
	}
	log.SetFlags(0)
	log.SetPrefix("socrates mcp-ask: ")
	if err := ServeMCPAsk(context.Background(), *sock, version, chattools.FromEnv(), *pages, os.Stdin, os.Stdout); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}

// ServeMCPAsk serves ask_user, and the chat tools at chats when it is
// usable, on one MCP stdio stream until in ends; version is the server's in
// initialize, and pages the chat's pages folder its instructions name ("" for
// none). Requests run concurrently, each on a goroutine of its own because a
// tools/call blocks, under a context that is
// registered before the next line is read: a notifications/cancelled, read
// in order, always finds the call it withdraws. A call still waiting when in
// ends is withdrawn.
func ServeMCPAsk(ctx context.Context, sock, version string, chats chattools.Endpoint, pages string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wmu sync.Mutex
	write := func(line []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_, _ = out.Write(append(line, '\n'))
	}
	var instructions string
	switch {
	case chats.Usable():
		instructions = askuser.Guidance(MCPAskTool, askuser.ServerName+"_") + "\n\n" + chats.Guidance(askuser.ServerName+"_", false) +
			"\n\n" + agent.TodoGuidance("todowrite")
	default:
		instructions = askuser.Guidance(MCPAskTool, askuser.ServerName+"_") + "\n\n" + agent.TodoGuidance("todowrite")
	}
	// How to link a Markdown file so that it opens beside the chat (DOCS.md
	// §3), and the chat's pages folder.
	instructions = agent.WithPagesGuidance(instructions+"\n\n"+agent.DocsGuidance, pages)
	m := &askuser.MCP{
		Version:      version,
		Instructions: instructions,
		Chats:        chats,
		Put: func(ctx context.Context, call askuser.Call) (string, error) {
			return askHost(ctx, sock, call.Payload)
		},
		Questions: socketQuestions{sock},
	}
	var calls sync.WaitGroup
	var cmu sync.Mutex
	inflight := map[string]context.CancelFunc{}
	r := bufio.NewReaderSize(in, 64<<10)
	for {
		line, err := readLine(r)
		if len(strings.TrimSpace(string(line))) > 0 {
			msg := json.RawMessage(append([]byte(nil), line...))
			if id, ok := askuser.RequestID(msg); ok {
				cctx, ccancel := context.WithCancel(ctx)
				cmu.Lock()
				inflight[id] = ccancel
				cmu.Unlock()
				calls.Add(1)
				go func() {
					defer calls.Done()
					defer func() {
						ccancel()
						cmu.Lock()
						delete(inflight, id)
						cmu.Unlock()
					}()
					if resp := m.Handle(cctx, msg); resp != nil {
						write(resp)
					}
				}()
			} else if id, ok := askuser.Cancelled(msg); ok {
				cmu.Lock()
				withdraw := inflight[id]
				cmu.Unlock()
				if withdraw != nil {
					withdraw()
				}
			} else if resp := m.Handle(ctx, msg); resp != nil {
				write(resp)
			}
		}
		if err != nil {
			cancel()
			calls.Wait()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// askHost puts one question into the chat's queue through its host and
// returns its ask id: the answer goes to the agent as a message (REBUILD.md
// §5.3). The connection asks for no events: a since far beyond any journal.
func askHost(ctx context.Context, sock string, payload askuser.Payload) (string, error) {
	ack, err := hostCommand(ctx, sock, Command{Op: OpAsk, Payload: payload.JSON(), Via: agent.ViaMessage})
	if err != nil {
		return "", err
	}
	return ack.AskID, nil
}

// socketQuestions is check_questions and withdraw_question through the
// chat's host.
type socketQuestions struct{ sock string }

func (q socketQuestions) List(ctx context.Context) ([]askuser.Asked, error) {
	ack, err := hostCommand(ctx, q.sock, Command{Op: OpQuestions})
	if err != nil {
		return nil, err
	}
	return ack.Asks, nil
}

func (q socketQuestions) Withdraw(ctx context.Context, id, reason string) (bool, error) {
	_, err := hostCommand(ctx, q.sock, Command{Op: OpWithdrawAsk, AskID: id, Reason: reason})
	if errors.Is(err, errUnknownAsk) {
		return false, nil
	}
	return err == nil, err
}

// errUnknownAsk is the host's unknown_ask.
var errUnknownAsk = errors.New("unknown question")

// hostCommand sends one command of a local tool to the chat's host and
// returns its ack.
func hostCommand(ctx context.Context, sock string, cmd Command) (*Ack, error) {
	c, err := Dial(ctx, sock, NoEvents)
	if err != nil {
		return nil, fmt.Errorf("the chat is not reachable: %w", err)
	}
	defer c.Close()
	if c.Hello.Proto < QuestionsProtocol {
		return nil, errors.New("the chat's agent host is too old for this; it can be done again after the chat restarts")
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err := c.Send(cmd); err != nil {
		return nil, err
	}
	for {
		f, err := c.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("the chat went away before it answered: %w", err)
		}
		switch {
		case f.Fail != nil && f.Fail.Code == CodeUnknownAsk:
			return nil, errUnknownAsk
		case f.Fail != nil:
			return nil, fmt.Errorf("the chat refused it: %s", f.Fail.Msg)
		case f.Ack != nil:
			return f.Ack, nil
		}
	}
}
