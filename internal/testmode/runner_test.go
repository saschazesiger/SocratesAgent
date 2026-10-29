package testmode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// TestMain runs the tests with a home of their own (internal/testhome).
func TestMain(m *testing.M) { os.Exit(testhome.Main(m)) }

// fakeHost is the server as a test sees it: sessions are ids, messages a
// list, the kv a map.
type fakeHost struct {
	mu       sync.Mutex
	n        int
	sessions map[string]SessionSpec
	messages map[string][]string
	stopped  []string
	removed  []string
	notes    []string
	kv       map[string]string
	accounts []string
	frames   int
	running  map[string]bool
}

func newFakeHost() *fakeHost {
	return &fakeHost{sessions: map[string]SessionSpec{}, messages: map[string][]string{}, kv: map[string]string{},
		accounts: []string{"acc-1", "acc-2"}, running: map[string]bool{}}
}

func (h *fakeHost) StartSession(_ context.Context, spec SessionSpec) (string, error) {
	h.mu.Lock()
	h.n++
	id := fmt.Sprintf("chat-%d", h.n)
	h.sessions[id] = spec
	h.messages[id] = append(h.messages[id], spec.Message)
	h.mu.Unlock()
	if spec.Created != nil {
		spec.Created(id)
	}
	return id, nil
}

// SessionStatus: every session started is there; turnRunning says which run
// a turn.
func (h *fakeHost) SessionStatus(chat string) (bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.sessions[chat]
	return ok, h.running[chat]
}

func (h *fakeHost) SendMessage(_ context.Context, chat, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages[chat] = append(h.messages[chat], text)
	return nil
}

func (h *fakeHost) StopSession(_ context.Context, chat string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, chat)
}

func (h *fakeHost) RemoveSession(_ context.Context, chat string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removed = append(h.removed, chat)
}

func (h *fakeHost) PickAccount(_ string, allowed []string, load map[string]int) (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	best := ""
	for _, a := range h.accounts {
		if len(allowed) > 0 && !contains(allowed, a) {
			continue
		}
		if best == "" || load[a] < load[best] {
			best = a
		}
	}
	if best == "" {
		return "", "waiting: no account"
	}
	return best, ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (h *fakeHost) Publish(map[string]any) {
	h.mu.Lock()
	h.frames++
	h.mu.Unlock()
}

func (h *fakeHost) Notify(title, body, _ string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notes = append(h.notes, title+": "+body)
}

func (h *fakeHost) GetKV(key string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.kv[key], nil
}

func (h *fakeHost) SetKV(key, value string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kv[key] = value
	return nil
}

func (h *fakeHost) list(of func(*fakeHost) []string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, of(h)...)
}

func (h *fakeHost) msgs(chat string) []string {
	return h.list(func(h *fakeHost) []string { return h.messages[chat] })
}

func (h *fakeHost) notesNow() []string { return h.list(func(h *fakeHost) []string { return h.notes }) }

func (h *fakeHost) stoppedNow() []string {
	return h.list(func(h *fakeHost) []string { return h.stopped })
}

func (h *fakeHost) removedNow() []string {
	return h.list(func(h *fakeHost) []string { return h.removed })
}

func (h *fakeHost) chats() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for i := 1; i <= h.n; i++ {
		out = append(out, fmt.Sprintf("chat-%d", i))
	}
	return out
}

func (h *fakeHost) spec(chat string) SessionSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[chat]
}

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// pngBytes is a 1x1 PNG.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
	"\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

// fixture is a repository with a catalog of one area: tests on the devices
// a, b and c, with a tool whose pool has two accounts.
func fixture(t *testing.T, tests map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	base := filepath.Join(repo, ".socrates", "tests")
	write(t, filepath.Join(base, "TESTS.md"), "---\ntitle: Fixture\n---\nTest the fixture.\n", 0o644)
	write(t, filepath.Join(base, "web", "AREA.md"), `---
title: The web
tools: [pool]
dimensions:
  device:
    values: {a: Device A, b: Device B, c: Device C}
evidence: screenshot
---
Work every cell carefully.
`, 0o644)
	write(t, filepath.Join(base, "tools", "pool", "TOOL.md"), `---
title: The pool tool
secrets:
  command: printf 'ACCOUNTS=u1:key-one-secret,u2:key-two-secret\nOTHER=not-named\n'
  names: [ACCOUNTS]
pool:
  secret: ACCOUNTS
end: bin/end
---
Run shot to take a screenshot.
`, 0o644)
	write(t, filepath.Join(base, "tools", "pool", "bin", "end"), "#!/bin/sh\necho ended > \"$SOCRATES_TEST_EVIDENCE/ended.txt\"\n", 0o755)
	for name, text := range tests {
		write(t, filepath.Join(base, "web", name+".md"), text, 0o644)
	}
	return repo
}

const twoSteps = `---
title: Two steps
priority: P0
device: all
---
Context of the test.

## Steps

1. Open the page.
   Expect: the page.
2. Press the button.
   Expect: it works.
`

func newRunner(t *testing.T, host *fakeHost) (*Runner, string) {
	t.Helper()
	data := t.TempDir()
	r := New(data, host)
	r.Start()
	t.Cleanup(r.Close)
	return r, data
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *Runner) runState(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id].run.State
}

func (r *Runner) attemptsOf(id string) []*Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Attempt
	rs := r.runs[id]
	for _, ref := range rs.run.Order {
		c := *rs.attempts[ref.ID]
		out = append(out, &c)
	}
	return out
}

// act is what a tool does: an action line and a screenshot in the evidence.
func act(t *testing.T, r *Runner, chat, action string, n int) {
	t.Helper()
	l, err := r.LaunchFor(refJSON(t, r, chat))
	if err != nil {
		t.Fatal(err)
	}
	dir := envOf(l.Env, "SOCRATES_TEST_EVIDENCE")
	shot := fmt.Sprintf("%02d-%s.png", n, action)
	write(t, filepath.Join(dir, shot), string(pngBytes), 0o644)
	line, _ := json.Marshal(map[string]any{"at": "2026-10-29T10:00:00Z", "tool": "pool", "action": action,
		"summary": action + " done with key-one-secret", "screenshot": shot, "interaction": action == "tap"})
	f, err := os.OpenFile(filepath.Join(dir, ActionsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(line, '\n'))
	f.Close()
}

func refJSON(t *testing.T, r *Runner, chat string) json.RawMessage {
	t.Helper()
	r.mu.Lock()
	ref := r.byChat[chat]
	r.mu.Unlock()
	raw, _ := json.Marshal(ref)
	return raw
}

func envOf(env []string, name string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, name+"="); ok {
			return v
		}
	}
	return ""
}

func call(t *testing.T, r *Runner, chat, tool string, args any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	return r.Call(context.Background(), chat, tool, raw)
}

func mustCall(t *testing.T, r *Runner, chat, tool string, args any) string {
	t.Helper()
	text, isErr := call(t, r, chat, tool, args)
	if isErr {
		t.Fatalf("%s: %s", tool, text)
	}
	return text
}

// passCell works a cell through: two steps, each with an action, passed.
func passCell(t *testing.T, r *Runner, chat string) {
	t.Helper()
	act(t, r, chat, "open", 1)
	mustCall(t, r, chat, testtools.Judge, map[string]any{"step": 1, "verdict": "pass", "observed": "the page"})
	act(t, r, chat, "tap", 2)
	mustCall(t, r, chat, testtools.Judge, map[string]any{"step": 2, "verdict": "pass", "observed": "it works"})
	mustCall(t, r, chat, testtools.End, map[string]any{"summary": "all good"})
}

func startRun(t *testing.T, r *Runner, repo string, s *Settings) *Run {
	t.Helper()
	got, err := r.AddRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	run, err := r.StartRun(got.Key, "", Selection{}, s)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to be prepared", func() bool { return r.runState(run.ID) != RunPreparing })
	if st := r.runState(run.ID); st != RunRunning {
		r.mu.Lock()
		e := r.runs[run.ID].run.Error
		r.mu.Unlock()
		t.Fatalf("run %s: %s", st, e)
	}
	return run
}

func noReview() *Settings {
	s := DefaultSettings()
	s.Review, s.Analysis = false, false
	return &s
}

func TestCellsQueueForThePoolAndStartWhenAMemberIsFree(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	run := startRun(t, r, fixture(t, map[string]string{"two": twoSteps}), noReview())
	r.schedule()
	waitFor(t, "two sessions", func() bool { return len(host.chats()) == 2 })
	var waiting *Attempt
	for _, a := range r.attemptsOf(run.ID) {
		if a.State == AttemptWaiting {
			waiting = a
		}
	}
	if waiting == nil || !strings.Contains(waiting.Wait, "free The pool tool account") {
		t.Fatalf("the third cell should wait for the pool, got %+v", waiting)
	}
	first := host.chats()[0]
	spec := host.spec(first)
	if spec.Ref.Kind != testtools.KindCell || spec.Workdir == "" || !strings.Contains(spec.Message, "1. Open the page.") {
		t.Fatalf("first session: %+v", spec)
	}
	// The launch carries the vault, the lease and the tool on PATH.
	l, err := r.LaunchFor(refJSON(t, r, first))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := os.ReadFile(envOf(l.Env, "SOCRATES_TEST_VAULT"))
	lease1 := strings.Contains(string(vault), "SOCRATES_LEASE_POOL=1\nSOCRATES_LEASE_POOL_VALUE=u1:key-one-secret\n")
	lease2 := strings.Contains(string(vault), "SOCRATES_LEASE_POOL=2\nSOCRATES_LEASE_POOL_VALUE=u2:key-two-secret\n")
	if err != nil || lease1 == lease2 || strings.Contains(string(vault), "ACCOUNTS=") || strings.Contains(string(vault), "OTHER") {
		t.Fatalf("vault: %q %v", vault, err)
	}
	if len(l.Path) != 1 || !strings.HasSuffix(l.Path[0], "bin") || !strings.Contains(l.Instructions, "Work every cell carefully.") ||
		!strings.Contains(l.Instructions, "Run shot to take a screenshot.") {
		t.Fatalf("launch: %+v", l)
	}
	if envOf(l.Env, "SOCRATES_TEST_DIM_DEVICE") == "" {
		t.Fatalf("no dimension in %v", l.Env)
	}

	passCell(t, r, first)
	// Its end hook runs, its lease is released, and the third cell starts.
	waitFor(t, "the end hook", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.runs[run.ID].ending) == 0
	})
	r.schedule()
	waitFor(t, "the third session", func() bool { return len(host.chats()) == 3 })
	var a *Attempt
	for _, x := range r.attemptsOf(run.ID) {
		if x.Chat == first {
			a = x
		}
	}
	if a.Verdict != testtools.Pass || a.Summary != "all good" || len(a.Steps[0].Actions) != 1 || len(a.Steps[1].Actions) != 1 {
		t.Fatalf("record: %+v", a)
	}
	if strings.Contains(a.Actions[0].Summary, "key-one-secret") || !strings.Contains(a.Actions[0].Summary, "‹ACCOUNTS›") {
		t.Fatalf("the secret was not redacted: %q", a.Actions[0].Summary)
	}
	if _, err := os.Stat(filepath.Join(r.layout.EvidenceDir(a), "ended.txt")); err != nil {
		t.Fatalf("the end hook did not run: %v", err)
	}
	if _, err := os.Stat(r.layout.RuntimeDir(a.ID)); !os.IsNotExist(err) {
		t.Fatalf("the runtime folder is still there: %v", err)
	}

	for _, chat := range host.chats()[1:] {
		passCell(t, r, chat)
	}
	waitFor(t, "the run to be done", func() bool { r.schedule(); return r.runState(run.ID) == RunDone })
	waitFor(t, "the push", func() bool { return len(host.notesNow()) == 1 })
	if notes := host.notesNow(); !strings.Contains(notes[0], "3 cells: 3 pass") {
		t.Fatalf("notes: %v", notes)
	}
}

func TestAJudgementNeedsAScreenshot(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	startRun(t, r, fixture(t, map[string]string{"two": twoSteps}), noReview())
	r.schedule()
	waitFor(t, "a session", func() bool { return len(host.chats()) > 0 })
	chat := host.chats()[0]
	if text, isErr := call(t, r, chat, testtools.Judge, map[string]any{"step": 1, "verdict": "pass", "observed": "x"}); !isErr || !strings.Contains(text, "screenshot") {
		t.Fatalf("a pass without a screenshot: %q %v", text, isErr)
	}
	// Blocked needs none.
	mustCall(t, r, chat, testtools.Judge, map[string]any{"step": 1, "verdict": "blocked", "observed": "the device is gone"})
	if text, isErr := call(t, r, chat, testtools.End, map[string]any{"summary": "x"}); !isErr || !strings.Contains(text, "2") {
		t.Fatalf("an end with a step open: %q", text)
	}
	if text, isErr := call(t, r, chat, testtools.Judge, map[string]any{"rest": true, "verdict": "pass", "observed": "x"}); !isErr {
		t.Fatalf("the rest judged pass: %q", text)
	}
	text := mustCall(t, r, chat, testtools.Steps, nil)
	if !strings.Contains(text, "Verdict: blocked") || !strings.Contains(text, "Next: step 2") {
		t.Fatalf("steps: %s", text)
	}
}

func TestABlockedCellIsRetriedOnce(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	one := strings.Replace(twoSteps, "device: all", "device: [a]", 1)
	run := startRun(t, r, fixture(t, map[string]string{"one": one}), noReview())
	r.schedule()
	waitFor(t, "a session", func() bool { return len(host.chats()) == 1 })
	mustCall(t, r, host.chats()[0], testtools.Abort, map[string]any{"reason": "the device died"})
	waitFor(t, "the retry", func() bool { r.schedule(); return len(host.chats()) == 2 })
	if !strings.Contains(host.spec(host.chats()[1]).Message, "Attempt: 2") {
		t.Fatalf("second message: %s", host.spec(host.chats()[1]).Message)
	}
	mustCall(t, r, host.chats()[1], testtools.Abort, map[string]any{"reason": "again"})
	waitFor(t, "the run to end", func() bool { r.schedule(); return r.runState(run.ID) == RunDone })
	if len(host.chats()) != 2 {
		t.Fatalf("retried more than once: %d sessions", len(host.chats()))
	}
	list := r.attemptsOf(run.ID)
	if len(list) != 2 || list[0].Retried != list[1].ID || list[1].Verdict != testtools.Blocked ||
		list[1].Steps[0].Observed != "not reached: again" {
		t.Fatalf("attempts: %+v %+v", list[0], list[1])
	}
	cells, _ := r.Cells(run.ID)
	if len(cells) != 1 || cells[0].N != 2 {
		t.Fatalf("cells: %+v", cells)
	}
}

func TestATurnThatEndsWithoutTheEndIsNudgedThenAborted(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	one := strings.Replace(twoSteps, "device: all", "device: [a]", 1)
	s := noReview()
	s.Attempts = 1
	run := startRun(t, r, fixture(t, map[string]string{"one": one}), s)
	r.schedule()
	waitFor(t, "a session", func() bool { return len(host.chats()) == 1 })
	chat := host.chats()[0]
	r.Observe(chat, Observation{TurnRunning: true})
	r.Observe(chat, Observation{Idle: true})
	waitFor(t, "the nudge", func() bool { return len(host.msgs(chat)) == 2 })
	if msgs := host.msgs(chat); len(msgs) != 2 || !strings.Contains(msgs[1], "steps 1, 2 are not judged") {
		t.Fatalf("nudge: %v", msgs)
	}
	r.Observe(chat, Observation{TurnRunning: true})
	r.Observe(chat, Observation{Idle: true})
	a := r.attemptsOf(run.ID)[0]
	if a.State != AttemptAborted || a.Verdict != testtools.Blocked {
		t.Fatalf("attempt: %s %s", a.State, a.Verdict)
	}
}

func TestReviewAndAnalysis(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	one := strings.Replace(twoSteps, "device: all", "device: [a]", 1)
	s := DefaultSettings()
	run := startRun(t, r, fixture(t, map[string]string{"one": one}), &s)
	r.schedule()
	waitFor(t, "a session", func() bool { return len(host.chats()) == 1 })
	cell := host.chats()[0]
	act(t, r, cell, "open", 1)
	mustCall(t, r, cell, testtools.Judge, map[string]any{"step": 1, "verdict": "issue", "observed": "the logo overlaps"})
	act(t, r, cell, "tap", 2)
	mustCall(t, r, cell, testtools.Judge, map[string]any{"step": 2, "verdict": "pass", "observed": "it works"})
	mustCall(t, r, cell, testtools.End, map[string]any{"summary": "works, logo overlaps"})

	waitFor(t, "the reviewer", func() bool { r.schedule(); return len(host.chats()) == 2 })
	rev := host.chats()[1]
	if host.spec(rev).Ref.Kind != testtools.KindReviewer {
		t.Fatalf("second session: %+v", host.spec(rev))
	}
	list := mustCall(t, r, rev, testtools.ReviewList, nil)
	cellID := "web/one@a"
	if !strings.Contains(list, cellID) || !strings.Contains(list, "judged it issue") {
		t.Fatalf("review list: %s", list)
	}
	ev := mustCall(t, r, rev, testtools.ReviewEvidence, map[string]any{"cell": cellID})
	if strings.Contains(ev, "the logo overlaps") || !strings.Contains(ev, "01-open.png") {
		t.Fatalf("evidence holds the agent's verdicts or lacks the screenshot: %s", ev)
	}
	mustCall(t, r, rev, testtools.ReviewStep, map[string]any{"cell": cellID, "step": 1, "verdict": "pass", "note": "nothing overlaps"})
	mustCall(t, r, rev, testtools.ReviewDone, map[string]any{"cell": cellID, "note": "step 1 is a pass"})
	r.Observe(rev, Observation{TurnRunning: true})
	r.Observe(rev, Observation{Idle: true})

	waitFor(t, "the analyst", func() bool { r.schedule(); return len(host.chats()) == 3 })
	an := host.chats()[2]
	over := mustCall(t, r, an, testtools.RunOverview, nil)
	if !strings.Contains(over, cellID+" (Two steps, P0): pass (agent: issue)") {
		t.Fatalf("overview: %s", over)
	}
	mustCall(t, r, an, testtools.BugsPut, map[string]any{"bugs": []map[string]any{{"id": "B1", "title": "Nothing", "severity": "cosmetic",
		"cells": []string{cellID, "nope@x"}, "description": "none"}}})
	r.Observe(an, Observation{TurnRunning: true})
	r.Observe(an, Observation{Idle: true})
	waitFor(t, "the analyst's nudge", func() bool { return len(host.msgs(an)) == 2 })
	if msgs := host.msgs(an); len(msgs) != 2 || !strings.Contains(msgs[1], "conclusions") {
		t.Fatalf("analyst nudge: %v", msgs)
	}
	mustCall(t, r, an, testtools.ConclusionsPut, map[string]any{"markdown": "# Fine"})
	r.Observe(an, Observation{TurnRunning: true})
	r.Observe(an, Observation{Idle: true})
	waitFor(t, "the run to be done", func() bool { r.schedule(); return r.runState(run.ID) == RunDone })
	bugs, md, _ := r.Bugs(run.ID)
	if len(bugs) != 1 || len(bugs[0].Cells) != 1 || !strings.Contains(md, "# Fine") {
		t.Fatalf("bugs %+v, conclusions %q", bugs, md)
	}
	a := r.attemptsOf(run.ID)[0]
	if a.Final() != testtools.Pass || a.Verdict != testtools.Issue || len(a.Bugs) != 1 {
		t.Fatalf("attempt after review: %+v", a)
	}
	if hits := r.Search(run.Repo, "nothing overlaps", 10); len(hits) != 1 {
		t.Fatalf("search: %+v", hits)
	}
	if h := r.History(run.Repo, 10, ""); len(h["web/one"]) != 1 || h["web/one"][0].Verdict != testtools.Pass {
		t.Fatalf("history: %+v", h)
	}
}

func TestARestartTakesTheRunUp(t *testing.T) {
	host := newFakeHost()
	data := t.TempDir()
	r := New(data, host)
	r.Start()
	one := strings.Replace(twoSteps, "device: all", "device: [a, b]", 1)
	run := startRun(t, r, fixture(t, map[string]string{"one": one}), noReview())
	r.schedule()
	waitFor(t, "two sessions", func() bool { return len(host.chats()) == 2 })
	chat := host.chats()[0]
	l, _ := r.LaunchFor(refJSON(t, r, chat))
	vault := envOf(l.Env, "SOCRATES_TEST_VAULT")
	r.Close()
	os.Remove(vault)

	r2 := New(data, host)
	r2.Start()
	defer r2.Close()
	waitFor(t, "the vault again", func() bool { _, err := os.Stat(vault); return err == nil })
	if st := r2.runState(run.ID); st != RunRunning {
		t.Fatalf("state after restart: %s", st)
	}
	passCell(t, r2, chat)
	for _, a := range r2.attemptsOf(run.ID) {
		if a.Chat == chat && a.Verdict != testtools.Pass {
			t.Fatalf("attempt after restart: %+v", a)
		}
	}
}

func TestStopAndDelete(t *testing.T) {
	host := newFakeHost()
	r, data := newRunner(t, host)
	run := startRun(t, r, fixture(t, map[string]string{"two": twoSteps}), noReview())
	r.schedule()
	waitFor(t, "two sessions", func() bool { return len(host.chats()) == 2 })
	if err := r.StopRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if st := r.runState(run.ID); st != RunStopped {
		t.Fatalf("state: %s", st)
	}
	for _, a := range r.attemptsOf(run.ID) {
		if a.State != AttemptStopped {
			t.Fatalf("attempt %s is %s", a.CellID, a.State)
		}
	}
	waitFor(t, "the stops", func() bool { return len(host.stoppedNow()) == 2 })
	if stopped := host.stoppedNow(); len(stopped) != 2 {
		t.Fatalf("stopped: %v", stopped)
	}
	if err := r.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removals", func() bool { return len(host.removedNow()) == 2 })
	if removed := host.removedNow(); len(removed) != 2 {
		t.Fatalf("removed: %v", removed)
	}
	if _, err := os.Stat(filepath.Join(data, "tests", run.Repo, "runs", run.ID)); !os.IsNotExist(err) {
		t.Fatalf("the run's folder is still there: %v", err)
	}
}

func TestExclusiveNamesRunOneAtATime(t *testing.T) {
	host := newFakeHost()
	r, _ := newRunner(t, host)
	ex := strings.Replace(twoSteps, "device: all", "device: [a, b]\nexclusive: [premium]", 1)
	run := startRun(t, r, fixture(t, map[string]string{"ex": ex}), noReview())
	r.schedule()
	waitFor(t, "one session", func() bool { return len(host.chats()) == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := len(host.chats()); n != 1 {
		t.Fatalf("%d sessions for one exclusive name", n)
	}
	for _, a := range r.attemptsOf(run.ID) {
		if a.State == AttemptWaiting && a.Wait != "waiting for the fixture premium" {
			t.Fatalf("wait: %q", a.Wait)
		}
	}
}

func TestEvidenceRefusesLinksAndPaths(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ok.png"), string(pngBytes), 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "logs", "a.txt"), "log", 0o644)
	for name, ok := range map[string]bool{"ok.png": true, "link.png": false, "../x": false, "/etc/passwd": false,
		"logs/a.txt": true, "logs/../ok.png": false, ".hidden": false, "a/b/c": false} {
		if _, err := EvidenceFile(dir, name); (err == nil) != ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	a := &Attempt{}
	lines := `{"action":"open","summary":"s","screenshot":"link.png"}` + "\n" + `{"action":"x","summary":"y","screenshot":"ok.png"}` + "\n" + `{"partial":`
	write(t, filepath.Join(dir, ActionsFile), lines, 0o644)
	if n, err := ingest(a, dir, nil); err != nil || n != 2 {
		t.Fatalf("ingest: %d %v", n, err)
	}
	if a.Actions[0].Screenshot != "" || a.Actions[1].Screenshot != "ok.png" {
		t.Fatalf("actions: %+v", a.Actions)
	}
	if name, err := attach(dir, "/etc/hostname", 1); err == nil && !strings.HasPrefix(name, "attach-01-") {
		t.Fatalf("attach: %s", name)
	}
	if _, err := attach(dir, filepath.Join(dir, "link.png"), 2); err == nil {
		t.Fatal("a link was attached")
	}
}

func TestRedactor(t *testing.T) {
	red := NewRedactor(map[string]string{"ACCOUNTS": "alice:longsecretkey,bob:otherkey99", "SHORT": "abc"})
	got := red.String("alice:longsecretkey and longsecretkey and otherkey99 and abc")
	if got != "‹ACCOUNTS› and ‹ACCOUNTS› and ‹ACCOUNTS› and abc" {
		t.Fatalf("%q", got)
	}
	raw := red.JSON(json.RawMessage(`{"k":"otherkey99"}`))
	if string(raw) != `{"k":"‹ACCOUNTS›"}` {
		t.Fatalf("%s", raw)
	}
}

// A restart reconciles the running sessions with what they did meanwhile:
// one whose turn ended is nudged, one that is gone ends its cell.
func TestARestartReconcilesTheSessions(t *testing.T) {
	host := newFakeHost()
	data := t.TempDir()
	r := New(data, host)
	r.Start()
	one := strings.Replace(twoSteps, "device: all", "device: [a, b]", 1)
	s := noReview()
	s.Attempts = 1
	run := startRun(t, r, fixture(t, map[string]string{"one": one}), s)
	r.schedule()
	waitFor(t, "two sessions", func() bool { return len(host.chats()) == 2 })
	r.Close()
	idle, gone := host.chats()[0], host.chats()[1]
	host.mu.Lock()
	delete(host.sessions, gone)
	host.mu.Unlock()

	r2 := New(data, host)
	r2.Start()
	defer r2.Close()
	waitFor(t, "the nudge of the idle session", func() bool { return len(host.msgs(idle)) == 2 })
	waitFor(t, "the gone session's cell to end", func() bool {
		for _, a := range r2.attemptsOf(run.ID) {
			if a.Chat == gone {
				return a.State == AttemptAborted && strings.Contains(a.EndReason, "gone")
			}
		}
		return false
	})
	// The idle session's cell goes on: it can still be ended.
	passCell(t, r2, idle)
}
