//go:build !windows

package termux

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The tasks state: a turn that has ended while something it started - a
// background shell, a monitor, a background sub-agent - is still running.
// The screens below are verbatim captures of Claude Code 2.1.281 and Codex
// 0.158.0 in exactly that situation.

const (
	claudeShellFooter = "✻ Baked for 4s · done 12:55 PM · 1 shell still running\n" +
		"────────────────────────────────────────\n" +
		"❯ check on it when it's done\n" +
		"────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on · 1 shell · ← for agents · ↓ to manage"
	claudeMixedFooter = "✻ Sautéed for 4s · done 1:00 PM · 2 shells, 1 monitor still running\n" +
		"────────────────────────────────────────\n" +
		"❯ check on the background tasks\n" +
		"────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on · 2 shells, 1 monitor · ← for agents · ↓ to manage"
	// The same session once the shell has ended and Claude has had its say:
	// the transcript line lost its suffix, the bar lost its pill.
	claudeDoneFooter = "● The background sleep task completed.\n" +
		"✻ Crunched for 1s · done 12:58 PM\n" +
		"────────────────────────────────────────\n" +
		"❯ check on it when it's done\n" +
		"────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents"
	codexTerminal = "• I’ll start sleep 150 in a background terminal.\n" +
		"• OK\n" +
		"  1:01 PM\n" +
		"  1 background terminal running · /ps to view · /stop to close\n" +
		"› Ask Codex to do anything\n" +
		"  GPT-6-Astra medium · /tmp/cx · St…  ⚠ 1 warning · f2 to view"
	codexNoTerminal = "• I’ll start sleep 150 in a background terminal.\n" +
		"• OK\n" +
		"  1:01 PM\n" +
		"› Ask Codex to do anything\n" +
		"  GPT-6-Astra medium · /tmp/cx · St…  ⚠ 1 warning · f2 to view"
)

func TestScrapeBackground(t *testing.T) {
	cases := []struct {
		name   string
		kind   harnesses.Kind
		screen string
		want   bool
	}{
		{"claude one shell", harnesses.KindClaude, claudeShellFooter, true},
		{"claude shells and a monitor", harnesses.KindClaude, claudeMixedFooter, true},
		{"claude one monitor", harnesses.KindClaude,
			"❯ wait for the monitor to report\n──────\n  ⏵⏵ bypass permissions on · 1 monitor · ← for agents · ↓ to manage", true},
		{"claude manual mode", harnesses.KindClaude,
			"❯ \n──────\n  ⏸ manual mode on · 2 shells, 1 monitor · ← for agents · ↓ to manage", true},
		{"claude accept edits, narrow", harnesses.KindClaude,
			"❯ check on the background tasks\n──────\n  ⏵⏵ accept edits on · 2 shells, 1 monitor · ← for agents…", true},
		// A sub-agent sent off in the background is listed below the bar.
		{"claude with its agents listed below the bar", harnesses.KindClaude,
			"✻ Baked for 12s · done 1:27 PM · 1 shell still running\n──────\n❯ wait for it to finish\n──────\n" +
				"  ⏵⏵ bypass permissions on · 1 shell · ← for agents · ↓ to manage\n" +
				"  ● main\n  ◯ general-purpose  Sleep 60 then reply      7s · ↓ 33.4k tok", true},
		// No mode on the bar: the pill is the first thing on it.
		{"claude bar with no mode", harnesses.KindClaude, "❯ \n──────\n  1 shell · ↓ to manage", true},
		// A diff or a file that quotes the bar is not the bar.
		{"claude quoting the bar", harnesses.KindClaude,
			"  42 +      \"  ⏸ manual mode on · 2 shells, 1 monitor · ← for agents\", true},\n" +
				"❯ \n──────\n  ⏵⏵ bypass permissions on · ← for agents", false},
		{"claude after the shell ended", harnesses.KindClaude, claudeDoneFooter, false},
		// The transcript line on its own is history, not the bar.
		{"claude transcript only", harnesses.KindClaude,
			"✻ Baked for 4s · done 12:55 PM · 1 shell still running\n──────\n❯ \n──────\n  ⏵⏵ bypass permissions on · ← for agents", false},
		{"claude prose about shells", harnesses.KindClaude,
			"● I started 2 shells and they are running.\n❯ \n  ⏵⏵ bypass permissions on · ← for agents", false},
		{"codex background terminal", harnesses.KindCodex, codexTerminal, true},
		{"codex two terminals", harnesses.KindCodex,
			"• OK\n  2 background terminals running · /ps to view · /stop to close\n› Ask Codex to do anything\n  footer", true},
		{"codex after the terminal ended", harnesses.KindCodex, codexNoTerminal, false},
		{"codex line far up the history", harnesses.KindCodex,
			"  1 background terminal running · /ps to view\n" + strings.Repeat("• line\n", 12) + "› Ask Codex to do anything", false},
		{"opencode has no such line", harnesses.KindOpenCode, codexTerminal, false},
		{"shell has no such line", harnesses.KindShell, claudeShellFooter, false},
	}
	for _, c := range cases {
		if got := scrapeBackground(c.kind, c.screen); got != c.want {
			t.Errorf("%s: scrapeBackground = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestActivityClaudeShellStatusIsTasks: Claude Code writes `"status":"shell"`
// into its session file when its turn is over and a background shell or a
// Monitor is still running.
func TestActivityClaudeShellStatusIsTasks(t *testing.T) {
	// The status files are looked for under the plan's HOME; a
	// CLAUDE_CONFIG_DIR inherited from whatever runs the tests - a Claude Code
	// session, for one - would win over it.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	raw, _ := json.Marshal(map[string]any{"pid": self, "status": "shell"})
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(self)+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got := newClaudeSource().Read(context.Background(), snapshot{
		row:  store.Session{ID: "s1"},
		pane: paneLine{pid: self},
		plan: harnesses.LaunchPlan{Env: map[string]string{"HOME": home}},
		now:  time.Now(),
	})
	if !got.ok || got.state != StateTasks {
		t.Fatalf("a shell status read as %#v, want tasks", got)
	}
}

// TestActivityTasksIsNotAFinishedTurn: the turn ending with a shell still
// running sets no unread mark - nothing has finished - and the mark, the
// chime that hangs on it and the row moving up all come when the work behind
// it is over and the agent has said its last word.
func TestActivityTasksIsNotAFinishedTurn(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	panes := func() map[string]paneLine { return b.pane(row, 0) }
	b.m.NoteTyped("s1")

	b.source(harnesses.KindClaude, seen(StateBusy, ""))
	b.tick(rows, panes())
	b.want("s1", StateBusy)

	// One stray tasks answer does not commit: it settles like idle does.
	b.source(harnesses.KindClaude, seen(StateTasks, ""), seen(StateBusy, ""))
	b.ticks(2, rows, panes)
	b.want("s1", StateBusy)

	b.source(harnesses.KindClaude, seen(StateTasks, ""))
	b.until("s1", StateTasks, IdleSettle+1, rows, panes)
	if a := b.m.ActivityOf("s1"); a.Unread {
		t.Fatalf("a turn that left a shell running marked itself finished: %#v", a)
	}
	movedAt := b.at().Add(-ActivityInterval).UnixMilli()
	if got, _ := b.st.GetSession("s1"); got == nil || got.ActiveAt != movedAt {
		t.Fatalf("the end of the turn did not move the row: %#v", got)
	}

	// The shell reports back, Claude takes the turn up again and finishes.
	b.source(harnesses.KindClaude, seen(StateBusy, ""))
	b.until("s1", StateBusy, 2, rows, panes)
	if a := b.m.ActivityOf("s1"); a.Unread {
		t.Fatalf("taking the turn up again is not finishing: %#v", a)
	}
	b.source(harnesses.KindClaude, seen(StateIdle, ""))
	b.until("s1", StateIdle, IdleSettle+1, rows, panes)
	if a := b.m.ActivityOf("s1"); !a.Unread {
		t.Fatalf("the real end of the work is unread, got %#v", a)
	}
	if got := b.states(); fmt.Sprint(got) != "[busy tasks busy idle]" {
		t.Fatalf("the callback saw %v, want [busy tasks busy idle]", got)
	}
}

// TestActivityTasksEndingQuietlyIsFinished: Codex does not take its turn up
// again when a background terminal ends, it just takes the line away. That
// is the end of the work, and it is announced.
func TestActivityTasksEndingQuietlyIsFinished(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	panes := func() map[string]paneLine { return b.pane(row, 0) }
	b.m.NoteTyped("s1")
	b.source(harnesses.KindClaude, seen(StateBusy, ""))
	b.tick(rows, panes())
	b.source(harnesses.KindClaude, seen(StateTasks, ""))
	b.until("s1", StateTasks, IdleSettle+1, rows, panes)
	b.source(harnesses.KindClaude, seen(StateIdle, ""))
	b.until("s1", StateIdle, IdleSettle+1, rows, panes)
	if a := b.m.ActivityOf("s1"); !a.Unread {
		t.Fatalf("work that ended behind a finished turn is unread, got %#v", a)
	}

	// And a pane that dies while the work is still running is something that
	// ended and nobody saw, like one that dies mid-turn.
	b.m.MarkRead("s1")
	b.source(harnesses.KindClaude, seen(StateTasks, ""))
	b.until("s1", StateTasks, IdleSettle+1, rows, panes)
	dead := map[string]paneLine{row.TmuxName: {session: row.TmuxName, dead: true}}
	b.ticks(UnknownSettle, rows, func() map[string]paneLine { return dead })
	if a := b.m.ActivityOf("s1"); a.State != StateUnknown || !a.Unread {
		t.Fatalf("a pane that died with work running is unread, got %#v", a)
	}
}

// TestActivityStartUpIntoTasksIsNoNews: a session nobody has typed at, or a
// detector coming up after a restart to find a turn already over with a
// shell running, says nothing and moves nothing.
func TestActivityStartUpIntoTasksIsNoNews(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	before, _ := b.st.GetSession("s1")
	b.source(harnesses.KindClaude, seen(StateTasks, ""))
	b.until("s1", StateTasks, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	if mid, _ := b.st.GetSession("s1"); mid.ActiveAt != before.ActiveAt {
		t.Fatalf("finding a session already in tasks moved its row")
	}
	b.source(harnesses.KindClaude, seen(StateIdle, ""))
	b.until("s1", StateIdle, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	if a := b.m.ActivityOf("s1"); a.Unread {
		t.Fatalf("nobody asked for anything, yet it is unread: %#v", a)
	}
	// unknown -> tasks does not move the row; tasks -> idle does, as busy ->
	// idle does, because it is work ending.
	after, _ := b.st.GetSession("s1")
	if after.ActiveAt == before.ActiveAt {
		t.Fatalf("the end of the background work did not move the row")
	}
}

// TestActivityIdleScreenWithABackgroundTerminalIsTasks: Codex's title says
// idle while a background terminal runs; the screen says the rest. The
// screen is captured again only once the pane has printed something.
func TestActivityIdleScreenWithABackgroundTerminalIsTasks(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "codex")
	rows := []store.Session{row}
	var captures atomic.Int32
	inner := b.m.act.visible
	b.m.act.visible = func(ctx context.Context, id string) (string, error) {
		captures.Add(1)
		return inner(ctx, id)
	}
	b.m.NoteTyped("s1")
	b.source(harnesses.KindCodex, seen(StateBusy, ""))
	b.tick(rows, b.pane(row, 0))

	// The turn ends; the last output was the repaint that put the line up.
	b.setScreen(codexTerminal)
	b.source(harnesses.KindCodex, seen(StateIdle, ""))
	lastOutput := b.at()
	still := func() map[string]paneLine {
		return map[string]paneLine{row.TmuxName: {session: row.TmuxName, pid: os.Getpid(), activity: lastOutput.Unix()}}
	}
	b.until("s1", StateTasks, IdleSettle+2, rows, still)
	if a := b.m.ActivityOf("s1"); a.Source != sourceScreen || a.Unread {
		t.Fatalf("a background terminal read as %#v, want tasks from the screen, not unread", a)
	}

	// Minutes of silence: the guard does not touch it, and nothing more is
	// captured once the pane has been quiet since the last look.
	b.ticks(5, rows, still)
	settled := captures.Load()
	b.ticks(int(3*HardQuiet/ActivityInterval), rows, still)
	b.want("s1", StateTasks)
	if got := captures.Load(); got != settled {
		t.Fatalf("a silent pane was captured %d more times", got-settled)
	}

	// The terminal ends and Codex takes the line away: output, a new look,
	// and the end of the work, announced.
	b.setScreen(codexNoTerminal)
	b.until("s1", StateIdle, IdleSettle+2, rows, func() map[string]paneLine { return b.pane(row, 0) })
	if a := b.m.ActivityOf("s1"); !a.Unread {
		t.Fatalf("the background terminal ending is the work ending, got %#v", a)
	}
}

// TestActivityBackgroundLineSurvivesTheGuardWithoutAnExactLayer: with no
// exact signal at all, a Claude screen that is idle with a shell on its bar
// stays tasks however long it is quiet - the runaway guard is about spinners.
func TestActivityBackgroundLineSurvivesTheGuardWithoutAnExactLayer(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude) // no file at all
	b.setScreen(claudeShellFooter)
	quiet := func() map[string]paneLine { return b.pane(row, 2*HardQuiet) }
	b.until("s1", StateTasks, IdleSettle+3, rows, quiet)
	b.ticks(int(2*HardQuiet/ActivityInterval), rows, quiet)
	b.want("s1", StateTasks)
}

// TestActivityQuietIdleIsNotLookedBehind: an idle that is nothing but thirty
// seconds of silence - no exact answer, no screen anybody recognised - is
// not turned into tasks by a line that happens to be on the screen.
func TestActivityQuietIdleIsNotLookedBehind(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "codex")
	rows := []store.Session{row}
	b.source(harnesses.KindCodex) // no recognised title
	b.setScreen("  1 background terminal running · /ps to view · /stop to close\n  some dialog")
	b.until("s1", StateIdle, IdleSettle+3, rows, func() map[string]paneLine { return b.pane(row, 2*HardQuiet) })
}

// TestActivityWaitingBeatsTasks: a prompt with a shell still running behind
// it needs the user, and a tasks answer releases a waiting one like any
// exact answer does.
func TestActivityWaitingBeatsTasks(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	panes := func() map[string]paneLine { return b.pane(row, 0) }
	b.m.NoteTyped("s1")
	b.source(harnesses.KindClaude, seen(StateTasks, ""))
	b.until("s1", StateTasks, IdleSettle+1, rows, panes)
	b.source(harnesses.KindClaude, seen(StateWaiting, "permission prompt"))
	b.tick(rows, panes())
	if a := b.m.ActivityOf("s1"); a.State != StateWaiting || !a.Unread {
		t.Fatalf("a prompt is waiting and unread whatever runs behind it, got %#v", a)
	}
	b.source(harnesses.KindClaude, seen(StateTasks, ""))
	b.until("s1", StateTasks, IdleSettle+1, rows, panes)
}

// TestActivityTasksFilesAnUnsortedSessionAsInProgress: work left running is
// work, for the one-off sort of the sessions an upgrade left unsorted.
func TestActivityTasksFilesAnUnsortedSessionAsInProgress(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	// The store cannot write an unsorted status, so the row is filed for
	// review - which the detector never writes - and handed to the tick as
	// unsorted, as TestActivitySortsTheSessionsAnUpgradeLeftUnsorted does.
	if err := b.st.SetSessionStatus("s1", store.StatusReview); err != nil {
		t.Fatal(err)
	}
	unsorted := func() []store.Session {
		rows := b.fresh("s1")
		if rows[0].Status == store.StatusReview {
			rows[0].Status = store.StatusUnsorted
		}
		return rows
	}
	b.source(harnesses.KindClaude, seen(StateTasks, ""))
	for i := 0; i < IdleSettle; i++ {
		b.tick(unsorted(), b.pane(row, 0))
	}
	b.want("s1", StateTasks)
	if got := b.statusOf("s1"); got != store.StatusInProgress {
		t.Fatalf("an unsorted session with work running was filed as %q", got)
	}
}

// TestActivityWaitIdleAnswersOnTasks: the turn is over, so whoever waits for
// it to end is told.
func TestActivityWaitIdleAnswersOnTasks(t *testing.T) {
	b := newBench(t)
	row := b.row("s1", "claude")
	rows := []store.Session{row}
	b.source(harnesses.KindClaude, seen(StateBusy, ""))
	b.tick(rows, b.pane(row, 0))
	done := make(chan Activity, 1)
	go func() {
		a, _ := b.m.WaitIdle(context.Background(), "s1")
		done <- a
	}()
	time.Sleep(20 * time.Millisecond)
	b.source(harnesses.KindClaude, seen(StateTasks, ""))
	b.until("s1", StateTasks, IdleSettle+1, rows, func() map[string]paneLine { return b.pane(row, 0) })
	select {
	case a := <-done:
		if a.State != StateTasks {
			t.Fatalf("WaitIdle answered %#v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle did not answer on tasks")
	}
}

// TestActivityOpenCodeBackgroundSubAgentIsTasks: a sub-agent sent off in the
// background keeps working after its parent's turn is over. A sub-agent run
// in the foreground keeps its parent busy, so that is plain busy.
func TestActivityOpenCodeBackgroundSubAgentIsTasks(t *testing.T) {
	w := &openCodeWatcher{busy: map[string]bool{}, waiting: map[string]openCodePrompt{}}
	status := func(id, kind string) {
		w.event(fmt.Sprintf(`{"type":"session.status","properties":{"sessionID":%q,"status":{"type":%q}}}`, id, kind))
	}
	created := func(id, parent string) {
		info := map[string]any{"id": id}
		if parent != "" {
			info["parentID"] = parent
		}
		raw, _ := json.Marshal(map[string]any{"type": "session.created",
			"properties": map[string]any{"sessionID": id, "info": info}})
		w.event(string(raw))
	}

	created("ses_root", "")
	status("ses_root", "busy")
	created("ses_child", "ses_root")
	status("ses_child", "busy")
	if got := w.answer(); got.state != StateBusy {
		t.Fatalf("a parent and its foreground sub-agent read as %#v, want busy", got)
	}
	status("ses_root", "idle")
	if got := w.answer(); !got.ok || got.state != StateTasks {
		t.Fatalf("a background sub-agent under an idle parent read as %#v, want tasks", got)
	}
	// The sub-agent reports back and the parent takes the turn up.
	status("ses_root", "busy")
	if got := w.answer(); got.state != StateBusy {
		t.Fatalf("the parent working again read as %#v, want busy", got)
	}
	status("ses_child", "idle")
	status("ses_root", "idle")
	if got := w.answer(); got.state != StateIdle {
		t.Fatalf("everything idle read as %#v", got)
	}

	// A busy session nobody introduced counts as one of its own.
	status("ses_stranger", "busy")
	if got := w.answer(); got.state != StateBusy {
		t.Fatalf("a busy session of unknown parentage read as %#v, want busy", got)
	}
	// A deleted session will never say it is idle.
	w.event(`{"type":"session.deleted","properties":{"sessionID":"ses_stranger","info":{"id":"ses_stranger"}}}`)
	if got := w.answer(); got.state != StateIdle {
		t.Fatalf("a deleted busy session kept the answer at %#v", got)
	}
	// An update says the same as a creation.
	w.event(`{"type":"session.updated","properties":{"info":{"id":"ses_late","parentID":"ses_root"}}}`)
	status("ses_late", "busy")
	if got := w.answer(); got.state != StateTasks {
		t.Fatalf("a sub-agent introduced by an update read as %#v, want tasks", got)
	}
	// And a prompt outranks everything.
	w.event(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_late"}}`)
	if got := w.answer(); got.state != StateWaiting {
		t.Fatalf("a prompt read as %#v, want waiting", got)
	}
}

// TestOpenCodeWatcherLooksUpTheParentOfASeededSession: a sub-agent that was
// already working when the watcher came up was never introduced by the
// stream. The seed asks the server whose it is, and the stream asks about a
// session it sees working without having seen it created.
func TestOpenCodeWatcherLooksUpTheParentOfASeededSession(t *testing.T) {
	events := make(chan string, 8)
	var lookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "opencode" || pass != "shh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/session/status":
			_, _ = io.WriteString(w, `{"ses_child":{"type":"busy"}}`)
		case "/permission":
			_, _ = io.WriteString(w, `[]`)
		case "/session/ses_child":
			lookups.Add(1)
			_, _ = io.WriteString(w, `{"id":"ses_child","parentID":"ses_root","title":"Sleep"}`)
		case "/session/ses_old":
			lookups.Add(1)
			_, _ = io.WriteString(w, `{"id":"ses_old","parentID":"ses_root"}`)
		case "/event":
			flusher := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
			flusher.Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case event := <-events:
					_, _ = io.WriteString(w, "data: "+event+"\n\n")
					flusher.Flush()
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	w := startOpenCodeWatcher(harnesses.ServerAccess{Port: portOf(t, srv), Username: "opencode", Password: "shh"}, "/tmp/work")
	defer w.stop()
	waitFor(t, 10*time.Second, "the seeded sub-agent to read as tasks", func() bool {
		got := w.answer()
		return got.ok && got.state == StateTasks
	})

	events <- `{"type":"session.status","properties":{"sessionID":"ses_child","status":{"type":"idle"}}}`
	waitFor(t, 10*time.Second, "idle", func() bool { return w.answer().state == StateIdle })

	// A session from before the stream opened starts working: it is busy
	// until the lookup says it is a sub-agent.
	events <- `{"type":"session.status","properties":{"sessionID":"ses_old","status":{"type":"busy"}}}`
	waitFor(t, 10*time.Second, "the looked-up sub-agent to read as tasks", func() bool {
		return w.answer().state == StateTasks
	})
	events <- `{"type":"session.status","properties":{"sessionID":"ses_old","status":{"type":"busy"}}}`
	time.Sleep(100 * time.Millisecond)
	if got := lookups.Load(); got != 2 {
		t.Fatalf("the server was asked %d times, want once per session", got)
	}
}

// TestOpenCodeWatcherWithoutTheSessionLookup: a server that will not say
// whose a session is leaves it counting as one of its own - busy, which is
// what every busy session read as before anybody asked.
func TestOpenCodeWatcherWithoutTheSessionLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			_, _ = io.WriteString(w, `{"ses_x":{"type":"busy"}}`)
		case "/permission":
			_, _ = io.WriteString(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	w := startOpenCodeWatcher(harnesses.ServerAccess{Port: portOf(t, srv), Username: "opencode", Password: "shh"}, "/tmp/work")
	defer w.stop()
	waitFor(t, 10*time.Second, "busy", func() bool {
		got := w.answer()
		return got.ok && got.state == StateBusy
	})
}
