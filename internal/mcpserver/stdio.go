package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The agent reaches a built-in server through `socrates mcp-server <name>`
// (Main), started by its CLI as any stdio MCP server, with the server's
// tools socket and the chat's token in its environment (Endpoint.Env). The
// process holds nothing: every request goes to the Socrates server
// (EndpointPath), which runs the tool in its own process.

// The variables `socrates mcp-server` reads its endpoint from. No name holds
// KEY, SECRET or TOKEN, which Codex leaves out of what it passes on.
const (
	EnvSocket = "SOCRATES_MCP_SOCKET"
	EnvCaller = "SOCRATES_MCP_CALLER"
)

// EndpointPath is the request path on the tools socket.
const EndpointPath = "/mcp"

// Endpoint is where a session's built-in servers reach Socrates: its tools
// socket, and the token that names the chat.
type Endpoint struct {
	Socket string
	Token  string

	once   sync.Once
	client *http.Client
}

// httpClient is the endpoint's one client: its connection to the socket
// is kept between calls, and dropped after a while idle.
func (e *Endpoint) httpClient() *http.Client {
	e.once.Do(func() {
		e.client = &http.Client{Transport: &http.Transport{
			IdleConnTimeout: 30 * time.Second,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", e.Socket)
			},
		}}
	})
	return e.client
}

// Usable is whether the endpoint can be called at all.
func (e *Endpoint) Usable() bool { return e.Socket != "" && e.Token != "" }

// Env is the endpoint as `socrates mcp-server`'s environment.
func (e *Endpoint) Env() map[string]string {
	return map[string]string{EnvSocket: e.Socket, EnvCaller: e.Token}
}

// Request is one request of `socrates mcp-server` to the Socrates server.
type Request struct {
	Server string `json:"server"`
	// Method is "describe" (the server's instructions and tools) or "call".
	Method    string          `json:"method"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Description is the answer to describe.
type Description struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	Tools        []Tool `json:"tools"`
}

// Describe is a server as `socrates mcp-server` tells its agent about it.
func Describe(s Server) Description {
	return Description{Title: s.Title(), Instructions: s.Instructions(), Tools: s.Tools()}
}

// post sends one request and decodes the answer into out. A call has no
// time limit of its own: it lasts as long as the tool (a device that
// BrowserStack queues), and ends when ctx does.
func (e *Endpoint) post(ctx context.Context, req Request, out any) error {
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://socrates"+EndpointPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Authorization", "Bearer "+e.Token)
	hr.Header.Set("Content-Type", "application/json")
	res, err := e.httpClient().Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Socrates is not answering")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("Socrates answered %s", res.Status)
	}
	return json.Unmarshal(raw, out)
}

// Main is `socrates mcp-server <name>`, and its exit code.
func Main(args []string, version string) int {
	log.SetFlags(0)
	log.SetPrefix("socrates mcp-server: ")
	if len(args) != 1 || !NameRE.MatchString(args[0]) {
		fmt.Fprintln(os.Stderr, "usage: socrates mcp-server <name>   (started by an agent's CLI, never by hand)")
		return 2
	}
	e := &Endpoint{Socket: os.Getenv(EnvSocket), Token: os.Getenv(EnvCaller)}
	if !e.Usable() {
		fmt.Fprintln(os.Stderr, "socrates mcp-server: "+EnvSocket+" and "+EnvCaller+" are not set; Socrates starts this for a chat")
		return 2
	}
	if err := ServeStdio(context.Background(), e, args[0], version, os.Stdin, os.Stdout); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}

// progressEvery is how often a running call tells the client it still
// runs: OpenCode's MCP client restarts its call timeout on every progress
// notification.
const progressEvery = 20 * time.Second

// protocolVersions are the MCP versions spoken, newest first.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// stdio is one `socrates mcp-server` serving one server on a stream.
type stdio struct {
	e       *Endpoint
	name    string
	version string

	wmu sync.Mutex
	out io.Writer

	dmu  sync.Mutex
	desc *Description
}

// ServeStdio serves the built-in server name on one MCP stdio stream until
// in ends. Requests run concurrently, each on a goroutine of its own because
// a tools/call may take minutes, under a context registered before the next
// line is read: a notifications/cancelled, read in order, always finds the
// call it withdraws.
func ServeStdio(ctx context.Context, e *Endpoint, name, version string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &stdio{e: e, name: name, version: version, out: out}
	var calls sync.WaitGroup
	var cmu sync.Mutex
	inflight := map[string]context.CancelFunc{}
	r := bufio.NewReaderSize(in, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var msg rpcMessage
			if jerr := json.Unmarshal(line, &msg); jerr != nil {
				s.write(rpcError(nil, -32700, "the message is not JSON"))
			} else if id, ok := idKey(msg.ID); ok && msg.Method != "" {
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
					if resp := s.handle(cctx, msg); resp != nil {
						s.write(resp)
					}
				}()
			} else if msg.Method == "notifications/cancelled" {
				var p struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				_ = json.Unmarshal(msg.Params, &p)
				if id, ok := idKey(p.RequestID); ok {
					cmu.Lock()
					withdraw := inflight[id]
					cmu.Unlock()
					if withdraw != nil {
						withdraw()
					}
				}
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

func (s *stdio) write(line []byte) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.out.Write(append(line, '\n'))
}

// description is the server's, asked once.
func (s *stdio) description(ctx context.Context) (*Description, error) {
	s.dmu.Lock()
	defer s.dmu.Unlock()
	if s.desc != nil {
		return s.desc, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var d Description
	if err := s.e.post(ctx, Request{Server: s.name, Method: "describe"}, &d); err != nil {
		return nil, err
	}
	s.desc = &d
	return s.desc, nil
}

// handle answers one request: the response, or nil for a call the client
// withdrew.
func (s *stdio) handle(ctx context.Context, msg rpcMessage) []byte {
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		version := protocolVersions[0]
		for _, v := range protocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		own := s.version
		if own == "" {
			own = "1"
		}
		out := map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": own},
		}
		if d, err := s.description(ctx); err == nil {
			out["serverInfo"].(map[string]any)["title"] = d.Title
			if d.Instructions != "" {
				out["instructions"] = d.Instructions
			}
		}
		return rpcResult(msg.ID, out)
	case "ping":
		return rpcResult(msg.ID, map[string]any{})
	case "tools/list":
		d, err := s.description(ctx)
		if err != nil {
			return rpcError(msg.ID, -32603, "the tools could not be read from Socrates: "+err.Error())
		}
		tools := make([]any, 0, len(d.Tools))
		for _, t := range d.Tools {
			tools = append(tools, t)
		}
		return rpcResult(msg.ID, map[string]any{"tools": tools})
	case "tools/call":
		return s.call(ctx, msg)
	}
	return rpcError(msg.ID, -32601, "socrates does not offer "+msg.Method)
}

func (s *stdio) call(ctx context.Context, msg rpcMessage) []byte {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return rpcError(msg.ID, -32602, "unreadable tools/call parameters")
	}
	if token := p.Meta.ProgressToken; len(token) > 0 && string(token) != "null" {
		tctx, stop := context.WithCancel(ctx)
		ticking := make(chan struct{})
		go func() {
			defer close(ticking)
			tick := time.NewTicker(progressEvery)
			defer tick.Stop()
			for n := 1; ; n++ {
				select {
				case <-tctx.Done():
					return
				case <-tick.C:
					raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress",
						"params": map[string]any{"progressToken": token, "progress": n, "message": "Still working."}})
					s.write(raw)
				}
			}
		}()
		defer func() {
			stop()
			<-ticking
		}()
	}
	var res Result
	err := s.e.post(ctx, Request{Server: s.name, Method: "call", Tool: p.Name, Arguments: p.Arguments}, &res)
	switch {
	case err == nil:
		if res.Content == nil {
			res.Content = []Content{}
		}
		return rpcResult(msg.ID, res)
	case ctx.Err() != nil:
		// The client withdrew the call; it expects no answer.
		return nil
	}
	return rpcResult(msg.ID, Error("Socrates could not be reached: "+err.Error()))
}

// idKey is a JSON-RPC id as a map key; false for none.
func idKey(id json.RawMessage) (string, bool) {
	var b bytes.Buffer
	if len(id) == 0 || strings.TrimSpace(string(id)) == "null" || json.Compact(&b, id) != nil {
		return "", false
	}
	return b.String(), true
}

func rpcResult(id json.RawMessage, result any) []byte {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return raw
}

func rpcError(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	return raw
}
