package tests

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/testmode"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// catalogRepo is a git repository with a catalog of one area and two tests
// on two devices, and a folder below its root.
func catalogRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	files := map[string]string{
		".git/HEAD":                     "ref: refs/heads/main\n",
		"sub/x.txt":                     "x",
		".socrates/tests/TESTS.md":      "---\ntitle: Shop\n---\nTest the shop.\n",
		".socrates/tests/web/AREA.md":   "---\ntitle: Web\ndimensions:\n  device:\n    values: {a: A, b: B}\nevidence: none\n---\n",
		".socrates/tests/web/buy.md":    "---\ntitle: Buy\npriority: P0\ntags: [smoke]\n---\n## Steps\n\n1. Buy.\n   Expect: bought.\n",
		".socrates/tests/web/browse.md": "---\ntitle: Browse\npriority: P1\n---\n## Steps\n\n1. Browse.\n   Expect: a list.\n",
	}
	for name, text := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// host is a test runner's host that starts no session.
type host struct{}

func (host) StartSession(context.Context, testmode.SessionSpec) (string, error) {
	return "", errors.New("no sessions in this test")
}
func (host) SendMessage(context.Context, string, string) error             { return nil }
func (host) StopSession(context.Context, string)                           {}
func (host) RemoveSession(context.Context, string)                         {}
func (host) SessionStatus(string) (bool, bool)                             { return false, false }
func (host) PickAccount(string, []string, map[string]int) (string, string) { return "acct", "" }
func (host) Publish(map[string]any)                                        {}
func (host) Notify(string, string, string)                                 {}
func (host) GetKV(string) (string, error)                                  { return "", nil }
func (host) SetKV(string, string) error                                    { return nil }
func (host) MCPPool(string) mcpserver.Pool                                 { return nil }
func (host) EndMCP(string)                                                 {}
func (host) SessionCosts([]string) map[string]float64                      { return map[string]float64{} }

type tester struct {
	t    *testing.T
	srv  *Server
	sess mcpserver.Session
}

func (x tester) call(tool, args string) mcpserver.Result {
	x.t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	return x.srv.Call(context.Background(), x.sess, tool, raw)
}

// ok calls a tool that must answer, and gives its text.
func (x tester) ok(tool, args string, want ...string) string {
	x.t.Helper()
	res := x.call(tool, args)
	text := res.Content[0].Text
	if res.IsError {
		x.t.Fatalf("%s %s: %s", tool, args, text)
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			x.t.Errorf("%s %s lacks %q:\n%s", tool, args, w, text)
		}
	}
	return text
}

// fails calls a tool that must refuse, with words that say why.
func (x tester) fails(tool, args string, want string) {
	x.t.Helper()
	res := x.call(tool, args)
	if !res.IsError || !strings.Contains(res.Content[0].Text, want) {
		x.t.Errorf("%s %s: want an error with %q, got %+v", tool, args, want, res)
	}
}

func newTester(t *testing.T, repo string, runner *testmode.Runner) tester {
	srv := New(Deps{Runner: func() *testmode.Runner { return runner }})
	return tester{t: t, srv: srv, sess: mcpserver.Session{ID: "chat1", Title: "Fix the cart", Workdir: filepath.Join(repo, "sub")}}
}

// Every tool is listed once, with a schema, and the tools the chat tools
// have too keep their names and schemas.
func TestTheToolsAreListedOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range New(Deps{}).Tools() {
		if seen[tool.Name] || tool.Schema["type"] != "object" || tool.Description == "" {
			t.Errorf("tool %s: listed twice or without a schema or description", tool.Name)
		}
		seen[tool.Name] = true
	}
	for _, d := range chattools.TestTools() {
		if !seen[d.Name] {
			t.Errorf("%s is missing", d.Name)
		}
	}
	if !mcpserver.NameRE.MatchString(Name) {
		t.Fatalf("bad name %s", Name)
	}
}

// The catalog's files are read, written and deleted inside the catalog
// only, and every change is checked.
func TestTheCatalogsFiles(t *testing.T) {
	repo := catalogRepo(t)
	x := newTester(t, repo, nil)
	if got := x.ok(chattools.TestsFormat, ""); got != catalog.Format {
		t.Fatal("tests_format is not the format reference")
	}
	x.ok(chattools.TestsCheck, "", "2 test(s), 4 cell(s); 0 error(s)")
	x.ok(CatalogRead, "", "- TESTS.md", "- web/AREA.md", "- web/buy.md")
	x.ok(CatalogRead, `{"path":".socrates/tests/web/buy.md"}`, "title: Buy")

	x.ok(CatalogWrite, `{"path":"web/cart/empty.md","content":"---\ntitle: Empty cart\npriority: P2\n---\n## Steps\n\n1. Open the cart.\n   Expect: it says it is empty.\n"}`,
		"Wrote .socrates/tests/web/cart/empty.md", "It has no problems.", "3 test(s), 6 cell(s); 0 error(s)")
	x.ok(CatalogWrite, `{"path":"web/bad.md","content":"---\ntitel: x\n---\n"}`, "Its problems:", ".socrates/tests/web/bad.md")
	x.ok(CatalogWrite, `{"path":"tools/shot/bin/shot","content":"#!/bin/sh\n","executable":true}`, "Wrote")
	if fi, err := os.Stat(filepath.Join(repo, ".socrates/tests/tools/shot/bin/shot")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the tool is not executable: %v", err)
	}
	x.ok(CatalogWrite, `{"path":"tools/shot/bin/shot","content":"#!/bin/sh\necho\n"}`, "Wrote")
	if fi, err := os.Stat(filepath.Join(repo, ".socrates/tests/tools/shot/bin/shot")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the tool written again lost its executable bit: %v", err)
	}
	x.ok(CatalogDelete, `{"path":"web/cart/empty.md"}`, "Deleted .socrates/tests/web/cart/empty.md")
	if _, err := os.Stat(filepath.Join(repo, ".socrates/tests/web/cart")); !os.IsNotExist(err) {
		t.Fatalf("the empty folder stayed: %v", err)
	}

	for _, bad := range []string{"../x.md", "/etc/passwd", "web/.hidden.md", "web/../../x.md", "web/a b.md", ""} {
		args, _ := json.Marshal(map[string]string{"path": bad, "content": "x"})
		x.fails(CatalogWrite, string(args), "")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, ".socrates/tests/out")); err != nil {
		t.Fatal(err)
	}
	x.fails(CatalogWrite, `{"path":"out/x.md","content":"x"}`, "symbolic link")
	x.fails(CatalogRead, `{"path":"web/nope.md"}`, "has no file")
	x.fails(CatalogRead, `{"path":"web/buy.md","nope":1}`, "could not be read")
	x.fails(Runs, "", "not running")

	// A test reads with its file, without a runner too.
	x.ok(TestRead, `{"test":"web/buy"}`, ".socrates/tests/web/buy.md", "title: Buy")
}

// A folder without a catalog gets one written from nothing.
func TestAFirstCatalogIsWritten(t *testing.T) {
	repo := t.TempDir()
	x := newTester(t, repo, nil)
	x.sess.Workdir = repo
	x.ok(CatalogRead, "", "has no .socrates/tests/ yet")
	x.ok(CatalogWrite, `{"path":"web/AREA.md","content":"---\ntitle: Web\n---\n"}`, "has no TESTS.md yet")
	x.ok(CatalogWrite, `{"path":"TESTS.md","content":"---\ntitle: New\n---\nTest it.\n"}`, "1 area(s)")
}

// A test session never uses the server, and an area that names it is
// refused.
func TestATestSessionCannotUseIt(t *testing.T) {
	x := newTester(t, catalogRepo(t), nil)
	x.sess.Test = &mcpserver.TestCell{Run: "r"}
	x.fails(chattools.TestsCheck, "", "a test session cannot use it")
	if len(New(Deps{}).CheckArea(nil, nil)) == 0 {
		t.Fatal("an area may name it")
	}
}

// With a runner: settings, estimates, runs, bugs, search; run_tests starts
// a run for the chat that the runs list and test_run shows.
func TestRunsAndSettings(t *testing.T) {
	repo := catalogRepo(t)
	runner := testmode.New(t.TempDir(), host{})
	t.Cleanup(runner.Close)
	x := newTester(t, repo, runner)

	x.ok(Runs, "", "no test run yet")
	x.ok(Bugs, "", "no bugs")
	x.ok(Estimate, `{"tests":["web/buy"]}`, "2 cell(s) of 1 test(s)")
	x.ok(Settings, "", `"parallel"`, `"claude-sonnet-5-5"`)
	x.ok(Settings, `{"set":{"parallel":2,"cell":{"model":"claude-opus-5-5"}}}`, `"parallel": 2`, `"model": "claude-opus-5-5"`, `"effort": "medium"`)
	x.fails(Settings, `{"set":{"paralel":2}}`, "could not be read")

	x.ok(Settings, `{"set":{"cell":{"accounts":["a","b"]}}}`, `"a"`)
	refused := New(Deps{Runner: func() *testmode.Runner { return runner }, CheckSettings: func(*testmode.Settings) string { return "no such harness" }})
	if res := refused.Call(context.Background(), x.sess, Settings, json.RawMessage(`{"set":{"parallel":3,"cell":{"accounts":["x"]}}}`)); !res.IsError {
		t.Fatalf("checked settings: %+v", res)
	}
	// Refused settings leave the runner's as they were.
	if got := runner.Settings(testmode.RepoKey(repo)); got.Parallel != 2 || strings.Join(got.Cell.Accounts, ",") != "a,b" {
		t.Fatalf("refused settings changed the runner's: %+v", got)
	}

	x.fails(chattools.RunTests, `{"tests":["web/nope"]}`, `there is no test "web/nope"`)
	x.ok(chattools.RunTests, `{"tests":["web/buy"],"name":"cart"}`, "Started test run 1", "2 cell(s)")
	x.ok(Runs, "", "Run 1", `"cart"`, `started by the chat "Fix the cart"`)
	x.ok(Run, "", "Run 1")
	x.fails(Cell, `{"cell":"web/nope@a"}`, "no cell")
	x.fails(RunControl, `{"run":"latest","action":"explode"}`, "must be one of")
	x.fails(Bug, `{"id":"B-9"}`, "no bug B-9")
	x.ok(Search, `{"query":"nothing like it"}`, "No cell")
	x.fails(Run, `{"run":"r-nope"}`, "no run r-nope")
}

// run_tests' arguments become the run's selection, cleaned up.
func TestRunTestsArgumentsAreTheSelection(t *testing.T) {
	var a chattools.RunTestsArgs
	raw := `{"areas":["web"," "],"tests":["web/buy.md"],"cells":["web/buy@a"],"tags":["smoke"],"priorities":["p0"],
		"dims":{"device":["a",""],"locale":[]},"problems_of":" latest ","name":"after the fix"}`
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	sel := SelectionOf(a)
	got, _ := json.Marshal(sel)
	want := `{"areas":["web"],"tests":["web/buy"],"tags":["smoke"],"priorities":["P0"],"dims":{"device":["a"]},"cells":["web/buy@a"],"problems_of":"latest"}`
	if string(got) != want {
		t.Fatalf("selection\n%s\nwant\n%s", got, want)
	}
	if a.Name != "after the fix" {
		t.Fatalf("name %q", a.Name)
	}
}

// test_results is cut at its cap, at a line's end, with a note that says
// so; a short one is left as it is.
func TestTestResultsAreCapped(t *testing.T) {
	if got := CapReport("short\n", 100, true); got != "short\n" {
		t.Fatalf("short: %q", got)
	}
	long := strings.Repeat("a line of the report\n", 5000)
	got := CapReport(long, chattools.MaxResults, true)
	body, note, ok := strings.Cut(got, "\n\n[The report is cut here at 40 KB")
	if !ok || len(body) > chattools.MaxResults || !strings.HasSuffix(body, "a line of the report") || !strings.HasSuffix(note, "]") {
		t.Fatalf("capped: %d bytes, ends %q", len(got), got[len(got)-200:])
	}
	if got := CapReport(long, 1000, false); !strings.Contains(got, "problems_only true") {
		t.Fatalf("not problems only: %q", got[len(got)-200:])
	}
}
