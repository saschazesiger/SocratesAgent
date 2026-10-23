package costs

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func total(rows []store.CostRow) (cost float64, requests int64) {
	for _, r := range rows {
		cost += r.CostUSD
		requests += r.Requests
	}
	return
}

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
	// A filesystem with coarse times must still see the change.
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
}

func answer(id, model, ts string, in, out, read, write1h int64) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"cwd":"/root/proj","message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":%d}}}}`,
		ts, id, model, in, out, read, write1h, write1h)
}

func TestClaudePricesMatchClaudeCode(t *testing.T) {
	// A transcript's own numbers: Claude Code recorded $0.3328484 for them.
	cost, ok := claudeCost("claude-opus-5-5", claudeTokens{input: 106, output: 3644, cacheRead: 258722, write1h: 25975})
	if !ok || !near(cost, 0.3328484) {
		t.Fatalf("cost = %v %v, want 0.3328484", cost, ok)
	}
	for model, want := range map[string]string{
		"claude-opus-5[1m]":                   "claude-opus-5",
		"claude-haiku-4-5-20251001":           "claude-haiku-4-5",
		"us.anthropic.claude-sonnet-4-5-v1:0": "claude-sonnet-4-5",
		"claude-sonnet-4-5@20250929":          "claude-sonnet-4-5",
		"gpt-5":                               "",
	} {
		if got := claudeModel(model); got != want {
			t.Errorf("claudeModel(%q) = %q, want %q", model, got, want)
		}
	}
	if _, ok := claudeCost("claude-opus-5-7", claudeTokens{output: 1}); ok {
		t.Error("a model without a price was priced as its older sibling")
	}
	short, _ := claudeCost("claude-haiku-5-5", claudeTokens{input: 1000, output: 1000})
	long, _ := claudeCost("claude-haiku-5-5", claudeTokens{input: 1000, cacheRead: 100_000, output: 1000})
	if !near(short, 0.0006) || !near(long, 5*(0.0001+0.001+0.0005)) {
		t.Errorf("haiku short %v long %v", short, long)
	}
	slow, _ := claudeCost("claude-opus-5-5", claudeTokens{output: 1000})
	fast, _ := claudeCost("claude-opus-5-5", claudeTokens{output: 1000, fast: true})
	if !near(fast, 2*slow) {
		t.Errorf("fast %v, standard %v", fast, slow)
	}
}

func TestClaudeConversation(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "projects", "-root-proj", "conv-1.jsonl")
	write(t, main,
		`{"type":"custom-title","customTitle":"Fix the build"}`,
		// One answer written once per content block; the last has the count.
		answer("msg_a", "claude-opus-5-5", "2026-10-20T10:15:00Z", 10, 100, 1000, 0),
		answer("msg_a", "claude-opus-5-5", "2026-10-20T10:15:01Z", 10, 500, 1000, 0),
		`{"type":"assistant","timestamp":"2026-10-20T10:16:00Z","message":{"id":"x","model":"<synthetic>","usage":{"output_tokens":5}}}`,
		`not json at all`,
		answer("msg_b", "claude-opus-5-5", "2026-10-20T11:05:00Z", 0, 1000, 0, 1000),
	)
	// An agent of the conversation, in its own transcript.
	write(t, filepath.Join(dir, "projects", "-root-proj", "conv-1", "subagents", "agent-x.jsonl"),
		answer("msg_c", "claude-haiku-5-5", "2026-10-20T10:30:00Z", 1000, 1000, 0, 0))

	sc := NewScanner()
	convs := sc.Scan([]Root{{Profile: "p1", ClaudeDir: dir}}, nil)
	if len(convs) != 1 {
		t.Fatalf("conversations = %+v", convs)
	}
	c := convs[0]
	if c.Key() != "claude/conv-1" || c.Profile != "p1" || c.Cwd != "/root/proj" || c.Title != "Fix the build" {
		t.Fatalf("conversation = %+v", c)
	}
	a, _ := claudeCost("claude-opus-5-5", claudeTokens{input: 10, output: 500, cacheRead: 1000})
	b, _ := claudeCost("claude-opus-5-5", claudeTokens{output: 1000, write1h: 1000})
	h, _ := claudeCost("claude-haiku-5-5", claudeTokens{input: 1000, output: 1000})
	cost, requests := total(c.Rows)
	if requests != 3 || !near(cost, a+b+h) {
		t.Fatalf("cost %v requests %d, want %v and 3: %+v", cost, requests, a+b+h, c.Rows)
	}
	if len(c.Rows) != 3 { // 10:00 opus, 10:00 haiku, 11:00 opus
		t.Fatalf("rows = %+v", c.Rows)
	}

	// Nothing changed: nothing to report.
	seen := map[string]string{c.Key(): c.Fingerprint}
	if again := sc.Scan([]Root{{Profile: "p1", ClaudeDir: dir}}, seen); len(again) != 0 {
		t.Fatalf("an unchanged transcript was reported: %+v", again)
	}

	// The run exits: Claude Code says it spent $1 more on opus than its
	// answers show (helpers that are never written down), and something on a
	// model nobody has a price for.
	start := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC).UnixMilli()
	appendLines(t, main,
		answer("msg_d", "claude-mystery-9", "2026-10-20T11:06:00Z", 10, 10, 0, 0),
		fmt.Sprintf(`{"type":"cost-state","startTime":%d,"modelUsage":{"claude-opus-5-5":{"inputTokens":10,"outputTokens":1500,"cacheReadInputTokens":1000,"cacheCreationInputTokens":1000,"costUSD":%v},"claude-mystery-9":{"inputTokens":10,"outputTokens":10,"costUSD":0.5}}}`,
			start, a+b+1))
	convs = sc.Scan([]Root{{Profile: "p1", ClaudeDir: dir}}, seen)
	if len(convs) != 1 {
		t.Fatalf("an appended transcript was not reported: %+v", convs)
	}
	cost, requests = total(convs[0].Rows)
	if requests != 4 || !near(cost, a+b+h+1+0.5) {
		t.Fatalf("after the run: cost %v requests %d, want %v and 4", cost, requests, a+b+h+1.5)
	}
	var unpriced int64
	for _, r := range convs[0].Rows {
		unpriced += r.Unpriced
	}
	if unpriced != 1 {
		t.Fatalf("unpriced = %d", unpriced)
	}

	// A fresh scanner - a restart - reads the same thing from scratch.
	fresh := NewScanner().Scan([]Root{{Profile: "p1", ClaudeDir: dir}}, nil)
	if len(fresh) != 1 {
		t.Fatal("a fresh scan lost the conversation")
	}
	if c2, _ := total(fresh[0].Rows); !near(c2, cost) {
		t.Fatalf("a fresh read says %v, the incremental one %v", c2, cost)
	}
}

func TestCodexConversationSpansItsThreads(t *testing.T) {
	home := t.TempDir()
	rec := func(ts, session, thread, response string, in, cached, out int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"token_usage_record","payload":{"session_id":%q,"thread_id":%q,"response_id":%q,"usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d}}}`,
			ts, session, thread, response, in, cached, out)
	}
	write(t, filepath.Join(home, "sessions", "2026", "10", "20", "rollout-a-root.jsonl"),
		`{"timestamp":"2026-10-20T08:00:00Z","type":"session_meta","payload":{"session_id":"root","id":"root","cwd":"/root/app"}}`,
		`{"timestamp":"2026-10-20T08:00:01Z","type":"turn_context","payload":{"model":"gpt-5.5","cwd":"/root/app"}}`,
		rec("2026-10-20T08:00:05Z", "root", "root", "r1", 1000, 400, 100),
		rec("2026-10-20T08:00:05Z", "root", "root", "r1", 1000, 400, 100), // written twice
	)
	write(t, filepath.Join(home, "sessions", "2026", "10", "20", "rollout-b-child.jsonl"),
		`{"timestamp":"2026-10-20T08:01:00Z","type":"session_meta","payload":{"session_id":"root","id":"child","cwd":"/root/app"}}`,
		`{"timestamp":"2026-10-20T08:01:01Z","type":"turn_context","payload":{"model":"gpt-9-unknown"}}`,
		rec("2026-10-20T08:01:05Z", "root", "child", "r2", 500, 0, 50),
	)
	convs := NewScanner().Scan([]Root{{CodexHome: home}}, nil)
	if len(convs) != 1 || convs[0].Key() != "codex/root" || convs[0].Cwd != "/root/app" {
		t.Fatalf("conversations = %+v", convs)
	}
	want, _ := CodexCost("gpt-5.5", 1000, 400, 0, 100)
	cost, requests := total(convs[0].Rows)
	if requests != 2 || !near(cost, want) {
		t.Fatalf("cost %v requests %d, want %v and 2: %+v", cost, requests, want, convs[0].Rows)
	}
	for _, r := range convs[0].Rows {
		if r.Model == "gpt-9-unknown" && r.Unpriced != 1 {
			t.Fatalf("the unknown model's request is not marked: %+v", r)
		}
	}
}

func TestOpenCodeAgentsCountUnderTheirConversation(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, directory TEXT NOT NULL, title TEXT NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, data TEXT NOT NULL)`,
		`INSERT INTO session VALUES ('ses_root', NULL, '/root/web', 'Landing page'), ('ses_child', 'ses_root', '/root/web', 'agent')`,
		`INSERT INTO message VALUES
		 ('m1', 'ses_root', '{"role":"user","time":{"created":1792400000000}}'),
		 ('m2', 'ses_root', '{"role":"assistant","cost":0.25,"modelID":"~anthropic/claude-haiku","tokens":{"input":3,"output":20,"reasoning":5,"cache":{"read":100,"write":7}},"time":{"created":1792400000000}}'),
		 ('m3', 'ses_child', '{"role":"assistant","cost":0.5,"modelID":"glm","tokens":{"input":1,"output":2,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1792403600000}}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	sc := NewScanner()
	convs := sc.Scan([]Root{{OpenCodeDir: dir}}, nil)
	if len(convs) != 1 || convs[0].Key() != "opencode/ses_root" || convs[0].Title != "Landing page" || convs[0].Cwd != "/root/web" {
		t.Fatalf("conversations = %+v", convs)
	}
	cost, requests := total(convs[0].Rows)
	if requests != 2 || !near(cost, 0.75) {
		t.Fatalf("cost %v requests %d", cost, requests)
	}
	for _, r := range convs[0].Rows {
		if r.Model == "anthropic/claude-haiku" && (r.Output != 25 || r.CacheRead != 100 || r.CacheWrite != 7) {
			t.Fatalf("tokens = %+v", r)
		}
	}
	// The database did not change: it is not read again.
	if again := sc.Scan([]Root{{OpenCodeDir: dir}}, nil); len(again) != 0 {
		t.Fatalf("an unchanged database was read again: %+v", again)
	}
}
