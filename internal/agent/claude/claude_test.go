//go:build !windows

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

var fakeBin string

func TestMain(m *testing.M) {
	// The test binary is also `socrates agent-host` (host_test.go).
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		os.Exit(agenthost.Main(os.Args[2:], "test"))
	}
	dir, err := os.MkdirTemp("", "fakeclaude-")
	if err != nil {
		panic(err)
	}
	fakeBin = filepath.Join(dir, "claude")
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	build := exec.Command("go", "build", "-o", fakeBin, "./e2e/fakebin/fakeclaude")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		panic("build fakeclaude: " + err.Error() + "\n" + string(out))
	}
	code := testhome.Main(m)
	os.RemoveAll(dir)
	os.Exit(code)
}

// rec is an Emitter that keeps everything, numbered the way the journal
// would, and folds it into a projection.
type rec struct {
	mu     sync.Mutex
	events []agent.Event
	deltas []agent.Delta
	proj   *agent.Projection
	exited chan error
	wake   chan struct{}
}

func newRec() *rec {
	return &rec{proj: agent.NewProjection(), exited: make(chan error, 1), wake: make(chan struct{}, 1)}
}

func (r *rec) Emit(events ...agent.Event) {
	r.mu.Lock()
	for _, ev := range events {
		ev.Seq = int64(len(r.events) + 1)
		ev.At = time.Now().UnixMilli()
		r.events = append(r.events, ev)
		r.proj.Apply(ev)
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *rec) Delta(messageID, partID, text string) {
	r.mu.Lock()
	r.deltas = append(r.deltas, agent.Delta{MessageID: messageID, PartID: partID, Text: text})
	r.mu.Unlock()
}

func (r *rec) Exited(err error) { r.exited <- err }

// waitFor waits until pred holds for the events so far.
func (r *rec) waitFor(t *testing.T, what string, pred func([]agent.Event) bool) []agent.Event {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		r.mu.Lock()
		evs := append([]agent.Event(nil), r.events...)
		r.mu.Unlock()
		if pred(evs) {
			return evs
		}
		select {
		case <-r.wake:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s; events:\n%s", what, dump(evs))
		}
	}
}

// turnsEnded waits for n turn.ended events.
func (r *rec) turnsEnded(t *testing.T, n int) []agent.Event {
	t.Helper()
	return r.waitFor(t, "turns to end", func(evs []agent.Event) bool { return count(evs, agent.TypeTurnEnded) >= n })
}

func (r *rec) view(fn func(*agent.Projection)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r.proj)
}

func count(evs []agent.Event, typ string) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func dump(evs []agent.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		raw, _ := json.Marshal(ev)
		b.WriteString(string(raw) + "\n")
	}
	return b.String()
}

// env is a test chat's environment: its own config directory, the fake on
// PATH first.
type env struct {
	conf, work, state string
	vars              []string
}

func newEnv(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	e := env{conf: filepath.Join(root, "conf"), work: filepath.Join(root, "work"), state: filepath.Join(root, "state")}
	for _, d := range []string{e.conf, e.work, e.state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.work, _ = filepath.EvalSymlinks(e.work)
	e.vars = []string{"PATH=" + filepath.Dir(fakeBin) + ":/usr/bin:/bin", "HOME=" + root, "CLAUDE_CONFIG_DIR=" + e.conf,
		"ANTHROPIC_API_KEY=ambient", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDECODE=1"}
	return e
}

func (e env) spec(resume string) agent.StartSpec {
	return agent.StartSpec{ChatID: "c1", Workdir: e.work, StateDir: e.state, Env: e.vars, Model: "haiku", Effort: "low", ResumeID: resume}
}

func start(t *testing.T, e env, resume string) (*Adapter, *rec) {
	t.Helper()
	a := New(Options{})
	r := newRec()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Start(ctx, e.spec(resume), r); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	return a, r
}

func send(t *testing.T, a *Adapter, id, text string) {
	t.Helper()
	if err := a.Send(context.Background(), agent.Outgoing{MessageID: id, ClientID: "c_" + id, Text: text}); err != nil {
		t.Fatalf("send %q: %v", text, err)
	}
}

// lastAssistant is the newest assistant message.
func lastAssistant(p *agent.Projection) *agent.Message {
	for i := len(p.Messages) - 1; i >= 0; i-- {
		if p.Messages[i].Role == agent.RoleAssistant {
			return p.Messages[i]
		}
	}
	return nil
}

func partsOf(m *agent.Message, kind string) []*agent.Part {
	var out []*agent.Part
	if m == nil {
		return nil
	}
	for _, p := range m.Parts {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

func content(t *testing.T, p *agent.Part) toolContent {
	t.Helper()
	var c toolContent
	if err := json.Unmarshal(p.Content, &c); err != nil {
		t.Fatalf("content of %s: %v", p.ID, err)
	}
	return c
}

func TestATextTurnStreamsAndEnds(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "hello there")
	evs := r.turnsEnded(t, 1)
	r.view(func(p *agent.Projection) {
		if p.ConversationID == "" {
			t.Fatal("no conversation id")
		}
		if m := p.Message("m1"); m != nil {
			t.Fatalf("the user message is the host's to journal, the adapter only moves its state: %+v", m)
		}
		am := lastAssistant(p)
		texts := partsOf(am, agent.PartText)
		if len(texts) != 1 || texts[0].Text != "you said: hello there" || texts[0].State != agent.PartDone {
			t.Fatalf("text parts = %+v", texts)
		}
		if am.State != agent.MessageDone {
			t.Fatalf("assistant state %q", am.State)
		}
		if p.LastTurn == nil || p.LastTurn.Outcome != agent.OutcomeCompleted || p.LastTurn.MessageID != "m1" {
			t.Fatalf("last turn = %+v", p.LastTurn)
		}
		if p.LastTurn.Usage == nil || p.LastTurn.Usage.Context != 127 || p.LastTurn.Usage.Window != 200000 {
			t.Fatalf("usage = %+v", p.LastTurn.Usage)
		}
		if len(p.Agent) == 0 || !strings.Contains(string(p.Agent), `"compact"`) || !strings.Contains(string(p.Agent), "fake@example.com") {
			t.Fatalf("agent info = %s", p.Agent)
		}
		if strings.Contains(string(p.Agent), `"doctor"`) {
			t.Fatalf("a terminal-only command is offered: %s", p.Agent)
		}
		if p.Model != "claude-haiku-5-5" {
			t.Fatalf("model = %q", p.Model)
		}
	})
	delivered := false
	for _, ev := range evs {
		if ev.Type == agent.TypeMessageState && ev.MessageID == "m1" && ev.State == agent.MessageDelivered {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("m1 never delivered:\n%s", dump(evs))
	}
	r.mu.Lock()
	n := len(r.deltas)
	r.mu.Unlock()
	if n < 2 {
		t.Fatalf("only %d deltas", n)
	}
}

// total_cost_usd is cumulative per conversation, restored on --resume: a
// turn's cost is model_usage_since_last_result, or the total's growth from
// the transcript's last cost-state - never the whole history on the first
// turn after a park.
func TestCostIsPerTurnAcrossAResume(t *testing.T) {
	for _, sinceLast := range []bool{true, false} {
		t.Run(fmt.Sprintf("since_last=%v", sinceLast), func(t *testing.T) {
			e := newEnv(t)
			if !sinceLast {
				e.vars = append(e.vars, "FAKE_NO_SINCE_LAST=1")
			}
			a, r := start(t, e, "")
			send(t, a, "m1", "one")
			r.turnsEnded(t, 1)
			send(t, a, "m2", "two")
			evs := r.turnsEnded(t, 2)
			var conv string
			r.view(func(p *agent.Projection) { conv = p.ConversationID })
			if err := a.Park(context.Background()); err != nil {
				t.Fatal(err)
			}
			b, r2 := start(t, e, conv)
			send(t, b, "m3", "three")
			evs = append(evs, r2.turnsEnded(t, 1)...)
			var costs []float64
			for _, ev := range evs {
				if ev.Type == agent.TypeTurnEnded {
					costs = append(costs, ev.CostUSD)
				}
			}
			if len(costs) != 3 {
				t.Fatalf("costs = %v", costs)
			}
			for _, c := range costs {
				if c < 0.00099 || c > 0.00101 {
					t.Fatalf("costs = %v, want 0.001 each", costs)
				}
			}
		})
	}
}

func TestEnvironmentIsBuiltFromScratch(t *testing.T) {
	e := newEnv(t)
	e.vars = append(e.vars, APIKeyEnv+"=sk-account")
	a, r := start(t, e, "")
	send(t, a, "m1", "/env")
	r.turnsEnded(t, 1)
	var text string
	r.view(func(p *agent.Projection) { text = partsOf(lastAssistant(p), agent.PartText)[0].Text })
	want := []string{"CLAUDE_CONFIG_DIR=" + e.conf, "CLAUDE_CODE_ENABLE_TODO_TOOLS=1", "ANTHROPIC_API_KEY=sk-account"}
	if os.Geteuid() == 0 {
		want = append(want, "IS_SANDBOX=1")
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("env lacks %s:\n%s", w, text)
		}
	}
	for _, bad := range []string{"ambient", "CLAUDECODE=", "CLAUDE_CODE_ENTRYPOINT", APIKeyEnv} {
		if strings.Contains(text, bad) {
			t.Errorf("env has %s:\n%s", bad, text)
		}
	}
	without := cliEnv([]string{"ANTHROPIC_API_KEY=ambient", "ANTHROPIC_BASE_URL=x", "PATH=/bin"})
	for _, kv := range without {
		if strings.HasPrefix(kv, "ANTHROPIC") {
			t.Errorf("a subscription account got %s", kv)
		}
	}
}

func TestArgvIsThePolicy(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/argv")
	r.turnsEnded(t, 1)
	var text string
	r.view(func(p *agent.Projection) { text = partsOf(lastAssistant(p), agent.PartText)[0].Text })
	for _, w := range []string{"-p", "--input-format stream-json", "--output-format stream-json", "--verbose",
		"--include-partial-messages", "--replay-user-messages", "--permission-prompt-tool stdio",
		"--setting-sources project,local", "--strict-mcp-config", "--dangerously-skip-permissions",
		"--disallowedTools AskUserQuestion", `"disableRemoteControl":true`, "--model haiku", "--effort low", "--session-id "} {
		if !strings.Contains(text, w) {
			t.Errorf("argv lacks %q:\n%s", w, text)
		}
	}
}

func TestToolsDiffsAndTheTaskList(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/tool")
	send(t, a, "m2", "/edit")
	send(t, a, "m3", "/write")
	send(t, a, "m4", "/todo")
	r.turnsEnded(t, 4)
	r.view(func(p *agent.Projection) {
		var msgs []*agent.Message
		for _, m := range p.Messages {
			if m.Role == agent.RoleAssistant {
				msgs = append(msgs, m)
			}
		}
		if len(msgs) != 4 {
			t.Fatalf("%d assistant messages", len(msgs))
		}
		tools := partsOf(msgs[0], agent.PartTool)
		if len(tools) != 1 || tools[0].Text != "one\ntwo" || tools[0].State != agent.PartDone {
			t.Fatalf("bash = %+v", tools)
		}
		if c := content(t, tools[0]); c.Tool != "Bash" || !strings.Contains(c.Command, "printf") || c.Summary == "" {
			t.Fatalf("bash content = %+v", c)
		}
		diffs := partsOf(msgs[1], agent.PartDiff)
		if len(diffs) != 1 {
			t.Fatalf("edit parts = %+v", msgs[1].Parts)
		}
		c := content(t, diffs[0])
		if !strings.Contains(c.Diff, "-beta\n+BETA\n") || c.Added != 1 || c.Removed != 1 || !strings.HasSuffix(c.Path, "fake.txt") {
			t.Fatalf("edit diff = %+v", c)
		}
		if strings.Contains(string(diffs[0].Content), "originalFile") {
			t.Fatal("the whole original file went into the journal")
		}
		w := content(t, partsOf(msgs[2], agent.PartDiff)[0])
		if !strings.Contains(w.Diff, "+hello\n+world\n") || w.Added != 2 {
			t.Fatalf("write diff = %+v", w)
		}
		if n := len(partsOf(msgs[3], agent.PartTool)); n != 0 {
			t.Fatalf("the task tools made %d tool rows", n)
		}
		todos := partsOf(msgs[3], agent.PartTodo)
		if len(todos) != 1 {
			t.Fatalf("todo parts = %+v", msgs[3].Parts)
		}
		var list struct {
			Items []todoItem `json:"items"`
		}
		_ = json.Unmarshal(todos[0].Content, &list)
		if len(list.Items) != 2 || list.Items[0].Status != "completed" || list.Items[1].Status != "pending" || list.Items[0].ActiveForm == "" {
			t.Fatalf("todo = %+v", list.Items)
		}
	})
}

func TestASubagentIsANestedGroup(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/subagent")
	r.turnsEnded(t, 1)
	r.view(func(p *agent.Projection) {
		groups := partsOf(lastAssistant(p), agent.PartTool)
		if len(groups) != 1 {
			t.Fatalf("top-level tools = %+v", groups)
		}
		g := groups[0]
		c := content(t, g)
		if c.Tool != "Agent" || c.Agent == nil || c.Agent.Status != "completed" || !c.Agent.Background || c.Agent.Summary != "sub-ok" {
			t.Fatalf("group = %+v / %+v", c, c.Agent)
		}
		if g.State != agent.PartDone {
			t.Fatalf("group state %q", g.State)
		}
		if len(g.Children) != 2 || g.Children[0].Kind != agent.PartTool || g.Children[0].Text != "sub-ok" ||
			g.Children[1].Kind != agent.PartText || g.Children[1].Text != "sub-ok" {
			t.Fatalf("children = %+v", g.Children)
		}
		if p.Tasks != 0 {
			t.Fatalf("tasks = %d", p.Tasks)
		}
	})
}

func TestAWorkflowShowsPhasesAgentsAndTheirCalls(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/workflow 1.5")
	r.turnsEnded(t, 1)
	if a.Background() != 1 {
		t.Fatalf("background = %d while the workflow runs", a.Background())
	}
	// The workflow's end starts a turn of the CLI's own.
	evs := r.turnsEnded(t, 2)
	var second agent.Event
	for _, ev := range evs {
		if ev.Type == agent.TypeTurnStarted {
			second = ev
		}
	}
	if second.MessageID != "" {
		t.Fatalf("the CLI's own turn names a user message: %+v", second)
	}
	r.waitFor(t, "the last agent's call", func([]agent.Event) bool {
		done := false
		r.view(func(p *agent.Projection) {
			for _, m := range p.Messages {
				for _, part := range partsOf(m, agent.PartTool) {
					if len(part.Children) == 3 && len(part.Children[2].Children) == 1 && part.Children[2].Children[0].State == agent.PartDone {
						done = true
					}
				}
			}
		})
		return done
	})
	r.view(func(p *agent.Projection) {
		wf := partsOf(p.Messages[0], agent.PartTool)
		if len(wf) != 1 {
			t.Fatalf("workflow parts = %+v", p.Messages[0].Parts)
		}
		c := content(t, wf[0])
		if c.Tool != "Workflow" || c.Workflow == nil || c.Workflow.Status != "completed" || len(c.Workflow.Phases) != 2 ||
			c.Workflow.Agents != 3 || c.Workflow.AgentsDone != 3 {
			t.Fatalf("workflow = %+v", c.Workflow)
		}
		if wf[0].State != agent.PartDone {
			t.Fatalf("workflow state %q", wf[0].State)
		}
		for i, ag := range wf[0].Children {
			ac := content(t, ag)
			if ac.Agent == nil || ac.Agent.Status != "completed" || ag.State != agent.PartDone {
				t.Fatalf("agent %d = %+v", i, ac.Agent)
			}
			if len(ag.Children) != 1 || content(t, ag.Children[0]).Tool != "Bash" {
				t.Fatalf("agent %d calls = %+v", i, ag.Children)
			}
		}
		if p.Tasks != 0 {
			t.Fatalf("tasks = %d", p.Tasks)
		}
	})
	a.mu.Lock()
	kept := len(a.st.parts)
	a.mu.Unlock()
	if kept != 0 {
		t.Fatalf("%d finished parts are still kept", kept)
	}
}

func TestTheBackgroundTaskReportsBackWithATurnOfItsOwn(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/bg 0.5")
	r.turnsEnded(t, 1)
	if a.Background() != 1 {
		t.Fatalf("background = %d", a.Background())
	}
	r.turnsEnded(t, 2)
	r.waitFor(t, "no task left", func(evs []agent.Event) bool { return a.Background() == 0 })
	r.view(func(p *agent.Projection) {
		bash := content(t, partsOf(p.Messages[0], agent.PartTool)[0])
		if bash.Task == nil || bash.Task.Status != "completed" {
			t.Fatalf("background shell = %+v", bash.Task)
		}
	})
}

func TestThePlanCardApprovesOrKeepsPlanning(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	if err := a.Command(context.Background(), "plan", ""); err != nil {
		t.Fatal(err)
	}
	send(t, a, "m1", "/plan")
	evs := r.waitFor(t, "the plan ask", func(evs []agent.Event) bool { return count(evs, agent.TypeAsk) == 1 })
	var ask agent.Event
	for _, ev := range evs {
		if ev.Type == agent.TypeAsk {
			ask = ev
		}
	}
	if !strings.Contains(string(ask.Payload), `"kind":"plan"`) || !strings.Contains(string(ask.Payload), "Create hi.txt") {
		t.Fatalf("payload = %s", ask.Payload)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"decision":"keep_planning","note":"add tests"}`)); err != nil {
		t.Fatal(err)
	}
	r.turnsEnded(t, 1)
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"decision":"approve"}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("a second answer = %v", err)
	}
	r.view(func(p *agent.Projection) {
		m := lastAssistant(p)
		plan := partsOf(m, agent.PartPlan)
		if len(plan) != 1 || plan[0].Text == "" {
			t.Fatalf("plan parts = %+v", m.Parts)
		}
		if c := content(t, plan[0]); c.Plan == nil || c.Plan.Decision != "keep_planning" || c.Plan.Note != "add tests" {
			t.Fatalf("plan = %+v", c.Plan)
		}
		if text := partsOf(m, agent.PartText); len(text) != 1 || !strings.Contains(text[0].Text, "add tests") {
			t.Fatalf("text = %+v", text)
		}
	})
	send(t, a, "m2", "/plan")
	evs = r.waitFor(t, "the second plan ask", func(evs []agent.Event) bool { return count(evs, agent.TypeAsk) == 2 })
	ask = evs[len(evs)-1]
	for _, ev := range evs {
		if ev.Type == agent.TypeAsk {
			ask = ev
		}
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"decision":"approve"}`)); err != nil {
		t.Fatal(err)
	}
	r.turnsEnded(t, 2)
	r.view(func(p *agent.Projection) {
		m := lastAssistant(p)
		if c := content(t, partsOf(m, agent.PartPlan)[0]); c.Plan.Decision != "approved" {
			t.Fatalf("plan = %+v", c.Plan)
		}
		if !strings.Contains(partsOf(m, agent.PartText)[0].Text, "approved") {
			t.Fatalf("parts = %+v", m.Parts)
		}
	})
}

func TestInterruptKeepsWhatWasWritten(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/slow 10")
	r.waitFor(t, "streaming", func([]agent.Event) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.deltas) >= 3
	})
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := r.turnsEnded(t, 1)
	end := evs[len(evs)-1]
	for _, ev := range evs {
		if ev.Type == agent.TypeTurnEnded {
			end = ev
		}
	}
	if end.Outcome != agent.OutcomeInterrupted {
		t.Fatalf("outcome = %q", end.Outcome)
	}
	r.view(func(p *agent.Projection) {
		m := lastAssistant(p)
		text := partsOf(m, agent.PartText)
		if m.State != agent.MessageInterrupted || len(text) != 1 || !strings.HasPrefix(text[0].Text, "s1 s2 ") {
			t.Fatalf("after the interrupt: %+v %+v", m, text)
		}
	})
	send(t, a, "m2", "again")
	r.turnsEnded(t, 2)
}

func TestAMessageSentDuringATurnIsQueued(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/slow 0.5")
	r.waitFor(t, "the turn", func(evs []agent.Event) bool { return count(evs, agent.TypeTurnStarted) == 1 })
	send(t, a, "m2", "next")
	evs := r.turnsEnded(t, 2)
	var states []string
	for _, ev := range evs {
		if ev.Type == agent.TypeMessageState && ev.MessageID == "m2" {
			states = append(states, ev.State)
		}
	}
	if strings.Join(states, ",") != "queued,delivered" {
		t.Fatalf("m2 went %v", states)
	}
}

func TestParkAndResumeKeepTheConversation(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "first")
	r.turnsEnded(t, 1)
	var conv string
	r.view(func(p *agent.Projection) { conv = p.ConversationID })
	if err := a.Park(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-r.exited:
		t.Fatalf("a park reported an exit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	b, r2 := start(t, e, conv)
	// The host re-sends a message it had journaled; the transcript says the
	// CLI has it already, so it is not sent twice.
	send(t, b, "m1", "first")
	r2.waitFor(t, "m1 delivered", func(evs []agent.Event) bool {
		for _, ev := range evs {
			if ev.Type == agent.TypeMessageState && ev.MessageID == "m1" && ev.State == agent.MessageDelivered {
				return true
			}
		}
		return false
	})
	send(t, b, "m2", "second")
	evs := r2.turnsEnded(t, 1)
	if count(evs, agent.TypeTurnStarted) != 1 {
		t.Fatalf("the re-sent message ran again:\n%s", dump(evs))
	}
	r2.view(func(p *agent.Projection) {
		if p.ConversationID != conv {
			t.Fatalf("conversation %q after resume, want %q", p.ConversationID, conv)
		}
	})
}

func TestResumingAnUnknownConversationFailsFast(t *testing.T) {
	e := newEnv(t)
	a := New(Options{})
	start := time.Now()
	err := a.Start(context.Background(), e.spec("00000000-0000-4000-8000-000000000000"), newRec())
	if !errors.Is(err, agent.ErrConversationNotFound) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestACrashIsReportedAndTheResumeShowsTheCutOffTurn(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/crash")
	select {
	case err := <-r.exited:
		if err == nil {
			t.Fatal("exit without a reason")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the crash was not reported")
	}
	var conv string
	r.view(func(p *agent.Projection) { conv = p.ConversationID })
	b, r2 := start(t, e, conv)
	send(t, b, "m2", "/tool")
	r2.turnsEnded(t, 1)
	path := findTranscript(e.conf, conv)
	events, err := RebuildTranscript(path, conv)
	if err != nil {
		t.Fatal(err)
	}
	p := agent.Fold(number(events))
	if p.ConversationID != conv {
		t.Fatalf("conversation %q", p.ConversationID)
	}
	var users []string
	for _, m := range p.Messages {
		if m.Role == agent.RoleUser {
			users = append(users, m.Text)
		}
		for _, part := range m.Parts {
			if part.Kind == agent.PartText && strings.Contains(part.Text, noResponse) {
				t.Fatalf("the synthetic answer is rendered as text: %+v", part)
			}
		}
	}
	if strings.Join(users, "|") != "/crash|/tool" {
		t.Fatalf("users = %v", users)
	}
	if p.LastTurn == nil || p.Messages[1].State != agent.MessageInterrupted {
		t.Fatalf("the cut-off turn is not interrupted: %+v", p.Messages[1])
	}
}

func number(events []agent.Event) []agent.Event {
	out := make([]agent.Event, len(events))
	for i, ev := range events {
		ev.Seq = int64(i + 1)
		out[i] = ev
	}
	return out
}

func TestRebuildFromTheTranscript(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "hello")
	send(t, a, "m2", "/edit")
	send(t, a, "m3", "/subagent")
	send(t, a, "m4", "/compact")
	send(t, a, "m5", "/model sonnet")
	r.turnsEnded(t, 5)
	var conv string
	r.view(func(p *agent.Projection) { conv = p.ConversationID })
	events, err := a.Rebuild(agent.StartSpec{Env: e.vars, ResumeID: conv})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now().Add(-time.Minute).UnixMilli()
	for _, ev := range events {
		if ev.At < began || ev.At > time.Now().UnixMilli() {
			t.Fatalf("an event without the transcript's time: %+v", ev)
		}
	}
	p := agent.Fold(number(events))
	var users []string
	var diffs, notices, groups int
	for _, m := range p.Messages {
		if m.Role == agent.RoleUser {
			users = append(users, m.Text)
		}
		diffs += len(partsOf(m, agent.PartDiff))
		notices += len(partsOf(m, agent.PartNotice))
		for _, g := range partsOf(m, agent.PartTool) {
			if content(t, g).Tool == "Agent" && len(g.Children) == 2 {
				groups++
			}
		}
	}
	if strings.Join(users, "|") != "hello|/edit|/subagent|/model sonnet" {
		t.Fatalf("users = %v", users)
	}
	if diffs != 1 || groups != 1 || notices != 1 {
		t.Fatalf("diffs %d, subagent groups %d, notices %d:\n%s", diffs, groups, notices, dump(events))
	}
	if count(events, agent.TypeNotice) != 1 {
		t.Fatalf("no compaction divider:\n%s", dump(events))
	}
	if _, err := a.Rebuild(agent.StartSpec{Env: e.vars, ResumeID: "nope"}); !errors.Is(err, agent.ErrConversationNotFound) {
		t.Fatalf("unknown = %v", err)
	}
}

// A host whose journal lost the conversation starts with Rebuild: the
// history is written first, then the conversation goes on.
func TestStartRebuildsALostJournal(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "hello")
	r.turnsEnded(t, 1)
	var conv string
	r.view(func(p *agent.Projection) { conv = p.ConversationID })
	if err := a.Park(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := New(Options{})
	r2 := newRec()
	spec := e.spec(conv)
	spec.Rebuild = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Start(ctx, spec, r2); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Stop() })
	var users []string
	var rebuilt bool
	r2.view(func(p *agent.Projection) {
		for _, m := range p.Messages {
			if m.Role == agent.RoleUser {
				users = append(users, m.Text)
			}
		}
	})
	r2.mu.Lock()
	for _, ev := range r2.events {
		rebuilt = rebuilt || (ev.Type == agent.TypeNotice && ev.Code == agent.NoticeRebuilt)
	}
	r2.mu.Unlock()
	if strings.Join(users, "|") != "hello" || !rebuilt {
		t.Fatalf("rebuilt users %v, notice %v", users, rebuilt)
	}
	send(t, b, "m2", "again")
	r2.turnsEnded(t, 2)
}

// Resumed on an account of another organization, Claude Code says it goes
// on without the earlier thinking: a hint for the switch, not a warning.
func TestTheThinkingOfAnotherOrganizationIsAHint(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/foreign")
	evs := r.turnsEnded(t, 1)
	var notices []agent.Event
	for _, ev := range evs {
		if ev.Type == agent.TypeNotice {
			notices = append(notices, ev)
		}
	}
	if len(notices) != 1 || notices[0].Code != agent.NoticeForeignThinking || !strings.Contains(notices[0].Text, "switch back") {
		t.Fatalf("notices:\n%s", dump(evs))
	}
}

func TestLimitsRetriesImagesAndCommands(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/limit")
	evs := r.turnsEnded(t, 1)
	var outcome string
	for _, ev := range evs {
		if ev.Type == agent.TypeTurnEnded {
			outcome = ev.Outcome
		}
	}
	if outcome != agent.OutcomeLimited || count(evs, agent.TypeRateLimit) != 1 {
		t.Fatalf("limit:\n%s", dump(evs))
	}
	send(t, a, "m2", "/retry")
	send(t, a, "m3", "/image")
	r.turnsEnded(t, 3)
	if err := a.Command(context.Background(), "compact", ""); err != nil {
		t.Fatal(err)
	}
	evs = r.turnsEnded(t, 4)
	if err := a.Command(context.Background(), "model", "sonnet"); err != nil {
		t.Fatal(err)
	}
	evs = r.turnsEnded(t, 5)
	if err := a.Command(context.Background(), "doctor", ""); !errors.Is(err, agent.ErrUnknownCommand) {
		t.Fatalf("doctor = %v", err)
	}
	codes := map[string]bool{}
	for _, ev := range evs {
		if ev.Type == agent.TypeNotice {
			codes[ev.Code] = true
		}
	}
	if !codes["api_retry"] || !codes["compacted"] {
		t.Fatalf("notices = %v", codes)
	}
	r.view(func(p *agent.Projection) {
		var image *agent.Part
		var commandOut string
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				for _, c := range append([]*agent.Part{part}, part.Children...) {
					if c.Kind == agent.PartImage {
						image = c
					}
					if c.Kind == agent.PartNotice {
						commandOut = c.Text
					}
				}
			}
		}
		if image == nil {
			t.Fatal("no image part")
		}
		var ic struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(image.Content, &ic)
		if _, err := os.Stat(ic.Path); err != nil {
			t.Fatalf("image file: %v", err)
		}
		if commandOut != "Set model to sonnet for this session only" {
			t.Fatalf("command output = %q", commandOut)
		}
	})
	send(t, a, "m6", "after the switch")
	r.turnsEnded(t, 6)
	r.view(func(p *agent.Projection) {
		if p.Model != "claude-sonnet-5-5" {
			t.Fatalf("model = %q", p.Model)
		}
	})
}

func TestSetModelSwitchesModelAndEffort(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	if err := a.SetModel(context.Background(), "sonnet", "high"); err != nil {
		t.Fatal(err)
	}
	r.view(func(p *agent.Projection) {
		if p.Model != "sonnet" || p.Effort != "high" {
			t.Fatalf("model %q effort %q", p.Model, p.Effort)
		}
	})
}

func TestPrepareConfigDirSharesTheConversationFolders(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "agents", "claude")
	acct := filepath.Join(root, "accounts", "a1")
	if err := os.MkdirAll(filepath.Join(acct, "projects", "p"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acct, "projects", "p", "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acct, ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := PrepareConfigDir(acct, shared); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range SharedDirs {
		target, err := os.Readlink(filepath.Join(acct, name))
		if err != nil || filepath.IsAbs(target) {
			t.Fatalf("%s: link %q, %v", name, target, err)
		}
	}
	if _, err := os.Stat(filepath.Join(shared, "projects", "p", "s.jsonl")); err != nil {
		t.Fatal("the account's transcripts were not moved into the shared folder")
	}
	if _, err := os.Stat(filepath.Join(shared, ".credentials.json")); err == nil {
		t.Fatal("credentials are shared")
	}
	other := filepath.Join(root, "accounts", "a2")
	if err := os.MkdirAll(filepath.Join(other, "projects", "q"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := PrepareConfigDir(other, shared); err == nil {
		t.Fatal("two transcript trees were merged silently")
	}
}

// An account switch is interrupt, park, and a start under the other
// account's config directory resuming the same conversation (spike S1).
func TestAConversationMovesToAnotherAccount(t *testing.T) {
	e := newEnv(t)
	root := filepath.Dir(e.conf)
	shared := filepath.Join(root, "agents", "claude")
	accA, accB := filepath.Join(root, "acct-a"), filepath.Join(root, "acct-b")
	for _, dir := range []string{accA, accB} {
		if err := PrepareConfigDir(dir, shared); err != nil {
			t.Fatal(err)
		}
	}
	withConf := func(dir string) env {
		c := e
		c.conf = dir
		c.vars = append([]string(nil), e.vars...)
		for i, kv := range c.vars {
			if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
				c.vars[i] = "CLAUDE_CONFIG_DIR=" + dir
			}
		}
		return c
	}
	a, r := start(t, withConf(accA), "")
	send(t, a, "m1", "/slow 5")
	r.waitFor(t, "the turn", func(evs []agent.Event) bool { return count(evs, agent.TypeTurnStarted) == 1 })
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.turnsEnded(t, 1)
	if err := a.Park(context.Background()); err != nil {
		t.Fatal(err)
	}
	var conv string
	r.view(func(p *agent.Projection) { conv = p.ConversationID })
	b, r2 := start(t, withConf(accB), conv)
	send(t, b, "m2", "/env")
	r2.turnsEnded(t, 1)
	r2.view(func(p *agent.Projection) {
		if p.ConversationID != conv {
			t.Fatalf("conversation %q, want %q", p.ConversationID, conv)
		}
		if text := partsOf(lastAssistant(p), agent.PartText)[0].Text; !strings.Contains(text, "CLAUDE_CONFIG_DIR="+accB) {
			t.Fatalf("not under account B:\n%s", text)
		}
	})
	if findTranscript(accA, conv) == "" || findTranscript(accB, conv) == "" {
		t.Fatal("the transcript is not shared between the accounts")
	}
}

// A background subagent's call still running when the turn ends belongs to
// the group, not the turn: it is not interrupted, and its result comes later.
func TestABackgroundSubagentsCallOutlivesTheTurn(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/subagent bg 0.5")
	r.turnsEnded(t, 1)
	r.view(func(p *agent.Projection) {
		g := partsOf(p.Messages[0], agent.PartTool)[0]
		if g.State != agent.PartRunning || len(g.Children) != 1 || g.Children[0].State != agent.PartRunning {
			t.Fatalf("after the turn: group %q, children %+v", g.State, g.Children)
		}
	})
	r.turnsEnded(t, 2)
	r.view(func(p *agent.Projection) {
		g := partsOf(p.Messages[0], agent.PartTool)[0]
		if g.State != agent.PartDone || g.Children[0].State != agent.PartDone || g.Children[0].Text != "late" {
			t.Fatalf("after the agent: group %q, children %+v", g.State, g.Children[0])
		}
	})
}

func TestAFailedSubagentStaysFailed(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/subagent fail")
	r.turnsEnded(t, 1)
	r.view(func(p *agent.Projection) {
		g := partsOf(lastAssistant(p), agent.PartTool)[0]
		if c := content(t, g); g.State != agent.PartError || c.Agent.Status != "failed" || c.Agent.Summary != "it broke" {
			t.Fatalf("group %q %+v", g.State, c.Agent)
		}
	})
}

// The dashboard's binary setting reaches the adapter through the spec.
func TestTheConfiguredBinaryIsRun(t *testing.T) {
	e := newEnv(t)
	e.vars = []string{"PATH=/nonexistent", "HOME=" + filepath.Dir(e.conf), "CLAUDE_CONFIG_DIR=" + e.conf}
	spec := e.spec("")
	spec.Binary = fakeBin
	a := New(Options{})
	r := newRec()
	if err := a.Start(context.Background(), spec, r); err != nil {
		t.Fatalf("start with the configured binary: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	send(t, a, "m1", "hello")
	r.turnsEnded(t, 1)
	spec.Binary = filepath.Join(t.TempDir(), "missing")
	if err := New(Options{}).Start(context.Background(), spec, newRec()); err == nil {
		t.Fatal("a missing configured binary started")
	}
}
