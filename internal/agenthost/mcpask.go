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

	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// MCPAskTool is the name an agent that prefixes MCP tools with their server
// (OpenCode: <server>_<tool>) knows ask_user by.
const MCPAskTool = askuser.ServerName + "_" + askuser.Name

// MCPAskMain is the `socrates mcp-ask --socket <host socket>` subcommand, and
// its exit code: ask_user as an MCP server on stdin and stdout, for an agent
// CLI that runs its MCP servers as processes of its own (OpenCode). Every
// call reaches the chat's agent host over its socket (OpAsk) and waits there
// for the answer, sending progress notifications meanwhile, so that the
// CLI's call timeout never runs out (research/05 §4.3).
func MCPAskMain(args []string) int {
	fs := flag.NewFlagSet("mcp-ask", flag.ContinueOnError)
	sock := fs.String("socket", "", "the agent host's socket")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *sock == "" {
		fmt.Fprintln(os.Stderr, "socrates mcp-ask: -socket is required")
		return 2
	}
	log.SetFlags(0)
	log.SetPrefix("socrates mcp-ask: ")
	if err := ServeMCPAsk(context.Background(), *sock, os.Stdin, os.Stdout); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}

// ServeMCPAsk serves ask_user on one MCP stdio stream until in ends. Calls
// run concurrently; a call still waiting when in ends is withdrawn.
func ServeMCPAsk(ctx context.Context, sock string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wmu sync.Mutex
	write := func(line []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_, _ = out.Write(append(line, '\n'))
	}
	m := &askuser.MCP{
		Instructions: askuser.Guidance(MCPAskTool),
		Ask: func(ctx context.Context, call askuser.Call) (json.RawMessage, error) {
			return askHost(ctx, sock, call.Payload)
		},
		Progress: func(token json.RawMessage, n int) { write(askuser.ProgressNotification(token, n)) },
	}
	var calls sync.WaitGroup
	r := bufio.NewReaderSize(in, 64<<10)
	for {
		line, err := readLine(r)
		if len(strings.TrimSpace(string(line))) > 0 {
			msg := json.RawMessage(append([]byte(nil), line...))
			if isRequest(msg) {
				calls.Add(1)
				go func() {
					defer calls.Done()
					if resp := m.Handle(ctx, msg); resp != nil {
						write(resp)
					}
				}()
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

// isRequest is a JSON-RPC message that expects a response, which is handled
// on a goroutine of its own because a tools/call blocks; notifications (a
// cancel among them) are handled in order.
func isRequest(msg json.RawMessage) bool {
	var head struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	return json.Unmarshal(msg, &head) == nil && head.Method != "" && len(head.ID) > 0 && string(head.ID) != "null"
}

// askHost puts one question to the chat through its host and waits for the
// answer on that connection. The connection asks for no events: a since far
// beyond any journal.
func askHost(ctx context.Context, sock string, payload askuser.Payload) (json.RawMessage, error) {
	c, err := Dial(ctx, sock, 1<<62)
	if err != nil {
		return nil, fmt.Errorf("the chat is not reachable: %w", err)
	}
	defer c.Close()
	if c.Hello.Proto < AskProtocol {
		return nil, errors.New("the chat's agent host is too old to ask; the question can be asked again after the chat restarts")
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err := c.Send(Command{Op: OpAsk, Payload: payload.JSON()}); err != nil {
		return nil, err
	}
	for {
		f, err := c.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("the chat went away before the answer: %w", err)
		}
		switch {
		case f.Fail != nil:
			return nil, fmt.Errorf("the chat refused the question: %s", f.Fail.Msg)
		case f.Answer != nil:
			// A connection asks one question, so its answer is the one.
			return f.Answer.Answer, nil
		}
	}
}
