//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
)

// fakeBin is e2e/fakebin/fakecodex, built once for the package's tests.
var fakeBin string

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		os.Exit(agenthost.Main(os.Args[2:], "test"))
	}
	dir, err := os.MkdirTemp("", "socrates-fakecodex-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeBin = filepath.Join(dir, "codex")
	build := exec.Command("go", "build", "-o", fakeBin, "./e2e/fakebin/fakecodex")
	build.Dir = filepath.Join("..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build fakecodex: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeLab is a data folder whose accounts share their sessions.
type fakeLab struct {
	t      *testing.T
	root   string
	shared string
	work   string
}

func newFakeLab(t *testing.T) *fakeLab {
	root := t.TempDir()
	l := &fakeLab{t: t, root: root, shared: filepath.Join(root, "agents", "codex"), work: filepath.Join(root, "work")}
	if err := os.MkdirAll(l.work, 0o700); err != nil {
		t.Fatal(err)
	}
	return l
}

// spec is a start on the account name; extra adds to its environment.
func (l *fakeLab) spec(account, resume string, extra ...string) agent.StartSpec {
	l.t.Helper()
	home := filepath.Join(l.root, "accounts", account)
	if err := PrepareHome(home, l.shared); err != nil {
		l.t.Fatal(err)
	}
	env := append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + l.root, "CODEX_HOME=" + home,
		BinEnv + "=" + fakeBin, "FAKECODEX_ACCOUNT=" + account}, extra...)
	return agent.StartSpec{ChatID: "c1", Workdir: l.work, StateDir: filepath.Join(l.root, "chat", "agent"),
		ResumeID: resume, Env: env, Model: "gpt-fake", Effort: "low"}
}

func (l *fakeLab) start(spec agent.StartSpec, r *recorder) *adapter {
	l.t.Helper()
	a := newAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Start(ctx, spec, r); err != nil {
		l.t.Fatalf("start: %v", err)
	}
	l.t.Cleanup(func() { _ = a.Stop() })
	return a
}

const wait = 10 * time.Second

func TestStreamingTurnAndUsage(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	if conv := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), wait); conv.ConversationID == "" {
		t.Fatal("no conversation id")
	}
	msg, n := send(t, a, r, "/stream 30 2")
	end := r.turnEnd(t, n, wait)
	p := r.projection()
	want := ""
	for i := 1; i <= 30; i++ {
		want += fmt.Sprintf("w%d ", i)
	}
	if end.Outcome != agent.OutcomeCompleted || lastTurnText(p) != want {
		t.Fatalf("turn %s %q\n%s", end.Outcome, lastTurnText(p), dump(r.since(n)))
	}
	if p.LastTurn.MessageID != msg.MessageID || messageState(p, msg.MessageID) != agent.MessageDelivered {
		t.Fatalf("the turn is not the message's: %+v %s", p.LastTurn, messageState(p, msg.MessageID))
	}
	var live strings.Builder
	r.mu.Lock()
	for _, d := range r.deltas {
		live.WriteString(d.Text)
	}
	r.mu.Unlock()
	if live.String() != want {
		t.Fatalf("live deltas %q", live.String())
	}
	if u := end.Usage; u == nil || u.Input != 60 || u.CacheRead != 40 || u.Output != 60 || u.Reasoning != 10 || u.Window != 200000 {
		t.Fatalf("usage %+v", end.Usage)
	}

	_, n = send(t, a, r, "hello")
	r.turnEnd(t, n, wait)
	p = r.projection()
	if got := lastTurnText(p); got != "you said: hello" {
		t.Fatalf("answer %q", got)
	}
	if rl := p.RateLimits["five_hour"]; rl.Used != 0.12 || rl.ResetsAt == 0 || p.RateLimits["weekly"].Used != 0.42 {
		t.Fatalf("rate limits %+v", p.RateLimits)
	}
	var reasoning bool
	for _, pt := range partsOf(p) {
		reasoning = reasoning || pt.Kind == agent.PartReasoning && pt.Text == "Thinking" && pt.State == agent.PartDone
	}
	if !reasoning {
		t.Fatalf("no reasoning\n%s", dump(r.since(n)))
	}
}

func TestToolsDiffsPlansAndSubagents(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)

	_, n := send(t, a, r, "/run make test")
	r.turnEnd(t, n, wait)
	cmd := findPart(r.projection(), func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "command" })
	if cmd == nil || cmd.Text != "line 1\nline 2\nline 3\n" || cmd.State != agent.PartDone || contentOf(cmd)["exit_code"] != float64(0) {
		t.Fatalf("command %+v\n%s", cmd, dump(r.since(n)))
	}
	_, n = send(t, a, r, "/run fail")
	r.turnEnd(t, n, wait)
	cmd = findPart(r.projection(), func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "command" })
	if cmd.State != agent.PartError || contentOf(cmd)["exit_code"] != float64(1) {
		t.Fatalf("failed command %+v", cmd)
	}

	_, n = send(t, a, r, "/patch notes.md")
	r.turnEnd(t, n, wait)
	p := r.projection()
	change := findPart(p, func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "file_change" })
	diff := findPart(p, func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "turn_diff" })
	if change == nil || change.Kind != agent.PartDiff || diff == nil || diff.State != agent.PartDone ||
		!strings.Contains(contentOf(diff)["diff"].(string), "+hello from fakecodex") {
		t.Fatalf("diffs\n%s", dump(r.since(n)))
	}
	changes := contentOf(change)["changes"].([]any)
	if c := changes[0].(map[string]any); c["kind"] != "add" || c["path"] != filepath.Join(lab.work, "notes.md") {
		t.Fatalf("change %+v", c)
	}

	_, n = send(t, a, r, "/plan")
	r.turnEnd(t, n, wait)
	p = r.projection()
	plan := findPart(p, func(pt *agent.Part) bool { return pt.Kind == agent.PartPlan })
	todo := findPart(p, func(pt *agent.Part) bool { return pt.Kind == agent.PartTodo })
	if plan == nil || plan.Text != "# Plan\n- one\n- two\n" || todo == nil || len(contentOf(todo)["steps"].([]any)) != 2 {
		t.Fatalf("plan\n%s", dump(r.since(n)))
	}

	_, n = send(t, a, r, "/subagent")
	r.turnEnd(t, n, wait)
	group := findPart(r.projection(), func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "subagent" })
	if group == nil || group.State != agent.PartDone || contentOf(group)["name"] != "helper" || contentOf(group)["status"] != "completed" {
		t.Fatalf("group %+v\n%s", group, dump(r.since(n)))
	}
	if len(group.Children) != 1 || group.Children[0].Text != "sub done" || group.Children[0].ParentID != group.ID {
		t.Fatalf("children %+v", group.Children)
	}
	if a.Background() != 0 {
		t.Fatal("a finished subagent still counts")
	}
}

func findPart(p *agent.Projection, match func(*agent.Part) bool) *agent.Part {
	for _, pt := range partsOf(p) {
		if match(pt) {
			return pt
		}
	}
	return nil
}

func TestSteeringQueueingAndShellLines(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)

	// Steered into the running turn: queued, delivered when codex takes it.
	first, n := send(t, a, r, "/sleep 0.3")
	steer, _ := send(t, a, r, "and this")
	if s := messageState(r.projection(), steer.MessageID); s != agent.MessageQueued {
		t.Fatalf("steered message is %q", s)
	}
	end := r.turnEnd(t, n, wait)
	p := r.projection()
	if end.Outcome != agent.OutcomeCompleted || messageState(p, steer.MessageID) != agent.MessageDelivered ||
		p.LastTurn.MessageID != first.MessageID {
		t.Fatalf("steer\n%s", dump(r.since(n)))
	}
	starts := 0
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeTurnStarted {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("%d turns for a steered message", starts)
	}

	// A steered message the turn ended without taking up gets a turn of its
	// own instead of staying queued.
	_, n = send(t, a, r, "/deaf 0.2")
	missed, _ := send(t, a, r, "missed")
	r.waitFor(t, n, "the missed message's turn", func(ev agent.Event) bool {
		return ev.Type == agent.TypeTurnStarted && ev.MessageID == missed.MessageID
	}, wait)
	r.turnEnd(t, len(r.all())-1, wait)
	if p := r.projection(); messageState(p, missed.MessageID) != agent.MessageDelivered || lastTurnText(p) != "you said: missed" {
		t.Fatalf("missed steer\n%s", dump(r.since(n)))
	}

	// Codex may take a steered message up before it answers the steer: the
	// receipt still counts, and the message gets no second turn.
	_, n = send(t, a, r, "/eager 0.3")
	time.Sleep(100 * time.Millisecond)
	eager, _ := send(t, a, r, "taken early")
	r.turnEnd(t, n, wait)
	time.Sleep(200 * time.Millisecond)
	if p := r.projection(); messageState(p, eager.MessageID) != agent.MessageDelivered {
		t.Fatalf("early receipt: %s\n%s", messageState(p, eager.MessageID), dump(r.since(n)))
	}
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeTurnStarted && ev.MessageID == eager.MessageID {
			t.Fatalf("the early-taken message got a turn of its own\n%s", dump(r.since(n)))
		}
	}

	// Behind a compaction, which cannot be steered: held, then a turn of
	// its own.
	n = len(r.all())
	if err := a.Command(context.Background(), "compact", ""); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, n, "the compaction turn", isType(agent.TypeTurnStarted), wait)
	held, _ := send(t, a, r, "after compacting")
	r.waitFor(t, n, "the held message's turn", func(ev agent.Event) bool {
		return ev.Type == agent.TypeTurnStarted && ev.MessageID == held.MessageID
	}, wait)
	r.turnEnd(t, len(r.all())-1, wait)
	p = r.projection()
	if lastTurnText(p) != "you said: after compacting" {
		t.Fatalf("held message\n%s", dump(r.since(n)))
	}
	if findPartIn(p, func(pt *agent.Part) bool { return contentOf(pt)["notice"] == "compacted" && pt.Text != "" }) == nil {
		t.Fatalf("no compaction part\n%s", dump(r.since(n)))
	}

	// "!" is a shell command.
	shell, n := send(t, a, r, "!ls -la")
	r.turnEnd(t, n, wait)
	p = r.projection()
	cmd := findPart(p, func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "command" })
	if cmd == nil || cmd.Text != "shell: ls -la\n" || messageState(p, shell.MessageID) != agent.MessageDelivered ||
		p.LastTurn.MessageID != shell.MessageID {
		t.Fatalf("shell\n%s", dump(r.since(n)))
	}

	// review and plan.
	n = len(r.all())
	if err := a.Command(context.Background(), "review", ""); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != "No issues found." {
		t.Fatalf("review %q", got)
	}
	if err := a.Command(context.Background(), "plan", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.Command(context.Background(), "nope", ""); !errors.Is(err, agent.ErrUnknownCommand) {
		t.Fatalf("unknown command: %v", err)
	}
}

// findPartIn looks through every message, not only the newest.
func findPartIn(p *agent.Projection, match func(*agent.Part) bool) *agent.Part {
	for _, m := range p.Messages {
		for _, pt := range m.Parts {
			if match(pt) {
				return pt
			}
		}
	}
	return nil
}

func TestInterruptLimitAndCrash(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)

	_, n := send(t, a, r, "/stream 200 20")
	r.waitFor(t, n, "streaming", isType(agent.TypePart), wait)
	time.Sleep(60 * time.Millisecond)
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	end := r.turnEnd(t, n, wait)
	p := r.projection()
	text := findPart(p, func(pt *agent.Part) bool { return pt.Kind == agent.PartText })
	if end.Outcome != agent.OutcomeInterrupted || text == nil || text.State != agent.PartInterrupted || text.Text == "" {
		t.Fatalf("interrupt\n%s", dump(r.since(n)))
	}

	_, n = send(t, a, r, "/limit")
	end = r.turnEnd(t, n, wait)
	p = r.projection()
	if end.Outcome != agent.OutcomeLimited || p.RateLimits["five_hour"].Used != 1 || end.Text != "You've hit your usage limit." {
		t.Fatalf("limit %+v\n%s", end, dump(r.since(n)))
	}

	_, n = send(t, a, r, "/crash")
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		exited := r.exited
		r.mu.Unlock()
		if len(exited) == 1 {
			if !strings.Contains(exited[0].Error(), "crashing as asked") {
				t.Fatalf("exit %v", exited[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the crash was not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := a.Send(context.Background(), userMessage(r, "after")); err == nil {
		t.Fatal("a send to a dead app-server succeeded")
	}
}

// A line too long to read ends the app-server, so the host sees an exit
// instead of calls that never come back.
func TestAnUnreadableLineEndsCodex(t *testing.T) {
	defer func(old int) { maxLine = old }(maxLine)
	maxLine = 256 << 10
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	send(t, a, r, "/huge 400000")
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		exited := r.exited
		r.mu.Unlock()
		if len(exited) == 1 {
			if !strings.Contains(exited[0].Error(), "could not be read") {
				t.Fatalf("exit %v", exited[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the unreadable line was not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a.PID() == 0 || !processGone(a.PID()) {
		t.Fatal("codex still runs")
	}
}

func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func TestRequestsAreAnswered(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	_, n := send(t, a, r, "/approve")
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != "decision: accept" {
		t.Fatalf("approval %q", got)
	}
	_, n = send(t, a, r, "/elicit")
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != "action: decline" {
		t.Fatalf("elicitation %q", got)
	}
	r.waitFor(t, n, "the notice", isNotice("elicitation_declined"), time.Second)
	_, n = send(t, a, r, "/tool not_offered")
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != "tool said: The tool not_offered is not available." {
		t.Fatalf("unknown tool %q", got)
	}
}

// The hook WP6 builds ask_user on: a dynamic tool whose handler asks the
// person and waits.
func TestDynamicToolAsks(t *testing.T) {
	RegisterTool(Tool{Name: "test_ask", Spec: json.RawMessage(`{"type":"function","name":"test_ask","description":"x","inputSchema":{"type":"object"}}`),
		Handle: func(ctx context.Context, c *Call) (any, error) {
			answer, err := c.Ask(ctx, json.RawMessage(`{"title":"Deploy?","questions":[]}`))
			if err != nil {
				return nil, err
			}
			return map[string]any{"contentItems": []any{map[string]any{"type": "inputText", "text": string(answer)}}, "success": true}, nil
		}})
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	_, n := send(t, a, r, "/tool test_ask")
	ask := r.waitFor(t, n, "the ask", isType(agent.TypeAsk), wait)
	if ask.TurnID == "" || string(ask.Payload) != `{"title":"Deploy?","questions":[]}` {
		t.Fatalf("ask %+v", ask)
	}
	if err := a.Answer(context.Background(), "nope", json.RawMessage(`1`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("unknown ask: %v", err)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`"yes please"`)); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != `tool said: "yes please"` {
		t.Fatalf("answer %q", got)
	}

	// Interrupted while it waits, the ask is gone.
	_, n = send(t, a, r, "/tool test_ask")
	ask = r.waitFor(t, n, "the second ask", isType(agent.TypeAsk), wait)
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, wait)
	deadline := time.Now().Add(wait)
	for !errors.Is(a.Answer(context.Background(), ask.AskID, json.RawMessage(`1`)), agent.ErrAskGone) {
		if time.Now().After(deadline) {
			t.Fatal("the ask outlived its turn")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestParkResumeRebuildAndBackground(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	thread := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), wait).ConversationID
	_, n := send(t, a, r, "one")
	r.turnEnd(t, n, wait)

	_, n = send(t, a, r, "/bg 0.4")
	r.turnEnd(t, n, wait)
	if a.Background() != 1 {
		t.Fatal("the background terminal does not count")
	}
	time.Sleep(500 * time.Millisecond)
	if a.Background() != 0 {
		t.Fatal("the background terminal still counts")
	}

	_, n = send(t, a, r, "/stream 500 10")
	r.waitFor(t, n, "the stream", isType(agent.TypePart), wait)
	_ = a.Stop()
	if a.PID() == 0 {
		t.Fatal("no pid")
	}

	r2 := newRecorder()
	b := lab.start(lab.spec("a", thread), r2)
	if got := r2.waitFor(t, 0, "conversation", isType(agent.TypeConversation), wait).ConversationID; got != thread {
		t.Fatalf("resumed %s", got)
	}
	_, n = send(t, b, r2, "two")
	r2.turnEnd(t, n, wait)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := b.Park(ctx); err != nil {
		t.Fatal(err)
	}
	r2.mu.Lock()
	exited := len(r2.exited)
	r2.mu.Unlock()
	if exited != 0 {
		t.Fatal("a park was reported as an exit")
	}

	// The journal is gone; the state names the thread.
	r3 := newRecorder()
	lab.start(lab.spec("a", ""), r3)
	r3.waitFor(t, 0, "conversation", isType(agent.TypeConversation), wait)
	var outcomes, users []string
	for _, ev := range r3.all() {
		switch {
		case ev.Type == agent.TypeTurnEnded:
			outcomes = append(outcomes, ev.Outcome)
		case ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser:
			users = append(users, ev.Text)
		}
	}
	if strings.Join(users, "|") != "one|/bg 0.4|/stream 500 10|two" ||
		strings.Join(outcomes, ",") != "completed,completed,interrupted,completed" {
		t.Fatalf("rebuilt %q %q\n%s", users, outcomes, dump(r3.all()))
	}
	r3.waitFor(t, 0, "the rebuilt notice", isNotice("history_rebuilt"), time.Second)
	if lastTurnText(r3.projection()) != "you said: two" {
		t.Fatalf("rebuilt answer %q", lastTurnText(r3.projection()))
	}

	c := newAdapter()
	err := c.Start(context.Background(), lab.spec("a", newThreadID()), newRecorder())
	if !errors.Is(err, agent.ErrConversationNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop() })

	// The host's fresh start after that: the state still names the lost
	// thread, which is not insisted on.
	spec := lab.spec("a", "")
	if err := os.WriteFile(filepath.Join(spec.StateDir, "codex", "thread.json"), []byte(`{"thread_id":"`+newThreadID()+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r4 := newRecorder()
	fresh := lab.start(spec, r4)
	conv := r4.waitFor(t, 0, "conversation", isType(agent.TypeConversation), wait).ConversationID
	_, n = send(t, fresh, r4, "anew")
	r4.turnEnd(t, n, wait)
	if conv == thread || lastTurnText(r4.projection()) != "you said: anew" {
		t.Fatalf("fresh start\n%s", dump(r4.all()))
	}
}

func TestAccountSwitchChain(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	thread := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), wait).ConversationID
	_, n := send(t, a, r, "one")
	r.turnEnd(t, n, wait)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := a.Park(ctx); err != nil {
		t.Fatal(err)
	}
	original, _ := filepath.Glob(filepath.Join(lab.shared, "sessions", "*", "*", "*", "*"+thread+".jsonl"))
	if len(original) != 1 {
		t.Fatalf("rollouts %v", original)
	}
	before, _ := os.ReadFile(original[0])

	// Account b cannot read a's reasoning: stripped copy, same journal turn.
	b := lab.start(lab.spec("b", thread), r)
	msg, n := send(t, b, r, "two")
	end := r.turnEnd(t, n, wait)
	r.waitFor(t, n, "the notice", isNotice(NoticeHistoryStripped), time.Second)
	conv := r.waitFor(t, n, "the new conversation", isType(agent.TypeConversation), time.Second).ConversationID
	p := r.projection()
	if end.Outcome != agent.OutcomeCompleted || lastTurnText(p) != "you said: two" || conv == thread ||
		p.LastTurn.MessageID != msg.MessageID || p.ConversationID != conv {
		t.Fatalf("strip\n%s", dump(r.since(n)))
	}
	// The copy ends before the refused turn: "two" is in it once, from the
	// retry.
	copies, _ := filepath.Glob(filepath.Join(lab.shared, "sessions", "*", "*", "*", "*"+conv+".jsonl"))
	if len(copies) != 1 {
		t.Fatalf("copies %v", copies)
	}
	copied, _ := os.ReadFile(copies[0])
	if n := strings.Count(string(copied), `{"text":"two","type":"input_text"}`); n != 1 {
		t.Fatalf("the message is in the copy %d times:\n%s", n, copied)
	}

	// The original keeps everything it had; codex only appended the refused
	// turn to it.
	after, _ := os.ReadFile(original[0])
	if !strings.HasPrefix(string(after), string(before)) || !strings.Contains(string(after), "encrypted_content") {
		t.Fatal("the original rollout lost something")
	}
	if err := b.Park(ctx); err != nil {
		t.Fatal(err)
	}

	// Account c refuses any history: a handoff thread.
	c := lab.start(lab.spec("c", conv, "FAKECODEX_STRICT=1"), r)
	_, n = send(t, c, r, "three")
	end = r.turnEnd(t, n, wait)
	r.waitFor(t, n, "the handoff", isNotice(NoticeHandoff), time.Second)
	if end.Outcome != agent.OutcomeCompleted || lastTurnText(r.projection()) != "you said: three" {
		t.Fatalf("handoff\n%s", dump(r.since(n)))
	}
	// The next refusal is not recovered again in this process.
	if err := c.Park(ctx); err != nil {
		t.Fatal(err)
	}
	d := lab.start(lab.spec("d", r.projection().ConversationID, "FAKECODEX_STRICT=1"), r)
	_, n = send(t, d, r, "four")
	r.turnEnd(t, n, wait)
	_, n = send(t, d, r, "five")
	end = r.turnEnd(t, n, wait)
	if end.Outcome != agent.OutcomeFailed || !strings.Contains(end.Text, "could not be verified") {
		t.Fatalf("after the chain: %+v\n%s", end, dump(r.since(n)))
	}
}

func TestModelAndEffortGoWithEveryTurn(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	if err := a.SetModel(context.Background(), "gpt-other", "high"); err != nil {
		t.Fatal(err)
	}
	ev := r.waitFor(t, 0, "model.changed", isType(agent.TypeModelChanged), time.Second)
	if ev.Model != "gpt-other" || ev.Effort != "high" {
		t.Fatalf("%+v", ev)
	}
	_, n := send(t, a, r, "hi")
	r.turnEnd(t, n, wait)
	m := r.waitFor(t, n, "the assistant message", func(ev agent.Event) bool {
		return ev.Type == agent.TypeMessage && ev.Role == agent.RoleAssistant
	}, time.Second)
	if m.Model != "gpt-other" {
		t.Fatalf("model %q", m.Model)
	}
}

func TestAttachmentsBecomeInput(t *testing.T) {
	got := input(agent.Outgoing{Text: "look", Attachments: []agent.Attachment{
		{Name: "a.png", Path: "/x/a.png", Mime: "image/png"}, {Name: "b.pdf", Path: "/x/b.pdf", Mime: "application/pdf"}}})
	raw, _ := json.Marshal(got)
	want := `[{"text":"look\n\nAttached file b.pdf: /x/b.pdf","text_elements":[],"type":"text"},{"path":"/x/a.png","type":"localImage"}]`
	if string(raw) != want {
		t.Fatalf("%s", raw)
	}
}

func TestCommandsArePaletted(t *testing.T) {
	names := map[string]bool{}
	for _, c := range agent.Commands(Harness) {
		names[c.Name] = true
	}
	for _, want := range []string{"compact", "review", "plan", "shell"} {
		if !names[want] {
			t.Fatalf("palette lacks %s: %v", want, names)
		}
	}
	if f, ok := agent.Lookup(Harness); !ok || f() == nil {
		t.Fatal("codex is not registered")
	}
}
