//go:build linux

package opencode

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

func TestATurnStreamsAndEnds(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	sid := r.conversation()
	if !validID(sid, "ses") {
		t.Fatalf("conversation %q", sid)
	}
	id := r.send("/stream 5 5")
	if got := r.waitTurns(1); got[0] != agent.OutcomeCompleted {
		t.Fatalf("outcome %v", got)
	}
	if r.lastText() != "w1 w2 w3 w4 w5 " {
		t.Fatalf("text %q", r.lastText())
	}
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		if m := p.Message(id); m == nil || m.State != agent.MessageDelivered {
			t.Fatalf("user message %+v", m)
		}
		if p.LastTurn.MessageID != id || p.LastTurn.Usage == nil || p.LastTurn.Usage.Input != 1001 || p.LastTurn.CostUSD == 0 {
			t.Fatalf("turn %+v usage %+v", p.LastTurn, p.LastTurn.Usage)
		}
		if p.LastTurn.Usage.Window != 100000 {
			t.Fatalf("window %d", p.LastTurn.Usage.Window)
		}
		var names []string
		for _, c := range p.Palette() {
			names = append(names, c.Name+" "+c.Args)
		}
		want := "compact |shell <command>|agent build|plan|init |review <arguments>|hello <arg1> <arg2>"
		if strings.Join(names, "|") != want {
			t.Fatalf("commands %q", names)
		}
	})
	if r.rec.deltas < 5 {
		t.Fatalf("%d deltas", r.rec.deltas)
	}
	if n := r.rec.count(agent.TypeUsage, nil); n == 0 {
		t.Fatal("no usage per step")
	}
}

func TestToolsStepsDiffsTodosAndReasoning(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/tool")
	r.send("/edit")
	r.send("/todo")
	r.send("/reason")
	for _, o := range r.waitTurns(4) {
		if o != agent.OutcomeCompleted {
			t.Fatalf("outcomes %v", r.rec.turns())
		}
	}
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		kinds := map[string]*agent.Part{}
		assistants := 0
		for _, m := range p.Messages {
			if m.Role == agent.RoleAssistant {
				assistants++
			}
			for _, part := range m.Parts {
				if part.Kind == agent.PartTool {
					if c := decode[ToolContent](t, part.Content); c.Tool != "bash" && c.Tool != "edit" {
						continue
					}
				}
				if kinds[part.Kind] == nil || part.Kind == agent.PartTool {
					kinds[part.Kind] = part
				}
			}
		}
		if assistants != 5 {
			t.Fatalf("%d assistant messages, want 5 (the tool turn has two steps)", assistants)
		}
		var bash *agent.Part
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Kind == agent.PartTool && decode[ToolContent](t, part.Content).Tool == "bash" {
					bash = part
				}
			}
		}
		if bash == nil || bash.State != agent.PartDone || bash.Text != "line 1\nline 2\nline 3\n" {
			t.Fatalf("bash part %+v", bash)
		}
		if c := decode[ToolContent](t, bash.Content); c.Exit == nil || *c.Exit != 0 || c.DurationMS <= 0 || !contains(string(c.Input), "echo hello") {
			t.Fatalf("bash content %s", bash.Content)
		}
		diff := kinds[agent.PartDiff]
		if diff == nil {
			t.Fatal("no diff part")
		}
		dc := decode[DiffContent](t, diff.Content)
		if len(dc.Files) != 1 || dc.Files[0].Path != "a.txt" || !contains(dc.Files[0].Patch, "+new") || dc.Files[0].Additions != 1 {
			t.Fatalf("diff %s", diff.Content)
		}
		todo := kinds[agent.PartTodo]
		if todo == nil || todo.State != agent.PartDone {
			t.Fatalf("todo %+v", todo)
		}
		if tc := decode[TodoContent](t, todo.Content); len(tc.Todos) != 3 || tc.Todos[1].Status != "in_progress" {
			t.Fatalf("todos %s", todo.Content)
		}
		if r := kinds[agent.PartReasoning]; r == nil || r.Text != "thinking it over" || r.State != agent.PartDone {
			t.Fatalf("reasoning %+v", r)
		}
	})
	// The bash output streamed: at least one delta went to the tool part.
	if n := r.rec.count(agent.TypePartDelta, func(ev agent.Event) bool { return ev.Text == "line 2\n" }); n == 0 {
		t.Fatal("the tool output did not stream")
	}
}

func TestASubagentIsNestedInItsTaskPart(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/subagent")
	r.waitTurns(1)
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		var task *agent.Part
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Kind == agent.PartTool && decode[ToolContent](t, part.Content).Tool == "task" {
					task = part
				}
			}
		}
		if task == nil {
			t.Fatal("no task part")
		}
		c := decode[ToolContent](t, task.Content)
		if c.SessionID == "" || c.Agent != "explore" || task.State != agent.PartDone {
			t.Fatalf("task %+v %s", task, task.Content)
		}
		if len(task.Children) != 2 || task.Children[0].Text != "child says hi" || task.Children[1].Kind != agent.PartTool {
			t.Fatalf("children %+v", task.Children)
		}
	})
	// The subagent's model call is the turn's too: 0.001 each, 50 + 5 tokens
	// for the subagent's.
	wantCost := func(rec *recorder, what string) {
		rec.view(func(p *agent.Projection, _ []agent.Event) {
			if lt := p.LastTurn; lt == nil || math.Abs(lt.CostUSD-0.002) > 1e-9 || lt.Usage == nil || lt.Usage.Input != 1001+50 {
				t.Fatalf("%s turn %+v usage %+v", what, lt, lt.Usage)
			}
		})
	}
	wantCost(r.rec, "the live")
	sid := r.conversation()
	_ = r.a.Park(context.Background())
	spec := r.spec(sid)
	spec.Rebuild = true
	spec.StateDir = filepath.Join(r.data, "chats", "c2", "agent")
	if err := r.start(spec); err != nil {
		t.Fatal(err)
	}
	wantCost(r.rec, "the rebuilt")
}

func TestMessagesSentDuringATurnWaitAndRunInOrder(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	first := r.send("/sleep 0.5")
	second := r.send("second")
	third := r.send("third")
	eventually(t, 5*time.Second, "queued", func() bool {
		return r.rec.count(agent.TypeMessageState, func(ev agent.Event) bool { return ev.State == agent.MessageQueued }) == 2
	})
	r.waitTurns(3)
	var order []string
	r.rec.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.Type == agent.TypeTurnStarted {
				order = append(order, ev.MessageID)
			}
		}
	})
	if strings.Join(order, " ") != strings.Join([]string{first, second, third}, " ") {
		t.Fatalf("turn order %v", order)
	}
	if !contains(r.lastText(), "you said: third (turn 3 of") {
		t.Fatalf("text %q", r.lastText())
	}
}

// A turn that is over before OpenCode answers its post does not hold up the
// messages queued behind it.
func TestATurnThatEndsBeforeItsPostIsAnsweredLetsTheQueueRun(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/sleep 0.3")
	r.send("/beforeack")
	last := r.send("after")
	r.waitTurns(3)
	if order := r.rec.turnOrder(); order[len(order)-2] != "start "+last {
		t.Fatalf("turns %q", order)
	}
}

func TestInterruptEndsTheTurnAndTheQueueRuns(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/sleep 30")
	r.send("after")
	eventually(t, 5*time.Second, "the turn", func() bool { return r.rec.count(agent.TypeTurnStarted, nil) == 1 })
	time.Sleep(100 * time.Millisecond)
	if err := r.a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.waitTurns(2)
	if got[0] != agent.OutcomeInterrupted || got[1] != agent.OutcomeCompleted {
		t.Fatalf("outcomes %v", got)
	}
}

// An answer an interrupt cut short is interrupted, not done.
func TestAnInterruptedAnswerIsNotDone(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/stream 200 20")
	eventually(t, 5*time.Second, "the answer to stream", func() bool { return r.rec.count(agent.TypePartDelta, nil) >= 3 })
	if err := r.a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.waitTurns(1); got[0] != agent.OutcomeInterrupted {
		t.Fatalf("outcome %v", got)
	}
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Kind == agent.PartText && part.State != agent.PartInterrupted {
					t.Fatalf("the cut-short answer is %s: %q", part.State, part.Text)
				}
			}
		}
	})
}

func TestALimitRetryStopsTheTurnAsLimited(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/limit")
	start := time.Now()
	if got := r.waitTurns(1); got[0] != agent.OutcomeLimited {
		t.Fatalf("outcome %v", got)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the limit took %s to stop the turn", time.Since(start))
	}
	if _, ok := r.rec.first(agent.TypeRateLimit, func(ev agent.Event) bool { return ev.Window == "free_tier_limit" && ev.Used == 1 }); !ok {
		t.Fatal("no rate_limit")
	}
	if n := r.rec.count(agent.TypeError, func(ev agent.Event) bool { return ev.Code == "rate_limit" }); n != 1 {
		t.Fatalf("%d rate_limit errors", n)
	}
	r.send("hello")
	if got := r.waitTurns(2); got[1] != agent.OutcomeCompleted {
		t.Fatalf("after the limit: %v", got)
	}
}

func TestErrorsPermissionsAndQuestions(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/error")
	r.send("/perm")
	r.send("/question")
	r.send("/retry")
	got := r.waitTurns(4)
	if got[0] != agent.OutcomeFailed || got[1] != agent.OutcomeCompleted {
		t.Fatalf("outcomes %v", got)
	}
	if _, ok := r.rec.first(agent.TypeError, func(ev agent.Event) bool { return ev.Code == "auth" && contains(ev.Text, "User not found") }); !ok {
		t.Fatal("no auth error")
	}
	// The error OpenCode repeats after the failed turn is idle is not news.
	if n := r.rec.count(agent.TypeError, nil); n != 1 {
		t.Fatalf("%d errors", n)
	}
	if _, ok := r.rec.first(agent.TypeNotice, func(ev agent.Event) bool { return ev.Code == NoticePermission }); !ok {
		t.Fatal("no permission notice")
	}
	if _, ok := r.rec.first(agent.TypeNotice, func(ev agent.Event) bool { return ev.Code == NoticeQuestion }); !ok {
		t.Fatal("no question notice")
	}
	if _, ok := r.rec.first(agent.TypeNotice, func(ev agent.Event) bool { return ev.Code == NoticeRetry }); !ok {
		t.Fatal("no retry notice")
	}
	var texts []string
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Kind == agent.PartText {
					texts = append(texts, part.Text)
				}
			}
		}
	})
	joined := strings.Join(texts, "|")
	if !contains(joined, "perm: always") || !contains(joined, "question: rejected") || !contains(joined, "after a retry") {
		t.Fatalf("texts %q", joined)
	}
}

func TestShellLinesCommandsAndCompaction(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("!ls -la")
	r.waitTurns(1)
	ctx := context.Background()
	if err := r.a.Command(ctx, "hello", "world"); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Command(ctx, "compact", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Command(ctx, "nosuch", ""); !errors.Is(err, agent.ErrUnknownCommand) {
		t.Fatalf("unknown command: %v", err)
	}
	got := r.waitTurns(3)
	for _, o := range got {
		if o != agent.OutcomeCompleted {
			t.Fatalf("outcomes %v", got)
		}
	}
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		var users []string
		var shell *agent.Part
		for _, m := range p.Messages {
			if m.Role == agent.RoleUser {
				users = append(users, m.Text)
			}
			for _, part := range m.Parts {
				if part.Kind == agent.PartTool {
					shell = part
				}
			}
		}
		if strings.Join(users, "|") != "!ls -la|/hello world|/compact" {
			t.Fatalf("user messages %q", users)
		}
		if shell == nil || shell.Text != "fake shell: ls -la\n" {
			t.Fatalf("shell %+v", shell)
		}
	})
	if _, ok := r.rec.first(agent.TypeNotice, func(ev agent.Event) bool { return ev.Code == NoticeCompacted }); !ok {
		t.Fatal("no compacted notice")
	}
}

// A shell line (like a slash command or a compaction) runs for as long as it
// takes, past the wait every other call has, and what was sent meanwhile
// waits for it to end.
func TestALongShellLineIsNotCutOff(t *testing.T) {
	r := newRig(t)
	r.wait = 300 * time.Millisecond
	r.mustStart("")
	first := r.send("!sleep 1")
	second := r.send("after")
	r.waitTurns(2)
	want := []string{"start " + first, "end completed", "start " + second, "end completed"}
	if got := r.rec.turnOrder(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("turns %q, want %q", got, want)
	}
	if !contains(r.lastText(), "you said: after") {
		t.Fatalf("text %q", r.lastText())
	}
}

func TestRevertForkAndPlanMode(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	first := r.send("one")
	r.send("two")
	r.waitTurns(2)
	ctx := context.Background()
	if err := r.a.Command(ctx, "revert", first); err != nil {
		t.Fatal(err)
	}
	if ev, ok := r.rec.first(agent.TypeNotice, func(ev agent.Event) bool { return ev.Code == NoticeReverted }); !ok || ev.MessageID != first {
		t.Fatalf("reverted notice %+v", ev)
	}
	if err := r.a.Command(ctx, "unrevert", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Command(ctx, "fork", first); err != nil {
		t.Fatal(err)
	}
	ev, ok := r.rec.first(agent.TypeNotice, func(ev agent.Event) bool { return ev.Code == NoticeForked })
	if !ok || !validID(ev.ConversationID, "ses") || ev.ConversationID == r.conversation() {
		t.Fatalf("forked %+v", ev)
	}
	if err := r.a.Command(ctx, "agent", "plan"); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Command(ctx, "agent", "nonsense"); err == nil {
		t.Fatal("a nonsense agent was taken")
	}
	if err := r.a.Command(ctx, "revert", "m_unknown"); err == nil {
		t.Fatal("a revert to a message OpenCode never had was sent")
	}
	// Plan mode is the chat's: a new host keeps it.
	_ = r.a.Park(ctx)
	r.mustStart(ev.ConversationID)
	if r.a.agentID != AgentPlan {
		t.Fatalf("agent after a restart: %q", r.a.agentID)
	}
}

func TestParkResumeRebuildAndRedelivery(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	sid := r.conversation()
	r.send("/tool")
	last := r.send("hello")
	r.waitTurns(2)
	pid := r.a.PID()
	before := r.rec
	if err := r.a.Park(context.Background()); err != nil {
		t.Fatal(err)
	}
	if alive(pid) {
		t.Fatalf("the server %d outlived the park", pid)
	}

	// A new host resumes the session: the model's context continues.
	r.mustStart(sid)
	if r.conversation() != sid {
		t.Fatalf("resumed %q, want %q", r.conversation(), sid)
	}
	// The previous host's last message, handed over again, is recognised.
	if err := r.a.Send(context.Background(), agent.Outgoing{MessageID: last, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "delivered again", func() bool {
		return r.rec.count(agent.TypeMessageState, func(ev agent.Event) bool { return ev.MessageID == last && ev.State == agent.MessageDelivered }) == 1
	})
	r.send("again")
	r.waitTurns(1)
	if !contains(r.lastText(), "(turn 3 of "+sid+")") {
		t.Fatalf("resumed text %q", r.lastText())
	}
	if n := r.rec.count(agent.TypeTurnStarted, nil); n != 1 {
		t.Fatalf("%d turns after the resume; the redelivered message ran again", n)
	}
	_ = r.a.Park(context.Background())

	// A chat whose journal is gone gets its history back.
	spec := r.spec(sid)
	spec.Rebuild = true
	spec.StateDir = filepath.Join(r.data, "chats", "c2", "agent")
	if err := r.start(spec); err != nil {
		t.Fatal(err)
	}
	var users, texts []string
	var outcomes []string
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			if m.Role == agent.RoleUser {
				users = append(users, m.Text)
			}
			for _, part := range m.Parts {
				if part.Kind == agent.PartText {
					texts = append(texts, part.Text)
				}
			}
		}
		if p.Turn != nil {
			t.Fatalf("a rebuilt chat has a running turn %+v", p.Turn)
		}
	})
	outcomes = r.rec.turns()
	if strings.Join(users, "|") != "/tool|hello|again" || len(outcomes) != 3 {
		t.Fatalf("rebuilt users %q outcomes %v", users, outcomes)
	}
	// The conversation is written after its history: until then, a new host
	// would rebuild again.
	r.rec.view(func(_ *agent.Projection, events []agent.Event) {
		conv, rebuilt := -1, -1
		for i, ev := range events {
			switch {
			case ev.Type == agent.TypeConversation:
				conv = i
			case ev.Type == agent.TypeNotice && ev.Code == agent.NoticeRebuilt:
				rebuilt = i
			}
		}
		if rebuilt < 0 || conv < rebuilt {
			t.Fatalf("the conversation is event %d, the rebuilt notice %d", conv, rebuilt)
		}
	})
	if len(texts) != 3 || texts[0] != "The tool finished." {
		t.Fatalf("rebuilt texts %q", texts)
	}
	// The rebuild shows the parts as the live chat had them.
	var liveParts, rebuiltParts int
	before.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			liveParts += len(m.Parts)
		}
	})
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			if m.Role == agent.RoleAssistant && len(m.Parts) > 0 {
				rebuiltParts += len(m.Parts)
			}
		}
	})
	if rebuiltParts < liveParts {
		t.Fatalf("rebuilt %d parts, the live chat had %d before the resume", rebuiltParts, liveParts)
	}
}

func TestAnUnknownSessionIsConversationNotFound(t *testing.T) {
	r := newRig(t)
	err := r.start(r.spec("ses_doesnotexist0000000000"))
	if !errors.Is(err, agent.ErrConversationNotFound) {
		t.Fatalf("start: %v", err)
	}
	err = r.start(r.spec("not-a-session"))
	if !errors.Is(err, agent.ErrConversationNotFound) {
		t.Fatalf("start with a malformed id: %v", err)
	}
}

func TestACrashIsReportedAndItsRunningToolInterrupted(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	sid := r.conversation()
	r.send("/crash")
	select {
	case err := <-r.rec.exited:
		if err == nil {
			t.Fatal("exited without an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the crash was not reported")
	}
	r.mustStart(sid)
	ev, ok := r.rec.first(agent.TypePart, func(ev agent.Event) bool { return ev.State == agent.PartInterrupted })
	if !ok || ev.Kind != agent.PartTool {
		t.Fatalf("dangling tool part: %+v", ev)
	}
	r.send("go on")
	if got := r.waitTurns(1); got[0] != agent.OutcomeCompleted {
		t.Fatalf("after the crash: %v", got)
	}
}

// A process that dies while text streams leaves OpenCode without that text;
// the next start interrupts the message and keeps the journal's text, and
// the start after it writes nothing again.
func TestACrashKeepsTheStreamedTextAndIsInterruptedOnce(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	sid := r.conversation()
	r.send("/crashtext")
	select {
	case <-r.rec.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the crash was not reported")
	}
	journal := r.rec
	if got := journal.lastText(); got != "partial answer" {
		t.Fatalf("streamed %q before the crash", got)
	}
	r.mustStart(sid)
	interrupted := func(ev agent.Event) bool { return ev.State == agent.MessageInterrupted }
	if n := r.rec.count(agent.TypeMessageState, interrupted); n != 1 {
		t.Fatalf("%d messages interrupted after the crash", n)
	}
	// The resume's events, applied to the journal the crash left.
	r.rec.view(func(_ *agent.Projection, events []agent.Event) { journal.Emit(events...) })
	if got := journal.lastText(); got != "partial answer" {
		t.Fatalf("text after the resume %q", got)
	}
	_ = r.a.Park(context.Background())
	r.mustStart(sid)
	if n := r.rec.count(agent.TypeMessageState, interrupted); n != 0 {
		t.Fatalf("a second start interrupted %d messages again", n)
	}
}

func TestAClosedEventStreamIsCaughtUp(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/dropstream")
	r.waitTurns(1)
	eventually(t, 5*time.Second, "the text after the drop", func() bool {
		found := false
		r.rec.view(func(p *agent.Projection, _ []agent.Event) {
			for _, m := range p.Messages {
				for _, part := range m.Parts {
					if part.Text == "after the drop" && part.State == agent.PartDone {
						found = true
					}
				}
			}
		})
		return found
	})
	r.send("still here")
	r.waitTurns(2)
	if !contains(r.lastText(), "still here") {
		t.Fatalf("after the reconnect: %q", r.lastText())
	}
}

// A permission asked while /event was closed is found by the catch-up and
// answered, so the turn does not wait for ever.
func TestAPermissionAskedWhileTheStreamWasClosedIsAnswered(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("/dropperm")
	if got := r.waitTurns(1); got[0] != agent.OutcomeCompleted {
		t.Fatalf("outcome %v", got)
	}
	if r.lastText() != "perm: always" {
		t.Fatalf("text %q", r.lastText())
	}
	if n := r.rec.count(agent.TypeNotice, func(ev agent.Event) bool { return ev.Code == NoticePermission }); n != 1 {
		t.Fatalf("%d permission notices", n)
	}
}

// After a resume, a catch-up does not write the subagents of the history
// again: the journal has them.
func TestACatchUpLeavesEarlierSubagentsAlone(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	sid := r.conversation()
	r.send("/subagent")
	r.waitTurns(1)
	_ = r.a.Park(context.Background())
	r.mustStart(sid)
	r.send("/dropstream")
	r.waitTurns(1)
	eventually(t, 5*time.Second, "the catch-up", func() bool {
		return contains(r.lastText(), "after the drop")
	})
	if n := r.rec.count(agent.TypePart, func(ev agent.Event) bool { return ev.ParentPartID != "" }); n != 0 {
		t.Fatalf("the catch-up wrote %d subagent parts of an earlier turn", n)
	}
}

func TestSwitchingAccountKeepsTheSession(t *testing.T) {
	r := newRig(t)
	keyA := `OPENCODE_AUTH_CONTENT={"fake":{"type":"api","key":"KEY-A"}}`
	keyB := `OPENCODE_AUTH_CONTENT={"fake":{"type":"api","key":"KEY-B"}}`
	spec := r.spec("")
	spec.Account, spec.Env = "a", append(append([]string{}, r.env...), keyA)
	if err := r.start(spec); err != nil {
		t.Fatal(err)
	}
	sid := r.conversation()
	r.send("/whoami")
	r.waitTurns(1)
	if r.lastText() != "account: fake:KEY-A (env)" {
		t.Fatalf("account a: %q", r.lastText())
	}
	pidA := r.a.PID()
	// A switch during a turn interrupts it first, and what was queued behind
	// it runs on the new account.
	r.send("/sleep 30")
	r.send("/whoami")
	eventually(t, 5*time.Second, "the slow turn", func() bool { return r.rec.count(agent.TypeTurnStarted, nil) == 2 })
	ctx := context.Background()
	if err := r.a.SwitchAccount(ctx, "b", append(append([]string{}, r.env...), keyB)); err != nil {
		t.Fatal(err)
	}
	if alive(pidA) || r.a.PID() == pidA {
		t.Fatalf("the old server %d is still there", pidA)
	}
	if got := r.rec.turns(); got[1] != agent.OutcomeInterrupted {
		t.Fatalf("the turn the switch cut short: %v", got)
	}
	r.waitTurns(3)
	if r.lastText() != "account: fake:KEY-B (env)" {
		t.Fatalf("the queued message ran as %q", r.lastText())
	}
	// An OAuth-style account keeps its credentials in its own data home.
	layout := NewLayout(r.data)
	if err := layout.PrepareAccount("c"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.AuthFile("c"), []byte(`{"fake":{"type":"oauth","access":"TOKEN-C"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.a.SwitchAccount(ctx, "c", r.env); err != nil {
		t.Fatal(err)
	}
	r.send("hello")
	r.send("/whoami")
	r.waitTurns(5)
	if r.lastText() != "account: fake:TOKEN-C (file)" {
		t.Fatalf("account c: %q", r.lastText())
	}
	if r.conversation() != sid {
		t.Fatalf("the session changed: %q", r.conversation())
	}
	var helloText string
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if strings.HasPrefix(part.Text, "you said: hello") {
					helloText = part.Text
				}
			}
		}
	})
	if !contains(helloText, "(turn 4 of "+sid+")") {
		t.Fatalf("the conversation did not continue: %q", helloText)
	}
	if n := r.rec.count(agent.TypeConversation, nil); n != 1 {
		t.Fatalf("%d conversation events", n)
	}
}

// A switch whose new process does not come up leaves no chat that only looks
// alive: the adapter reports its end, and takes nothing more.
func TestAFailedSwitchEndsTheAdapter(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	r.send("hello")
	r.waitTurns(1)
	ctx := context.Background()
	if err := r.a.SwitchAccount(ctx, "b", append(append([]string{}, r.env...), "FAKEOPENCODE_REFUSE=1")); err == nil {
		t.Fatal("the switch succeeded")
	}
	select {
	case err := <-r.rec.exited:
		if err == nil {
			t.Fatal("exited without an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the failed switch was not reported")
	}
	if err := r.a.Send(ctx, agent.Outgoing{MessageID: "m_after", Text: "after"}); !errors.Is(err, errStopped) {
		t.Fatalf("send after the failed switch: %v", err)
	}
}

func TestStopKillsAServerThatIgnoresTerm(t *testing.T) {
	r := newRig(t)
	r.env = append(r.env, "FAKEOPENCODE_IGNORE_TERM=1")
	r.mustStart("")
	pid := r.a.PID()
	start := time.Now()
	if err := r.a.Stop(); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if alive(pid) {
		t.Fatalf("the server %d is alive after Stop", pid)
	}
	if took < stopGrace || took > stopGrace+3*time.Second {
		t.Fatalf("Stop took %s", took)
	}
	select {
	case err := <-r.rec.exited:
		t.Fatalf("a stop was reported as a crash: %v", err)
	default:
	}
}

// A server that dies before its event stream opens fails the start; that is
// not reported as the death of a chat's process as well.
func TestAServerThatDiesBeforeItsStreamOpensOnlyFailsTheStart(t *testing.T) {
	r := newRig(t)
	r.env = append(r.env, "FAKEOPENCODE_EXIT_ON_EVENT=1")
	if err := r.start(r.spec("")); err == nil {
		t.Fatal("started")
	}
	select {
	case err := <-r.rec.exited:
		t.Fatalf("a failed start was also reported as an exit: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestStartRefusesAMissingBinaryAndMigratesOnce(t *testing.T) {
	r := newRig(t)
	r.env = []string{"PATH=" + t.TempDir()}
	spec := r.spec("")
	spec.Env = r.env
	if err := (New()).Start(context.Background(), spec, newRecorder()); err == nil {
		t.Fatal("started without a binary")
	}
	// The migration is one of the chat's children: it goes through Spawn.
	r = newRig(t)
	var mu sync.Mutex
	var spawned []string
	spec = r.spec("")
	spec.Spawn = func(cmd *exec.Cmd) error {
		mu.Lock()
		spawned = append(spawned, strings.Join(cmd.Args[1:], " "))
		mu.Unlock()
		return spawnPG(cmd)
	}
	if err := r.start(spec); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(spawned) == 0 || spawned[0] != "db select 1" {
		t.Fatalf("spawned %q", spawned)
	}
	mu.Unlock()
	layout := NewLayout(r.data)
	stamp, err := os.Stat(filepath.Join(layout.Root, migrateStamp))
	if err != nil {
		t.Fatal(err)
	}
	_ = r.a.Park(context.Background())
	r.mustStart(r.conversation())
	again, err := os.Stat(filepath.Join(layout.Root, migrateStamp))
	if err != nil || !again.ModTime().Equal(stamp.ModTime()) {
		t.Fatalf("migrated again: %v", err)
	}
}

// The dashboard's binary setting reaches the adapter through the spec: a path
// as it is, a bare name on the chat's PATH, and either wins over BinaryEnv.
func TestTheConfiguredBinaryIsRun(t *testing.T) {
	r := newRig(t)
	r.env = []string{"PATH=/nonexistent", "HOME=" + r.data, BinaryEnv + "=" + filepath.Join(t.TempDir(), "missing")}
	spec := r.spec("")
	spec.Binary = filepath.Join(fakeBin, "opencode")
	if err := r.start(spec); err != nil {
		t.Fatalf("start with the configured path: %v", err)
	}
	r.send("hello")
	if got := r.waitTurns(1); got[0] != agent.OutcomeCompleted {
		t.Fatalf("outcome %v", got)
	}
	_ = r.a.Park(context.Background())

	named := t.TempDir()
	if err := os.Symlink(filepath.Join(fakeBin, "opencode"), filepath.Join(named, "opencode-custom")); err != nil {
		t.Fatal(err)
	}
	r.env[0] = "PATH=" + named
	spec = r.spec(r.conversation())
	spec.Binary = "opencode-custom"
	if err := r.start(spec); err != nil {
		t.Fatalf("start with the configured name: %v", err)
	}
	if r.a.bin != filepath.Join(named, "opencode-custom") {
		t.Fatalf("ran %s", r.a.bin)
	}
	_ = r.a.Park(context.Background())

	spec.Binary = filepath.Join(t.TempDir(), "missing")
	if err := (New()).Start(context.Background(), spec, newRecorder()); err == nil {
		t.Fatal("a missing configured binary started")
	}
}
