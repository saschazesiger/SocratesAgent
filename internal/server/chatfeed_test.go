package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// fold numbers events the way the journal does and folds them.
func fold(events ...agent.Event) *agent.Projection {
	p := agent.NewProjection()
	for i, ev := range events {
		ev.Seq, ev.At = int64(i+1), int64(1000*(i+1))
		p.Apply(ev)
	}
	return p
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// A turn's start, the person's message, and the agent's message for it.
func turnOpen(id, text string) []agent.Event {
	return []agent.Event{
		{Type: agent.TypeMessage, MessageID: "u" + id, Role: agent.RoleUser, State: agent.MessageDelivered, Text: text},
		{Type: agent.TypeTurnStarted, TurnID: "t" + id, MessageID: "u" + id},
		{Type: agent.TypeMessage, MessageID: "a" + id, Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: "t" + id},
	}
}

func textPart(id, part, text string) agent.Event {
	return agent.Event{Type: agent.TypePart, MessageID: "a" + id, PartID: part, Kind: agent.PartText, State: agent.PartDone, Text: text}
}

func turnEnd(id, outcome, why string) agent.Event {
	return agent.Event{Type: agent.TypeTurnEnded, TurnID: "t" + id, Outcome: outcome, Text: why}
}

func TestAChatsLineSaysWhatItIsDoing(t *testing.T) {
	bash := agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "b1", Kind: agent.PartTool, State: agent.PartRunning,
		Content: raw(map[string]any{"tool": "Bash", "summary": "go test ./...", "command": "go test ./..."})}
	edit := agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "e1", Kind: agent.PartDiff, State: agent.PartRunning,
		Content: raw(map[string]any{"tool": "Edit", "summary": "/src/app/main.go", "path": "/src/app/main.go"})}
	codex := agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "c1", Kind: agent.PartTool, State: agent.PartRunning,
		Content: raw(map[string]any{"tool": "command", "command": "make build"})}
	opencode := agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "o1", Kind: agent.PartTool, State: agent.PartRunning,
		Content: raw(map[string]any{"tool": "read", "title": "README.md", "input": map[string]any{"filePath": "/w/README.md"}})}
	subagentCall := agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "s1", ParentPartID: "task", Kind: agent.PartTool,
		State: agent.PartRunning, Content: raw(map[string]any{"tool": "Bash", "summary": "ls"})}
	task := agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "task", Kind: agent.PartTool, State: agent.PartRunning,
		Content: raw(map[string]any{"tool": "Task", "summary": "Explore the code"})}
	done := func(ev agent.Event) agent.Event { ev.State = agent.PartDone; return ev }

	cases := []struct {
		name   string
		events []agent.Event
		want   string
	}{
		{"what it was asked, before anything else", turnOpen("1", "Rename the config loader"), "Rename the config loader"},
		{"the tool that runs", append(turnOpen("1", "x"), textPart("1", "p1", "Let me look."), bash), "Running go test ./..."},
		{"an edit, by its file", append(turnOpen("1", "x"), edit), "Editing main.go"},
		{"a codex command", append(turnOpen("1", "x"), codex), "Running make build"},
		{"an opencode read", append(turnOpen("1", "x"), opencode), "Reading README.md"},
		{"a subagent by its call, not by what it runs", append(turnOpen("1", "x"), task, subagentCall), "Subagent: Explore the code"},
		{"what it is writing once its tools are done",
			append(turnOpen("1", "x"), done(bash), textPart("1", "p2", "## Result\n\nAll **tests** pass.")), "Result"},
		{"the last answer between turns",
			append(turnOpen("1", "x"), textPart("1", "p1", "```go\nx := 1\n```\n- Renamed `load` to `Load`.\n\nMore."), turnEnd("1", agent.OutcomeCompleted, "")),
			"Renamed load to Load."},
		{"a turn that failed says so", append(turnOpen("1", "x"), turnEnd("1", agent.OutcomeFailed, "the API refused")),
			"Failed: the API refused"},
		{"a limit says so", append(turnOpen("1", "x"), turnEnd("1", agent.OutcomeLimited, "")), "Limit reached"},
		{"a stopped turn without words", append(turnOpen("1", "x"), turnEnd("1", agent.OutcomeInterrupted, "")), "Stopped"},
		{"a quiet turn says so first", append(turnOpen("1", "x"), bash, agent.Event{Type: agent.TypeQuiet, TurnID: "t1", Since: 1000}),
			"Quiet: Running go test ./..."},
		{"heard from again, it does not", append(turnOpen("1", "x"), bash, agent.Event{Type: agent.TypeQuiet, TurnID: "t1", Since: 1000},
			agent.Event{Type: agent.TypeQuiet, TurnID: "t1"}), "Running go test ./..."},
	}
	for _, c := range cases {
		if got := chatLine(fold(c.events...)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAChatThatAsksIsItsQuestion(t *testing.T) {
	payload := raw(map[string]any{"title": "Setup", "questions": []any{
		map[string]any{"id": "db", "header": "Database", "question": "Which database?", "kind": "single",
			"allow_other": true, "options": []any{map[string]any{"id": "pg", "label": "PostgreSQL", "recommended": true},
				map[string]any{"id": "my", "label": "MySQL"}}},
		map[string]any{"id": "n", "header": "Replicas", "question": "How many replicas?", "kind": "number"},
	}})
	events := append(turnOpen("1", "set it up"), textPart("1", "p1", "Two things first."),
		agent.Event{Type: agent.TypeAsk, AskID: "k1", TurnID: "t1", Payload: payload})
	p := fold(events...)
	if got := chatLine(p); got != "Which database?" {
		t.Fatalf("line %q", got)
	}
	q := chatQuestion(waitingAsk(p))
	if q == nil || q.Source != questionAsk || q.Kind != "choice" || q.Detail != "Setup" || len(q.Items) != 2 {
		t.Fatalf("question %+v", q)
	}
	if first := q.Items[0]; first.Header != "Database" || len(first.Options) != 2 || !first.Options[0].Recommended || !first.FreeText {
		t.Fatalf("first item %+v", first)
	}
	if second := q.Items[1]; !second.FreeText || second.Multi {
		t.Fatalf("second item %+v", second)
	}

	// Answered, it is no question any more: the line is the turn again.
	p.Apply(agent.Event{Seq: 100, At: 100000, Type: agent.TypeAskAnswered, AskID: "k1", Answer: raw("pg"), ClientID: "c"})
	if waitingAsk(p) != nil || chatLine(p) != "Two things first." {
		t.Fatalf("after the answer: %v %q", waitingAsk(p), chatLine(p))
	}

	// A plan is a question of its own kind.
	plan := fold(append(turnOpen("2", "plan it"), agent.Event{Type: agent.TypeAsk, AskID: "plan_1", TurnID: "t2",
		Payload: raw(map[string]any{"kind": "plan", "title": "Move to SQLite", "plan": "1. …"})})...)
	if q := chatQuestion(waitingAsk(plan)); q == nil || q.Kind != "plan" || q.Items[0].Text != "Move to SQLite" {
		t.Fatalf("plan %+v", q)
	}
}

// An open question of an earlier turn under a turn the agent started by
// itself does not make the chat waiting, and its line is the new turn.
func TestAnOldQuestionUnderANewTurnIsNotTheLine(t *testing.T) {
	events := append(turnOpen("1", "x"), agent.Event{Type: agent.TypeAsk, AskID: "k1", TurnID: "t1",
		Payload: raw(map[string]any{"questions": []any{map[string]any{"question": "Old?"}}})},
		turnEnd("1", agent.OutcomeInterrupted, ""),
		agent.Event{Type: agent.TypeTurnStarted, TurnID: "t2"},
		agent.Event{Type: agent.TypeMessage, MessageID: "a2", Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: "t2"},
		textPart("2", "p", "The background build finished."))
	p := fold(events...)
	if waitingAsk(p) != nil || chatLine(p) != "The background build finished." {
		t.Fatalf("waiting %v, line %q", waitingAsk(p), chatLine(p))
	}
}

func TestTheNewsOfAnEndedTurn(t *testing.T) {
	answered := fold(append(turnOpen("1", "x"), textPart("1", "p", "Renamed it.\nDetails…"), turnEnd("1", agent.OutcomeCompleted, ""))...)
	if got, ok := turnNews(answered); !ok || got != "Finished · Renamed it." {
		t.Fatalf("completed: %q %v", got, ok)
	}
	failed := fold(append(turnOpen("1", "x"), turnEnd("1", agent.OutcomeFailed, "overloaded"))...)
	if got, _ := turnNews(failed); got != "Failed · overloaded" {
		t.Fatalf("failed: %q", got)
	}
	// Stopped by the person: they know, and nothing is pushed.
	stopped := fold(append(turnOpen("1", "x"), agent.Event{Type: agent.TypeCommand, Op: agent.OpInterrupt, ClientID: "s"},
		turnEnd("1", agent.OutcomeInterrupted, ""))...)
	if _, ok := turnNews(stopped); ok {
		t.Fatal("a turn the person stopped is news")
	}
	// Ended by a host that died: that is news.
	died := fold(append(turnOpen("1", "x"), textPart("1", "p", "Half way"), turnEnd("1", agent.OutcomeInterrupted, ""))...)
	if got, ok := turnNews(died); !ok || got != "Stopped · Half way" {
		t.Fatalf("died: %q %v", got, ok)
	}
	// Stopped for a switch to another account made now: as a Stop. One that
	// waits for the turn stops nothing, so a turn that then dies is news.
	switched := fold(append(turnOpen("1", "x"), agent.Event{Type: agent.TypeCommand, Op: agent.OpSwitchAccount,
		ClientID: "w", To: "acct-b", Args: "now"}, turnEnd("1", agent.OutcomeInterrupted, ""))...)
	if _, ok := turnNews(switched); ok {
		t.Fatal("a turn the switch stopped is news")
	}
	waited := fold(append(turnOpen("1", "x"), agent.Event{Type: agent.TypeCommand, Op: agent.OpSwitchAccount,
		ClientID: "w", To: "acct-b", Args: agent.SwitchAfterTurn}, turnEnd("1", agent.OutcomeInterrupted, ""))...)
	if got, ok := turnNews(waited); !ok || got != "Stopped" {
		t.Fatalf("a switch after the turn: %q %v", got, ok)
	}
	// A switch to the account the turn already runs on stops nothing either.
	opened := turnOpen("1", "x")
	opened[1].Account = "acct-b"
	same := fold(append(opened, agent.Event{Type: agent.TypeCommand, Op: agent.OpSwitchAccount,
		ClientID: "w", To: "acct-b", Args: "now"}, turnEnd("1", agent.OutcomeInterrupted, ""))...)
	if got, ok := turnNews(same); !ok || got != "Stopped" {
		t.Fatalf("a switch to the account the turn runs on: %q %v", got, ok)
	}
	// A limit on an account the journal names is the accounts' to tell
	// (accountHub.onLimit), not news of the turn's end; with no account named
	// it is the turn's.
	onAccount := turnOpen("1", "x")
	onAccount[1].Account = "acct-a"
	if got := newsOf(fold(append(onAccount, turnEnd("1", agent.OutcomeLimited, "5-hour limit"))...), nil); got != "" {
		t.Fatalf("a limit on an account: %q", got)
	}
	if got := newsOf(fold(append(turnOpen("1", "x"), turnEnd("1", agent.OutcomeLimited, "5-hour limit"))...), nil); got != "Limit reached · 5-hour limit" {
		t.Fatalf("a limit on no account: %q", got)
	}
}

func TestATurnAsAModelIsToldIt(t *testing.T) {
	failing := agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "b", Kind: agent.PartTool, State: agent.PartError,
		Text: "ok 1\nFAIL TestX", Content: raw(map[string]any{"tool": "Bash", "summary": "go test", "command": "go test"})}
	todo := agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "todo", Kind: agent.PartTodo, State: agent.PartRunning,
		Content: raw(map[string]any{"items": []any{map[string]any{"subject": "Write it", "status": "completed"},
			map[string]any{"subject": "Test it", "status": "in_progress"}}})}
	p := fold(append(turnOpen("1", "Fix the loader"), failing, todo, textPart("1", "p", "One test still fails."),
		turnEnd("1", agent.OutcomeCompleted, ""))...)
	turn := readChatTurn(p, p.LastTurn)
	if turn.Prompt != "Fix the loader" || turn.Answer != "One test still fails." {
		t.Fatalf("turn %+v", turn)
	}
	if len(turn.Tools) != 1 || turn.Tools[0] != "Running go test (failed: FAIL TestX)" {
		t.Fatalf("tools %q", turn.Tools)
	}
	if len(turn.Tasks) != 2 || turn.Tasks[1].Status != "in_progress" {
		t.Fatalf("tasks %+v", turn.Tasks)
	}
	prompt := chatTurnPrompt(turn)
	for _, want := range []string{"Fix the loader", "One test still fails.", "[x] Write it", "[~] Test it", "- Running go test (failed"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the prompt has no %q:\n%s", want, prompt)
		}
	}

	// Codex's plan and OpenCode's todos are task lists too.
	codex := fold(append(turnOpen("1", "x"), agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "plan", Kind: agent.PartTodo,
		Content: raw(map[string]any{"steps": []any{map[string]any{"step": "Plan", "status": "inProgress"}}})})...)
	opencode := fold(append(turnOpen("1", "x"), agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "td", Kind: agent.PartTodo,
		Content: raw(map[string]any{"todos": []any{map[string]any{"content": "Ship", "status": "completed"}}})})...)
	if got := todoState(codex); len(got) != 1 || got[0].Status != "in_progress" {
		t.Fatalf("codex %+v", got)
	}
	if got := todoState(opencode); len(got) != 1 || got[0].Status != "completed" {
		t.Fatalf("opencode %+v", got)
	}
}
