//go:build live && !windows

// This file is a throwaway verification harness, not part of the suite: it is
// behind the `live` build tag because it starts the real `opencode` binary,
// talks to a real model provider and costs money and minutes.
//
//	go test -tags live -run TestLiveOpenCode -v -timeout 20m ./internal/termux/
//
// It exists to prove the OpenCode activity detection against the real program
// rather than against our idea of it: the event names, the `/permission` seed,
// and the permission card the TUI paints.

package termux

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// liveModel is the model the live sessions run on. Any fast tool-calling model
// does; this one is cheap and answers in a couple of seconds.
func liveModel() string {
	if m := strings.TrimSpace(os.Getenv("SOCRATES_LIVE_OC_MODEL")); m != "" {
		return m
	}
	return "openrouter/~anthropic/claude-haiku-latest"
}

// liveAskPermission is the permission document the live sessions run with.
//
// The shipped plan says allow-to-everything, which is the product's answer to
// "a prompt in a pane nobody is watching is a session that has stopped". A
// user config that asks is still an ordinary case - and it is the only way to
// see the waiting state at all - so bash is switched back to `ask` here and
// nothing else is touched.
var liveAskPermission = map[string]any{
	"*": "allow", "bash": "ask", "edit": "allow", "write": "allow",
	"patch": "allow", "read": "allow", "webfetch": "allow", "websearch": "allow",
	"task": "allow", "todowrite": "allow", "external_directory": "allow",
}

// liveLab is one real OpenCode session: a tmux server of its own, a real
// opencode in it, and the detector running against both.
type liveLab struct {
	*lab
	id      string
	workdir string
	rec     *stateRecorder
}

// stateRecorder samples the committed activity twice a second and keeps every
// change, which is the timeline the report is written from.
type stateRecorder struct {
	mu     sync.Mutex
	t      *testing.T
	m      *Manager
	id     string
	start  time.Time
	marks  []string
	rows   []liveSample
	stopCh chan struct{}
	done   chan struct{}
	once   sync.Once
}

type liveSample struct {
	at    time.Duration
	state State
	src   string
	note  string
	mark  string
}

func newRecorder(t *testing.T, m *Manager, id string) *stateRecorder {
	r := &stateRecorder{t: t, m: m, id: id, start: time.Now(),
		stopCh: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		last := liveSample{state: "<none>"}
		for {
			select {
			case <-r.stopCh:
				return
			case <-time.After(500 * time.Millisecond):
			}
			a := m.ActivityOf(id)
			if a.State == last.state && a.Source == last.src && a.Note == last.note {
				continue
			}
			r.mu.Lock()
			mark := ""
			if len(r.marks) > 0 {
				mark = r.marks[len(r.marks)-1]
			}
			s := liveSample{at: time.Since(r.start).Round(time.Second), state: a.State,
				src: a.Source, note: a.Note, mark: mark}
			r.rows = append(r.rows, s)
			r.mu.Unlock()
			t.Logf("  t+%-6s %-8s source=%-6s note=%q  [%s]", s.at, s.state, s.src, s.note, mark)
			last = s
		}
	}()
	return r
}

// mark labels everything recorded from now on, so the timeline says which
// scenario each row belongs to.
func (r *stateRecorder) mark(label string) {
	r.mu.Lock()
	r.marks = append(r.marks, label)
	r.mu.Unlock()
	r.t.Logf("--- t+%s %s", time.Since(r.start).Round(time.Second), label)
	r.snap(label)
}

// snap forces a row for the state as it stands, so that a mark and the end of
// the run both appear in the timeline whether or not anything changed.
func (r *stateRecorder) snap(label string) {
	a := r.m.ActivityOf(r.id)
	r.mu.Lock()
	r.rows = append(r.rows, liveSample{at: time.Since(r.start).Round(time.Second),
		state: a.State, src: a.Source, note: a.Note, mark: label + " (mark)"})
	r.mu.Unlock()
}

func (r *stateRecorder) stop() {
	r.once.Do(func() {
		r.snap("end")
		close(r.stopCh)
		<-r.done
	})
}

// states is every state committed under one mark, in order.
func (r *stateRecorder) states(mark string) []State {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []State
	for _, row := range r.rows {
		if row.mark == mark {
			out = append(out, row.state)
		}
	}
	return out
}

func startLiveOpenCode(t *testing.T) *liveLab {
	t.Helper()
	if _, err := os.Stat("/root/.opencode/bin/opencode"); err != nil {
		if _, err := os.Stat(os.Getenv("HOME") + "/.opencode/bin/opencode"); err != nil {
			t.Skip("opencode is not installed")
		}
	}
	l := newLab(t)
	workdir := realDir(t, t.TempDir())

	id := NewID()
	plan, err := harnesses.OpenCode{}.Plan(context.Background(), harnesses.PlanRequest{
		SessionID: id,
		Cwd:       workdir,
		Model:     liveModel(),
		Settings:  config.Default(),
		DataDir:   l.dataDir,
	})
	if err != nil {
		t.Skipf("could not plan an OpenCode session: %v", err)
	}
	// The one deviation from the shipped plan: bash asks.
	perm, err := json.Marshal(liveAskPermission)
	if err != nil {
		t.Fatal(err)
	}
	plan.Env["OPENCODE_PERMISSION"] = string(perm)
	var doc map[string]any
	if err := json.Unmarshal([]byte(plan.Env["OPENCODE_CONFIG_CONTENT"]), &doc); err != nil {
		t.Fatal(err)
	}
	doc["permission"] = liveAskPermission
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	plan.Env["OPENCODE_CONFIG_CONTENT"] = string(raw)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	row, err := l.Create(ctx, Spec{
		ID: id, Harness: string(harnesses.KindOpenCode), Model: liveModel(),
		Workdir: workdir, Plan: plan, Cols: 120, Rows: 40,
	})
	if err != nil {
		t.Fatalf("could not create the OpenCode session: %v", err)
	}
	if row.State != store.StateRunning {
		t.Fatalf("the session is %q, not running: %s", row.State, row.FailReason)
	}
	t.Cleanup(func() { harnesses.ForgetOpenCodeAccess(id) })

	// The detector, exactly as the server starts it.
	actx, acancel := context.WithCancel(context.Background())
	t.Cleanup(acancel)
	l.StartActivity(actx)

	ll := &liveLab{lab: l, id: id, workdir: workdir}
	ll.waitServer(t)
	ll.rec = newRecorder(t, l.Manager, id)
	t.Cleanup(ll.rec.stop)
	return ll
}

// waitServer blocks until the TUI's HTTP server answers, which is what the
// discoverer would otherwise be waiting for.
func (l *liveLab) waitServer(t *testing.T) {
	t.Helper()
	access, ok := l.OpenCodeAccessOf(l.id)
	if !ok {
		t.Fatal("the session has no OpenCode access")
	}
	w := &openCodeWatcher{access: access, workdir: l.workdir,
		busy: map[string]bool{}, waiting: map[string]openCodePrompt{}}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		w.poll(context.Background())
		w.mu.Lock()
		up := w.ok
		w.mu.Unlock()
		if up {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the OpenCode server never answered:\n%s", l.pane(t))
}

func (l *liveLab) pane(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := l.CapturePane(ctx, l.id, 40)
	if err != nil {
		return fmt.Sprintf("<capture failed: %v>", err)
	}
	return out
}

func (l *liveLab) type_(t *testing.T, keys ...Key) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.SendKeys(ctx, l.id, keys); err != nil {
		t.Fatalf("could not type into the pane: %v", err)
	}
	// What the WebSocket does for every keystroke that reaches a pane.
	l.NoteInput(l.id)
}

func (l *liveLab) prompt(t *testing.T, text string) {
	l.type_(t, Key{Text: text, Wait: 500 * time.Millisecond}, Key{Name: "Enter"})
}

// await waits for the committed state to be one of want, and fails with the
// pane on the timeout.
func (l *liveLab) await(t *testing.T, within time.Duration, why string, want ...State) Activity {
	t.Helper()
	deadline := time.Now().Add(within)
	var last Activity
	for time.Now().Before(deadline) {
		last = l.ActivityOf(l.id)
		for _, w := range want {
			if last.State == w {
				return last
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s: after %s the session is %#v, want one of %v\npane:\n%s",
		why, within, last, want, l.pane(t))
	return last
}

// TestLiveOpenCodeTimeline is the whole story against the real program: a
// prompt, the permission card, Allow once, and the turn ending. It also covers
// the two prompts in sequence, the rejection, and that WaitIdle does not block
// once a prompt has been answered.
func TestLiveOpenCodeTimeline(t *testing.T) {
	l := startLiveOpenCode(t)

	// --- 1. idle before anything is typed.
	l.rec.mark("boot")
	l.await(t, 60*time.Second, "the fresh session should settle on idle", StateIdle)

	// --- 2. a prompt that needs the shell.
	l.rec.mark("prompt 1")
	l.prompt(t, "Run the shell command: echo hello-from-bash")
	l.await(t, 60*time.Second, "the typed prompt should read as busy", StateBusy, StateWaiting)
	got := l.await(t, 120*time.Second, "the permission card should read as waiting", StateWaiting)
	if got.Note != "permission prompt" {
		t.Errorf("the waiting note is %q, want %q", got.Note, "permission prompt")
	}
	screen := l.pane(t)
	if !strings.Contains(screen, "Permission required") {
		t.Errorf("waiting was reported with no permission card on the screen:\n%s", screen)
	}
	// The screen layer on its own has to agree, because it is the fallback
	// when the server cannot be reached at all.
	if obs := scrapeScreen(harnesses.KindOpenCode, screen); obs.state != StateWaiting {
		t.Errorf("the permission screen scrapes as %#v, want waiting:\n%s", obs, screen)
	}
	// And it must not have been read as idle off `ctrl+p commands`, which the
	// card keeps on the bar underneath it.
	if !strings.Contains(screen, "ctrl+p commands") {
		t.Logf("note: the waiting screen carried no `ctrl+p commands` bar")
	}
	if a := l.ActivityOf(l.id); !a.Unread {
		t.Errorf("a session that started needing an answer is not unread: %#v", a)
	}

	// --- 3. Allow once, and the turn finishes.
	l.rec.mark("allow once")
	l.type_(t, Key{Name: "Enter"})
	l.await(t, 90*time.Second, "the answered prompt should leave waiting", StateBusy, StateIdle)
	l.await(t, 180*time.Second, "the turn should end idle", StateIdle)
	// No stale waiting, and nothing for WaitIdle to block on.
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if a, err := l.WaitIdle(wctx, l.id); err != nil {
		t.Errorf("WaitIdle blocked after the prompt was answered: %v (%#v)", err, a)
	}
	if a := l.ActivityOf(l.id); a.State != StateIdle {
		t.Errorf("after the turn the session is %#v, want idle", a)
	}

	// --- 4. a second prompt, rejected this time.
	l.rec.mark("prompt 2 (reject)")
	l.prompt(t, "Now run the shell command: echo second-one")
	l.await(t, 120*time.Second, "the second prompt should reach waiting too", StateWaiting)
	// Reject is the third button on the card.
	l.type_(t, Key{Name: "Right", Wait: 200 * time.Millisecond},
		Key{Name: "Right", Wait: 200 * time.Millisecond}, Key{Name: "Enter"})
	l.rec.mark("rejected")
	l.await(t, 120*time.Second, "a rejected prompt should not stay waiting", StateBusy, StateIdle)
	l.await(t, 180*time.Second, "the rejected turn should end idle", StateIdle)

	// The timeline, printed as one block.
	l.rec.stop()
	t.Log("timeline:")
	for _, row := range l.rec.rows {
		t.Logf("  t+%-6s %-8s %-6s %-20q %s", row.at, row.state, row.src, row.note, row.mark)
	}
	for _, mark := range []string{"prompt 1", "allow once", "prompt 2 (reject)"} {
		t.Logf("  states under %q: %v", mark, l.rec.states(mark))
	}
}

// TestLiveOpenCodeSeedAndFallback covers the three edges the happy path does
// not: a watcher that attaches only after the prompt is already pending, a
// stream that is dropped and reconnected while it stands, and a server that
// cannot be reached at all - where the pane text is the only evidence left.
func TestLiveOpenCodeSeedAndFallback(t *testing.T) {
	l := startLiveOpenCode(t)
	l.rec.mark("boot")
	l.await(t, 60*time.Second, "the fresh session should settle on idle", StateIdle)

	l.rec.mark("prompt")
	l.prompt(t, "Run the shell command: echo seed-me")
	l.await(t, 120*time.Second, "the permission card should read as waiting", StateWaiting)

	access, ok := l.OpenCodeAccessOf(l.id)
	if !ok {
		t.Fatal("the session has no OpenCode access")
	}

	// (a) A watcher built from nothing while the prompt already stands: the
	//     seed is the only thing that can find it, because the status map says
	//     the session is busy.
	t.Run("late attach seeds from /permission", func(t *testing.T) {
		w := &openCodeWatcher{access: access, workdir: l.workdir,
			busy: map[string]bool{}, waiting: map[string]openCodePrompt{}}
		w.poll(context.Background())
		if got := w.answer(); got.state != StateWaiting {
			t.Fatalf("a fresh watcher polled %#v while a prompt was open, want waiting", got)
		}
		w.mu.Lock()
		busy := len(w.busy)
		// The rule the code leans on, turned into evidence: a session holding
		// an open prompt reports `busy` in /session/status for as long as it
		// holds it, which is what makes the status map a second witness over
		// the waiting set when /permission cannot be read.
		for id, prompt := range w.waiting {
			if prompt.session == "" {
				t.Errorf("the pending prompt %q carries no sessionID", id)
				continue
			}
			if !w.busy[prompt.session] {
				t.Errorf("the session %q holding the open prompt %q is not busy in the status map %v",
					prompt.session, id, w.busy)
			}
		}
		w.mu.Unlock()
		if busy == 0 {
			t.Error("the status map reported no busy session; the seed test is not testing what it thinks")
		}
	})

	// (b) The stream dropped and reconnected while the prompt stands.
	t.Run("stream dropped and reconnected", func(t *testing.T) {
		w := startOpenCodeWatcher(access, l.workdir)
		defer w.stop()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) && w.answer().state != StateWaiting {
			time.Sleep(250 * time.Millisecond)
		}
		if got := w.answer(); got.state != StateWaiting {
			t.Fatalf("the reconnected watcher answered %#v, want waiting", got)
		}
	})
	// The live source's own watcher is dropped and rebuilt, which is what
	// ResetActivity and a reconnect both do, and the committed state must come
	// back to waiting rather than to the busy the status map reports.
	l.rec.mark("watcher dropped")
	l.act.mu.Lock()
	l.act.forgetSources(l.id)
	l.act.mu.Unlock()
	l.await(t, 30*time.Second, "the rebuilt watcher should find the pending prompt again", StateWaiting)

	// (e) The exact layer gone entirely: the pane text is the only evidence
	//     left, and it has to say waiting - which is also (f), because the
	//     bottom bar the card sits on carries `ctrl+p commands`, the idle
	//     literal.
	t.Run("no exact layer falls back to the screen", func(t *testing.T) {
		l.setSource(harnesses.KindOpenCode, silentSource{})
		defer l.setSource(harnesses.KindOpenCode, newOpenCodeSource(l.Manager))
		// Forget everything derived, so nothing but the screen can answer.
		l.rec.mark("no exact layer")
		l.ResetActivity(l.id)
		got := l.await(t, 30*time.Second, "the screen fallback should reach waiting", StateWaiting)
		if got.Source != sourceScreen {
			t.Errorf("waiting came from %q, want the screen", got.Source)
		}
		// And it must hold, not flicker to the quiescence idle underneath.
		time.Sleep(5 * time.Second)
		if a := l.ActivityOf(l.id); a.State != StateWaiting {
			t.Errorf("five seconds later the screen-only session is %#v, want waiting", a)
		}
		screen := l.pane(t)
		if obs := scrapeScreen(harnesses.KindOpenCode, screen); obs.state != StateWaiting {
			t.Errorf("with no exact layer the screen scraped %#v, want waiting:\n%s", obs, screen)
		}

		// (g) with no exact layer either: the prompt is answered, and the
		//     screen has to let waiting go. `capture-pane -S -40` reads
		//     scrollback as well as the visible pane, so an answered card that
		//     is still in the history would hold the row on waiting for ever.
		l.rec.mark("answered with no exact layer")
		l.type_(t, Key{Name: "Enter"})
		got = l.await(t, 90*time.Second, "an answered prompt must not stay waiting",
			StateIdle, StateBusy)
		t.Logf("the screen-only state after the answer is %#v", got)
		after := l.pane(t)
		if obs := scrapeScreen(harnesses.KindOpenCode, after); obs.state == StateWaiting {
			t.Errorf("the answered screen still scrapes as waiting:\n%s", after)
		}
	})

	// The turn finishes on the real source again.
	l.rec.mark("settle")
	l.await(t, 180*time.Second, "the session should end the run idle", StateIdle)

	l.rec.stop()
	t.Log("timeline:")
	for _, row := range l.rec.rows {
		t.Logf("  t+%-6s %-8s %-6s %-20q %s", row.at, row.state, row.src, row.note, row.mark)
	}
}

// silentSource is an exact layer that never answers, which is what a session
// whose OpenCode server cannot be reached at all looks like.
type silentSource struct{}

func (silentSource) Read(context.Context, snapshot) observation { return missing }
