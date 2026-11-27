//go:build !windows

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
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
	if !strings.Contains(argv, "--append-system-prompt Questions to the person go through the "+AskTool+" tool. It never waits") ||
		!strings.Contains(argv, "select:"+AskTool+","+chatsPrefix+"check_questions,"+chatsPrefix+"withdraw_question") {
		t.Errorf("the system prompt does not point at ask_user:\n%s", argv)
	}
}

// A chat with the chat tools has them beside ask_user, and both guidances,
// the docs' and its pages folder's, then the options' prompt and the model's
// instructions.
func TestAChatGetsTheChatToolsBesideAskUser(t *testing.T) {
	run := func(ep chattools.Endpoint) (mcp, argv string) {
		e := newEnv(t)
		spec := e.spec("")
		spec.ChatTools, spec.Instructions = ep, map[string]string{"haiku": "NOTE-HAIKU"}
		spec.DataDir = "/data"
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
	if !strings.Contains(argv, askGuidance+"\n\n"+chattools.Guidance(chatsPrefix)) ||
		!strings.Contains(argv, todoGuidance+"\n\n"+agent.DocsGuidance+"\n\n"+agent.PagesGuidance("/data/chats/c1/pages")+"\n\nOPTIONS-PROMPT") ||
		!strings.Contains(argv, "OPTIONS-PROMPT\n\n"+agent.ModelPrompt("haiku", "NOTE-HAIKU")) {
		t.Errorf("a chat's system prompt:\n%s", argv)
	}
}

// A call becomes an ask of the running turn, drawn at its tool call, whose
// answer goes as a message; the call returns at once with the question's id
// and the turn goes on.
func TestAskUserReturnsAtOnce(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/askuser")
	ask := askEvent(t, r, 1)
	p := askuser.Decode(ask.Payload)
	if p == nil || len(p.Questions) != 1 || p.Questions[0].Header != "Database" || p.Blocks != "The schema" {
		t.Fatalf("payload %s", ask.Payload)
	}
	if !strings.HasPrefix(ask.AskID, "ask_") || len(ask.AskID) != len("ask_")+8 || !strings.HasPrefix(p.PartID, "toolu_fake_") ||
		ask.TurnID == "" || ask.Via != agent.ViaMessage {
		t.Fatalf("ask %+v", ask)
	}
	r.turnsEnded(t, 1)
	if text := lastText(t, r); !strings.Contains(text, "ask_user returned: Question "+ask.AskID+" is now with the person") {
		t.Fatalf("the model got %q", text)
	}
	var open int
	r.view(func(pr *agent.Projection) { open = len(pr.OpenAsks()) })
	if open != 1 {
		t.Fatalf("%d open asks after the turn", open)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("an answer through the tool: %v", err)
	}
}

// A subagent's call asks nothing: its answer would reach only the main
// agent, so the subagent is told to hand the question back.
func TestASubagentCannotAsk(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/subask")
	evs := r.turnsEnded(t, 1)
	if count(evs, agent.TypeAsk) != 0 {
		t.Fatal("a subagent's call was asked")
	}
	var said bool
	for _, ev := range evs {
		said = said || strings.Contains(ev.Text+string(ev.Content), "Nothing was asked: only the main agent can ask the person")
	}
	if !said {
		t.Fatal("the subagent was not told why")
	}
}

// check_questions and withdraw_question reach the host's questions.
func TestTheQuestionToolsReachTheHost(t *testing.T) {
	e := newEnv(t)
	spec := e.spec("")
	q := &fakeQuestions{}
	spec.Questions = q
	a := New(Options{})
	r := newRec()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Start(ctx, spec, r); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	send(t, a, "m1", "/mcp")
	send(t, a, "m2", `/mcptool withdraw_question {"question_id":"ask_00000001","reason":"found it"}`)
	send(t, a, "m3", "/mcptool check_questions")
	r.turnsEnded(t, 3)
	var texts []string
	r.view(func(p *agent.Projection) {
		for _, m := range p.Messages {
			for _, part := range partsOf(m, agent.PartText) {
				texts = append(texts, part.Text)
			}
		}
	})
	if !strings.Contains(texts[0], "tools: "+AskTool+","+chatsPrefix+"check_questions,"+chatsPrefix+"withdraw_question") {
		t.Errorf("tools:\n%s", texts[0])
	}
	if q.withdrawn != "ask_00000001:found it" || !strings.Contains(texts[1], "is withdrawn") {
		t.Errorf("withdraw: %q, %s", q.withdrawn, texts[1])
	}
	if !strings.Contains(texts[2], "ask_00000001 [withdrawn]") {
		t.Errorf("check: %s", texts[2])
	}
}

// fakeQuestions is a host's questions with one open question.
type fakeQuestions struct{ withdrawn string }

func (f *fakeQuestions) List(context.Context) ([]askuser.Asked, error) {
	p, _ := askuser.Parse(json.RawMessage(`{"question":"Which database?","blocks":"x"}`))
	state := askuser.StateOpen
	if f.withdrawn != "" {
		state = askuser.StateWithdrawn
	}
	return []askuser.Asked{{ID: "ask_00000001", Payload: p.JSON(), State: state}}, nil
}

func (f *fakeQuestions) Withdraw(_ context.Context, id, reason string) (bool, error) {
	f.withdrawn = id + ":" + reason
	return true, nil
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
