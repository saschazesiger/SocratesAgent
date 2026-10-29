package testmode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// chatHost is a fakeHost that can tell chats (ChatHost); the chats in gone
// are no longer there.
type chatHost struct {
	*fakeHost
	mu   sync.Mutex
	told []string // chat|key|text
	gone map[string]bool
}

func newChatHost() *chatHost { return &chatHost{fakeHost: newFakeHost(), gone: map[string]bool{}} }

func (h *chatHost) TellChat(_ context.Context, chat, key, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gone[chat] {
		return ErrNotFound
	}
	h.told = append(h.told, chat+"|"+key+"|"+text)
	return nil
}

func (h *chatHost) toldNow() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.told...)
}

const failFirst = `---
title: Fails at first
priority: P1
device: [a]
---
## Steps

1. Open the page.
   Expect: the page.
2. Press the button.
   Expect: it works.
`

// failCell works a cell through with its first step failed.
func failCell(t *testing.T, r *Runner, chat string) {
	t.Helper()
	act(t, r, chat, "open", 1)
	mustCall(t, r, chat, testtools.Judge, map[string]any{"step": 1, "verdict": "fail", "observed": "an error page instead"})
	act(t, r, chat, "tap", 2)
	mustCall(t, r, chat, testtools.Judge, map[string]any{"step": 2, "verdict": "pass", "observed": "it works"})
	mustCall(t, r, chat, testtools.End, map[string]any{"summary": "the page fails"})
}

// startChatRun starts a run for the chat "chat-x" with the repository's
// settings made quick (no review, no analysis), and waits until it runs.
func startChatRun(t *testing.T, r *Runner, repo string, sel Selection) *ChatRun {
	t.Helper()
	// A git repository, with the chat in a folder below its root.
	for _, dir := range []string{".git", "sub"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.AddRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetSettings(got.Key, *noReview()); err != nil {
		t.Fatal(err)
	}
	c, err := r.StartChatRun(filepath.Join(repo, "sub"), "from a chat", sel, StartedBy{Chat: "chat-x", Title: "Fix the page", Told: 99})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to be prepared", func() bool { return r.runState(c.Run.ID) != RunPreparing })
	return c
}

// A run a chat started remembers the chat, and when it ends the chat is
// told once: the state, the counts, each problem cell with its step and
// observation, and where to read more. A restart does not tell it again.
func TestAChatsRunTellsTheChatOnceWhenItEnds(t *testing.T) {
	host := newChatHost()
	data := t.TempDir()
	r := New(data, host)
	r.Start()
	repo := fixture(t, map[string]string{"two": twoSteps, "fails": failFirst})
	c := startChatRun(t, r, repo, Selection{Tests: []string{"web/fails", "web/two"}, Dims: map[string][]string{"device": {"a"}}})
	if c.Cells != 2 || c.Run.StartedBy == nil || c.Run.StartedBy.Chat != "chat-x" || c.Run.StartedBy.Told != 0 || c.Behind != "" {
		t.Fatalf("started: %+v %+v", c, c.Run.StartedBy)
	}
	r.schedule()
	waitFor(t, "two sessions", func() bool { return len(host.chats()) == 2 })
	for _, chat := range host.chats() {
		if strings.Contains(host.spec(chat).Message, "Fails at first") {
			failCell(t, r, chat)
		} else {
			passCell(t, r, chat)
		}
	}
	waitFor(t, "the run to be done", func() bool { r.schedule(); return r.runState(c.Run.ID) == RunDone })
	waitFor(t, "the chat to be told", func() bool { return len(host.toldNow()) == 1 })
	told := host.toldNow()[0]
	for _, want := range []string{"chat-x|tests-" + c.Run.ID + "|", "[From Socrates Tests:", "Run " + c.Run.ID + " (“from a chat”) of Fixture: done.",
		"2 cells: 1 pass, 1 fail", "- web/fails@a (fail): step 1: an error page instead", "test_results"} {
		if !strings.Contains(told, want) {
			t.Errorf("the message lacks %q:\n%s", want, told)
		}
	}
	if lines := strings.Count(told, "\n"); lines > 16 {
		t.Errorf("the message has %d lines", lines)
	}
	detail, _ := r.RunDetail(c.Run.ID)
	if detail.StartedBy == nil || detail.StartedBy.Told == 0 || detail.StartedBy.Title != "Fix the page" {
		t.Fatalf("the run's view: %+v", detail.StartedBy)
	}

	// The report names the failing step, its observation and its
	// screenshot by its path.
	report, err := r.Report(c.Run.ID, ReportOptions{Paths: true, ProblemsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "web/fails@a") || !strings.Contains(report, "Observed: an error page instead") ||
		!strings.Contains(report, filepath.Join(data, "tests")) || !strings.Contains(report, "01-open.png") || strings.Contains(report, "web/two@a") {
		t.Fatalf("report:\n%s", report)
	}
	if full, _ := r.Report(c.Run.ID, ReportOptions{}); !strings.Contains(full, "web/two@a") || strings.Contains(full, filepath.Join(data, "tests")) {
		t.Fatalf("the full report:\n%s", full)
	}

	// After a restart the run is loaded told: nobody is told again.
	r.Close()
	r2 := New(data, host)
	r2.Start()
	r2.mu.Lock()
	r2.tellStartersLocked()
	r2.mu.Unlock()
	r2.Close()
	if n := len(host.toldNow()); n != 1 {
		t.Fatalf("told %d times", n)
	}
}

// A run that ended while Socrates went down before its chat was told is
// told when the runner starts again; a chat that is gone is not, and the
// run still counts as told.
func TestAChatsRunEndedBeforeARestartIsToldAtTheStart(t *testing.T) {
	host := newChatHost()
	data := t.TempDir()
	r := New(data, host)
	r.Start()
	repo := fixture(t, map[string]string{"two": twoSteps})
	c := startChatRun(t, r, repo, Selection{Cells: []string{"web/two@b"}})
	if c.Cells != 1 {
		t.Fatalf("cells %d", c.Cells)
	}
	if err := r.StopRun(c.Run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the chat to be told", func() bool { return len(host.toldNow()) == 1 })
	if told := host.toldNow()[0]; !strings.Contains(told, ": stopped.") {
		t.Fatalf("message: %s", told)
	}
	r.Close()

	// The run as it would be had the restart come before the message.
	path := filepath.Join(r.layout.RunDir(c.Run.Repo, c.Run.ID), "run.json")
	var run Run
	if err := readJSON(path, &run); err != nil {
		t.Fatal(err)
	}
	run.StartedBy.Told = 0
	if err := writeJSON(path, &run); err != nil {
		t.Fatal(err)
	}
	r2 := New(data, host)
	r2.Start()
	waitFor(t, "the chat to be told again after the restart", func() bool { return len(host.toldNow()) == 2 })
	r2.Close()
	if err := readJSON(path, &run); err != nil || run.StartedBy.Told == 0 {
		t.Fatalf("not kept as told: %+v %v", run.StartedBy, err)
	}

	// A chat that is gone: nothing is sent, and the run is told all the same.
	run.StartedBy.Told = 0
	_ = writeJSON(path, &run)
	host.gone["chat-x"] = true
	r3 := New(data, host)
	r3.Start()
	waitFor(t, "the run to be marked told", func() bool {
		var now Run
		return readJSON(path, &now) == nil && now.StartedBy.Told != 0
	})
	r3.Close()
	if n := len(host.toldNow()); n != 2 {
		t.Fatalf("told %d times", n)
	}
}

// run_tests refuses what a run would fail on at once, with words an agent
// can act on, and starts nothing.
func TestAChatsRunIsRefusedWhenItCannotRun(t *testing.T) {
	host := newChatHost()
	r := New(t.TempDir(), host)
	r.Start()
	t.Cleanup(r.Close)
	if _, err := r.StartChatRun(t.TempDir(), "", Selection{}, StartedBy{Chat: "c"}); !errors.Is(err, ErrNoCatalog) {
		t.Fatalf("no catalog: %v", err)
	}
	repo := fixture(t, map[string]string{"two": twoSteps})
	cases := []struct {
		sel  Selection
		want string
	}{
		{Selection{Tests: []string{"web/nope"}}, `there is no test "web/nope"`},
		{Selection{Areas: []string{"api"}}, `there is no area "api" (the areas: web)`},
		{Selection{Cells: []string{"web/two@z"}}, `there is no cell "web/two@z"`},
		{Selection{Priorities: []string{"P9"}}, `"P9" is no priority`},
		{Selection{Dims: map[string][]string{"device": {"z"}}}, `"z" is no value of device (a, b, c)`},
		{Selection{Dims: map[string][]string{"locale": {"de"}}}, `no area has a dimension "locale"`},
		{Selection{Priorities: []string{"P2"}}, "takes no cell of the catalog"},
		{Selection{ProblemsOf: ProblemsLatest}, "has no run that ended yet"},
		{Selection{ProblemsOf: "r-20261029-000000-abcd"}, "there is no run r-20261029-000000-abcd"},
	}
	for _, tc := range cases {
		if _, err := r.StartChatRun(repo, "", tc.sel, StartedBy{Chat: "c"}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.sel, err, tc.want)
		}
	}
	write(t, filepath.Join(repo, ".socrates", "tests", "web", "broken.md"), "---\ntitle: Broken\n---\nNo steps.\n", 0o644)
	if _, err := r.StartChatRun(repo, "", Selection{}, StartedBy{Chat: "c"}); err == nil ||
		!strings.Contains(err.Error(), "the catalog has errors") || !strings.Contains(err.Error(), ".socrates/tests/web/broken.md") {
		t.Fatalf("errors: %v", err)
	}
	if len(r.Runs(RepoKey(repo))) != 0 {
		t.Fatal("a refused run was started")
	}
	if _, err := os.Stat(filepath.Join(repo, ".socrates")); err != nil {
		t.Fatal(err)
	}
}

// problems_of latest is the repository's latest run that ended, and only
// its cells that did not pass run again; test_results' "latest" is the
// newest run, and a run of another repository is refused.
func TestAChatsRunOfTheLatestProblems(t *testing.T) {
	host := newChatHost()
	r := New(t.TempDir(), host)
	r.Start()
	t.Cleanup(r.Close)
	repo := fixture(t, map[string]string{"two": twoSteps, "fails": failFirst})
	first := startChatRun(t, r, repo, Selection{Dims: map[string][]string{"device": {"a"}}})
	r.schedule()
	waitFor(t, "two sessions", func() bool { return len(host.chats()) == 2 })
	for _, chat := range host.chats() {
		if strings.Contains(host.spec(chat).Message, "Fails at first") {
			failCell(t, r, chat)
		} else {
			passCell(t, r, chat)
		}
	}
	waitFor(t, "the run to be done", func() bool { r.schedule(); return r.runState(first.Run.ID) == RunDone })
	again, err := r.StartChatRun(repo, "", Selection{ProblemsOf: ProblemsLatest}, StartedBy{Chat: "chat-x"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Cells != 1 || again.Run.Selection.ProblemsOf != first.Run.ID {
		t.Fatalf("again: %d cells, problems of %q", again.Cells, again.Run.Selection.ProblemsOf)
	}
	if id, err := r.ChatRunID(repo, ""); err != nil || id != again.Run.ID {
		t.Fatalf("latest: %s %v", id, err)
	}
	if id, err := r.ChatRunID(filepath.Join(repo, "sub"), first.Run.ID); err != nil || id != first.Run.ID {
		t.Fatalf("by id: %s %v", id, err)
	}
	if _, err := r.ChatRunID(t.TempDir(), first.Run.ID); err == nil {
		t.Fatal("a run of another repository was given")
	}
}
