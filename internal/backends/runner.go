// Package backends runs the delegate coding agents (Claude Code, Codex,
// OpenCode) as child processes and normalises their very different JSON event
// streams into one shape that the web UI can render live.
package backends

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/proc"
)

// Event kinds emitted by a delegate agent.
const (
	EventStatus   = "status"
	EventThinking = "thinking"
	EventText     = "text"
	EventTool     = "tool"
	EventLog      = "log"
	EventError    = "error"
)

// Event is one normalised step of a delegate agent.
type Event struct {
	// ID makes an event updatable: emitting twice with the same ID replaces
	// the previous state (used for tool calls that finish later).
	ID     string
	Kind   string
	Title  string
	Body   string
	Status string // running | done | failed
	Detail map[string]any
}

// Request describes one delegation.
type Request struct {
	Backend config.Backend
	Prompt  string
	Workdir string

	// Interactive permission bridge (claude backend, approval mode "ask").
	SelfPath    string
	BridgeURL   string
	BridgeToken string
	RunID       string
	StepID      string
}

// Result is what the delegate agent produced.
type Result struct {
	Text     string
	ExitCode int
	Duration time.Duration
	Meta     map[string]any
}

// Emitter receives normalised events while the agent is running.
type Emitter func(Event)

// parser turns one line of agent output into events and collects the final answer.
type parser interface {
	Line(line string, emit Emitter)
	Final() string
	Meta() map[string]any
}

// Run executes a delegate agent and blocks until it is finished.
func Run(ctx context.Context, req Request, emit Emitter) (*Result, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	timeout := time.Duration(req.Backend.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args, stdin, p := build(req)

	cmd := exec.CommandContext(ctx, req.Backend.Command, args...)
	cmd.Dir = req.Workdir
	cmd.Env = environ()
	proc.Configure(cmd)
	cmd.Cancel = func() error { return proc.Kill(cmd) }
	cmd.WaitDelay = 5 * time.Second
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("command %q not found - install it or set the correct path in /admin", req.Backend.Command)
		}
		return nil, fmt.Errorf("start %s: %w", req.Backend.Command, err)
	}

	var mu sync.Mutex
	safeEmit := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		emit(e)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for sc.Scan() {
			p.Line(sc.Text(), safeEmit)
		}
	}()

	var errTail strings.Builder
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		logged := 0
		for sc.Scan() {
			line := strings.TrimSpace(stripANSI(sc.Text()))
			if line == "" {
				continue
			}
			if errTail.Len() < 8000 {
				errTail.WriteString(line)
				errTail.WriteString("\n")
			}
			if logged < 200 {
				logged++
				safeEmit(Event{
					Kind:   EventLog,
					Body:   line,
					Status: "done",
					Detail: map[string]any{"stream": "stderr"},
				})
			}
		}
	}()

	// cmd.Wait closes the pipes as soon as the process is gone, so every reader
	// has to be finished first - otherwise a fast agent loses its last lines,
	// including the result event.
	wg.Wait()
	waitErr := cmd.Wait()
	dur := time.Since(started)

	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	}

	res := &Result{Text: strings.TrimSpace(p.Final()), ExitCode: exitCode, Duration: dur, Meta: p.Meta()}

	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("%s timed out after %s", req.Backend.Name, timeout)
	}
	if ctx.Err() == context.Canceled {
		return res, context.Canceled
	}
	if waitErr != nil && exitCode != 0 {
		tail := strings.TrimSpace(lastLines(errTail.String(), 8))
		if res.Text != "" {
			// The agent produced an answer but exited non-zero: keep the answer.
			return res, nil
		}
		if tail == "" {
			tail = waitErr.Error()
		}
		return res, fmt.Errorf("%s exited with code %d: %s", req.Backend.Name, exitCode, tail)
	}
	if waitErr != nil {
		return res, waitErr
	}
	if res.Text == "" {
		res.Text = "(the agent finished without producing a final message)"
	}
	return res, nil
}

// build assembles the command line, the stdin payload and the output parser.
func build(req Request) (args []string, stdin string, p parser) {
	b := req.Backend
	switch b.Kind {
	case config.KindClaude:
		args = []string{"-p", "--output-format", "stream-json", "--verbose"}
		if b.Model != "" {
			args = append(args, "--model", b.Model)
		}
		if b.Approval == "ask" && req.BridgeURL != "" && req.SelfPath != "" {
			cfg := map[string]any{"mcpServers": map[string]any{
				"socrates": map[string]any{
					"command": req.SelfPath,
					"args":    []string{"bridge"},
					"env": map[string]string{
						"SOCRATES_BRIDGE_URL":   req.BridgeURL,
						"SOCRATES_BRIDGE_TOKEN": req.BridgeToken,
						"SOCRATES_RUN_ID":       req.RunID,
						"SOCRATES_STEP_ID":      req.StepID,
						"SOCRATES_AGENT_NAME":   b.Name,
					},
				},
			}}
			raw, _ := json.Marshal(cfg)
			args = append(args,
				"--mcp-config", string(raw),
				"--permission-prompt-tool", "mcp__socrates__permission_prompt",
			)
		} else {
			args = append(args, "--permission-mode", "bypassPermissions")
		}
		args = append(args, b.ExtraArgs...)
		return args, req.Prompt, newClaudeParser()

	case config.KindCodex:
		args = []string{"exec", "--json", "--skip-git-repo-check", "--color", "never"}
		if b.Model != "" {
			args = append(args, "-m", b.Model)
		}
		sandbox := b.Sandbox
		if b.Approval == "ask" {
			sandbox = "read-only"
		}
		if sandbox == "" {
			sandbox = "workspace-write"
		}
		args = append(args, "--sandbox", sandbox)
		args = append(args, b.ExtraArgs...)
		return args, req.Prompt, newCodexParser()

	case config.KindOpenCode:
		args = []string{"run", "--format", "json", "--thinking"}
		if b.Approval != "ask" {
			args = append(args, "--auto")
		}
		if b.Model != "" {
			args = append(args, "-m", b.Model)
		}
		args = append(args, b.ExtraArgs...)
		args = append(args, req.Prompt)
		return args, "", newOpenCodeParser()

	default:
		usedPlaceholder := false
		for _, a := range b.ExtraArgs {
			if strings.Contains(a, "{{prompt}}") {
				usedPlaceholder = true
			}
			args = append(args, strings.ReplaceAll(a, "{{prompt}}", req.Prompt))
		}
		if usedPlaceholder {
			return args, "", newTextParser()
		}
		return args, req.Prompt, newTextParser()
	}
}

func environ() []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"SOCRATES=1",
		"CI=1",
		"NO_COLOR=1",
		"TERM=dumb",
		"FORCE_COLOR=0",
	)
	return env
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// stripANSI removes escape sequences so log lines stay readable in HTML.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
					j++
				}
				i = j
				continue
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… (truncated)"
}

// jsonLine decodes a line of JSONL output, returning false for log noise.
func jsonLine(line string) (map[string]any, bool) {
	line = strings.TrimSpace(line)
	if line == "" || (!strings.HasPrefix(line, "{") && !strings.HasPrefix(line, "[")) {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return nil, false
	}
	return m, true
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func obj(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

func arr(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].([]any); ok {
		return v
	}
	return nil
}

// compactJSON renders a value as a single readable line for tool arguments.
func compactJSON(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// textOf flattens Anthropic style content blocks into plain text.
func textOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				if s := str(m, "text"); s != "" {
					parts = append(parts, s)
					continue
				}
				if c, ok := m["content"]; ok {
					if s := textOf(c); s != "" {
						parts = append(parts, s)
					}
				}
			} else if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if s := str(t, "text"); s != "" {
			return s
		}
		return compactJSON(t)
	}
	return ""
}
