//go:build !windows

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// Digests are tested against the stand in gateway and against screens the
// test hands the digester, because what has to be proven is when a screen is
// read and what becomes of the answer - not that tmux can capture a pane,
// which the Status button's tests already prove against a real one.

// digestEnv is a signed in server with no terminal substrate, a gateway, and
// sessions that are rows the test writes, with screens the test chooses.
type digestEnv struct {
	*testEnv
	t  *testing.T
	gw *scriptedGateway

	mu      sync.Mutex
	screens map[string]string
}

func newDigestEnv(t *testing.T) *digestEnv {
	t.Helper()
	e := &digestEnv{testEnv: signedIn(t), t: t, gw: newGateway(t), screens: map[string]string{}}
	e.configureOpenRouter(t, map[string]any{
		"base_url": e.gw.srv.URL, "api_key": "k", "home_model": "test/home-model",
	})
	read := func(_ context.Context, id string) (string, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		screen, ok := e.screens[id]
		if !ok {
			return "", fmt.Errorf("session %s has no pane to read", id)
		}
		return screen, nil
	}
	e.srv.digests.capture = func(ctx context.Context, id string, _ int) (string, error) { return read(ctx, id) }
	e.srv.digests.visible = read
	return e
}

// session writes a running row of a harness, with a screen.
func (e *digestEnv) session(id, harness, screen string) {
	e.t.Helper()
	if err := e.store.CreateSession(&store.Session{
		ID: id, Title: "Session " + id, Harness: harness, Workdir: e.t.TempDir(),
		TmuxName: "t-" + id, State: store.StateRunning,
	}); err != nil {
		e.t.Fatal(err)
	}
	e.show(id, screen)
}

func (e *digestEnv) show(id, screen string) {
	e.mu.Lock()
	e.screens[id] = screen
	e.mu.Unlock()
}

// commit is the detector committing a change, through the same callback.
func (e *digestEnv) commit(id string, state termux.State) {
	e.srv.onSessionActivity(id, termux.Activity{State: state, Since: time.Now().UnixMilli()})
}

// digestOf waits for a session's digest to satisfy a condition.
func (e *digestEnv) digestOf(id, what string, cond func(digest) bool) digest {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last digest
	var has bool
	for time.Now().Before(deadline) {
		last, has = e.srv.digests.get(id)
		if has && cond(last) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("the digest of %s never became %s; it is %#v (present %v)", id, what, last, has)
	return digest{}
}

func ready(d digest) bool { return d.Phase == digestReady }

// digestCalls is how many digest calls the gateway has seen.
func (e *digestEnv) digestCalls() int { return len(e.gw.digestsSeen()) }

const askedJSON = `{"summary":"Asking which database to use.","context":"Claude Code in shop wants a database.",` +
	`"question":{"kind":"choice","detail":"","items":[{"header":"DB","text":"Which database should we use?",` +
	`"multi":false,"free_text":true,"options":[{"label":"Postgres","description":"Relational, robust"},` +
	`{"label":"SQLite","description":"Embedded file","recommended":true}]}]}}`

// Triggers 1 and 2, and the staleness rule between them: a session that
// starts asking is read at once and its question arrives on the stream; the
// moment it stops asking the question is gone - before anything is asked of
// a model - and a turn that ends is read for its summary and for a question
// asked in prose.
func TestDigestReadsAQuestionAndClearsItWhenItIsGone(t *testing.T) {
	e := newDigestEnv(t)
	e.configureVoice(t, map[string]any{"language": "de"})
	e.session("q1", "claude", "Which database should we use?\n❯ 1. Postgres\n  2. SQLite\n  3. Type something.")
	// The detector has answered for it - a hello waits for that - but no
	// trigger read it: it was found idle.
	e.commit("q1", termux.StateIdle)
	client := e.dialEvents(t)
	client.hello()
	// A page connecting is the one reason to read a session that has not
	// been read at all (trigger 5); that reading is not what is tested here.
	e.digestOf("q1", "read for the hello", ready)
	base := e.digestCalls()

	e.commit("q1", termux.StateBusy)
	time.Sleep(200 * time.Millisecond)
	if n := e.digestCalls() - base; n != 0 {
		t.Fatalf("a session that started working was read %d times", n)
	}

	e.gw.digestScript(askedJSON)
	e.commit("q1", termux.StateWaiting)
	asked := e.digestOf("q1", "a question", func(d digest) bool { return ready(d) && d.Question != nil })
	if asked.State != "waiting" || asked.Model != "test/home-model" || asked.MadeAt == 0 {
		t.Fatalf("digest = %#v", asked)
	}
	if asked.Summary != "Asking which database to use." || asked.Context == "" {
		t.Fatalf("digest = %#v", asked)
	}
	q := asked.Question
	if q.Source != questionMenu || q.Kind != "choice" || len(q.Items) != 1 || len(q.Items[0].Options) != 2 ||
		q.Items[0].Options[1].Label != "SQLite" || !q.Items[0].Options[1].Recommended || !q.Items[0].FreeText {
		t.Fatalf("question = %#v", q)
	}
	client.frameWhere("the question on the stream", "digest", 10*time.Second, func(f map[string]any) bool {
		d, _ := f["digest"].(map[string]any)
		return f["id"] == "q1" && d != nil && d["phase"] == digestReady && d["question"] != nil
	})

	calls := e.gw.digestsSeen()[base:]
	if len(calls) != 1 {
		t.Fatalf("the gateway saw %d digest calls, want one", len(calls))
	}
	call := calls[0]
	if call.Model != "test/home-model" || call.MaxTokens != digestMaxTokens ||
		call.Temperature == nil || *call.Temperature != 0 {
		t.Fatalf("the request was %#v", call)
	}
	prompt := call.joined()
	for _, want := range []string{"Which database should we use?", "Claude Code", "Session q1",
		"waiting for the user", "German", `"question"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the digest prompt has no %q:\n%s", want, prompt)
		}
	}
	if e.gw.count() != 0 {
		t.Fatalf("a digest was counted as an operator call")
	}

	// Answered in the terminal: the question goes at once, before anything
	// is asked of a model - the very next frame is the same digest without
	// it - and then the session is read again, because the line that said
	// what it asked is history now.
	before := asked.Rev
	e.commit("q1", termux.StateBusy)
	client.frameWhere("the cleared question on the stream", "digest", 10*time.Second, func(f map[string]any) bool {
		d, _ := f["digest"].(map[string]any)
		if f["id"] != "q1" || d == nil || d["rev"] != float64(before+1) {
			return false
		}
		if d["question"] != nil || d["phase"] != digestReady || d["summary"] != asked.Summary {
			t.Fatalf("the first frame after the question went is %#v", d)
		}
		return true
	})
	reread := e.digestOf("q1", "read again", func(d digest) bool { return ready(d) && d.Rev > before+1 })
	if reread.Question != nil || reread.State != "busy" {
		t.Fatalf("reread = %#v", reread)
	}
	if n := e.digestCalls() - base; n != 2 {
		t.Fatalf("the gateway saw %d digest calls, want two", n)
	}

	// The turn ends with a question in prose.
	e.gw.digestScript(`{"summary":"Done; asks whether to push.","context":"It finished the migration.",` +
		`"question":{"kind":"text","items":[{"text":"Shall I push the branch?","options":[]}]}}`)
	e.show("q1", "All done. Shall I push the branch?\n> ")
	e.commit("q1", termux.StateIdle)
	prose := e.digestOf("q1", "a question in prose", func(d digest) bool {
		return ready(d) && d.Question != nil && d.State == "idle"
	})
	if prose.Question.Source != questionText || prose.Question.Kind != "text" || !prose.Question.Items[0].FreeText {
		t.Fatalf("question = %#v", prose.Question)
	}
	if n := e.digestCalls() - base; n != 3 {
		t.Fatalf("the gateway saw %d digest calls, want three", n)
	}
}

// A question is only kept in a state that asks one: a working session's
// screen may well show a question it has already been answered.
func TestDigestKeepsNoQuestionWhileWorking(t *testing.T) {
	e := newDigestEnv(t)
	e.session("w1", "codex", "Working (12s)")
	e.commit("w1", termux.StateBusy)
	e.gw.digestAlways(askedJSON)

	res, payload := e.do(t, e.client, "POST", "/api/sessions/w1/digest", "")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("digest now: %d %#v", res.StatusCode, payload)
	}
	if d, _ := payload["digest"].(map[string]any); d == nil || d["phase"] != digestPending {
		t.Fatalf("digest now answered %#v", payload)
	}
	d := e.digestOf("w1", "ready", ready)
	if d.Question != nil || d.State != "busy" || d.Summary == "" {
		t.Fatalf("digest = %#v", d)
	}

	// Tools have no digest, and neither has a session that is not running.
	if err := e.store.CreateSession(&store.Session{ID: "sh", Title: "Shell", Harness: "shell",
		Workdir: t.TempDir(), TmuxName: "t-sh", State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.do(t, e.client, "POST", "/api/sessions/sh/digest", ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a shell's digest = %d", res.StatusCode)
	}
	if err := e.store.CreateSession(&store.Session{ID: "ex", Title: "Gone", Harness: "claude",
		Workdir: t.TempDir(), TmuxName: "t-ex", State: store.StateExited}); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.do(t, e.client, "POST", "/api/sessions/ex/digest", ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an exited session's digest = %d", res.StatusCode)
	}
	e.commit("sh", termux.StateWaiting)
	time.Sleep(200 * time.Millisecond)
	if _, has := e.srv.digests.get("sh"); has {
		t.Fatal("a shell was digested")
	}
}

// With no key nothing is asked, and the digest says where to put one - once,
// not on every look.
func TestDigestWithoutAKey(t *testing.T) {
	e := newDigestEnv(t)
	e.configureOpenRouter(t, map[string]any{"api_key": ""})
	e.session("k1", "claude", "Do you want to proceed?")
	client := e.dialEvents(t)
	client.hello()

	e.commit("k1", termux.StateWaiting)
	d := e.digestOf("k1", "an error", func(d digest) bool { return d.Phase == digestError })
	if d.Error != digestNoKey {
		t.Fatalf("error = %q", d.Error)
	}
	res, payload := e.do(t, e.client, "POST", "/api/sessions/k1/digest", "")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("digest now: %d", res.StatusCode)
	}
	if again, _ := payload["digest"].(map[string]any); again["error"] != digestNoKey || again["rev"] != float64(d.Rev) {
		t.Fatalf("the second answer was %#v, want the same error at the same rev", payload)
	}
	e.srv.digests.lookAround(context.Background())
	if n := e.digestCalls(); n != 0 {
		t.Fatalf("the gateway saw %d calls without a key", n)
	}
}

// A model that fails keeps the last summary - the last true line about a
// session is a better line than none - and says why in words.
func TestDigestFailureKeepsTheSummary(t *testing.T) {
	e := newDigestEnv(t)
	// Two reads asked for one after the other are what this test is about;
	// the gap between them is TestDigestReadAgainIsNotAStorm's.
	e.srv.digests.setManualGap(0)
	e.session("f1", "opencode", "Thinking…")
	e.gw.digestAlways(`{"summary":"Refactoring the S3 mirror.","context":"","question":null}`)
	e.commit("f1", termux.StateBusy)
	e.commit("f1", termux.StateIdle)
	good := e.digestOf("f1", "ready", ready)

	e.gw.digestRefuse(http.StatusNotFound, "test/home-model is not a valid model ID")
	if res, _ := e.do(t, e.client, "POST", "/api/sessions/f1/digest", ""); res.StatusCode != http.StatusAccepted {
		t.Fatalf("digest now: %d", res.StatusCode)
	}
	failed := e.digestOf("f1", "an error", func(d digest) bool { return d.Phase == digestError })
	if !strings.Contains(failed.Error, "unknown model test/home-model") || !strings.Contains(failed.Error, "/admin") {
		t.Fatalf("error = %q", failed.Error)
	}
	if failed.Summary != good.Summary || failed.MadeAt != good.MadeAt {
		t.Fatalf("the failure lost the summary: %#v", failed)
	}

	// Prose twice is an error too, with the complaint sent in between.
	e.gw.digestRefuse(0, "")
	e.gw.digestScript("It is refactoring things.", "Still prose.")
	e.do(t, e.client, "POST", "/api/sessions/f1/digest", "")
	prose := e.digestOf("f1", "a JSON error", func(d digest) bool {
		return d.Phase == digestError && d.Error == errDigestJSON.Error()
	})
	if prose.Summary != good.Summary {
		t.Fatalf("summary = %q", prose.Summary)
	}
	calls := e.gw.digestsSeen()
	if last := calls[len(calls)-1]; !strings.Contains(last.joined(), "not usable JSON") {
		t.Fatalf("the retry did not carry the complaint:\n%s", last.joined())
	}
}

// Deleting a session forgets its digest, stops its call, and tells every page.
func TestDigestIsCancelledWhenTheSessionIsDeleted(t *testing.T) {
	e := newDigestEnv(t)
	e.session("d1", "claude", "Do you want to proceed?")
	client := e.dialEvents(t)
	client.hello()
	e.gw.digestBlock()
	defer e.gw.digestRelease()

	e.commit("d1", termux.StateWaiting)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if live, _ := e.gw.digestState(); live == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the digest never reached the gateway")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if res, payload := e.do(t, e.client, "DELETE", "/api/sessions/d1", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %#v", res.StatusCode, payload)
	}
	client.frameWhere("the digest gone", "digest", 10*time.Second, func(f map[string]any) bool {
		return f["id"] == "d1" && f["digest"] == nil
	})
	deadline = time.Now().Add(10 * time.Second)
	for {
		if live, _ := e.gw.digestState(); live == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the call of a deleted session was not cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, has := e.srv.digests.get("d1"); has {
		t.Fatal("a deleted session still has a digest")
	}
}

// Three calls at most, for every session together.
func TestDigestCallsShareThreeSlots(t *testing.T) {
	e := newDigestEnv(t)
	for i := range 5 {
		e.session(fmt.Sprintf("s%d", i), "claude", fmt.Sprintf("Question %d?", i))
	}
	e.gw.digestBlock()
	for i := range 5 {
		e.commit(fmt.Sprintf("s%d", i), termux.StateWaiting)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if live, _ := e.gw.digestState(); live == digestConcurrency {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("three digests never ran at once")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if live, peak := e.gw.digestState(); live != digestConcurrency || peak != digestConcurrency {
		t.Fatalf("%d calls in flight, %d at most; want %d", live, peak, digestConcurrency)
	}
	e.gw.digestRelease()
	for i := range 5 {
		e.digestOf(fmt.Sprintf("s%d", i), "ready", ready)
	}
	if _, peak := e.gw.digestState(); peak != digestConcurrency {
		t.Fatalf("%d calls ran at once", peak)
	}
	if n := e.digestCalls(); n != 5 {
		t.Fatalf("the gateway saw %d digest calls, want five", n)
	}
}

// Trigger 3: a waiting screen that moved on is read again - once it has been
// the same on two looks, and not while it is the screen the digest was made
// from.
func TestDigestReadsAWaitingScreenThatMovedOn(t *testing.T) {
	e := newDigestEnv(t)
	e.session("m1", "claude", "Question one?\n❯ 1. Yes")
	e.gw.digestAlways(askedJSON)
	e.commit("m1", termux.StateWaiting)
	e.digestOf("m1", "ready", ready)
	ctx := context.Background()

	e.srv.digests.lookAround(ctx)
	e.srv.digests.lookAround(ctx)
	if n := e.digestCalls(); n != 1 {
		t.Fatalf("an unchanged screen was read again: %d calls", n)
	}
	e.show("m1", "Question two?\n❯ 1. No")
	e.srv.digests.lookAround(ctx)
	if n := e.digestCalls(); n != 1 {
		t.Fatalf("a screen seen once was read at once: %d calls", n)
	}
	e.srv.digests.lookAround(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for e.digestCalls() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("a screen that moved on was never read again: %d calls", e.digestCalls())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if last := e.gw.digestsSeen()[1]; !strings.Contains(last.joined(), "Question two?") {
		t.Fatalf("the second digest read:\n%s", last.joined())
	}
}

// Trigger 4: a working session's summary is kept fresh while somebody is
// looking, and only then; and trigger 5: a page that connects has every
// running coding session that has no line yet read for it.
func TestDigestKeepsWorkingSummariesFreshForHome(t *testing.T) {
	e := newDigestEnv(t)
	e.srv.digests.freshFor = 0
	e.session("b1", "claude", "Working (1s)")
	e.session("b2", "codex", "Working (1s)")
	if err := e.store.CreateSession(&store.Session{ID: "tool", Title: "Shell", Harness: "shell",
		Workdir: t.TempDir(), TmuxName: "t-tool", State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	e.commit("b1", termux.StateBusy)
	e.commit("b2", termux.StateBusy)
	ctx := context.Background()

	e.srv.digests.lookAround(ctx)
	if n := e.digestCalls(); n != 0 {
		t.Fatalf("working sessions were read with nobody looking: %d calls", n)
	}

	client := e.dialEvents(t)
	client.hello()
	e.digestOf("b1", "ready", ready)
	e.digestOf("b2", "ready", ready)
	if _, has := e.srv.digests.get("tool"); has {
		t.Fatal("a hello read a tool")
	}
	if n := e.digestCalls(); n != 2 {
		t.Fatalf("a hello made %d digest calls, want two", n)
	}

	e.srv.digests.lookAround(ctx)
	time.Sleep(100 * time.Millisecond)
	if n := e.digestCalls(); n != 2 {
		t.Fatalf("unchanged screens were read again: %d calls", n)
	}
	e.show("b1", "Working (61s) - running the tests")
	e.srv.digests.lookAround(ctx)
	e.digestOf("b1", "read again", func(d digest) bool { return ready(d) && e.digestCalls() == 3 })
}

// Everything a model writes is held to what a dialog on a car's screen can
// show.
func TestSanitizeQuestion(t *testing.T) {
	long := strings.Repeat("ä", 400)
	q := &digestQuestion{Kind: "Something", Detail: strings.Repeat("d", 2000)}
	for i := range 6 {
		item := digestItem{Header: fmt.Sprintf("H%d", i), Text: long + long + long, FreeText: i == 2}
		if i != 2 {
			for j := range 15 {
				item.Options = append(item.Options, digestOption{Label: fmt.Sprintf("Option %d", j), Description: long})
			}
			item.Options = append(item.Options, digestOption{Label: "  "}, digestOption{Label: "Option 0"})
		}
		q.Items = append(q.Items, item)
	}
	q.Items[1].Options = []digestOption{{Label: " "}}

	got := sanitizeQuestion(q, termux.StateWaiting)
	if got == nil || got.Kind != "choice" || got.Source != questionMenu {
		t.Fatalf("question = %#v", got)
	}
	if len([]rune(got.Detail)) != digestTextRunes {
		t.Fatalf("detail is %d runes", len([]rune(got.Detail)))
	}
	// Item 1 had nothing to choose and no room for an answer of one's own.
	if len(got.Items) != digestItemsMax || got.Items[1].Header != "H2" {
		t.Fatalf("items = %d, the second is %q", len(got.Items), got.Items[1].Header)
	}
	first := got.Items[0]
	if len(first.Options) != digestOptionsMax || len([]rune(first.Text)) != digestTextRunes ||
		len([]rune(first.Options[0].Description)) != digestDescriptionRunes {
		t.Fatalf("first item = %d options, %d runes of text", len(first.Options), len([]rune(first.Text)))
	}
	if got.Items[1].Options != nil || !got.Items[1].FreeText {
		t.Fatalf("a free text item = %#v", got.Items[1])
	}
	label := sanitizeQuestion(&digestQuestion{Kind: "choice", Items: []digestItem{{
		Text: "?", Options: []digestOption{{Label: strings.Repeat("l", 300) + "\nsecond line"}},
	}}}, termux.StateIdle)
	if l := label.Items[0].Options[0].Label; len([]rune(l)) != digestLabelRunes || strings.Contains(l, "\n") {
		t.Fatalf("label = %q", l)
	}
	if label.Source != questionText {
		t.Fatalf("source = %q", label.Source)
	}

	for _, state := range []termux.State{termux.StateBusy, termux.StateTasks, termux.StateUnknown} {
		if got := sanitizeQuestion(q, state); got != nil {
			t.Fatalf("a %s session kept a question", state)
		}
	}
	if got := sanitizeQuestion(&digestQuestion{Kind: "choice", Items: []digestItem{{Text: "nothing to pick"}}},
		termux.StateWaiting); got != nil {
		t.Fatalf("a question with no answer to give = %#v", got)
	}
	if got := sanitizeQuestion(&digestQuestion{Kind: "text", Items: []digestItem{{Text: "Why?"}}},
		termux.StateIdle); got == nil || !got.Items[0].FreeText {
		t.Fatalf("a question in prose = %#v", got)
	}
	if s := oneLine("  one\n two   three ", 160); s != "one two three" {
		t.Fatalf("oneLine = %q", s)
	}
	if s := oneLine(long, digestSummaryRunes); len([]rune(s)) != digestSummaryRunes || !strings.HasSuffix(s, "…") {
		t.Fatalf("a long summary is %d runes", len([]rune(s)))
	}
}

// writeTranscript writes a Claude Code transcript where the resume check
// finds one.
func writeTranscript(t *testing.T, configDir, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(configDir, "projects", "-work-shop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func askLine(id, header string) string {
	input, _ := json.Marshal(map[string]any{"questions": []any{map[string]any{
		"header": header, "question": "Which " + header + "?", "multiSelect": false,
		"options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}},
	}}})
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"asking"},`+
		`{"type":"tool_use","id":%q,"name":"AskUserQuestion","input":%s}]}}`, id, input)
}

func resultLine(id string) string {
	return fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`, id)
}

// A Claude Code question with several tabs shows one on the screen, and the
// transcript has all of them: the pending AskUserQuestion is the last one
// with no result, and anything wrong on the way is no record at all.
func TestClaudePendingAsk(t *testing.T) {
	dir := t.TempDir()
	cli := "6f1d2c1e-0000-4000-8000-000000000001"
	writeTranscript(t, dir, cli,
		`{"type":"user","message":{"content":"a plain prompt"}}`,
		askLine("toolu_1", "Old"),
		resultLine("toolu_1"),
		"not json at all",
		askLine("toolu_2", "Color"),
	)
	got := claudePendingAsk(dir, cli)
	if !strings.Contains(string(got), `"header":"Color"`) || strings.Contains(string(got), "Old") {
		t.Fatalf("pending = %s", got)
	}

	writeTranscript(t, dir, cli, askLine("toolu_2", "Color"), resultLine("toolu_2"))
	if got := claudePendingAsk(dir, cli); got != nil {
		t.Fatalf("an answered question is pending: %s", got)
	}
	for _, id := range []string{"", "../escape", "missing-id"} {
		if got := claudePendingAsk(dir, id); got != nil {
			t.Fatalf("%q found %s", id, got)
		}
	}

	// A prompt of the person's after it - the program was killed while it
	// asked, and resumed - is a question left behind, never answered.
	for _, prompt := range []string{
		`{"type":"user","message":{"content":"never mind, fix the tests"}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"never mind"}]}}`,
	} {
		writeTranscript(t, dir, cli, askLine("toolu_3", "Color"), prompt,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}`)
		if got := claudePendingAsk(dir, cli); got != nil {
			t.Fatalf("a question left behind by %s is pending: %s", prompt, got)
		}
	}
	// The result of another tool called beside it is no answer to it.
	writeTranscript(t, dir, cli, askLine("toolu_4", "Color"), resultLine("toolu_other"))
	if got := claudePendingAsk(dir, cli); !strings.Contains(string(got), `"header":"Color"`) {
		t.Fatalf("a question with another tool's result after it = %s", got)
	}
}

// The record is handed to the model only for the question on the screen: a
// session that left its question behind - killed while it asked and started
// again, at its prompt or at a permission prompt - is not asking it.
func TestDigestHandsOnlyARecordOfTheQuestionOnTheScreen(t *testing.T) {
	e := newDigestEnv(t)
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	cli := "6f1d2c1e-0000-4000-8000-000000000003"
	writeTranscript(t, config, cli, askLine("toolu_9", "Database"))
	for i, c := range []struct {
		screen string
		state  termux.State
		want   bool
	}{
		{"Done.\n\n❯ \n", termux.StateIdle, false},
		{" Do you want to proceed?\n ❯ 1. Allow\n   2. Deny\n", termux.StateWaiting, false},
		{"☐ Database\nWhich Database?\n❯ 1. A\n  2. B\n", termux.StateWaiting, true},
	} {
		id := fmt.Sprintf("r%d", i)
		e.session(id, "claude", c.screen)
		if err := e.store.SetSessionCLI(id, cli, store.CLIKnown); err != nil {
			t.Fatal(err)
		}
		seen := len(e.gw.digestsSeen())
		e.commit(id, c.state)
		e.srv.digests.requestNow(e.row(id))
		e.digestOf(id, "ready", ready)
		prompt := e.gw.digestsSeen()[seen].joined()
		if got := strings.Contains(prompt, "authoritative"); got != c.want {
			t.Fatalf("%s (%s): the record was handed over: %v, want %v\n%s", c.screen, c.state, got, c.want, prompt)
		}
	}
}

// And the record reaches the model, for a Claude Code session whose transcript
// is where the resume check looks.
func TestDigestHandsTheModelClaudesOwnRecord(t *testing.T) {
	e := newDigestEnv(t)
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	cli := "6f1d2c1e-0000-4000-8000-000000000002"
	writeTranscript(t, config, cli, askLine("toolu_9", "Features"))
	e.session("c1", "claude", "☐ Features\nWhich Features?\n❯ 1. A")
	if err := e.store.SetSessionCLI("c1", cli, store.CLIKnown); err != nil {
		t.Fatal(err)
	}
	e.commit("c1", termux.StateWaiting)
	e.digestOf("c1", "ready", ready)
	if prompt := e.gw.digestsSeen()[0].joined(); !strings.Contains(prompt, "authoritative") ||
		!strings.Contains(prompt, `"header":"Features"`) {
		t.Fatalf("the model was not given the record:\n%s", prompt)
	}
}

/* --------------------------------------------- what an answer is checked against */

// answerOf posts an answer to item 0 for a digest, as the dialog does.
func (e *digestEnv) answerOf(id string, rev int64, label string) (*http.Response, map[string]any) {
	e.t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"digest_rev": rev,
		"answers":    []any{map[string]any{"item": 0, "choices": []string{label}}},
	})
	return e.do(e.t, e.client, "POST", "/api/sessions/"+id+"/answer", string(raw))
}

// waitingDigest is a session waiting on a prompt that has been read into a
// digest with its question.
func (e *digestEnv) waitingDigest(id, screen string) digest {
	e.t.Helper()
	e.session(id, "claude", screen)
	e.gw.digestAlways(askedJSON)
	e.commit(id, termux.StateWaiting)
	return e.digestOf(id, "a question", func(d digest) bool { return ready(d) && d.Question != nil })
}

// A re-read that fails for a screen that moved on does not keep the old
// question with buttons that answer it, and an answer to it is refused.
func TestDigestFailedReReadDropsTheOldQuestion(t *testing.T) {
	e := newDigestEnv(t)
	first := e.waitingDigest("m1", "Question one?\n❯ 1. Postgres\n  2. SQLite")

	e.gw.digestRefuse(http.StatusInternalServerError, "upstream down")
	e.show("m1", "Question two?\n❯ 1. Red\n  2. Blue")
	ctx := context.Background()
	e.srv.digests.lookAround(ctx)
	e.srv.digests.lookAround(ctx)
	failed := e.digestOf("m1", "a failed re-read", func(d digest) bool { return d.Phase == digestError })
	if failed.Question != nil || failed.Summary != first.Summary {
		t.Fatalf("after a failed re-read of a new screen the digest is %#v", failed)
	}

	// On the same screen a failure keeps the question it was read from, but
	// it is not answered while the read that failed stands.
	e.gw.digestRefuse(0, "")
	e.gw.digestAlways(askedJSON)
	e.srv.digests.requestNow(e.row("m1"))
	again := e.digestOf("m1", "read again", func(d digest) bool { return ready(d) && d.Question != nil })
	e.gw.digestRefuse(http.StatusInternalServerError, "upstream down")
	e.srv.digests.setManualGap(0)
	e.srv.digests.requestNow(e.row("m1"))
	kept := e.digestOf("m1", "an error on the same screen", func(d digest) bool {
		return d.Phase == digestError && d.Rev > again.Rev
	})
	if kept.Question == nil {
		t.Fatalf("a failure on the very screen the question came from lost it: %#v", kept)
	}
	res, payload := e.answerOf("m1", kept.Rev, "Postgres")
	if message, _ := payload["error"].(string); res.StatusCode != http.StatusConflict || !strings.Contains(message, "could not be read again") {
		t.Fatalf("an answer to a question whose re-read failed = %d %#v", res.StatusCode, payload)
	}
}

// A prompt that moved on in place - the state never leaving waiting - is read
// again when somebody answers the one before, instead of being answered with
// its options.
func TestAnswerToAQuestionThatChangedInPlaceIsRefused(t *testing.T) {
	e := newDigestEnv(t)
	first := e.waitingDigest("p1", "Which database should we use?\n❯ 1. Postgres\n  2. SQLite")
	e.show("p1", "Which features?\n❯ 1. [ ] Search\n  2. [ ] Export")

	res, payload := e.answerOf("p1", first.Rev, "Postgres")
	if res.StatusCode != http.StatusConflict || payload["error"] != "the question has changed" {
		t.Fatalf("an answer to the screen before = %d %#v", res.StatusCode, payload)
	}
	shown, _ := payload["digest"].(map[string]any)
	if shown == nil || shown["phase"] != digestPending || shown["rev"].(float64) <= float64(first.Rev) {
		t.Fatalf("the refusal did not start a new read: %#v", payload)
	}
	e.digestOf("p1", "read again", func(d digest) bool { return ready(d) && d.Rev > first.Rev })
	if calls := e.gw.count(); calls != 0 {
		t.Fatalf("the operator was asked %d times", calls)
	}

	// And a screen that is the same prompt in a pane of another height is the
	// same screen.
	if screenHash("\n\nDo you want to proceed?\n❯ 1. Yes   \n\n\n") != screenHash("Do you want to proceed?\n❯ 1. Yes") {
		t.Fatal("blank lines around a prompt changed its hash")
	}
}

// The rev does not say that a session without a question stopped asking, so
// the state does: an answer to a session that is working, or that went from
// waiting straight to idle, is refused.
func TestAnswerToASessionThatStoppedAskingIsRefused(t *testing.T) {
	e := newDigestEnv(t)
	e.session("s1", "claude", "Something odd is on the screen")
	e.commit("s1", termux.StateWaiting)
	read := e.digestOf("s1", "read without a question", func(d digest) bool { return ready(d) && d.Question == nil })
	raw := fmt.Sprintf(`{"digest_rev":%d,"text":"hello"}`, read.Rev)

	// The state as the detector committed it, with the digest exactly as it
	// was: the moment between a commit and anything reading the screen again.
	for _, state := range []termux.State{termux.StateBusy, termux.StateIdle, termux.StateTasks} {
		e.srv.digests.mu.Lock()
		e.srv.digests.seen["s1"] = termux.Activity{State: state}
		e.srv.digests.mu.Unlock()
		res, payload := e.do(t, e.client, "POST", "/api/sessions/s1/answer", raw)
		if message, _ := payload["error"].(string); res.StatusCode != http.StatusConflict || !strings.Contains(message, "no longer asking") {
			t.Fatalf("an answer to a session that is %s = %d %#v", state, res.StatusCode, payload)
		}
	}
	if calls := e.gw.count(); calls != 0 {
		t.Fatalf("the operator was asked %d times", calls)
	}
}

// A waiting session whose screen could not be read - on its very first read,
// or after a digest made while it was working - can still be answered in
// words of one's own: the failed read records the state it was made in, so
// the answer is not refused as one to a session that stopped asking.
func TestAnswerOfOnesOwnToAWaitingSessionWhoseReadFailed(t *testing.T) {
	e := newDigestEnv(t)
	e.gw.block()
	defer e.gw.release()
	e.gw.always(`{"actions":[{"wait_ms":10}],"done":false}`)

	for _, before := range []termux.State{termux.StateUnknown, termux.StateBusy} {
		id := "w-" + string(before)
		e.session(id, "claude", "Working (1s)")
		if before == termux.StateBusy {
			e.gw.digestAlways(`{"summary":"Refactoring the S3 mirror.","context":"","question":null}`)
			e.commit(id, termux.StateBusy)
			e.srv.digests.request(e.row(id))
			e.digestOf(id, "ready while working", func(d digest) bool { return ready(d) && d.State == string(termux.StateBusy) })
		}
		e.gw.digestRefuse(http.StatusInternalServerError, "upstream down")
		e.show(id, "Something odd is on the screen")
		e.commit(id, termux.StateWaiting)
		failed := e.digestOf(id, "a failed read", func(d digest) bool { return d.Phase == digestError })
		if failed.State != string(termux.StateWaiting) || failed.Question != nil {
			t.Fatalf("%s: a failed read of a waiting session is %#v", before, failed)
		}
		raw := fmt.Sprintf(`{"digest_rev":%d,"text":"use the second one"}`, failed.Rev)
		res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/answer", raw)
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: an answer of one's own after a failed read = %d %#v", before, res.StatusCode, payload)
		}
		// The run may have ended already: this server has no pane for it to
		// read, which is the run's business and not this test's.
		if res, payload := e.do(t, e.client, "POST", "/api/sessions/"+id+"/answer/cancel", ""); res.StatusCode != http.StatusOK {
			t.Fatalf("%s: cancel = %d %#v", before, res.StatusCode, payload)
		}
		e.gw.digestRefuse(0, "")
	}
}

// A session that stops asking straight into idle - Esc, a refused permission
// - is read again: nothing else would ever replace a summary that says what
// it asked.
func TestDigestReadsASessionThatStoppedAsking(t *testing.T) {
	e := newDigestEnv(t)
	e.waitingDigest("e1", "Which database should we use?\n❯ 1. Postgres")
	base := e.digestCalls()
	e.gw.digestAlways(`{"summary":"At its prompt; the question was dismissed.","context":"","question":null}`)
	e.show("e1", "Interrupted by user\n> ")
	e.commit("e1", termux.StateIdle)
	d := e.digestOf("e1", "read again", func(d digest) bool {
		return ready(d) && d.State == "idle" && d.Summary == "At its prompt; the question was dismissed."
	})
	if d.Question != nil || e.digestCalls() != base+1 {
		t.Fatalf("digest = %#v after %d calls", d, e.digestCalls()-base)
	}
}

// A run that typed answered the question its digest holds: it is taken out
// at once, at a new rev, and the screen is read again for what came after.
func TestDigestForgetsTheQuestionARunAnswered(t *testing.T) {
	e := newDigestEnv(t)
	asked := e.waitingDigest("r1", "Do you want to proceed?\n❯ 1. Yes\n  2. No")
	e.gw.digestBlock()
	defer e.gw.digestRelease()
	e.srv.digests.answered("r1", time.Now())
	d := e.digestOf("r1", "without the question", func(d digest) bool { return d.Question == nil })
	if d.Rev <= asked.Rev {
		t.Fatalf("the rev did not move: %#v", d)
	}
	e.digestOf("r1", "being read again", func(d digest) bool { return d.Phase == digestPending })
	res, payload := e.answerOf("r1", asked.Rev, "Yes")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a second answer to the rev a run answered = %d %#v", res.StatusCode, payload)
	}
}

// A model that keeps failing is not asked again on every look at a working
// screen whose clock ticks: the attempt, not the success, is what is fresh.
func TestDigestFailuresDoNotStorm(t *testing.T) {
	e := newDigestEnv(t)
	e.gw.digestRefuse(http.StatusNotFound, "no such model")
	e.session("b1", "claude", "Working (1s)")
	e.commit("b1", termux.StateBusy)
	e.dialEvents(t).hello()
	e.digestOf("b1", "an error", func(d digest) bool { return d.Phase == digestError })
	calls := e.digestCalls()
	ctx := context.Background()
	for i := range 5 {
		e.show("b1", fmt.Sprintf("Working (%ds)", 5+4*i))
		e.srv.digests.lookAround(ctx)
	}
	time.Sleep(200 * time.Millisecond)
	if n := e.digestCalls(); n != calls {
		t.Fatalf("a failing model was asked %d more times by five looks", n-calls)
	}

	// A waiting screen backs off too, doubling from one look.
	if after := e.srv.digests.retryAfter(1); after != 2*defaultDigestLookEvery {
		t.Fatalf("one failure waits %s", after)
	}
	if after := e.srv.digests.retryAfter(30); after != e.srv.digests.freshFor {
		t.Fatalf("many failures wait %s, want the cap %s", after, e.srv.digests.freshFor)
	}
}

// "Try again" while a read is on its way is that read, and a second tap
// within the gap is the read the first one started.
func TestDigestReadAgainIsNotAStorm(t *testing.T) {
	e := newDigestEnv(t)
	e.session("t1", "claude", "Working (1s)")
	e.commit("t1", termux.StateBusy)
	e.gw.digestBlock()
	res, first := e.do(t, e.client, "POST", "/api/sessions/t1/digest", "")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("digest now: %d", res.StatusCode)
	}
	for range 5 {
		_, again := e.do(t, e.client, "POST", "/api/sessions/t1/digest", "")
		if again["digest"].(map[string]any)["rev"] != first["digest"].(map[string]any)["rev"] {
			t.Fatalf("a read on its way was started again: %#v", again)
		}
	}
	e.gw.digestRelease()
	e.digestOf("t1", "ready", ready)
	e.do(t, e.client, "POST", "/api/sessions/t1/digest", "")
	time.Sleep(200 * time.Millisecond)
	if n := e.digestCalls(); n != 1 {
		t.Fatalf("six taps made %d calls", n)
	}
}

// A request that read its row before the session was deleted does not bring
// the deleted session's digest back.
func TestDigestOfADeletedSessionStaysGone(t *testing.T) {
	e := newDigestEnv(t)
	e.session("g1", "claude", "Do you want to proceed?")
	row := e.row("g1")
	if err := e.srv.deleteSession(context.Background(), "g1"); err != nil {
		t.Fatal(err)
	}
	e.srv.digests.request(row)
	e.srv.digests.observe("g1", termux.Activity{State: termux.StateWaiting})
	time.Sleep(100 * time.Millisecond)
	if _, has := e.srv.digests.get("g1"); has {
		t.Fatal("a deleted session has a digest again")
	}
	if _, has := e.srv.digests.all()["g1"]; has {
		t.Fatal("the hello carries a deleted session's digest")
	}
}

// Revs start from the clock, so that a dialog holding a rev from before a
// restart never matches a different question after it.
// A page that was open across a restart reconnects at once, and its hello
// asks for every session's digest while the detector has no state for any of
// them yet. Read then, a question asked in prose would be read in no state -
// and a question in prose is only a question while idle - and nothing would
// read it again: so the read waits for the session's first state. A digest
// that was read in no state anyway (asked for in that first second) is read
// again on the first state too.
func TestDigestAfterARestartWaitsForAState(t *testing.T) {
	e := newDigestEnv(t)
	e.gw.digestAlways(textQuestionJSON)
	e.session("r1", "claude", "● Which color should the button be? Red, green or blue?\n❯ \n")
	e.srv.digests.fillMissing()
	time.Sleep(300 * time.Millisecond)
	if n := e.digestCalls(); n != 0 {
		t.Fatalf("a session in no state was read %d times", n)
	}
	e.commit("r1", termux.StateIdle)
	d := e.digestOf("r1", "read once idle", func(d digest) bool { return ready(d) && d.State == string(termux.StateIdle) })
	if d.Question == nil || d.Question.Source != questionText {
		t.Fatalf("the question in prose was not read: %#v", d.Question)
	}

	// Read in no state: once there is one, it is read again.
	e.session("r2", "claude", "● Which color should the button be? Red, green or blue?\n❯ \n")
	e.srv.digests.request(e.row("r2"))
	blind := e.digestOf("r2", "read in no state", ready)
	if blind.State != string(termux.StateUnknown) || blind.Question != nil {
		t.Fatalf("a read in no state is %#v", blind)
	}
	e.commit("r2", termux.StateIdle)
	again := e.digestOf("r2", "read again once idle", func(d digest) bool { return ready(d) && d.Rev > blind.Rev && d.State == "idle" })
	if again.Question == nil || again.Question.Source != questionText {
		t.Fatalf("the question in prose was not read again: %#v", again)
	}
	// And only once: the next commit of the same state reads nothing.
	calls := e.digestCalls()
	e.commit("r2", termux.StateIdle)
	time.Sleep(300 * time.Millisecond)
	if n := e.digestCalls(); n != calls {
		t.Fatalf("an idle session was read again on a repeat of its state: %d calls, was %d", n, calls)
	}
}

// Digests do not outlive a restart, and a session that sits idle on a
// question in prose has no edge to be read again on: it is read once the
// digester starts, whether or not a page ever connects - or the asking set,
// and the app's icon with it, would leave it out until one did.
func TestDigestReadsAnIdleSessionAfterARestart(t *testing.T) {
	e := newDigestEnv(t)
	e.gw.digestAlways(textQuestionJSON)
	e.session("i1", "claude", "● Which color should the button be? Red, green or blue?\n❯ \n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.srv.digests.start(ctx)
	time.Sleep(300 * time.Millisecond)
	if n := e.digestCalls(); n != 0 {
		t.Fatalf("a session in no state was read %d times", n)
	}
	// The detector's first answer after the restart: idle, from nothing.
	e.commit("i1", termux.StateIdle)
	d := e.digestOf("i1", "read on its first state", func(d digest) bool { return ready(d) && d.State == string(termux.StateIdle) })
	if d.Question == nil || d.Question.Source != questionText {
		t.Fatalf("the question in prose was not read: %#v", d.Question)
	}
	if ids := e.srv.askingSet(); len(ids) != 1 || ids[0] != "i1" {
		t.Fatalf("asking set = %v, want the idle question", ids)
	}
}

func TestDigestRevsOutliveARestart(t *testing.T) {
	e := newDigestEnv(t)
	d := e.waitingDigest("v1", "Do you want to proceed?")
	if d.Rev < time.Now().Add(-time.Hour).UnixMicro() {
		t.Fatalf("rev = %d, want one from the clock", d.Rev)
	}
	time.Sleep(time.Millisecond)
	later := newDigester(e.srv)
	if later.epoch <= d.Rev {
		t.Fatalf("a later process starts at %d, below the rev %d of this one", later.epoch, d.Rev)
	}
}

// row is a session's row as the store has it.
func (e *digestEnv) row(id string) *store.Session {
	e.t.Helper()
	row, err := e.store.GetSession(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

// A question goes ahead of the summaries: with every bulk place taken by
// sessions a hello wanted read, a session that starts asking is read at once,
// in the call that is always left for it - not after the whole queue.
func TestDigestReadsAQuestionAheadOfTheSummaries(t *testing.T) {
	e := newDigestEnv(t)
	for i := range 6 {
		id := fmt.Sprintf("s%d", i)
		e.session(id, "claude", fmt.Sprintf("Working on part %d", i))
		e.commit(id, termux.StateBusy)
	}
	e.gw.digestBlock()
	e.srv.digests.fillMissing()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if live, _ := e.gw.digestState(); live == digestBulkConcurrency {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the summaries never took their places")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if live, peak := e.gw.digestState(); live != digestBulkConcurrency || peak != digestBulkConcurrency {
		t.Fatalf("summaries took %d calls (%d at most), want %d", live, peak, digestBulkConcurrency)
	}

	e.session("q", "claude", "Which database should we use?\n❯ 1. Postgres")
	e.commit("q", termux.StateWaiting)
	for {
		seen := e.gw.digestsSeen()
		if strings.Contains(seen[len(seen)-1].joined(), "Which database should we use?") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the question waited behind %d summaries", len(seen))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if live, _ := e.gw.digestState(); live != digestConcurrency {
		t.Fatalf("%d calls in flight, want %d", live, digestConcurrency)
	}
	e.gw.digestRelease()
	for i := range 6 {
		e.digestOf(fmt.Sprintf("s%d", i), "ready", ready)
	}
}

// A waiting session whose read failed is read again once the wait after the
// failure is over, on the very same screen: the question is still being
// asked, and nothing else would read it.
func TestDigestReadsAFailedQuestionAgain(t *testing.T) {
	e := newDigestEnv(t)
	e.srv.digests.lookEvery = 10 * time.Millisecond
	e.srv.digests.freshFor = 20 * time.Millisecond
	e.session("f1", "claude", "Which database should we use?\n❯ 1. Postgres")
	e.gw.digestRefuse(http.StatusBadGateway, "the provider is down")
	e.commit("f1", termux.StateWaiting)
	e.digestOf("f1", "an error", func(d digest) bool { return d.Phase == digestError })
	calls := e.digestCalls()

	e.gw.digestRefuse(0, "")
	e.gw.digestAlways(askedJSON)
	time.Sleep(50 * time.Millisecond)
	e.srv.digests.lookAround(context.Background())
	got := e.digestOf("f1", "read again", func(d digest) bool { return ready(d) && d.Question != nil })
	if n := e.digestCalls(); n != calls+1 || got.State != string(termux.StateWaiting) {
		t.Fatalf("%d calls after the failure, digest %#v", n-calls, got)
	}
}

// A page that can only poll is a page looking at Home: GET /api/home reads
// the sessions nobody has read - once a look, however often it polls - and
// keeps the working ones' summaries current as a stream would.
func TestDigestHomePollIsAHello(t *testing.T) {
	e := newDigestEnv(t)
	e.srv.digests.freshFor = 0
	e.session("p1", "claude", "● Which color should the button be? Red, green or blue?\n❯ ")
	e.commit("p1", termux.StateIdle)
	e.gw.digestAlways(textQuestionJSON)
	for range 3 {
		if res, payload := e.do(t, e.client, "GET", "/api/home", ""); res.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/home: %d %#v", res.StatusCode, payload)
		}
	}
	got := e.digestOf("p1", "read", func(d digest) bool { return ready(d) && d.Question != nil })
	if got.Question.Source != questionText {
		t.Fatalf("the question in prose was read as %#v", got.Question)
	}
	time.Sleep(100 * time.Millisecond)
	if n := e.digestCalls(); n != 1 {
		t.Fatalf("three polls made %d reads", n)
	}

	e.session("w1", "claude", "Working (1s)")
	e.commit("w1", termux.StateBusy)
	e.srv.digests.request(e.row("w1"))
	e.digestOf("w1", "ready", ready)
	e.show("w1", "Working (61s) - running the tests")
	e.srv.digests.lookAround(context.Background())
	e.digestOf("w1", "read again for the page that polls", func(d digest) bool { return ready(d) && e.digestCalls() == 3 })
}

// A menu under its cursor is a prompt that takes keys, whatever state the
// detector has caught up with: a session read in the moment between the
// menu's paint and its status says idle, and the question on its screen is
// no question asked in prose. A numbered list in an answer, and the idle
// prompt's own pointer, are not menus.
func TestMenuOpenIsAPromptNotProse(t *testing.T) {
	for _, c := range []struct {
		name, screen string
		want         bool
	}{
		{"claude menu", "Which features do you want?\n❯ 1. [ ] Dark mode\n  2. [ ] Search\n  3. [ ] Type something.\n     Submit\n  4. Chat about this\n" +
			"Enter to select · ↑/↓ to navigate · Esc to cancel\n", true},
		{"codex approval", "  Would you like to run the following command?\n\n  › 1. Yes, proceed (y)\n    2. No, and tell Codex what to do differently (esc)\n", true},
		{"prose with a list", "● Two ways to go:\n  1. SQLite for the prototype\n  2. Postgres for production\n  Which one should I use?\n\n❯ \n  ⏵⏵ auto mode on\n", false},
		{"prose", "● Which color should the button be? Red, green or blue?\n\n✻ Worked for 3s · done\n❯ \n", false},
	} {
		if got := menuOpen(c.screen); got != c.want {
			t.Errorf("%s: menuOpen = %v, want %v", c.name, got, c.want)
		}
	}
}

// An idle session whose read failed is read again once its wait is over, as a
// waiting one is: the turn may have ended on a question in prose, which no
// other trigger would read before the next turn. While nobody looks at Home
// it is tried a few times; while somebody does, for as long as it fails. And
// a hello or a reload counts a failed read whose wait is over as no reading.
func TestDigestReadsAFailedIdleSessionAgain(t *testing.T) {
	e := newDigestEnv(t)
	e.srv.digests.lookEvery = 10 * time.Millisecond
	e.srv.digests.freshFor = 20 * time.Millisecond
	prose := "● Which color should the button be? Red, green or blue?\n❯ "
	failed := func(id string) {
		e.t.Helper()
		e.gw.digestRefuse(http.StatusInternalServerError, "stub says 500")
		e.commit(id, termux.StateBusy)
		e.commit(id, termux.StateIdle)
		e.digestOf(id, "an error while idle", func(d digest) bool {
			return d.Phase == digestError && d.State == string(termux.StateIdle)
		})
	}

	e.session("i1", "claude", prose)
	failed("i1")
	e.gw.digestRefuse(0, "")
	e.gw.digestAlways(textQuestionJSON)
	time.Sleep(50 * time.Millisecond)
	e.srv.digests.lookAround(context.Background())
	got := e.digestOf("i1", "read again", func(d digest) bool { return ready(d) && d.Question != nil })
	if got.Question.Source != questionText {
		t.Fatalf("the question in prose was read as %#v", got.Question)
	}

	// Out of tries while nobody looks; read again once somebody does.
	e.session("i2", "claude", prose)
	failed("i2")
	e.gw.digestRefuse(0, "")
	e.srv.digests.mu.Lock()
	e.srv.digests.entries["i2"].fails = digestIdleRetries
	e.srv.digests.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	calls := e.digestCalls()
	e.srv.digests.lookAround(context.Background())
	time.Sleep(100 * time.Millisecond)
	if n := e.digestCalls(); n != calls {
		t.Fatalf("an idle session out of tries was read %d more times with nobody looking", n-calls)
	}
	e.srv.digests.mu.Lock()
	e.srv.digests.polled = time.Now()
	e.srv.digests.mu.Unlock()
	e.srv.digests.lookAround(context.Background())
	e.digestOf("i2", "read again for the page", func(d digest) bool { return ready(d) && d.Question != nil })

	// A hello reads one whose wait is over, and leaves one whose wait is not.
	e.session("i3", "claude", prose)
	failed("i3")
	e.gw.digestRefuse(0, "")
	e.srv.digests.freshFor = time.Hour
	e.srv.digests.lookEvery = time.Hour
	calls = e.digestCalls()
	e.srv.digests.fillMissing()
	time.Sleep(100 * time.Millisecond)
	if n := e.digestCalls(); n != calls {
		t.Fatalf("a hello read a failed session before its wait was over: %d calls", n-calls)
	}
	e.srv.digests.lookEvery = 10 * time.Millisecond
	e.srv.digests.freshFor = 20 * time.Millisecond
	time.Sleep(50 * time.Millisecond)
	if res, _ := e.do(t, e.client, "GET", "/api/home", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/home: %d", res.StatusCode)
	}
	e.digestOf("i3", "read again by the reload", func(d digest) bool { return ready(d) && d.Question != nil })
}

// A key saved while Home is open - from another device, say - reads the
// sessions that could only say "no key", the idle ones too, without the page
// reconnecting. The "no key" digest says what the session was doing.
func TestDigestReadsWhenAKeyIsSaved(t *testing.T) {
	e := newDigestEnv(t)
	e.configureOpenRouter(t, map[string]any{"api_key": ""})
	e.session("n1", "claude", "● Which color should the button be? Red, green or blue?\n❯ ")
	client := e.dialEvents(t)
	client.hello()
	e.commit("n1", termux.StateBusy)
	e.commit("n1", termux.StateIdle)
	e.digestOf("n1", "no key, while idle", func(d digest) bool {
		return d.Error == digestNoKey && d.State == string(termux.StateIdle)
	})
	if n := e.digestCalls(); n != 0 {
		t.Fatalf("%d calls without a key", n)
	}
	e.gw.digestAlways(textQuestionJSON)
	e.configureOpenRouter(t, map[string]any{"api_key": "k"})
	got := e.digestOf("n1", "read with the key", func(d digest) bool { return ready(d) && d.Question != nil })
	if got.Question.Source != questionText {
		t.Fatalf("the question in prose was read as %#v", got.Question)
	}
	// Saving the same settings again reads nothing.
	calls := e.digestCalls()
	e.configureOpenRouter(t, map[string]any{"api_key": "k"})
	time.Sleep(100 * time.Millisecond)
	if n := e.digestCalls(); n != calls {
		t.Fatalf("unchanged settings read %d sessions", n-calls)
	}
}

// An idle session whose read failed is read again with the summaries, not
// ahead of them: a gateway that is slow is when the call kept for a question
// is wanted most, and a session that starts asking is read in it at once.
func TestDigestRetriesOfIdleSessionsLeaveTheQuestionItsCall(t *testing.T) {
	e := newDigestEnv(t)
	e.srv.digests.lookEvery = 10 * time.Millisecond
	e.srv.digests.freshFor = 20 * time.Millisecond
	e.gw.digestRefuse(http.StatusBadRequest, "stub says no")
	for i := range 4 {
		id := fmt.Sprintf("i%d", i)
		e.session(id, "claude", fmt.Sprintf("Done with part %d\n❯ ", i))
		e.commit(id, termux.StateBusy)
		e.commit(id, termux.StateIdle)
		e.digestOf(id, "an error while idle", func(d digest) bool {
			return d.Phase == digestError && d.State == string(termux.StateIdle)
		})
	}
	e.gw.digestRefuse(0, "")
	e.gw.digestBlock()
	defer e.gw.digestRelease()
	time.Sleep(50 * time.Millisecond)
	e.srv.digests.lookAround(context.Background())
	deadline := time.Now().Add(10 * time.Second)
	for {
		if live, _ := e.gw.digestState(); live == digestBulkConcurrency {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the retries never took their places")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if live, peak := e.gw.digestState(); peak != digestBulkConcurrency {
		t.Fatalf("idle retries took %d calls (%d at most), want %d", live, peak, digestBulkConcurrency)
	}

	e.session("q", "claude", "Which database should we use?\n❯ 1. Postgres")
	e.commit("q", termux.StateWaiting)
	for {
		seen := e.gw.digestsSeen()
		if strings.Contains(seen[len(seen)-1].joined(), "Which database should we use?") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the question waited behind the idle retries")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A session that comes back through unknown - a relaunch, a restart, a
// resume, a crash - is read again when its digest was made in another state,
// or the question it held was dropped on the way: no other trigger starts from
// unknown, and Home would say "Asks which database to use" of a session at
// its prompt until its next turn. One that comes back as it was is not.
func TestDigestReadsASessionThatCameBackThroughUnknown(t *testing.T) {
	e := newDigestEnv(t)
	e.waitingDigest("w1", "Which database should we use?\n❯ 1. Postgres")
	e.gw.digestAlways(`{"summary":"At its prompt after a restart.","context":"","question":null}`)
	e.show("w1", "> ")
	e.commit("w1", termux.StateUnknown)
	time.Sleep(100 * time.Millisecond)
	e.commit("w1", termux.StateIdle)
	e.digestOf("w1", "read again at its prompt", func(d digest) bool {
		return ready(d) && d.State == "idle" && d.Summary == "At its prompt after a restart."
	})

	// A question in prose, dropped by the unknown, read again on the idle
	// that follows - the same state the digest was made in.
	e.session("p1", "claude", "● Which color should the button be? Red, green or blue?\n❯ ")
	e.gw.digestAlways(textQuestionJSON)
	e.commit("p1", termux.StateBusy)
	e.commit("p1", termux.StateIdle)
	asked := e.digestOf("p1", "a question in prose", func(d digest) bool { return ready(d) && d.Question != nil })
	e.commit("p1", termux.StateUnknown)
	e.digestOf("p1", "without its question", func(d digest) bool { return d.Question == nil })
	e.commit("p1", termux.StateIdle)
	e.digestOf("p1", "the question read again", func(d digest) bool {
		return ready(d) && d.Question != nil && d.Rev > asked.Rev
	})

	// Back as it was, with nothing dropped: no read.
	e.session("s1", "claude", "Done.\n❯ ")
	e.gw.digestAlways(`{"summary":"Done.","context":"","question":null}`)
	e.commit("s1", termux.StateBusy)
	e.commit("s1", termux.StateIdle)
	e.digestOf("s1", "read", func(d digest) bool { return ready(d) && d.State == "idle" })
	calls := e.digestCalls()
	e.commit("s1", termux.StateUnknown)
	e.commit("s1", termux.StateIdle)
	time.Sleep(300 * time.Millisecond)
	if n := e.digestCalls(); n != calls {
		t.Fatalf("a session back as it was was read %d more times", n-calls)
	}
}

// What ticks on a screen - a sub-agent's "(12s · ↓ 345 tokens)", a clock, a
// token count in the bottom bar - is no change of the question on it, and an
// answer to it goes in; a change of its words or its rows is one, and the
// read it starts is not started again by every tap that follows.
func TestAnswerGoesInOnAScreenThatTicks(t *testing.T) {
	ticking := func(n int) string {
		return fmt.Sprintf("  ⎿ Agent running… (%ds · ↓ %d tokens)\n\nWhich database should we use?\n❯ 1. Postgres\n  2. SQLite\n"+
			"  ⬝⬝⬝⬝⬝⬝  %d.5K (%d%%) · $0.0%d   ctrl+p commands  %02d:%02d", n, 100*n, n, n, n%10, n%24, n%60)
	}
	for _, n := range []int{2, 13, 47} {
		if screenHash(ticking(1)) != screenHash(ticking(n)) {
			t.Fatalf("a screen that ticks is not the same screen:\n%s\n%s", ticking(1), ticking(n))
		}
	}
	for _, other := range []string{
		strings.Replace(ticking(1), "Which database", "Which cache", 1),
		strings.Replace(ticking(1), "2. SQLite", "2. MySQL", 1),
		"Kill process 1234?\n❯ 1. Yes\n",
	} {
		if screenHash(other) == screenHash(ticking(1)) {
			t.Fatalf("a different question is the same screen:\n%s", other)
		}
	}
	if screenHash("Kill process 1234?\n❯ 1. Yes\n") == screenHash("Kill process 5678?\n❯ 1. Yes\n") {
		t.Fatal("two numbers are one question")
	}

	e := newDigestEnv(t)
	first := e.waitingDigest("t1", ticking(1))
	e.show("t1", ticking(5))
	e.gw.always(`{"actions":[],"done":true,"summary":"Nothing to do."}`)
	res, payload := e.answerOf("t1", first.Rev, "Postgres")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("an answer on a screen that ticks = %d %#v", res.StatusCode, payload)
	}

	// The question moved on in place: refused with a read, and the taps
	// after it get that read rather than starting another.
	deadline := time.Now().Add(20 * time.Second)
	for e.srv.answers.live("t1") {
		if time.Now().After(deadline) {
			t.Fatal("the run never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	now, _ := e.srv.digests.get("t1")
	e.gw.digestBlock()
	defer e.gw.digestRelease()
	e.show("t1", strings.Replace(ticking(6), "Which database", "Which cache", 1))
	calls := e.digestCalls()
	for range 3 {
		res, payload := e.answerOf("t1", now.Rev, "Postgres")
		if res.StatusCode != http.StatusConflict {
			t.Fatalf("an answer to a question that moved on = %d %#v", res.StatusCode, payload)
		}
	}
	e.gw.digestRelease()
	// Moved again a moment after: the read just made is the answer to it.
	moved := e.digestOf("t1", "the moved question read", func(d digest) bool { return ready(d) && d.Rev > now.Rev })
	e.show("t1", strings.Replace(ticking(7), "Which database", "Which queue", 1))
	for range 2 {
		if res, payload := e.answerOf("t1", moved.Rev, "Postgres"); res.StatusCode != http.StatusConflict {
			t.Fatalf("an answer to a question that moved on = %d %#v", res.StatusCode, payload)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := e.digestCalls() - calls; n != 1 {
		t.Fatalf("five taps on a moved question started %d reads", n)
	}
}

// A question asked in prose and answered at the machine: the turn it starts
// is read at once, with the summaries, and does not say "Asks …" for a
// minute under "Working".
func TestDigestReadsAProseQuestionAnsweredAtTheMachine(t *testing.T) {
	e := newDigestEnv(t)
	e.session("p1", "claude", "● Which color should the button be? Red, green or blue?\n\n> ")
	e.commit("p1", termux.StateBusy)
	e.gw.digestAlways(`{"summary":"Asks which color the button should be.","context":"",` +
		`"question":{"kind":"text","items":[{"text":"Which color should the button be?","options":[]}]}}`)
	e.commit("p1", termux.StateIdle)
	asked := e.digestOf("p1", "a question in prose", func(d digest) bool { return ready(d) && d.Question != nil })
	base := e.digestCalls()
	e.gw.digestAlways(`{"summary":"Making the button blue.","context":"","question":null}`)
	e.show("p1", "> blue\n\n✻ Cooking… (2s · esc to interrupt)\n")
	e.commit("p1", termux.StateBusy)
	d := e.digestOf("p1", "read for the turn", func(d digest) bool {
		return ready(d) && d.Rev > asked.Rev && d.Summary == "Making the button blue."
	})
	if d.Question != nil || d.State != "busy" || e.digestCalls() != base+1 {
		t.Fatalf("digest = %#v after %d calls", d, e.digestCalls()-base)
	}
}
