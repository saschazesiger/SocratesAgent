//go:build !windows

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// lastText is the newest assistant message's last text part.
func lastText(t *testing.T, r *rec) string {
	t.Helper()
	var text string
	r.view(func(p *agent.Projection) {
		if parts := partsOf(lastAssistant(p), agent.PartText); len(parts) > 0 {
			text = parts[len(parts)-1].Text
		}
	})
	return text
}

// askEvent waits for the n-th ask event.
func askEvent(t *testing.T, r *rec, n int) agent.Event {
	t.Helper()
	evs := r.waitFor(t, "an ask", func(evs []agent.Event) bool { return count(evs, agent.TypeAsk) >= n })
	seen := 0
	for _, ev := range evs {
		if ev.Type == agent.TypeAsk {
			if seen++; seen == n {
				return ev
			}
		}
	}
	panic("unreachable")
}

// Every chat has ask_user as the in-process server "socrates", with no
// timeout worth the name and no auto-backgrounding, and the system prompt
// points the model at it.
func TestAskUserIsAnInProcessServerOfEveryChat(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/mcp")
	send(t, a, "m2", "/argv")
	r.turnsEnded(t, 2)
	var mcp, argv string
	r.view(func(p *agent.Projection) {
		var texts []string
		for _, m := range p.Messages {
			for _, part := range partsOf(m, agent.PartText) {
				texts = append(texts, part.Text)
			}
		}
		mcp, argv = texts[0], texts[1]
	})
	for _, want := range []string{"servers: socrates", `"timeout":2147483647`, `"disableAutoBackground":true`, "tools: " + AskTool} {
		if !strings.Contains(mcp, want) {
			t.Errorf("the MCP setup lacks %q:\n%s", want, mcp)
		}
	}
	if !strings.Contains(argv, "--append-system-prompt When you need a decision or any information from the person, call the "+AskTool) {
		t.Errorf("the system prompt does not point at ask_user:\n%s", argv)
	}
}

// A chat with the chat tools has them beside ask_user, and both guidances; a
// test session has its role's tools instead, and its role's protocol, then
// the options' prompt, the model's instructions and the catalog's, in that
// order, and nobody to ask.
func TestATestSessionGetsItsRolesToolsInsteadOfAskUser(t *testing.T) {
	run := func(ep chattools.Endpoint) (mcp, argv string) {
		e := newEnv(t)
		spec := e.spec("")
		spec.ChatTools, spec.Instructions = ep, map[string]string{"haiku": "NOTE-HAIKU"}
		if ep.Test() {
			spec.TestInstructions = "CATALOG-NOTES"
		}
		a := New(Options{AppendSystemPrompt: "OPTIONS-PROMPT"})
		r := newRec()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Start(ctx, spec, r); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Stop() })
		send(t, a, "m1", "/mcp")
		send(t, a, "m2", "/argv")
		r.turnsEnded(t, 2)
		r.view(func(p *agent.Projection) {
			var texts []string
			for _, m := range p.Messages {
				for _, part := range partsOf(m, agent.PartText) {
					texts = append(texts, part.Text)
				}
			}
			mcp, argv = texts[0], texts[1]
		})
		return mcp, argv
	}
	sock := "/nonexistent/chat-tools.sock"
	mcp, argv := run(chattools.Endpoint{Socket: sock, Token: "c.x"})
	if !strings.Contains(mcp, "tools: "+AskTool+","+chatsPrefix+chattools.List) {
		t.Errorf("a chat's tools:\n%s", mcp)
	}
	if !strings.Contains(argv, askGuidance+"\n\n"+chattools.Guidance(chatsPrefix)) || strings.Contains(argv, "CATALOG-NOTES") {
		t.Errorf("a chat's system prompt:\n%s", argv)
	}

	mcp, argv = run(chattools.Endpoint{Socket: sock, Token: "c.x", Kind: testtools.KindCell})
	if !strings.Contains(mcp, "tools: "+chatsPrefix+testtools.Steps+","+chatsPrefix+testtools.Judge) || strings.Contains(mcp, AskTool) {
		t.Errorf("a cell's tools:\n%s", mcp)
	}
	want := "--append-system-prompt " + testtools.Guidance(testtools.KindCell, chatsPrefix, true) + "\n\nOPTIONS-PROMPT\n\n" +
		agent.ModelPrompt("haiku", "NOTE-HAIKU") + "\n\nCATALOG-NOTES"
	if !strings.Contains(argv, want) || strings.Contains(argv, askGuidance) || strings.Contains(argv, chattools.List) {
		t.Errorf("a cell's system prompt:\n%s", argv)
	}
	if !strings.Contains(argv, "--disallowedTools AskUserQuestion") {
		t.Errorf("AskUserQuestion is allowed in a test session:\n%s", argv)
	}
}

// A call becomes an ask of the running turn, named after its tool call; it
// blocks until the answer, and the model gets §5.2 back.
func TestAskUserWaitsForTheAnswer(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/askuser")
	ask := askEvent(t, r, 1)
	p := askuser.Decode(ask.Payload)
	if p == nil || p.Title != "Setup" || len(p.Questions) != 2 || !p.Questions[1].AllowOther || p.Questions[1].Header == "" {
		t.Fatalf("payload %s", ask.Payload)
	}
	if !strings.HasPrefix(ask.AskID, "ask_toolu_fake_") || p.PartID != strings.TrimPrefix(ask.AskID, "ask_") || ask.TurnID == "" {
		t.Fatalf("ask %+v", ask)
	}
	var running bool
	r.view(func(pr *agent.Projection) { running = pr.Turn != nil && pr.Turn.ID == ask.TurnID })
	if !running {
		t.Fatal("the ask is not of the running turn")
	}
	time.Sleep(100 * time.Millisecond)
	if count(r.waitFor(t, "", func([]agent.Event) bool { return true }), agent.TypeTurnEnded) != 0 {
		t.Fatal("the turn ended before the answer")
	}
	answer := json.RawMessage(`{"answers":{"db":{"selected":["sqlite"],"note":"one file"},"name":{"other":"Atlas"}},"general_note":null,"follow_up":null}`)
	if err := a.Answer(context.Background(), ask.AskID, answer); err != nil {
		t.Fatal(err)
	}
	r.turnsEnded(t, 1)
	text := lastText(t, r)
	if !strings.Contains(text, `ask_user returned: {"answers":{"db":{"selected":["sqlite"],"other":null,"note":"one file"`) ||
		!strings.Contains(text, `"name":{"selected":[],"other":"Atlas"`) {
		t.Fatalf("the model got %q", text)
	}
	if err := a.Answer(context.Background(), ask.AskID, answer); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("a second answer: %v", err)
	}
}

// A call the schema does not allow is the model's to fix: nothing is asked.
func TestABadAskUserCallGoesBackToTheModel(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", `/askuser {"questions":[{"id":"x","question":"Pick","kind":"single"}]}`)
	evs := r.turnsEnded(t, 1)
	if count(evs, agent.TypeAsk) != 0 {
		t.Fatal("a bad call was asked")
	}
	if text := lastText(t, r); !strings.Contains(text, "ask_user failed: ask_user was called with problems") ||
		!strings.Contains(text, `kind "single" needs options`) {
		t.Fatalf("the model got %q", text)
	}
}

// An interrupt withdraws the call: the ask stays open, and an answer to it
// is reported gone from the moment the CLI took the interrupt, before the
// call's own cancel has come, so the host sends it as a message.
func TestAnInterruptedAskUserCallIsGone(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/askuser")
	ask := askEvent(t, r, 1)
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{}}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("the answer right after the interrupt: %v", err)
	}
	evs := r.turnsEnded(t, 1)
	if end := evs[len(evs)-1]; end.Type == agent.TypeTurnEnded && end.Outcome != agent.OutcomeInterrupted {
		t.Fatalf("outcome %q", end.Outcome)
	}
	waitHandled(t, a)
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{}}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("the answer after the interrupt: %v", err)
	}
	var open bool
	r.view(func(p *agent.Projection) { open = len(p.OpenAsks()) == 1 })
	if !open {
		t.Fatal("the ask was closed by the interrupt")
	}
	send(t, a, "m2", "again")
	r.turnsEnded(t, 2)
}

// A call the CLI withdraws the moment it made it (notifications/cancelled
// right behind the tools/call) ends its handler: the cancel finds the call
// by its JSON-RPC id, though the server may not have seen it yet.
func TestACallWithdrawnAtOnceEndsItsHandler(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/askdrop")
	r.turnsEnded(t, 1)
	waitHandled(t, a)
	var open []string
	r.view(func(p *agent.Projection) {
		for _, ask := range p.OpenAsks() {
			open = append(open, ask.ID)
		}
	})
	for _, id := range open {
		if err := a.Answer(context.Background(), id, json.RawMessage(`{"answers":{}}`)); !errors.Is(err, agent.ErrAskGone) {
			t.Fatalf("the answer to a withdrawn call: %v", err)
		}
	}
}

// A CLI that dies ends every handler waiting on it, and its asks are gone.
func TestACLIThatExitsEndsItsWaitingCalls(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/askuser")
	ask := askEvent(t, r, 1)
	_ = syscall.Kill(a.PID(), syscall.SIGKILL)
	select {
	case <-r.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the exit was not reported")
	}
	waitHandled(t, a)
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{}}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("the answer after the exit: %v", err)
	}
}

// waitHandled waits until no handler of the CLI's requests is left.
func waitHandled(t *testing.T, a *Adapter) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.ctlMu.Lock()
		n := len(a.handling)
		a.ctlMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d handlers still run", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An MCP elicitation is asked as a form of questions and answered in its own
// shape.
func TestAnElicitationIsAskedAndAnsweredAsAForm(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/elicit")
	ask := askEvent(t, r, 1)
	p := askuser.Decode(ask.Payload)
	if p == nil || p.Source != askuser.SourceElicitation || p.Questions[0].ID != "colour" || p.Questions[1].ID != "notes" {
		t.Fatalf("payload %s", ask.Payload)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{"colour":{"selected":["green"]},"notes":{"other":"from the phone"}}}`)); err != nil {
		t.Fatal(err)
	}
	r.turnsEnded(t, 1)
	if text := lastText(t, r); text != `elicitation: {"action":"accept","content":{"colour":"green","notes":"from the phone"}}` {
		t.Fatalf("the server got %q", text)
	}

	// Words the form has no field for: the form gets its values, and the
	// answer is reported deferred, so that the host sends it as a message.
	send(t, a, "m2", "/elicit")
	ask = askEvent(t, r, 2)
	err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{"colour":{"selected":["red"],"note":"the dark one"}}}`))
	if !errors.Is(err, agent.ErrAskDeferred) {
		t.Fatalf("an answer with a note: %v", err)
	}
	r.turnsEnded(t, 2)
	if text := lastText(t, r); text != `elicitation: {"action":"accept","content":{"colour":"red"}}` {
		t.Fatalf("the server got %q", text)
	}

	// A URL cannot be shown: declined, and the chat says so.
	send(t, a, "m3", "/elicit url")
	evs := r.turnsEnded(t, 3)
	if text := lastText(t, r); text != `elicitation: {"action":"decline"}` {
		t.Fatalf("the server got %q", text)
	}
	var notice bool
	for _, ev := range evs {
		notice = notice || ev.Type == agent.TypeNotice && ev.Code == "elicitation_declined"
	}
	if !notice {
		t.Fatal("no notice for the declined elicitation")
	}
}
