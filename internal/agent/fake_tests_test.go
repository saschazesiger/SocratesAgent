//go:build !windows

package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"image/png"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// toolCall is one call the scripted agent made of a test tool.
type toolCall struct {
	Tool string
	Args map[string]any
}

// toolServer answers the test tools on a Unix socket as the server does,
// with canned texts, and keeps the calls.
type toolServer struct {
	mu      sync.Mutex
	calls   []toolCall
	answers map[string]string
}

func (s *toolServer) list() []toolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]toolCall(nil), s.calls...)
}

func (s *toolServer) names() string {
	var out []string
	for _, c := range s.list() {
		out = append(out, c.Tool)
	}
	return strings.Join(out, " ")
}

// startToolServer serves the tools and returns the endpoint of a session
// of that role.
func startToolServer(t *testing.T, kind string, answers map[string]string) (*toolServer, chattools.Endpoint) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ft")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &toolServer{answers: answers}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chattools.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		var args map[string]any
		_ = json.Unmarshal(req.Arguments, &args)
		s.mu.Lock()
		s.calls = append(s.calls, toolCall{Tool: req.Tool, Args: args})
		s.mu.Unlock()
		text, ok := s.answers[req.Tool]
		if !ok {
			text = "ok"
		}
		_ = json.NewEncoder(w).Encode(chattools.Response{Text: text})
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s, chattools.Endpoint{Socket: sock, Token: "token", Kind: kind}
}

// runFakeTurn starts the scripted agent with text as its first message and
// waits for the turn's end; it returns the events.
func runFakeTurn(t *testing.T, tools chattools.Endpoint, env []string, text string) []Event {
	t.Helper()
	out := &emits{}
	f := &fakeAdapter{}
	spec := StartSpec{ChatID: "c1", Workdir: t.TempDir(), StateDir: t.TempDir(), Env: env, Model: "fake-small",
		ChatTools: tools, First: &Outgoing{MessageID: "m1", Text: text}}
	if err := f.Start(t.Context(), spec, out); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out.mu.Lock()
		events := append([]Event(nil), out.events...)
		out.mu.Unlock()
		for _, e := range events {
			if e.Type == TypeTurnEnded {
				return events
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the turn did not end")
	return nil
}

// cellMessage is a runner's first message to a cell of two steps.
func cellMessage(run int, mode string) string {
	params := ""
	if mode != "" {
		params = "- Parameters: fake=" + mode + "\n"
	}
	return "Run this cell of the test catalog.\n\n- Test: shop/cart - The cart updates its total\n" +
		"- Run: " + itoa(run) + "\n- Priority: P1\n- Device: phone (iPhone 15)\n- Attempt: 1\n" + params +
		"\n## Steps\n\n1. Raise the quantity to two.\n   Expect: the total doubles.\n" +
		"2. Remove the lamp.\n   Expect: the empty cart.\n\nBegin with step 1."
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func cellEnv(evidence string, extra ...string) []string {
	return append([]string{"PATH=" + filepath.Join(evidence, "nobin"), "SOCRATES_TEST_EVIDENCE=" + evidence,
		"SOCRATES_TEST_DIM_DEVICE=phone", "SOCRATES_TEST_DIM_LOCALE=de"}, extra...)
}

// judged is the verdicts of the test_judge calls, "rest:" before a judgement
// of every step left.
func judged(calls []toolCall) []string {
	var out []string
	for _, c := range calls {
		if c.Tool != testtools.Judge {
			continue
		}
		v, _ := c.Args["verdict"].(string)
		if rest, _ := c.Args["rest"].(bool); rest {
			v = "rest:" + v
		}
		out = append(out, v)
	}
	return out
}

// A cell judges every step on a screenshot of its own, in the shape of the
// cell's device, with its line of evidence, and ends with a summary.
func TestFakeCellWritesEvidenceAndJudges(t *testing.T) {
	srv, ep := startToolServer(t, testtools.KindCell, nil)
	dir := t.TempDir()
	events := runFakeTurn(t, ep, cellEnv(dir), cellMessage(1, ""))
	if got := srv.names(); got != "test_judge test_judge test_end" {
		t.Fatalf("the calls: %s", got)
	}
	if got := strings.Join(judged(srv.list()), ","); got != "pass,pass" {
		t.Fatalf("the verdicts: %s", got)
	}
	if obs, _ := srv.list()[0].Args["observed"].(string); !strings.Contains(obs, "the total doubles") {
		t.Errorf("the observation: %q", obs)
	}
	for _, name := range []string{"step-1.png", "step-2.png"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := img.Bounds(); b.Dy() <= b.Dx() {
			t.Errorf("%s: a phone's screenshot is %v", name, b)
		}
	}
	file, err := os.Open(filepath.Join(dir, "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var lines []map[string]any
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("a line of actions.jsonl: %v", err)
		}
		lines = append(lines, line)
	}
	if len(lines) != 2 || lines[1]["screenshot"] != "step-2.png" || lines[0]["summary"] != "step 1 on phone in de" ||
		lines[0]["action"] == "" || lines[0]["at"] == "" {
		t.Fatalf("actions.jsonl: %v", lines)
	}
	tools := 0
	for _, e := range events {
		if e.Type == TypePart && e.Kind == PartTool && e.State == PartDone {
			tools++
		}
	}
	if tools != 3 {
		t.Errorf("the transcript shows %d tool calls", tools)
	}
}

// The test's parameter fake makes the outcomes, as with fakeclaude.
func TestFakeCellModes(t *testing.T) {
	for _, c := range []struct {
		mode    string
		run     int
		calls   string
		verdict string
	}{
		{"fail", 1, "test_judge test_judge test_end", "fail,pass"},
		{"issue", 1, "test_judge test_judge test_end", "issue,pass"},
		{"blocked", 1, "test_judge test_end", "rest:blocked"},
		{"flaky", 1, "test_judge test_judge test_end", "fail,pass"},
		{"flaky", 2, "test_judge test_judge test_end", "pass,pass"},
		{"failfrom3", 2, "test_judge test_judge test_end", "pass,pass"},
		{"failfrom3", 3, "test_judge test_judge test_end", "fail,pass"},
		{"issueuntil3", 2, "test_judge test_judge test_end", "issue,pass"},
		{"issueuntil3", 3, "test_judge test_judge test_end", "pass,pass"},
		{"misjudged", 1, "test_judge test_judge test_end", "pass,pass"},
		{"abort", 1, "test_abort", ""},
		{"quiet", 1, "", ""},
	} {
		t.Run(c.mode+"-"+itoa(c.run), func(t *testing.T) {
			srv, ep := startToolServer(t, testtools.KindCell, nil)
			dir := t.TempDir()
			runFakeTurn(t, ep, cellEnv(dir), cellMessage(c.run, c.mode))
			if got := srv.names(); got != c.calls {
				t.Fatalf("the calls: %q", got)
			}
			if got := strings.Join(judged(srv.list()), ","); got != c.verdict {
				t.Fatalf("the verdicts: %s", got)
			}
			actions, _ := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
			if got := strings.Contains(string(actions), `"error"`); got != (c.mode == "misjudged") {
				t.Errorf("an action with an error: %v\n%s", got, actions)
			}
		})
	}
}

// A catalog whose tool puts shot on the session's PATH has it take the
// screenshots, as fakeclaude does.
func TestFakeCellRunsTheCatalogsShot(t *testing.T) {
	srv, ep := startToolServer(t, testtools.KindCell, nil)
	dir, bin := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\necho \"$1 $SOCRATES_TEST_DIM_DEVICE\" >> \"$SOCRATES_TEST_EVIDENCE/shots.txt\"\necho took $1\n"
	if err := os.WriteFile(filepath.Join(bin, "shot"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := cellEnv(dir)
	env[0] = "PATH=" + bin + ":/usr/bin:/bin"
	runFakeTurn(t, ep, env, cellMessage(1, ""))
	shots, err := os.ReadFile(filepath.Join(dir, "shots.txt"))
	if err != nil || string(shots) != "1 phone\n2 phone\n" {
		t.Fatalf("the shots: %q %v", shots, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "step-1.png")); err == nil {
		t.Error("the agent drew a screenshot of its own besides the tool's")
	}
	if got := srv.names(); got != "test_judge test_judge test_end" {
		t.Fatalf("the calls: %s", got)
	}
}

// A reviewer reads every cell, turns a pass whose action failed into an
// issue and closes each cell.
func TestFakeReview(t *testing.T) {
	evidence := "Cell shop/cart@phone.de: The cart\nAttempt 1.\n\n## Step 1: Raise it.\nExpect: doubles\n" +
		"- #1 screenshot: step 1 on phone ERROR: the first tap on the button did nothing\n  screenshot: /x/step-1.png\n" +
		"\n## Step 2: Remove it.\nExpect: empty\n- #2 screenshot: step 2 on phone\n  screenshot: /x/step-2.png\n"
	agent := "Verdict of the cell: pass\nSummary: fine\n\n1. pass - It works.\n2. pass - It works."
	srv, ep := startToolServer(t, testtools.KindReviewer, map[string]string{
		testtools.ReviewEvidence: evidence, testtools.ReviewAgent: agent})
	msg := "Review these cells of the test run, each with review_evidence:\n\n- shop/cart@phone.de\n- shop/home@desktop.en\n\nreview_list says why."
	runFakeTurn(t, ep, nil, msg)
	want := "review_evidence review_agent review_step review_done review_evidence review_agent review_step review_done"
	if got := srv.names(); got != want {
		t.Fatalf("the calls: %s", got)
	}
	step := srv.list()[2]
	if step.Args["cell"] != "shop/cart@phone.de" || step.Args["step"] != float64(1) || step.Args["verdict"] != "issue" {
		t.Fatalf("the correction: %v", step.Args)
	}
	// Without a failed action nothing is corrected.
	srv, ep = startToolServer(t, testtools.KindReviewer, map[string]string{
		testtools.ReviewEvidence: strings.ReplaceAll(evidence, " ERROR: the first tap on the button did nothing", ""),
		testtools.ReviewAgent:    agent})
	runFakeTurn(t, ep, nil, msg)
	if got := srv.names(); got != "review_evidence review_agent review_done review_evidence review_agent review_done" {
		t.Fatalf("the calls: %s", got)
	}
}

// An analyst writes one bug per test that failed or has an issue, under the
// id of a known bug of the test, none for a test whose bug is not a bug, and
// the conclusions.
func TestFakeAnalysis(t *testing.T) {
	known := "The bugs of this repository known from earlier runs.\n\n" +
		"## B-2 [open, major] The cart: an error\nCells: shop/cart@phone.en, shop/cart@desktop.de\nReported in 1 run(s), last in run 1.\n\n" +
		"## B-3 [not_a_bug, minor] The wish list: overlaps\nCells: shop/wishlist@tablet.fr\nReported in 1 run(s), last in run 1.\n"
	overview := "- shop/cart@phone.en (The cart updates its total, P1): fail - It fails.\n    step 1 fail: An error.\n" +
		"- shop/cart@desktop.de (The cart updates its total, P1): pass - Fine.\n" +
		"- shop/checkout@phone.de (The checkout takes an order, P0): fail - It fails.\n" +
		"- shop/wishlist@tablet.fr (The wish list keeps a product, P2): issue - Overlaps.\n" +
		"- shop/product@tablet.en (The product page, P2): issue (agent: pass) - Overlaps.\n" +
		"- account/reset@phone.fr (A reset by mail, P1): blocked - No device.\n"
	srv, ep := startToolServer(t, testtools.KindAnalyst, map[string]string{
		testtools.BugsKnown: known, testtools.RunOverview: overview})
	runFakeTurn(t, ep, nil, "Analyse test run r1 (run 2, everything): 6 cells.")
	if got := srv.names(); got != "bugs_known run_overview bugs_put conclusions_put" {
		t.Fatalf("the calls: %s", got)
	}
	raw, _ := json.Marshal(srv.list()[2].Args)
	var put testtools.BugsArgs
	if err := json.Unmarshal(raw, &put); err != nil {
		t.Fatal(err)
	}
	got := map[string]testtools.Bug{}
	for _, b := range put.Bugs {
		test, _, _ := strings.Cut(b.Cells[0], "@")
		got[test] = b
	}
	if len(put.Bugs) != 3 || got["shop/cart"].ID != "B-2" || got["shop/cart"].Severity != "major" ||
		got["shop/checkout"].ID != "" || got["shop/checkout"].Severity != "critical" ||
		got["shop/product"].Severity != "cosmetic" || len(got["shop/cart"].Cells) != 1 {
		t.Fatalf("the bugs: %+v", put.Bugs)
	}
	md, _ := srv.list()[3].Args["markdown"].(string)
	for _, want := range []string{"**shop**: 1 of 5 cells passed", "**account**: 0 of 1", "**phone**", "- critical: The checkout"} {
		if !strings.Contains(md, want) {
			t.Errorf("the conclusions have no %q:\n%s", want, md)
		}
	}
}

// Outside a test session the runner's words are a message like any other.
func TestFakeCellOnlyInATestSession(t *testing.T) {
	srv, ep := startToolServer(t, "", nil)
	events := runFakeTurn(t, ep, nil, cellMessage(1, ""))
	if len(srv.list()) != 0 {
		t.Fatalf("a chat called %s", srv.names())
	}
	text := ""
	for _, e := range events {
		if e.Type == TypePart && e.Kind == PartText {
			text = e.Text
		}
	}
	if !strings.HasPrefix(text, "you said: ") {
		t.Fatalf("the answer: %q", text)
	}
}
