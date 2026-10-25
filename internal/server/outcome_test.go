//go:build !windows

package server

import (
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The verdict on a turn is tested here without a model and without a chat's
// host: rows the test writes, the edges the detector commits handed to the
// digester's own entry point, and the verdict handed back as the judge would.
// The judge itself - a chat's turn read from its journal and the model asked -
// is chatjudge_test.go's.

// outcomeEnv is a signed in server with the outcomes on.
type outcomeEnv struct {
	*testEnv
	t *testing.T
}

func newOutcomeEnv(t *testing.T) *outcomeEnv {
	t.Helper()
	e := &outcomeEnv{testEnv: signedIn(t), t: t}
	e.configureOpenRouter(t, map[string]any{"api_key": "k", "outcomes": true})
	return e
}

// row writes a running session of a harness: a chat for an agent, a
// terminal for a tool.
func (e *outcomeEnv) row(id, harness string) {
	e.t.Helper()
	kind := store.KindChat
	if harness == config.HarnessShell {
		kind = store.KindTerminal
	}
	if err := e.store.CreateSession(&store.Session{
		ID: id, Title: "Session " + id, Harness: harness, Workdir: e.t.TempDir(),
		State: store.StateRunning, Kind: kind,
	}); err != nil {
		e.t.Fatal(err)
	}
}

// edge is the detector committing a change, as outcomeEdge hears it. The
// judge it hands back is not run: the test judges by hand (judge).
func (e *outcomeEnv) edge(id string, state termux.State, since int64, asked bool) bool {
	return e.srv.digests.outcomeEdge(id, termux.Activity{State: state, Since: since, Asked: asked, Unread: asked}) != nil
}

// judge is the verdict of the judge that was started in activity in.
func (e *outcomeEnv) judge(id string, in termux.Activity, kind string) {
	d := e.srv.digests
	d.mu.Lock()
	defer d.mu.Unlock()
	d.judgedLocked(id, in, &outcomeAnswer{Kind: kind, Reason: "Because."}, "test/home-model", "")
}

// The edges a verdict is made for: the end of a turn somebody asked for, of
// background work, and of a question - and nothing else, and never for a
// Shell.
func TestWhichChangesAreJudged(t *testing.T) {
	e := newOutcomeEnv(t)
	e.row("c1", config.HarnessClaude)
	e.row("sh", config.HarnessShell)
	pending := func(id string) bool {
		o, ok := e.srv.digests.outcomeOf(id)
		return ok && o.Phase == digestPending
	}
	for _, step := range []struct {
		what  string
		prev  termux.State
		asked bool
		want  bool
	}{
		{"an agent starting up", termux.StateBusy, false, false},
		{"the detector finding its footing", termux.StateUnknown, true, false},
		{"a turn that ended", termux.StateBusy, true, true},
		{"background work that ended", termux.StateTasks, true, true},
		{"a question that was declined", termux.StateWaiting, true, true},
	} {
		e.edge("c1", step.prev, 10, step.asked)
		judged := e.edge("c1", termux.StateIdle, 11, step.asked)
		if got := pending("c1"); got != step.want || judged != step.want {
			t.Fatalf("%s: pending = %v, judged = %v, want %v", step.what, got, judged, step.want)
		}
	}
	e.edge("sh", termux.StateBusy, 1, true)
	if e.edge("sh", termux.StateIdle, 2, true) {
		t.Fatal("a shell was judged")
	}
	if _, ok := e.srv.digests.outcomeOf("sh"); ok {
		t.Fatal("a shell has a verdict")
	}

	e.configureOpenRouter(t, map[string]any{"outcomes": false})
	e.edge("c1", termux.StateBusy, 20, true)
	if e.edge("c1", termux.StateIdle, 21, true) || pending("c1") {
		t.Fatal("a turn was judged with the setting off")
	}
}

// An outcome goes with its turn - the next one starts, a question opens -
// and survives the detector losing sight of the chat for a tick, onto the
// same idle in a new commit.
func TestAnOutcomeGoesWithItsTurn(t *testing.T) {
	e := newOutcomeEnv(t)
	e.row("o4", config.HarnessCodex)
	e.edge("o4", termux.StateBusy, 100, true)
	e.edge("o4", termux.StateIdle, 101, true)
	e.judge("o4", termux.Activity{State: termux.StateIdle, Since: 101}, outcomeComplete)
	if o, ok := e.srv.digests.outcomeOf("o4"); !ok || o.Phase != digestReady || o.Kind != outcomeComplete {
		t.Fatalf("the verdict: %#v %v", o, ok)
	}

	e.edge("o4", termux.StateUnknown, 200, true)
	e.edge("o4", termux.StateIdle, 201, true)
	if o, ok := e.srv.digests.outcomeOf("o4"); !ok || o.Since != 201 || o.Kind != outcomeComplete {
		t.Fatalf("a tick of unknown lost the verdict: %#v %v", o, ok)
	}
	e.edge("o4", termux.StateBusy, 300, true)
	if _, ok := e.srv.digests.outcomeOf("o4"); ok {
		t.Fatal("the verdict outlived its turn")
	}

	// A tick of unknown while the verdict is being read: the judge started
	// in the commit before it still answers it.
	e.edge("o4", termux.StateIdle, 401, true)
	e.edge("o4", termux.StateUnknown, 500, true)
	e.edge("o4", termux.StateIdle, 501, true)
	e.judge("o4", termux.Activity{State: termux.StateIdle, Since: 401}, outcomePartial)
	if o, ok := e.srv.digests.outcomeOf("o4"); !ok || o.Since != 501 || o.Phase != digestReady || o.Kind != outcomePartial {
		t.Fatalf("judged after the tick: %#v %v", o, ok)
	}
	e.edge("o4", termux.StateBusy, 600, true)
	var kept map[string]outcome
	if err := e.store.GetJSON(outcomesKey, &kept); err != nil {
		t.Fatal(err)
	}
	if _, ok := kept["o4"]; ok {
		t.Fatal("the verdict of a turn that is over is still kept")
	}

	// A judge whose chat left the idle meanwhile has nothing to say.
	e.edge("o4", termux.StateIdle, 701, true)
	e.edge("o4", termux.StateBusy, 800, true)
	e.judge("o4", termux.Activity{State: termux.StateIdle, Since: 701}, outcomeComplete)
	if o, ok := e.srv.digests.outcomeOf("o4"); ok {
		t.Fatalf("a verdict on an idle the chat left: %#v", o)
	}
}

// A judge that says nothing the outcome can be read as is no verdict, and
// says why.
func TestANonsenseVerdictIsNoVerdict(t *testing.T) {
	e := newOutcomeEnv(t)
	e.row("o5", config.HarnessCodex)
	e.edge("o5", termux.StateBusy, 100, true)
	e.edge("o5", termux.StateIdle, 101, true)
	e.judge("o5", termux.Activity{State: termux.StateIdle, Since: 101}, "splendid")
	o, ok := e.srv.digests.outcomeOf("o5")
	if !ok || o.Phase != digestError || !strings.Contains(o.Error, "splendid") {
		t.Fatalf("the verdict: %#v %v", o, ok)
	}
}

// Switched off, the verdicts go; a restart shows a kept one again only while
// the chat is at the idle it was made for.
func TestOutcomesSwitchedOffAndKeptAcrossARestart(t *testing.T) {
	e := newOutcomeEnv(t)
	for _, id := range []string{"o7", "o8"} {
		e.row(id, config.HarnessCodex)
		e.edge(id, termux.StateBusy, 100, true)
		e.edge(id, termux.StateIdle, 101, true)
		e.judge(id, termux.Activity{State: termux.StateIdle, Since: 101}, outcomeComplete)
	}

	// A process that starts again finds both.
	again := newDigester(e.srv)
	if len(again.outcomes) != 2 || again.outcomes["o7"].Since != 101 {
		t.Fatalf("a restart found %#v", again.outcomes)
	}
	again.outcomeEdge("o7", termux.Activity{State: termux.StateIdle, Since: 101})
	again.outcomeEdge("o8", termux.Activity{State: termux.StateIdle, Since: 555})
	if _, ok := again.outcomeOf("o7"); !ok {
		t.Fatal("the verdict of the idle the chat is still at was dropped")
	}
	if _, ok := again.outcomeOf("o8"); ok {
		t.Fatal("the verdict of an idle the chat left was shown again")
	}

	e.configureOpenRouter(t, map[string]any{"outcomes": false})
	if len(e.srv.digests.outcomesAll()) != 0 {
		t.Fatalf("switched off, the verdicts stay: %#v", e.srv.digests.outcomesAll())
	}
}

// The Status button is told the verdict on the turn the session rests after.
func TestStatusPromptSaysTheVerdict(t *testing.T) {
	words := outcomeWords(outcome{Phase: digestReady, Kind: outcomePartial, Reason: "Docs missing.", Todos: []string{"Write docs"}})
	prompt := statusPrompt(config.HarnessShell, termux.StateIdle, config.LanguageEN, words, "the screen")
	if !strings.Contains(prompt, "this round is done but the task is not (Docs missing.); still open: Write docs") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	if strings.Contains(statusPrompt(config.HarnessShell, termux.StateIdle, config.LanguageEN, "", "the screen"), "check of the turn") {
		t.Fatal("a session with no verdict is said to have one")
	}
}

func TestOutcomeBody(t *testing.T) {
	for _, c := range []struct {
		o    outcome
		body string
	}{
		{outcome{Phase: digestError}, "Finished"},
		{outcome{Phase: digestReady, Kind: outcomeBlocked, Reason: "The build fails."}, "Blocked · The build fails."},
		{outcome{Phase: digestReady, Kind: outcomeNeedsInput, Reason: "Needs a login."}, "Waiting on you · Needs a login."},
		{outcome{Phase: digestReady, Kind: outcomePartial, Reason: "Half."}, "Partly done · Half."},
		{outcome{Phase: digestReady, Kind: outcomePartial, Todos: []string{"Docs", "Tests"}}, "Partly done · 2 open: Docs"},
		{outcome{Phase: digestReady, Kind: outcomeComplete}, "Complete"},
	} {
		if body := outcomeBody(c.o); body != c.body {
			t.Errorf("%#v: %q", c.o, body)
		}
	}
}
