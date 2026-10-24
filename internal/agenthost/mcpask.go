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
func MCPAskMain(args []string, version string) int {
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
	if err := ServeMCPAsk(context.Background(), *sock, version, os.Stdin, os.Stdout); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}

// ServeMCPAsk serves ask_user on one MCP stdio stream until in ends; version
// is the server's in initialize. Requests run concurrently, each on a
// goroutine of its own because a tools/call blocks, under a context that is
// registered before the next line is read: a notifications/cancelled, read
// in order, always finds the call it withdraws. A call still waiting when in
// ends is withdrawn.
func ServeMCPAsk(ctx context.Context, sock, version string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wmu sync.Mutex
	write := func(line []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_, _ = out.Write(append(line, '\n'))
	}
	m := &askuser.MCP{
		Version:      version,
		Instructions: askuser.Guidance(MCPAskTool),
		Ask: func(ctx context.Context, call askuser.Call) (json.RawMessage, error) {
			return askHost(ctx, sock, call.Payload)
		},
		Progress: func(token json.RawMessage, n int) { write(askuser.ProgressNotification(token, n)) },
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

// askHost puts one question to the chat through its host and waits for the
// answer on that connection. The connection asks for no events: a since far
// beyond any journal.
func askHost(ctx context.Context, sock string, payload askuser.Payload) (json.RawMessage, error) {
	c, err := Dial(ctx, sock, NoEvents)
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
