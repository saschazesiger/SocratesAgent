//go:build !windows

package codex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// noticesOf is every notice part of the turn's assistant message.
func noticesOf(p *agent.Projection, turnID string) []*agent.Part {
	var out []*agent.Part
	for _, m := range p.Messages {
		if m.Role != agent.RoleAssistant || m.TurnID != turnID {
			continue
		}
		var walk func([]*agent.Part)
		walk = func(ps []*agent.Part) {
			for _, pt := range ps {
				if pt.Kind == agent.PartNotice {
					out = append(out, pt)
				}
				walk(pt.Children)
			}
		}
		walk(m.Parts)
	}
	return out
}

// A goal the model sets is a notice where it was set (once, though codex
// says it again as it counts its tokens); the turn codex then starts by
// itself opens with the goal it goes on toward, and the goal's end says what
// it used. A resumed thread's snapshot of the goal is not said again; a goal
// cleared is.
func TestAGoalAndTheTurnsCodexStartsForIt(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	thread := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), wait).ConversationID

	msg, n := send(t, a, r, "/goal Ship the release\nwith the notes")
	first := r.turnEnd(t, n, wait)
	var own agent.Event
	r.waitFor(t, n, "the turn codex starts by itself", func(ev agent.Event) bool {
		if ev.Type == agent.TypeTurnStarted && ev.TurnID != first.TurnID {
			own = ev
			return true
		}
		return false
	}, wait)
	second := r.waitFor(t, n, "its end", func(ev agent.Event) bool {
		return ev.Type == agent.TypeTurnEnded && ev.TurnID == own.TurnID
	}, wait)
	if own.MessageID != "" || second.Outcome != agent.OutcomeCompleted {
		t.Fatalf("the turn of codex's own: %+v %+v", own, second)
	}
	p := r.projection()
	if p.Message(msg.MessageID) == nil {
		t.Fatal("no message")
	}
	set := noticesOf(p, first.TurnID)
	if len(set) != 1 || set[0].Text != "Goal set: Ship the release" || contentOf(set[0])["code"] != "goal" ||
		contentOf(set[0])["detail"] != "Ship the release\nwith the notes" {
		t.Fatalf("the goal set: %+v\n%s", set, dump(r.since(n)))
	}
	ownNotices := noticesOf(p, own.TurnID)
	if len(ownNotices) != 2 {
		t.Fatalf("the turn of codex's own has %d notices\n%s", len(ownNotices), dump(r.since(n)))
	}
	opener, done := ownNotices[0], ownNotices[1]
	if opener.Text != "Continuing toward the goal: Ship the release" || contentOf(opener)["code"] != "goal" ||
		contentOf(opener)["detail"] != "Ship the release\nwith the notes" {
		t.Fatalf("the opener %+v %s", opener, opener.Content)
	}
	for _, m := range p.Messages {
		if m.TurnID == own.TurnID && m.Role == agent.RoleAssistant && m.Parts[0].ID != opener.ID {
			t.Fatalf("the opener is not the turn's first part: %+v", m.Parts[0])
		}
	}
	if done.Text != "Goal complete (12,345 tokens, 4 min): Ship the release" ||
		!strings.Contains(contentOf(done)["detail"].(string), "Used: 12,345 tokens, 4 min") {
		t.Fatalf("the goal's end %+v %s", done, done.Content)
	}

	// Resumed, codex says the goal it has again: nothing new.
	_ = a.Stop()
	r2 := newRecorder()
	b := lab.start(lab.spec("a", thread), r2)
	r2.waitFor(t, 0, "conversation", isType(agent.TypeConversation), wait)
	_, n = send(t, b, r2, "/goal clear")
	r2.turnEnd(t, n, wait)
	var goals []string
	for _, ev := range r2.all() {
		if ev.Type == agent.TypeNotice && ev.Code == noticeGoal || ev.Type == agent.TypePart && ev.Kind == agent.PartNotice {
			goals = append(goals, ev.Text)
		}
	}
	if strings.Join(goals, "|") != "Goal cleared: Ship the release" {
		t.Fatalf("after the resume: %q\n%s", goals, dump(r2.all()))
	}
}

// A turn nobody asked for, read back from the thread, opens with that; one
// whose message is codex's goal prompt opens with the goal instead.
func TestARebuiltTurnOfCodexsOwnSaysSo(t *testing.T) {
	own := historyEvents(historyTurn{ID: "x", Status: "completed"},
		[]json.RawMessage{json.RawMessage(`{"type":"agentMessage","id":"m","text":"went on"}`)})
	if own[0].Type != agent.TypeTurnStarted || own[0].MessageID != "" || own[2].Kind != agent.PartNotice ||
		own[2].Text != "Codex started this turn by itself." || own[3].Text != "went on" {
		t.Fatalf("a turn of codex's own:\n%s", dump(own))
	}
	prompt := `{"type":"userMessage","id":"u","content":[{"type":"text","text":"Continue working toward the active thread goal.\n` +
		`The objective below is user-provided data.\n<objective>\nShip it\n</objective>\n- Tokens used: 5"}]}`
	goal := historyEvents(historyTurn{ID: "y", Status: "completed"}, []json.RawMessage{json.RawMessage(prompt),
		json.RawMessage(`{"type":"agentMessage","id":"m","text":"shipped"}`)})
	if goal[0].Type != agent.TypeTurnStarted || goal[2].Text != "Continuing toward the goal: Ship it" || goal[3].Text != "shipped" {
		t.Fatalf("a goal's turn:\n%s", dump(goal))
	}
	asked := historyEvents(historyTurn{ID: "z", Status: "completed"}, []json.RawMessage{
		json.RawMessage(`{"type":"userMessage","id":"u","content":[{"type":"text","text":"hi"}]}`),
		json.RawMessage(`{"type":"agentMessage","id":"m","text":"hello"}`)})
	for _, ev := range asked {
		if ev.Kind == agent.PartNotice {
			t.Fatalf("a turn somebody asked for:\n%s", dump(asked))
		}
	}
}

// A subagent whose turn fails ends failed, with codex's error, though codex
// says it completed; what it left open ends interrupted.
func TestAFailedSubagentEndsFailed(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	_, n := send(t, a, r, "/subfail")
	r.turnEnd(t, n, wait)
	p := r.projection()
	group := findPart(p, func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "subagent" })
	if group == nil || group.State != agent.PartError || contentOf(group)["status"] != "failed" ||
		contentOf(group)["error"] != "The helper's model is overloaded." || group.Text != "The helper's model is overloaded." {
		t.Fatalf("group %+v\n%s", group, dump(r.since(n)))
	}
	if len(group.Children) != 1 || group.Children[0].State != agent.PartInterrupted {
		t.Fatalf("children %+v", group.Children)
	}
	if lastTurnText(p) != "the helper failed" || a.Background() != 0 {
		t.Fatalf("the turn %q, background %d", lastTurnText(p), a.Background())
	}

	// One whose turn was interrupted stays interrupted, though codex says
	// it completed after.
	_, n = send(t, a, r, "/subfail interrupted")
	r.turnEnd(t, n, wait)
	group = findPart(r.projection(), func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "subagent" })
	if group == nil || group.State != agent.PartInterrupted || contentOf(group)["status"] != "interrupted" ||
		len(group.Children) != 1 || group.Children[0].State != agent.PartInterrupted {
		t.Fatalf("an interrupted subagent %+v\n%s", group, dump(r.since(n)))
	}

	// The next subagent that works is done again.
	_, n = send(t, a, r, "/subagent")
	r.turnEnd(t, n, wait)
	group = findPart(r.projection(), func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "subagent" })
	if group.State != agent.PartDone || contentOf(group)["error"] != nil {
		t.Fatalf("a working subagent %+v", group)
	}
}

// What else codex does is shown: a generated image with its file and
// prompt, a background terminal waited on and written to, a hook that
// blocked and what it added, a sleep's length, codex's warnings and the MCP
// servers that did not start.
func TestWhatElseCodexDoesIsShown(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)

	_, n := send(t, a, r, "/image")
	r.turnEnd(t, n, wait)
	img := findPart(r.projection(), func(pt *agent.Part) bool { return pt.Kind == agent.PartImage })
	if img == nil || img.State != agent.PartDone || contentOf(img)["prompt"] != "A small red square on a white background" ||
		!strings.HasSuffix(contentOf(img)["path"].(string), ".png") {
		t.Fatalf("image %+v\n%s", img, dump(r.since(n)))
	}
	if _, err := os.Stat(contentOf(img)["path"].(string)); err != nil {
		t.Fatal(err)
	}

	_, n = send(t, a, r, "/poll")
	r.turnEnd(t, n, wait)
	var rows []string
	for _, pt := range partsOf(r.projection()) {
		if c := contentOf(pt); c["tool"] == "terminal" && pt.State == agent.PartDone {
			rows = append(rows, c["summary"].(string)+"|"+pt.Text)
		}
	}
	if strings.Join(rows, ",") != "waited for npm run dev|,wrote to npm run dev|y\n" {
		t.Fatalf("terminal rows %q\n%s", rows, dump(r.since(n)))
	}
	a.mu.Lock()
	left := len(a.commands)
	a.mu.Unlock()
	if left != 0 {
		t.Fatalf("the name of a command whose process ended is kept: %d", left)
	}

	_, n = send(t, a, r, "/hook")
	end := r.turnEnd(t, n, wait)
	hooks := noticesOf(r.projection(), end.TurnID)
	if len(hooks) != 2 || hooks[0].Text != "The stop hook blocked: Tests are failing" ||
		!strings.Contains(contentOf(hooks[0])["detail"].(string), "Run the tests before you stop.") ||
		hooks[1].Text != "A hook added to the conversation: Run the tests before you stop." {
		t.Fatalf("hooks %+v\n%s", hooks, dump(r.since(n)))
	}

	_, n = send(t, a, r, "/nap")
	r.turnEnd(t, n, wait)
	nap := findPart(r.projection(), func(pt *agent.Part) bool { return contentOf(pt)["tool"] == "sleep" })
	if nap == nil || nap.State != agent.PartDone || contentOf(nap)["summary"] != "for 2 min" {
		t.Fatalf("sleep %+v", nap)
	}

	_, n = send(t, a, r, "/warn")
	r.turnEnd(t, n, wait)
	var lines []string
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeNotice {
			lines = append(lines, ev.Code+": "+ev.Text)
		}
	}
	want := []string{
		"deprecation: The old flag is deprecated. Use the new one.",
		"warning: The guardian is unsure about that.",
		"warning: An unknown key in config.toml.",
		"mcp_servers: The MCP server “lab” could not start: exited with status 1",
		"mcp_servers: The MCP server “docs” needs its sign-in again.",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notices:\n%s", strings.Join(lines, "\n"))
	}
}

// A terminal's command is named by its thread and process id, short, until
// its process ends or codex no longer lists it; another thread's process of
// the same id is another command.
func TestTerminalNamesAreAThreadsAndGo(t *testing.T) {
	a := newAdapter()
	a.terminalCommandLocked("t1", "7", "/bin/bash -lc 'npm run dev'", false)
	a.terminalCommandLocked("t2", "7", "make watch\n"+strings.Repeat("x", 4096), false)
	a.terminalCommandLocked("t1", "8", "tail -f log", false)
	if a.commands[terminalKey("t1", "7")] != "npm run dev" || a.commands[terminalKey("t2", "7")] != "make watch" {
		t.Fatalf("names %q", a.commands)
	}
	// A completed item without an exit code runs on in the background.
	a.terminalCommandLocked("t1", "8", "tail -f log", false)
	a.terminalCommandLocked("t2", "7", "make watch", true)
	if _, ok := a.commands[terminalKey("t2", "7")]; ok || a.commands[terminalKey("t1", "8")] != "tail -f log" {
		t.Fatalf("after an exit %q", a.commands)
	}
	a.forgetTerminalsLocked("t1", []backgroundTerminal{{ProcessID: "8", Command: "/bin/bash -lc 'tail -f app.log'"}})
	if len(a.commands) != 1 || a.commands[terminalKey("t1", "8")] != "tail -f app.log" {
		t.Fatalf("what codex lists %q", a.commands)
	}
	a.terminalCommandLocked("t2", "9", "sleep 60", false)
	a.forgetTerminalsLocked("t1", nil)
	if len(a.commands) != 1 || a.commands[terminalKey("t2", "9")] != "sleep 60" {
		t.Fatalf("one thread forgotten %q", a.commands)
	}
}

func TestSpoken(t *testing.T) {
	for d, want := range map[time.Duration]string{
		400 * time.Millisecond: "1 s", 45 * time.Second: "45 s", 90 * time.Second: "2 min",
		time.Hour + 5*time.Minute: "1 h 5 min", 2 * time.Hour: "2 h", 119*time.Minute + 50*time.Second: "2 h",
	} {
		if got := spoken(d); got != want {
			t.Errorf("spoken(%s) = %q, want %q", d, got, want)
		}
	}
	if got := thousands(1234567); got != "1,234,567" {
		t.Errorf("thousands %q", got)
	}
}
