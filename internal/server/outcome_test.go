//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The outcome of a turn is tested against the digest's own stand ins: rows
// the test writes, screens it chooses, and the gateway's digest queue - the
// verdict is part of the digest's answer, so that is where it is scripted.

const completeJSON = `{"summary":"Added the endpoint.","context":"","question":null,` +
	`"outcome":{"kind":"complete","reason":"The endpoint and its tests are in.","todos":[]}}`

const partialJSON = `{"summary":"Added the endpoint.","context":"","question":null,` +
	`"outcome":{"kind":"partial","reason":"The docs are still missing.",` +
	`"todos":["Write the docs","Update the changelog","a","b","c","d","e"]}}`

// outcomesOnFor switches the outcomes on for a digest environment.
func (e *digestEnv) outcomesOnFor() {
	e.t.Helper()
	e.configureOpenRouter(e.t, map[string]any{"outcomes": true})
}

// turnEnds is a turn of a session somebody typed at: working, then at its
// prompt with the unread mark, as the detector commits it.
func (e *digestEnv) turnEnds(id string, since int64) {
	e.srv.onSessionActivity(id, termux.Activity{State: termux.StateBusy, Since: since, Asked: true})
	e.srv.onSessionActivity(id, termux.Activity{State: termux.StateIdle, Since: since + 1, Unread: true, Asked: true})
}

// outcomeOf waits for a session's outcome to satisfy a condition.
func (e *digestEnv) outcomeOf(id, what string, cond func(outcome, bool) bool) outcome {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last outcome
	var has bool
	for time.Now().Before(deadline) {
		last, has = e.srv.digests.outcomeOf(id)
		if cond(last, has) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("the outcome of %s never became %s; it is %#v (present %v)", id, what, last, has)
	return outcome{}
}

func judgedAs(kind string) func(outcome, bool) bool {
	return func(o outcome, has bool) bool { return has && o.Phase == digestReady && o.Kind == kind }
}

// lastDigestCall is the newest digest call the gateway saw.
func (e *digestEnv) lastDigestCall() gatewayCall {
	e.t.Helper()
	calls := e.gw.digestsSeen()
	if len(calls) == 0 {
		e.t.Fatal("no digest was asked for")
	}
	return calls[len(calls)-1]
}

// A turn that ends is put on pending at once - before the read that judges it
// is made - and the read that answers is asked for the verdict, with the room
// to give it. The verdict is kept for the very idle it was made for.
func TestATurnsEndIsJudged(t *testing.T) {
	e := newDigestEnv(t)
	e.outcomesOnFor()
	e.session("o1", config.HarnessCodex, "• Added the endpoint.\n› \n")
	e.gw.digestBlock()
	e.gw.digestAlways(completeJSON)

	e.turnEnds("o1", 100)
	if o, ok := e.srv.digests.outcomeOf("o1"); !ok || o.Phase != digestPending || o.Since != 101 || o.Due == 0 {
		t.Fatalf("the turn's end was not put on pending: %#v %v", o, ok)
	}
	if !e.srv.digests.outcomePending("o1", 101) || e.srv.digests.outcomePending("o1", 100) {
		t.Fatal("outcomePending is not about the very idle")
	}
	e.gw.digestRelease()
	o := e.outcomeOf("o1", "complete", judgedAs(outcomeComplete))
	if o.Reason != "The endpoint and its tests are in." || o.Model != "test/home-model" || o.Since != 101 {
		t.Fatalf("the verdict = %#v", o)
	}
	call := e.lastDigestCall()
	if !strings.Contains(call.Messages[0].Content, `"outcome":{"kind":"complete|partial|needs_input|blocked"`) {
		t.Fatalf("the read was not asked for the verdict:\n%s", call.Messages[0].Content)
	}
	if call.MaxTokens != outcomeMaxTokens {
		t.Fatalf("max_tokens = %d, want %d", call.MaxTokens, outcomeMaxTokens)
	}

	// It is kept on this machine.
	var kept map[string]outcome
	if err := e.store.GetJSON(outcomesKey, &kept); err != nil || kept["o1"].Kind != outcomeComplete {
		t.Fatalf("the verdict was not kept: %#v %v", kept, err)
	}
}

// Partly done lists what is still open, and no more than five of it.
func TestAPartlyDoneTurnListsWhatIsOpen(t *testing.T) {
	e := newDigestEnv(t)
	e.outcomesOnFor()
	e.session("o2", config.HarnessCodex, "• Added the endpoint.\n› \n")
	e.gw.digestAlways(partialJSON)
	e.turnEnds("o2", 100)
	o := e.outcomeOf("o2", "partial", judgedAs(outcomePartial))
	if len(o.Todos) != outcomeTodos || o.Todos[0] != "Write the docs" {
		t.Fatalf("todos = %#v", o.Todos)
	}
}

// A turn that ends by asking is waiting on the person, whatever the model
// said: the question and the verdict are one answer.
func TestAQuestionIsWaitingOnYou(t *testing.T) {
	e := newDigestEnv(t)
	e.outcomesOnFor()
	e.session("o3", config.HarnessClaude, "● Which color should the button be? Red, green or blue?\n❯ \n")
	e.gw.digestAlways(strings.TrimSuffix(textQuestionJSON, "}") +
		`,"outcome":{"kind":"complete","reason":"Asks which colour.","todos":[]}}`)
	e.turnEnds("o3", 100)
	e.outcomeOf("o3", "needs_input", judgedAs(outcomeNeedsInput))
}

// Which changes are a turn's end: work, background work or a prompt that
// asked, ending at the prompt of a session somebody typed at. Not a
// harness's own start-up, not the detector finding its footing, not a tool,
// and nothing with the setting off.
func TestWhichChangesAreJudged(t *testing.T) {
	e := newDigestEnv(t)
	e.session("e1", config.HarnessCodex, "› \n")
	e.session("sh", config.HarnessShell, "$ \n")
	at := func(id string, state termux.State, since int64, asked bool) {
		e.srv.onSessionActivity(id, termux.Activity{State: state, Since: since, Asked: asked, Unread: asked})
	}
	pending := func(id string) bool {
		o, ok := e.srv.digests.outcomeOf(id)
		return ok && o.Phase == digestPending
	}
	e.gw.digestBlock()

	at("e1", termux.StateBusy, 1, true)
	at("e1", termux.StateIdle, 2, true)
	if pending("e1") {
		t.Fatal("a turn was judged with the setting off")
	}
	e.outcomesOnFor()

	for _, step := range []struct {
		what  string
		prev  termux.State
		asked bool
		want  bool
	}{
		{"a harness starting up", termux.StateBusy, false, false},
		{"the detector finding its footing", termux.StateUnknown, true, false},
		{"a turn that ended", termux.StateBusy, true, true},
		{"background work that ended", termux.StateTasks, true, true},
		{"a prompt that was declined", termux.StateWaiting, true, true},
	} {
		at("e1", step.prev, 10, step.asked)
		at("e1", termux.StateIdle, 11, step.asked)
		if got := pending("e1"); got != step.want {
			t.Fatalf("%s: pending = %v, want %v", step.what, got, step.want)
		}
	}
	at("sh", termux.StateBusy, 1, true)
	at("sh", termux.StateIdle, 2, true)
	if _, ok := e.srv.digests.outcomeOf("sh"); ok {
		t.Fatal("a shell was judged")
	}
	e.gw.digestRelease()
}

// An outcome goes with its turn - the next one starts, a prompt opens - and
// survives the detector losing sight of the session for a tick, onto the
// same prompt in a new commit.
func TestAnOutcomeGoesWithItsTurn(t *testing.T) {
	e := newDigestEnv(t)
	e.outcomesOnFor()
	e.session("o4", config.HarnessCodex, "• Done.\n› \n")
	e.gw.digestAlways(completeJSON)
	e.turnEnds("o4", 100)
	e.outcomeOf("o4", "complete", judgedAs(outcomeComplete))

	e.srv.onSessionActivity("o4", termux.Activity{State: termux.StateUnknown, Since: 200, Asked: true})
	e.srv.onSessionActivity("o4", termux.Activity{State: termux.StateIdle, Since: 201, Asked: true})
	if o, ok := e.srv.digests.outcomeOf("o4"); !ok || o.Since != 201 || o.Kind != outcomeComplete {
		t.Fatalf("a tick of unknown lost the verdict: %#v %v", o, ok)
	}

	e.srv.onSessionActivity("o4", termux.Activity{State: termux.StateBusy, Since: 300, Asked: true})
	if _, ok := e.srv.digests.outcomeOf("o4"); ok {
		t.Fatal("the verdict outlived its turn")
	}

	// A tick of unknown while the verdict is being read: the read made in
	// the commit before it still answers it.
	e.gw.digestBlock()
	e.turnEnds("o4", 400)
	e.srv.onSessionActivity("o4", termux.Activity{State: termux.StateUnknown, Since: 500, Asked: true})
	e.srv.onSessionActivity("o4", termux.Activity{State: termux.StateIdle, Since: 501, Asked: true})
	e.gw.digestRelease()
	e.outcomeOf("o4", "judged after the tick", func(o outcome, has bool) bool {
		return has && o.Since == 501 && o.Phase == digestReady
	})
	e.srv.onSessionActivity("o4", termux.Activity{State: termux.StateBusy, Since: 600, Asked: true})
	var kept map[string]outcome
	if err := e.store.GetJSON(outcomesKey, &kept); err != nil {
		t.Fatal(err)
	}
	if _, ok := kept["o4"]; ok {
		t.Fatal("the verdict of a turn that is over is still kept")
	}
}

// A read that fails is a verdict that could not be had, and says why; with
// the setting off a digest is asked exactly what it always was.
func TestAFailedReadIsNoVerdict(t *testing.T) {
	e := newDigestEnv(t)
	e.outcomesOnFor()
	e.session("o5", config.HarnessCodex, "• Done.\n› \n")
	e.gw.digestRefuse(500, "the gateway is down")
	e.turnEnds("o5", 100)
	o := e.outcomeOf("o5", "an error", func(o outcome, has bool) bool { return has && o.Phase == digestError })
	if o.Error == "" {
		t.Fatalf("the error says nothing: %#v", o)
	}

	// A model that says nothing about the outcome is no verdict either.
	e.gw.digestRefuse(0, "")
	e.gw.digestAlways(`{"summary":"Done.","context":"","question":null}`)
	e.turnEnds("o5", 200)
	o = e.outcomeOf("o5", "an error", func(o outcome, has bool) bool { return has && o.Since == 201 && o.Phase == digestError })
	if o.Error != "the home model gave no outcome" {
		t.Fatalf("error = %q", o.Error)
	}
}

func TestOutcomesOffLeaveTheDigestAsItWas(t *testing.T) {
	e := newDigestEnv(t)
	e.session("o6", config.HarnessCodex, "• Done.\n› \n")
	e.turnEnds("o6", 100)
	e.digestOf("o6", "ready", ready)
	call := e.lastDigestCall()
	if call.Messages[0].Content != digestSystemPrompt("English") || call.MaxTokens != digestMaxTokens {
		t.Fatalf("a digest with the outcomes off was asked something else (max_tokens %d):\n%s",
			call.MaxTokens, call.Messages[0].Content)
	}
	if _, ok := e.srv.digests.outcomeOf("o6"); ok {
		t.Fatal("an outcome was made with the setting off")
	}
}

// Switched off, the verdicts go; a restart shows a kept one again only while
// the session is at the idle it was made for.
func TestOutcomesSwitchedOffAndKeptAcrossARestart(t *testing.T) {
	e := newDigestEnv(t)
	e.outcomesOnFor()
	e.session("o7", config.HarnessCodex, "• Done.\n› \n")
	e.session("o8", config.HarnessCodex, "• Done.\n› \n")
	e.gw.digestAlways(completeJSON)
	e.turnEnds("o7", 100)
	e.turnEnds("o8", 100)
	e.outcomeOf("o7", "complete", judgedAs(outcomeComplete))
	e.outcomeOf("o8", "complete", judgedAs(outcomeComplete))

	// A process that starts again finds both.
	again := newDigester(e.srv)
	if len(again.outcomes) != 2 || again.outcomes["o7"].Since != 101 {
		t.Fatalf("a restart found %#v", again.outcomes)
	}
	again.outcomeEdge("o7", termux.Activity{State: termux.StateIdle, Since: 101})
	again.outcomeEdge("o8", termux.Activity{State: termux.StateIdle, Since: 555})
	if _, ok := again.outcomeOf("o7"); !ok {
		t.Fatal("the verdict of the idle the session is still at was dropped")
	}
	if _, ok := again.outcomeOf("o8"); ok {
		t.Fatal("the verdict of an idle the session left was shown again")
	}

	e.configureOpenRouter(t, map[string]any{"outcomes": false})
	if len(e.srv.digests.outcomesAll()) != 0 {
		t.Fatalf("switched off, the verdicts stay: %#v", e.srv.digests.outcomesAll())
	}
}

// Read again by hand, the verdict is read again with the summary.
func TestReadAgainJudgesAgain(t *testing.T) {
	e := newDigestEnv(t)
	e.outcomesOnFor()
	e.session("o9", config.HarnessCodex, "• Done.\n› \n")
	e.gw.digestAlways(completeJSON)
	e.turnEnds("o9", 100)
	e.outcomeOf("o9", "complete", judgedAs(outcomeComplete))
	e.srv.digests.setManualGap(0)

	e.gw.digestAlways(partialJSON)
	row, err := e.store.GetSession("o9")
	if err != nil {
		t.Fatal(err)
	}
	e.srv.digests.requestNow(row)
	e.outcomeOf("o9", "partial", judgedAs(outcomePartial))
}

// The Status button is told the verdict on the turn the session rests after.
func TestStatusPromptSaysTheVerdict(t *testing.T) {
	words := outcomeWords(outcome{Phase: digestReady, Kind: outcomePartial, Reason: "Docs missing.", Todos: []string{"Write docs"}})
	prompt := statusPrompt(config.HarnessClaude, termux.StateIdle, config.LanguageEN, words, "screen")
	if !strings.Contains(prompt, "this round is done but the task is not (Docs missing.); still open: Write docs") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	if strings.Contains(statusPrompt(config.HarnessClaude, termux.StateIdle, config.LanguageEN, "", "screen"), "check of the turn") {
		t.Fatal("a session with no verdict is said to have one")
	}
}

// Claude's own record of the turn: the instruction, the answer after it, and
// the task list as TaskCreate and TaskUpdate left it - as Claude Code 2.1
// writes them.
func TestClaudeTurnIsReadFromTheTranscript(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"an older instruction"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"An older answer."}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Add the endpoint and document it"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"TaskCreate","input":{"subject":"Add the endpoint","description":"…"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"Task #1 created successfully: Add the endpoint"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu2","name":"TaskCreate","input":{"subject":"Write the docs"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu2","content":[{"type":"text","text":"Task #2 created successfully: Write the docs"}]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu3","name":"TaskUpdate","input":{"taskId":"1","status":"completed"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu3","content":"Updated task #1 status"}]}}`,
		`{"type":"user","isMeta":true,"message":{"content":"Caveat: the messages below were generated by the user"}}`,
		`{"type":"user","message":{"content":"<task-notification>\n<task-id>x</task-id>\n</task-notification>"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"The endpoint is in. The docs are next."}]}}`,
		`not json`,
	}
	turn := parseClaudeTurn([]byte(strings.Join(lines, "\n")))
	if turn.Prompt != "Add the endpoint and document it" {
		t.Fatalf("prompt = %q", turn.Prompt)
	}
	if turn.Answer != "The endpoint is in. The docs are next." {
		t.Fatalf("answer = %q", turn.Answer)
	}
	if len(turn.Tasks) != 2 || turn.Tasks[0].Status != "completed" || turn.Tasks[1].Status != "pending" ||
		turn.Tasks[1].Subject != "Write the docs" {
		t.Fatalf("tasks = %#v", turn.Tasks)
	}
	words := turnPrompt(turn)
	if !strings.Contains(words, "[x] Add the endpoint\n[ ] Write the docs") {
		t.Fatalf("turn prompt:\n%s", words)
	}

	// The read of a session's own transcript, by its id, under its config.
	dir := t.TempDir()
	project := filepath.Join(dir, "projects", "p")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "cli-1.jsonl"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readClaudeTurn(dir, "cli-1"); !ok || got.Prompt != turn.Prompt {
		t.Fatalf("readClaudeTurn = %#v %v", got, ok)
	}
	if _, ok := readClaudeTurn(dir, "../cli-1"); ok {
		t.Fatal("a session id that is a path was read")
	}
}

// A turn's end whose verdict is being read waits for it, and is pushed saying
// it. One whose verdict does not come in time is pushed as "Finished"; one
// that turns out not to be over - the session works again, or somebody read
// it - is not pushed at all.
func TestAFinishedTurnIsPushedWithItsVerdict(t *testing.T) {
	e := newDigestEnv(t)
	e.outcomesOnFor()
	sent := catch(e.srv.push)
	if err := e.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	next := func(what string) sentPush {
		t.Helper()
		select {
		case one := <-sent:
			return one
		case <-time.After(10 * time.Second):
			t.Fatalf("no push was sent for %s", what)
		}
		return sentPush{}
	}
	none := func(what string, within time.Duration) {
		t.Helper()
		select {
		case one := <-sent:
			t.Fatalf("%s pushed %+v", what, one.note)
		case <-time.After(within):
		}
	}
	e.session("p1", config.HarnessCodex, "• Added the endpoint.\n› \n")

	e.gw.digestBlock()
	e.gw.digestAlways(partialJSON)
	e.turnEnds("p1", 100)
	none("a turn whose verdict is being read", 300*time.Millisecond)
	e.gw.digestRelease()
	if one := next("the verdict"); one.note.Body != "Partly done · 5 open: Write the docs" || one.note.Kind != pushFinished {
		t.Fatalf("the verdict = %+v", one.note)
	}

	e.gw.digestAlways(completeJSON)
	e.turnEnds("p1", 200)
	if one := next("the verdict"); one.note.Body != "Complete · The endpoint and its tests are in." {
		t.Fatalf("the verdict = %+v", one.note)
	}

	// Too slow: "Finished", and nothing more when it does come.
	e.srv.push.mu.Lock()
	e.srv.push.outcomeWait = 50 * time.Millisecond
	e.srv.push.mu.Unlock()
	e.gw.digestBlock()
	e.turnEnds("p1", 300)
	if one := next("a verdict that is late"); one.note.Body != "Finished" {
		t.Fatalf("a verdict that is late = %+v", one.note)
	}
	e.gw.digestRelease()
	e.outcomeOf("p1", "complete", func(o outcome, has bool) bool { return has && o.Since == 301 && o.Phase == digestReady })
	none("a verdict after the wait", 300*time.Millisecond)

	// Working again before the verdict: it was not the turn's end.
	e.srv.push.mu.Lock()
	e.srv.push.outcomeWait = outcomeWait
	e.srv.push.mu.Unlock()
	e.gw.digestBlock()
	e.turnEnds("p1", 400)
	e.srv.onSessionActivity("p1", termux.Activity{State: termux.StateBusy, Since: 402, Asked: true})
	e.gw.digestRelease()
	none("a turn that went on", 500*time.Millisecond)

	// Read before the verdict: the person has seen how it ended.
	e.gw.digestBlock()
	e.turnEnds("p1", 500)
	e.srv.onSessionActivity("p1", termux.Activity{State: termux.StateIdle, Since: 501, Asked: true})
	e.gw.digestRelease()
	e.outcomeOf("p1", "complete", func(o outcome, has bool) bool { return has && o.Since == 501 && o.Phase == digestReady })
	none("a turn somebody read", 300*time.Millisecond)

	// A turn that ends by asking is pushed as its question.
	e.gw.digestAlways(strings.TrimSuffix(textQuestionJSON, "}") +
		`,"outcome":{"kind":"complete","reason":"Asks which colour.","todos":[]}}`)
	e.turnEnds("p1", 600)
	if one := next("a question"); one.note.Kind != pushQuestion || one.note.Body != "Which color should the button be?" {
		t.Fatalf("a question = %+v", one.note)
	}
	none("a question pushed twice", 300*time.Millisecond)
}

func TestOutcomeBody(t *testing.T) {
	for _, c := range []struct {
		o          outcome
		body, kind string
	}{
		{outcome{Phase: digestError}, "Finished", pushFinished},
		{outcome{Phase: digestReady, Kind: outcomeBlocked, Reason: "The build fails."}, "Blocked · The build fails.", pushFinished},
		{outcome{Phase: digestReady, Kind: outcomeNeedsInput, Reason: "Needs a login."}, "Waiting on you · Needs a login.", pushFinished},
		{outcome{Phase: digestReady, Kind: outcomePartial, Reason: "Half."}, "Partly done · Half.", pushFinished},
		{outcome{Phase: digestReady, Kind: outcomeComplete}, "Complete", pushFinished},
	} {
		if body, kind := outcomeBody(c.o, nil); body != c.body || kind != c.kind {
			t.Errorf("%#v: %q %q", c.o, body, kind)
		}
	}
}
