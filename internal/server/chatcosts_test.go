package server

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/costs"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// journalOf writes events into a chat's journal, as its host would.
func journalOf(t *testing.T, data, id string, events ...agent.Event) string {
	t.Helper()
	path := agenthost.JournalPath(data, id)
	j, _, err := agent.OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.Append(events...); err != nil {
		t.Fatal(err)
	}
	return path
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A chat's journal is what it cost: Claude's own price per turn, each
// account apart (the turns before the first switch on the account it
// switched from), every conversation it had; Codex's tokens priced by the
// ledger's rates; OpenCode's own price, under its model's name.
func TestAChatsCostIsReadFromItsJournal(t *testing.T) {
	data := t.TempDir()
	path := journalOf(t, data, "c1",
		agent.Event{Type: agent.TypeConversation, ConversationID: "conv-1"},
		agent.Event{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, TurnID: "t1", Model: "claude-opus-5-5"},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeCompleted, CostUSD: 0.5,
			Usage: &agent.Usage{Input: 10, Output: 20, CacheRead: 30}},
		agent.Event{Type: agent.TypeAccountSwitched, From: "acct-a", To: "acct-b"},
		agent.Event{Type: agent.TypeConversation, ConversationID: "conv-2"},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t2", Outcome: agent.OutcomeCompleted, CostUSD: 0.25},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t3", Outcome: agent.OutcomeInterrupted},
	)
	got, err := readChatCosts(path, "claude", "sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.convs) != 2 || got.convs[0] != "conv-1" || got.convs[1] != "conv-2" {
		t.Fatalf("conversations %v", got.convs)
	}
	a, b := got.spends["acct-a"], got.spends["acct-b"]
	if len(got.spends) != 2 || len(a) != 1 || len(b) != 1 || !near(a[0].CostUSD, 0.5) || a[0].Model != "claude-opus-5-5" ||
		a[0].CacheRead != 30 || !near(b[0].CostUSD, 0.25) || b[0].Model != "claude-opus-5-5" {
		t.Fatalf("spends %+v", got.spends)
	}

	// Each kind of token once: the reasoning is a part of the output, and
	// the input is what was neither read from the cache nor written to it.
	codex := journalOf(t, data, "c2",
		agent.Event{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, TurnID: "t1", Model: "gpt-5.4"},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t1", Usage: &agent.Usage{Input: 1_000_000, CacheRead: 1_000_000,
			CacheWrite: 1_000_000, Output: 1_000_000, Reasoning: 400_000}})
	got, _ = readChatCosts(codex, "codex", "")
	price := costs.CodexPrices["gpt-5.4"]
	want := price.Input + price.Cached + price.CacheWrite + price.Output
	if s := got.spends[""]; len(s) != 1 || !s[0].Priced || !near(s[0].CostUSD, want) || s[0].Input != 1_000_000 ||
		s[0].Output != 1_000_000 {
		t.Fatalf("codex %+v, want %v", s, want)
	}

	opencode := journalOf(t, data, "c3",
		agent.Event{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, TurnID: "t1", Model: "opencode/big-pickle"},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t1", CostUSD: 0.02, Usage: &agent.Usage{Input: 5}})
	got, _ = readChatCosts(opencode, "opencode", "")
	if s := got.spends[""]; len(s) != 1 || s[0].Model != "big-pickle" || !near(s[0].CostUSD, 0.02) {
		t.Fatalf("opencode %+v", s)
	}
}

// One source per chat: its journal is counted, the transcripts of its
// conversations are not - neither a ledger the scanner made of one before nor
// one it finds later, and not after the chat is deleted either. Transcripts in
// the agents' shared folders are counted like any other.
func TestAChatIsCountedOnceFromItsJournal(t *testing.T) {
	env := adminEnv(t)
	empty := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(empty, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(empty, "codex"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(empty, "xdg"))
	data := env.srv.dataDir
	transcript := func(dir, conv string) {
		path := filepath.Join(dir, "projects", "-x", conv+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"assistant","timestamp":"2026-10-20T10:15:00Z","cwd":"/w","message":{"id":"m-` + conv +
			`","model":"claude-opus-5-5","usage":{"input_tokens":0,"output_tokens":1000000}}}` + "\n"
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The chat's conversation, already counted from its transcript by a
	// build before this one.
	transcript(filepath.Join(empty, "claude"), "chat-conv")
	env.srv.costs.pass()
	if _, err := env.store.GetCostLedger("claude/chat-conv"); err != nil {
		t.Fatalf("the transcript was not counted to begin with: %v", err)
	}
	// A conversation nobody's chat had, in the shared folder.
	transcript(filepath.Join(data, "agents", "claude"), "shared-conv")

	if err := env.store.CreateSession(&store.Session{ID: "chat1", Title: "A chat", Harness: "claude", Workdir: "/w",
		WorkdirMode: "custom", Kind: store.KindChat, State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	journalOf(t, data, "chat1",
		agent.Event{Type: agent.TypeConversation, ConversationID: "chat-conv"},
		agent.Event{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, TurnID: "t1", Model: "claude-opus-5-5"},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t1", CostUSD: 1.5})
	env.srv.costs.pass()
	ledger, err := env.store.GetCostLedger("claude/" + chatLedgerID("chat1", "", ""))
	if err != nil || ledger.SessionID != "chat1" || ledger.Title != "A chat" || len(ledger.Rows) != 1 || !near(ledger.Rows[0].CostUSD, 1.5) {
		t.Fatalf("the chat's ledger %+v %v", ledger, err)
	}
	if _, err := env.store.GetCostLedger("claude/chat-conv"); err != store.ErrNotFound {
		t.Fatalf("the chat's transcript is still counted: %v", err)
	}
	if _, err := env.store.GetCostLedger("claude/shared-conv"); err != nil {
		t.Fatalf("the shared folder was not counted: %v", err)
	}

	// The transcript grows and the chat is deleted: still not counted twice.
	transcript(filepath.Join(empty, "claude"), "chat-conv")
	if err := env.store.DeleteSession("chat1"); err != nil {
		t.Fatal(err)
	}
	env.srv.costs.pass()
	if _, err := env.store.GetCostLedger("claude/chat-conv"); err != store.ErrNotFound {
		t.Fatalf("a deleted chat's transcript is counted: %v", err)
	}
	if _, err := env.store.GetCostLedger("claude/" + chatLedgerID("chat1", "", "")); err != nil {
		t.Fatalf("the deleted chat's cost went with it: %v", err)
	}
}

// A chat's ledgers are its journal as it reads now: the turns before an
// account switch move to the account they ran on and are not left behind
// on the ledger they were counted in before. A journal rebuilt after its
// folder was lost does not count the history it got back again, and leaves
// the lost journal's ledgers as they were.
func TestAChatsLedgersFollowItsJournal(t *testing.T) {
	env := adminEnv(t)
	empty := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(empty, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(empty, "codex"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(empty, "xdg"))
	data := env.srv.dataDir
	if err := env.store.CreateSession(&store.Session{ID: "chat1", Title: "A chat", Harness: "claude", Workdir: "/w",
		WorkdirMode: "custom", Kind: store.KindChat, State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	ledgers := func() map[string]float64 {
		t.Helper()
		all, err := env.store.ListCostLedgers()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, l := range all {
			for _, r := range l.Rows {
				out[l.ConvID] += r.CostUSD
			}
		}
		return out
	}
	turn := func(id string, cost float64) []agent.Event {
		return []agent.Event{
			{Type: agent.TypeMessage, MessageID: "a" + id, Role: agent.RoleAssistant, TurnID: id, Model: "claude-opus-5-5"},
			{Type: agent.TypeTurnEnded, TurnID: id, Outcome: agent.OutcomeCompleted, CostUSD: cost},
		}
	}

	journalOf(t, data, "chat1", turn("t1", 1)...)
	env.srv.costs.pass()
	if got := ledgers(); len(got) != 1 || !near(got["chat-chat1"], 1) {
		t.Fatalf("after the first turn %v", got)
	}
	journalOf(t, data, "chat1", append([]agent.Event{{Type: agent.TypeAccountSwitched, From: "acct-a", To: "acct-b"}},
		turn("t2", 0.5)...)...)
	env.srv.costs.pass()
	if got := ledgers(); len(got) != 2 || !near(got["chat-chat1@acct-a"], 1) || !near(got["chat-chat1@acct-b"], 0.5) {
		t.Fatalf("after the switch %v", got)
	}

	// The folder is lost and its journal rebuilt: the history comes back
	// with what it cost (OpenCode's rebuild says so), then a new turn.
	if err := os.RemoveAll(agenthost.ChatDir(data, "chat1")); err != nil {
		t.Fatal(err)
	}
	rebuilt := append(turn("t1", 1), turn("t2", 0.5)...)
	rebuilt = append(rebuilt, agent.Event{Type: agent.TypeNotice, Code: agent.NoticeRebuilt, At: 1_800_000_000_000})
	journalOf(t, data, "chat1", append(rebuilt, turn("t3", 0.25)...)...)
	env.srv.costs.pass()
	got := ledgers()
	total := 0.0
	for _, cost := range got {
		total += cost
	}
	if len(got) != 3 || !near(got["chat-chat1@acct-a"], 1) || !near(got["chat-chat1@acct-b"], 0.5) ||
		!near(got["chat-chat1~1800000000000"], 0.25) || !near(total, 1.75) {
		t.Fatalf("after the rebuild %v", got)
	}
}
