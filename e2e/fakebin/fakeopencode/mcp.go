//go:build !windows

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// The local MCP servers of the config (OPENCODE_CONFIG_CONTENT's `mcp`), run
// the way opencode 1.19.4 runs them: the command as a child on stdio pipes,
// JSON-RPC 2.0 one message per line, initialize (protocol 2025-11-25),
// notifications/initialized and tools/list when the server starts; a tool is
// <server>_<tool> to the model, a call carries a progress token, and an abort
// is a notifications/cancelled (research/05 §4.3).

// mcpClient is one running local server.
type mcpClient struct {
	name         string
	stdin        io.WriteCloser
	tools        []string
	instructions string

	mu       sync.Mutex
	next     int
	tokens   int
	waiting  map[int]chan json.RawMessage
	progress map[string]int
}

// mcpConfig is the part of an `mcp` entry the fake uses.
type mcpConfig struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Timeout     int64             `json:"timeout"`
	Enabled     *bool             `json:"enabled"`
}

// startMCP starts every local server of the config and connects it.
func (s *srv) startMCP() {
	var doc struct {
		MCP map[string]mcpConfig `json:"mcp"`
	}
	_ = json.Unmarshal([]byte(os.Getenv("OPENCODE_CONFIG_CONTENT")), &doc)
	s.mcp = map[string]*mcpClient{}
	s.mcpConfig = doc.MCP
	s.mcpFailed = map[string]string{}
	for name, cfg := range doc.MCP {
		if cfg.Type != "local" || len(cfg.Command) == 0 || (cfg.Enabled != nil && !*cfg.Enabled) {
			continue
		}
		c, err := startMCPServer(name, cfg.Command, cfg.Environment)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp %s: %v\n", name, err)
			s.mcpFailed[name] = err.Error()
			continue
		}
		s.mcp[name] = c
	}
}

// mcpStates is GET /mcp, as opencode 1.19.5 answers it: every server of the
// config by name, connected, disabled, or failed with why. A remote server
// is taken for connected.
func (s *srv) mcpStates() map[string]any {
	out := map[string]any{}
	for name, cfg := range s.mcpConfig {
		switch {
		case cfg.Enabled != nil && !*cfg.Enabled:
			out[name] = map[string]any{"status": "disabled"}
		case s.mcpFailed[name] != "":
			out[name] = map[string]any{"status": "failed", "error": s.mcpFailed[name]}
		default:
			out[name] = map[string]any{"status": "connected"}
		}
	}
	return out
}

func startMCPServer(name string, command []string, env map[string]string) (*mcpClient, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stderr = os.Stderr
	// OpenCode starts a local server with the config's environment on top
	// of its own.
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &mcpClient{name: name, stdin: stdin, waiting: map[int]chan json.RawMessage{}, progress: map[string]int{}}
	go c.read(stdout)
	init, err := c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "opencode", "version": version}}, nil, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var ir struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	_ = json.Unmarshal(init, &ir)
	c.instructions = ir.Result.Instructions
	c.notify("notifications/initialized", nil)
	list, err := c.request("tools/list", map[string]any{}, nil, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var lr struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	_ = json.Unmarshal(list, &lr)
	for _, t := range lr.Result.Tools {
		c.tools = append(c.tools, name+"_"+t.Name)
	}
	return c, nil
}

func (c *mcpClient) read(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		line := append([]byte(nil), sc.Bytes()...)
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		if msg.Method == "notifications/progress" {
			var p struct {
				Token json.RawMessage `json:"progressToken"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			c.mu.Lock()
			c.progress[string(p.Token)]++
			c.mu.Unlock()
			continue
		}
		if msg.ID == nil || msg.Method != "" {
			continue
		}
		c.mu.Lock()
		ch := c.waiting[*msg.ID]
		delete(c.waiting, *msg.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- line
		}
	}
	c.mu.Lock()
	for id, ch := range c.waiting {
		close(ch)
		delete(c.waiting, id)
	}
	c.mu.Unlock()
}

func (c *mcpClient) write(v any) {
	raw, _ := json.Marshal(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.stdin.Write(append(raw, '\n'))
}

func (c *mcpClient) notify(method string, params any) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	c.write(msg)
}

var errMCPClosed = errors.New("MCP error -32000: Connection closed")

// request sends one request and waits for its response, until timeout (0:
// none) or cancel, which withdraws it with notifications/cancelled.
func (c *mcpClient) request(method string, params any, cancel <-chan struct{}, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.next
	c.next++
	ch := make(chan json.RawMessage, 1)
	c.waiting[id] = ch
	c.mu.Unlock()
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	var expire <-chan time.Time
	if timeout > 0 {
		expire = time.After(timeout)
	}
	select {
	case raw, ok := <-ch:
		if !ok {
			return nil, errMCPClosed
		}
		return raw, nil
	case <-cancel:
		c.mu.Lock()
		delete(c.waiting, id)
		c.mu.Unlock()
		c.notify("notifications/cancelled", map[string]any{"requestId": id, "reason": "AbortError: This operation was aborted"})
		return nil, errAborted
	case <-expire:
		return nil, errors.New("MCP error -32001: Request timed out")
	}
}

// mcpStatus is what /mcp says: every server with its tools and the start of
// its instructions.
func (s *srv) mcpStatus() string {
	var names []string
	for name := range s.mcpConfig {
		names = append(names, name)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		cfg := s.mcpConfig[name]
		c := s.mcp[name]
		if c == nil {
			lines = append(lines, fmt.Sprintf("%s: failed (%s)", name, strings.Join(cfg.Command, " ")))
			continue
		}
		instr := c.instructions
		if len(instr) > 60 {
			instr = instr[:60]
		}
		lines = append(lines, fmt.Sprintf("%s: connected timeout=%d tools=%s instructions=%s", name, cfg.Timeout,
			strings.Join(c.tools, ","), instr))
	}
	return strings.Join(lines, "\n")
}

// askUserCall is the call /askuser makes without arguments.
const askUserCall = `{"title":"Setup","questions":[` +
	`{"id":"db","header":"Database","question":"Which database?","kind":"single",` +
	`"options":[{"id":"pg","label":"PostgreSQL","recommended":true},{"id":"sqlite","label":"SQLite"}]},` +
	`{"id":"name","question":"What is it called?","kind":"text"}]}`

// askUser is the model calling socrates_ask_user: a tool part that runs for
// as long as the call does, and the call over MCP.
func (t *turn) askUser(arg string) error {
	c := t.s.mcp["socrates"]
	if c == nil {
		return t.say([]string{"ask_user is not available"}, 5*time.Millisecond)
	}
	args := json.RawMessage(askUserCall)
	if strings.TrimSpace(arg) != "" {
		args = json.RawMessage(strings.TrimSpace(arg))
	}
	var input map[string]any
	_ = json.Unmarshal(args, &input)
	start := time.Now().UnixMilli()
	p := t.part(map[string]any{"type": "tool", "tool": "socrates_ask_user", "callID": "call_" + newID("x")[2:12],
		"state": map[string]any{"status": "running", "input": input, "time": map[string]any{"start": start}}})
	c.mu.Lock()
	c.tokens++
	token := c.tokens
	c.mu.Unlock()
	raw, err := c.request("tools/call", map[string]any{"name": "ask_user", "arguments": args,
		"_meta": map[string]any{"progressToken": token}}, t.run.cancel, time.Duration(t.s.mcpConfig["socrates"].Timeout)*time.Millisecond)
	if err != nil {
		p["state"] = map[string]any{"status": "error", "input": input, "error": err.Error(),
			"time": map[string]any{"start": start, "end": time.Now().UnixMilli()}}
		t.part(p)
		if errors.Is(err, errAborted) {
			return err
		}
		return t.say([]string{"ask_user failed: " + err.Error()}, 5*time.Millisecond)
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &resp)
	text := ""
	if len(resp.Result.Content) > 0 {
		text = resp.Result.Content[0].Text
	}
	c.mu.Lock()
	progress := c.progress[fmt.Sprint(token)]
	c.mu.Unlock()
	status := "completed"
	if resp.Result.IsError {
		status = "error"
	}
	p["state"] = map[string]any{"status": status, "input": input, "output": text, "title": "ask_user",
		"metadata": map[string]any{"progress": progress}, "time": map[string]any{"start": start, "end": time.Now().UnixMilli()}}
	t.part(p)
	if resp.Result.IsError {
		return t.say([]string{"ask_user failed: " + text}, 5*time.Millisecond)
	}
	return t.say([]string{"ask_user returned: " + text}, 5*time.Millisecond)
}

// mcpTool is the model calling another tool of the "socrates" server (the
// chat tools): a tool part, the call, then "<name> returned: <result>" or
// "<name> failed: <result>".
func (t *turn) mcpTool(arg string) error {
	name, js, _ := strings.Cut(strings.TrimSpace(arg), " ")
	c := t.s.mcp["socrates"]
	if c == nil || name == "" {
		return t.say([]string{name + " is not available"}, 5*time.Millisecond)
	}
	args := json.RawMessage(`{}`)
	if strings.TrimSpace(js) != "" {
		args = json.RawMessage(strings.TrimSpace(js))
	}
	var input map[string]any
	_ = json.Unmarshal(args, &input)
	start := time.Now().UnixMilli()
	p := t.part(map[string]any{"type": "tool", "tool": "socrates_" + name, "callID": "call_" + newID("x")[2:12],
		"state": map[string]any{"status": "running", "input": input, "time": map[string]any{"start": start}}})
	raw, err := c.request("tools/call", map[string]any{"name": name, "arguments": args}, t.run.cancel, time.Minute)
	if err != nil {
		p["state"] = map[string]any{"status": "error", "input": input, "error": err.Error(),
			"time": map[string]any{"start": start, "end": time.Now().UnixMilli()}}
		t.part(p)
		if errors.Is(err, errAborted) {
			return err
		}
		return t.say([]string{name + " failed: " + err.Error()}, 5*time.Millisecond)
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &resp)
	text := ""
	if len(resp.Result.Content) > 0 {
		text = resp.Result.Content[0].Text
	}
	if resp.Error != nil {
		text, resp.Result.IsError = resp.Error.Message, true
	}
	status := "completed"
	if resp.Result.IsError {
		status = "error"
	}
	p["state"] = map[string]any{"status": status, "input": input, "output": text, "title": name,
		"time": map[string]any{"start": start, "end": time.Now().UnixMilli()}}
	t.part(p)
	if resp.Result.IsError {
		return t.say([]string{name + " failed: " + text}, 5*time.Millisecond)
	}
	return t.say([]string{name + " returned: " + text}, 5*time.Millisecond)
}
