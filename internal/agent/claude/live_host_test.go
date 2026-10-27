//go:build linux

package claude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
)

// liveHost launches a real agent host running the real claude on the
// account of conf, as Socrates does, and connects to it.
func liveHost(t *testing.T, conf, model string) (*hostRun, []string, string) {
	t.Helper()
	data := t.TempDir()
	work := filepath.Join(data, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("no claude on the PATH")
	}
	home, _ := os.UserHomeDir()
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	chat := "liveswitch"
	l := &agenthost.Launcher{DataDir: data, Binary: os.Args[0], Logf: t.Logf}
	t.Cleanup(func() { l.Stop(context.Background(), chat) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := l.Launch(ctx, agenthost.Spec{ChatID: chat, Harness: Harness, Workdir: work, Account: "first", Model: model,
		Binary: bin, Env: append(append([]string{}, base...), "CLAUDE_CONFIG_DIR="+conf), ParkAfterMS: 600000}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	c, err := agenthost.Dial(context.Background(), agenthost.SocketPath(data, chat), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &hostRun{t: t, c: c, wait: 6 * time.Minute}, base, work
}

// liveSwitch switches the chat now, with Continue, and returns how long it
// took until the divider.
func liveSwitch(t *testing.T, h *hostRun, base []string, conf string) time.Duration {
	t.Helper()
	began := time.Now()
	if err := h.c.Send(agenthost.Command{Op: agenthost.OpSwitchAccount, ClientID: "s1", Account: "second",
		When: agenthost.WhenNow, Continue: true, Env: append(append([]string{}, base...), "CLAUDE_CONFIG_DIR="+conf)}); err != nil {
		t.Fatal(err)
	}
	h.until("the divider", func(ev agent.Event) bool { return ev.Type == agent.TypeAccountSwitched })
	return time.Since(began)
}

// toolCalls is every tool part of the chat, nested ones included, in order.
func toolCalls(t *testing.T, p *agent.Projection) []toolContent {
	var out []toolContent
	var walk func(parts []*agent.Part)
	walk = func(parts []*agent.Part) {
		for _, part := range parts {
			if part.Kind == agent.PartTool {
				out = append(out, content(t, part))
			}
			walk(part.Children)
		}
	}
	for _, m := range p.Messages {
		walk(m.Parts)
	}
	return out
}

// A switch while a background command runs: the old CLI goes at once, the
// agent on the second account is told what stopped and starts it again.
func TestLiveASwitchRestartsABackgroundCommand(t *testing.T) {
	e := liveEnv(t)
	second := liveSecond(t)
	h, base, work := liveHost(t, e.conf, "haiku")
	h.reply("m1", "Use the Bash tool with run_in_background set to true to run exactly this command: "+
		"`sleep 240 && echo finished > marker.txt`. Do not wait for it; end your turn right after starting it.")
	if p := agent.Fold(h.events); p.Tasks != 1 {
		t.Fatalf("the command is not running in the background: tasks %d\n%s", p.Tasks, dump(h.events))
	}
	took := liveSwitch(t, h, base, second)
	t.Logf("the switch took %v", took)
	if took > 10*time.Second {
		t.Fatalf("the switch waited for the background command: %v", took)
	}
	// The restart: the agent runs the command again in the background.
	h.until("the command started again", func(ev agent.Event) bool {
		return ev.Type == agent.TypeTasks && ev.Count == 1
	})
	h.until("the restart's turn to end", func(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded })
	p := agent.Fold(h.events)
	calls := toolCalls(t, p)
	runs := 0
	for _, c := range calls {
		if c.Tool == "Bash" && strings.Contains(c.Command, "sleep 240") {
			runs++
		}
	}
	if runs < 2 {
		t.Fatalf("the command ran %d times:\n%s", runs, dump(h.events))
	}
	if p.Account != "second" || p.Tasks != 1 {
		t.Fatalf("after the restart: account %s, tasks %d", p.Account, p.Tasks)
	}
	_ = work
}

// A switch while a workflow runs: the agent on the second account resumes
// the workflow from its run, and what its first phase finished is not done
// again.
func TestLiveASwitchResumesAWorkflow(t *testing.T) {
	e := liveEnv(t)
	second := liveSecond(t)
	model := os.Getenv("SOCRATES_CLAUDE_LIVE_MODEL")
	if model == "" {
		model = "sonnet"
	}
	h, base, work := liveHost(t, e.conf, model)
	h.reply("m1", "Start a dynamic workflow with the Workflow tool, in the background, and end your turn right after "+
		"launching it without waiting. Its script: two phases, one agent each, run one after the other. "+
		"Phase 'Quick': an agent that runs the Bash command `echo quick-done >> quick.log` and replies 'quick ok'. "+
		"Phase 'Slow': an agent that runs the Bash command `sleep 240; echo slow-done >> slow.log` and replies 'slow ok'.")
	// Wait until the first phase has finished and the slow agent works.
	h.until("the quick phase to finish", func(ev agent.Event) bool {
		if ev.Type != agent.TypePart {
			return false
		}
		raw, _ := os.ReadFile(filepath.Join(work, "quick.log"))
		return strings.Contains(string(raw), "quick-done") && strings.Contains(string(ev.Content), "sleep 240")
	})
	if p := agent.Fold(h.events); p.Tasks < 1 {
		t.Fatalf("the workflow is not running: tasks %d", p.Tasks)
	}
	took := liveSwitch(t, h, base, second)
	t.Logf("the switch took %v", took)
	if took > 10*time.Second {
		t.Fatalf("the switch waited for the workflow: %v", took)
	}
	h.until("the workflow resumed", func(ev agent.Event) bool {
		if ev.Type != agent.TypePart || !strings.Contains(string(ev.Content), `"tool":"Workflow"`) {
			return false
		}
		var c toolContent
		_ = json.Unmarshal(ev.Content, &c)
		return strings.Contains(string(c.Input), "resumeFromRunId")
	})
	// The slow agent runs again; the quick one comes from the run journal.
	h.until("the slow agent to run again", func(ev agent.Event) bool {
		return ev.Type == agent.TypePart && strings.Contains(string(ev.Content), "sleep 240") && ev.State == agent.PartRunning
	})
	raw, _ := os.ReadFile(filepath.Join(work, "quick.log"))
	p := agent.Fold(h.events)
	for _, m := range p.Messages {
		if m.Role == agent.RoleUser {
			t.Logf("user: %s", m.Text)
		}
	}
	for _, c := range toolCalls(t, p) {
		in := string(c.Input)
		if len(in) > 300 {
			in = in[:300] + "…"
		}
		t.Logf("call %s %q status %s", c.Tool, c.Summary, in)
	}
	if n := strings.Count(string(raw), "quick-done"); n != 1 {
		t.Fatalf("the quick phase ran %d times", n)
	}
}
