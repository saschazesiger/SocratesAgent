//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/codex/fakemodel"
)

// RealEnv runs the tests against the installed codex binary, with the
// scripted model of package fakemodel standing in for OpenAI. They are
// skipped otherwise: they need codex on the PATH and take a while.
const RealEnv = "SOCRATES_REAL_CODEX"

// realLab is a fake model and a data folder of CODEX_HOMEs sharing their
// sessions, the way accounts share them.
type realLab struct {
	t      *testing.T
	model  *httptest.Server
	root   string
	shared string
	work   string
}

func newRealLab(t *testing.T) *realLab {
	t.Helper()
	if os.Getenv(RealEnv) != "1" {
		t.Skip("set " + RealEnv + "=1 to run against the installed codex")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex is not installed")
	}
	root := t.TempDir()
	logf, err := os.Create(filepath.Join(root, "model.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logf.Close() })
	lab := &realLab{t: t, model: httptest.NewServer(fakemodel.New(logf)), root: root,
		shared: filepath.Join(root, "agents", "codex"), work: filepath.Join(root, "work")}
	t.Cleanup(lab.model.Close)
	if err := os.MkdirAll(lab.work, 0o700); err != nil {
		t.Fatal(err)
	}
	return lab
}

// home is an account's CODEX_HOME whose model calls go to /acct/<path>.
func (l *realLab) home(name, path, model string) string {
	l.t.Helper()
	home := filepath.Join(l.root, "accounts", name)
	if err := PrepareHome(home, l.shared); err != nil {
		l.t.Fatal(err)
	}
	base := l.model.URL + "/v1"
	if path != "" {
		base = l.model.URL + "/acct/" + path + "/v1"
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(fakemodel.Config(base, model)), 0o600); err != nil {
		l.t.Fatal(err)
	}
	return home
}

// lastRequestCount is how often text appears as a user message in the input
// of the last model request that mentions it.
func (l *realLab) lastRequestCount(t *testing.T, text string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(l.root, "model.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	count := -1
	for _, line := range strings.Split(string(raw), "\n") {
		var req struct {
			Body struct {
				Input []struct {
					Role    string `json:"role"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"input"`
			} `json:"body"`
		}
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		n := 0
		for _, in := range req.Body.Input {
			for _, c := range in.Content {
				if in.Role == "user" && c.Text == text {
					n++
				}
			}
		}
		if n > 0 {
			count = n
		}
	}
	return count
}

func (l *realLab) spec(home, stateDir, resume string) agent.StartSpec {
	return agent.StartSpec{ChatID: "c1", Workdir: l.work, StateDir: stateDir, ResumeID: resume,
		Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + l.root, "CODEX_HOME=" + home}}
}

func (l *realLab) start(spec agent.StartSpec, r *recorder) *adapter {
	l.t.Helper()
	a := newAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := a.Start(ctx, spec, r); err != nil {
		l.t.Fatalf("start: %v", err)
	}
	l.t.Cleanup(func() { _ = a.Stop() })
	return a
}

func park(t *testing.T, a *adapter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Park(ctx); err != nil {
		t.Fatalf("park: %v", err)
	}
}

func TestRealTurnsToolsAndSteering(t *testing.T) {
	lab := newRealLab(t)
	home := lab.home("a", "a", "gpt-5.5")
	r := newRecorder()
	a := lab.start(lab.spec(home, filepath.Join(lab.root, "state"), ""), r)
	conv := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), time.Second)
	if conv.ConversationID == "" {
		t.Fatal("no conversation id")
	}

	msg, n := send(t, a, r, "SAY hello there")
	end := r.turnEnd(t, n, 30*time.Second)
	p := r.projection()
	if end.Outcome != agent.OutcomeCompleted || lastTurnText(p) != "hello there" {
		t.Fatalf("turn: %s %q\n%s", end.Outcome, lastTurnText(p), dump(r.since(n)))
	}
	if messageState(p, msg.MessageID) != agent.MessageDelivered {
		t.Fatalf("message state %q", messageState(p, msg.MessageID))
	}
	if end.Usage == nil || end.Usage.Output == 0 || end.Usage.CacheRead == 0 || end.Usage.Window == 0 {
		t.Fatalf("usage %+v", end.Usage)
	}
	if len(p.RateLimits) == 0 {
		t.Fatal("no rate limits")
	}
	var reasoning bool
	for _, pt := range partsOf(p) {
		reasoning = reasoning || pt.Kind == agent.PartReasoning && strings.Contains(pt.Text, "Thinking")
	}
	if !reasoning {
		t.Fatalf("no reasoning part\n%s", dump(r.since(n)))
	}

	_, n = send(t, a, r, "RUN: echo out; exit 3")
	r.turnEnd(t, n, 30*time.Second)
	var cmd *agent.Part
	for _, pt := range partsOf(r.projection()) {
		if pt.Kind == agent.PartTool && contentOf(pt)["tool"] == "command" {
			cmd = pt
		}
	}
	if cmd == nil || cmd.Text != "out\n" || contentOf(cmd)["exit_code"] != float64(3) || cmd.State != agent.PartError {
		t.Fatalf("command part %+v\n%s", cmd, dump(r.since(n)))
	}

	_, n = send(t, a, r, "PATCH: made.txt")
	r.turnEnd(t, n, 30*time.Second)
	var change, turnDiff bool
	for _, pt := range partsOf(r.projection()) {
		c := contentOf(pt)
		change = change || pt.Kind == agent.PartDiff && c["tool"] == "file_change"
		turnDiff = turnDiff || pt.Kind == agent.PartDiff && c["tool"] == "turn_diff" && strings.Contains(c["diff"].(string), "+hello from the fake model")
	}
	if !change || !turnDiff {
		t.Fatalf("diff parts\n%s", dump(r.since(n)))
	}
	if _, err := os.Stat(filepath.Join(lab.work, "made.txt")); err != nil {
		t.Fatal(err)
	}

	// A message sent during a turn is steered into it: queued, then
	// delivered once codex takes it up after the command.
	_, n = send(t, a, r, "RUN: sleep 2; echo slept")
	r.waitFor(t, n, "the command", func(ev agent.Event) bool { return ev.Type == agent.TypePart && ev.Kind == agent.PartTool }, 30*time.Second)
	steer, _ := send(t, a, r, "SAY steered in")
	r.waitFor(t, n, "queued", func(ev agent.Event) bool {
		return ev.Type == agent.TypeMessageState && ev.MessageID == steer.MessageID && ev.State == agent.MessageQueued
	}, 5*time.Second)
	end = r.turnEnd(t, n, 30*time.Second)
	p = r.projection()
	if messageState(p, steer.MessageID) != agent.MessageDelivered || !strings.Contains(lastTurnText(p), "steered in") {
		t.Fatalf("steer: %s %q\n%s", messageState(p, steer.MessageID), lastTurnText(p), dump(r.since(n)))
	}

	// "!" runs a shell command as a turn of its own.
	shell, n := send(t, a, r, "!echo from-shell")
	r.turnEnd(t, n, 30*time.Second)
	p = r.projection()
	var shellOut string
	for _, pt := range partsOf(p) {
		if contentOf(pt)["tool"] == "command" {
			shellOut = pt.Text
		}
	}
	if shellOut != "from-shell\n" || messageState(p, shell.MessageID) != agent.MessageDelivered {
		t.Fatalf("shell: %q\n%s", shellOut, dump(r.since(n)))
	}

	// Interrupt ends a streaming turn as interrupted.
	_, n = send(t, a, r, "SLOW 100 100")
	r.waitFor(t, n, "streaming", isType(agent.TypePart), 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if end := r.turnEnd(t, n, 10*time.Second); end.Outcome != agent.OutcomeInterrupted {
		t.Fatalf("interrupt: %s", end.Outcome)
	}

	// Compacting is a turn of its own, and a message sent meanwhile waits.
	n = len(r.all())
	if err := a.Command(context.Background(), "compact", ""); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, 30*time.Second)
	var compacted bool
	for _, pt := range partsOf(r.projection()) {
		compacted = compacted || contentOf(pt)["notice"] == "compacted"
	}
	if !compacted {
		t.Fatalf("no compaction\n%s", dump(r.since(n)))
	}
}

func TestRealParkResumeAndRebuild(t *testing.T) {
	lab := newRealLab(t)
	home := lab.home("a", "", "gpt-5.5")
	state := filepath.Join(lab.root, "state")
	r := newRecorder()
	a := lab.start(lab.spec(home, state, ""), r)
	thread := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), time.Second).ConversationID
	_, n := send(t, a, r, "SAY first answer")
	r.turnEnd(t, n, 30*time.Second)
	_, n = send(t, a, r, "SLOW 100 100")
	r.waitFor(t, n, "streaming", isType(agent.TypePart), 10*time.Second)
	// Killed mid-turn, as a dying host would take it along.
	_ = a.Stop()

	r2 := newRecorder()
	b := lab.start(lab.spec(home, state, thread), r2)
	if got := r2.waitFor(t, 0, "conversation", isType(agent.TypeConversation), time.Second).ConversationID; got != thread {
		t.Fatalf("resumed %s, want %s", got, thread)
	}
	_, n = send(t, b, r2, "SAY after resume")
	if end := r2.turnEnd(t, n, 30*time.Second); end.Outcome != agent.OutcomeCompleted || lastTurnText(r2.projection()) != "after resume" {
		t.Fatalf("after resume\n%s", dump(r2.all()))
	}
	park(t, b)

	// The journal is gone; the adapter's state still names the thread, and
	// the history comes back from codex.
	r3 := newRecorder()
	lab.start(lab.spec(home, state, ""), r3)
	r3.waitFor(t, 0, "conversation", isType(agent.TypeConversation), 10*time.Second)
	p := r3.projection()
	var turns, interrupted int
	var texts []string
	for _, m := range p.Messages {
		if m.Role == agent.RoleUser {
			texts = append(texts, m.Text)
		}
	}
	for _, ev := range r3.all() {
		if ev.Type == agent.TypeTurnEnded {
			turns++
			if ev.Outcome == agent.OutcomeInterrupted {
				interrupted++
			}
		}
	}
	if turns != 3 || interrupted != 1 || strings.Join(texts, "|") != "SAY first answer|SLOW 100 100|SAY after resume" {
		t.Fatalf("rebuilt %d turns (%d interrupted), users %q\n%s", turns, interrupted, texts, dump(r3.all()))
	}

	// An id codex does not know is ErrConversationNotFound.
	c := newAdapter()
	err := c.Start(context.Background(), lab.spec(home, filepath.Join(lab.root, "state2"), newThreadID()), newRecorder())
	if !errors.Is(err, agent.ErrConversationNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
}

func TestRealAccountSwitchFallbackChain(t *testing.T) {
	lab := newRealLab(t)
	state := filepath.Join(lab.root, "state")
	r := newRecorder()
	a := lab.start(lab.spec(lab.home("a", "a", "gpt-5.5"), state, ""), r)
	thread := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), time.Second).ConversationID
	_, n := send(t, a, r, "SAY one")
	r.turnEnd(t, n, 30*time.Second)
	park(t, a)

	// Account b cannot read a's reasoning: the turn goes on, on a stripped
	// copy, in the same journal turn.
	b := lab.start(lab.spec(lab.home("b", "b", "gpt-5.5"), state, thread), r)
	_, n = send(t, b, r, "SAY two")
	end := r.turnEnd(t, n, 60*time.Second)
	got := r.since(n)
	r.waitFor(t, n, "the notice", isNotice(NoticeHistoryStripped), time.Second)
	newConv := r.waitFor(t, n, "the new conversation", isType(agent.TypeConversation), time.Second).ConversationID
	if end.Outcome != agent.OutcomeCompleted || lastTurnText(r.projection()) != "two" || newConv == thread {
		t.Fatalf("strip: %s %q\n%s", end.Outcome, lastTurnText(r.projection()), dump(got))
	}
	starts := 0
	for _, ev := range got {
		if ev.Type == agent.TypeTurnStarted {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("%d journal turns for one message\n%s", starts, dump(got))
	}
	original, err := filepath.Glob(filepath.Join(lab.shared, "sessions", "*", "*", "*", "*"+thread+".jsonl"))
	if err != nil || len(original) != 1 {
		t.Fatalf("the original rollout: %v %v", original, err)
	}
	// The retried call carries the message once: the copy ends before the
	// refused turn.
	if got := lab.lastRequestCount(t, "SAY two"); got != 1 {
		t.Fatalf("the retried request carries the message %d times", got)
	}
	park(t, b)

	// Account c refuses any replayed history: a new thread with a handoff.
	c := lab.start(lab.spec(lab.home("c", "c/strict", "gpt-5.5"), state, newConv), r)
	_, n = send(t, c, r, "SAY three")
	end = r.turnEnd(t, n, 60*time.Second)
	r.waitFor(t, n, "the handoff notice", isNotice(NoticeHandoff), time.Second)
	if end.Outcome != agent.OutcomeCompleted || lastTurnText(r.projection()) != "three" {
		t.Fatalf("handoff: %s\n%s", end.Outcome, dump(r.since(n)))
	}
}

func TestRealSubagentsNest(t *testing.T) {
	lab := newRealLab(t)
	r := newRecorder()
	a := lab.start(lab.spec(lab.home("a", "", "gpt-6-astra"), filepath.Join(lab.root, "state"), ""), r)
	_, n := send(t, a, r, "SPAWN: helper")
	r.turnEnd(t, n, 30*time.Second)
	r.waitFor(t, n, "the helper's end", func(ev agent.Event) bool {
		return ev.Type == agent.TypeTasks && ev.Count == 0
	}, 30*time.Second)
	var group *agent.Part
	for _, pt := range partsOf(r.projection()) {
		if contentOf(pt)["tool"] == "subagent" {
			group = pt
		}
	}
	if group == nil || contentOf(group)["name"] != "helper" || group.State != agent.PartDone {
		t.Fatalf("group %+v\n%s", group, dump(r.since(n)))
	}
	var text bool
	for _, c := range group.Children {
		text = text || c.Kind == agent.PartText && c.Text != "" && c.State == agent.PartDone
	}
	if !text {
		t.Fatalf("children of the group\n%s", dump(r.since(n)))
	}
	if a.Background() != 0 {
		t.Fatalf("background %d", a.Background())
	}
}

// A message steered while the final answer streams: codex cuts the answer
// short and takes the message up in the same turn (0.164).
func TestRealSteerDuringTheFinalAnswer(t *testing.T) {
	lab := newRealLab(t)
	r := newRecorder()
	a := lab.start(lab.spec(lab.home("a", "", "gpt-5.5"), filepath.Join(lab.root, "state"), ""), r)
	_, n := send(t, a, r, "SLOW 10 100")
	r.waitFor(t, n, "streaming", isType(agent.TypePart), 10*time.Second)
	steer, _ := send(t, a, r, "SAY late steer")
	r.turnEnd(t, n, 30*time.Second)
	p := r.projection()
	if messageState(p, steer.MessageID) != agent.MessageDelivered || !strings.Contains(lastTurnText(p), "late steer") {
		t.Fatalf("late steer: %s\n%s", messageState(p, steer.MessageID), dump(r.since(n)))
	}
}
