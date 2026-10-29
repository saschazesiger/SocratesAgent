package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/testmode"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// catalogRepo is a git repository with a catalog of one area and one test
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
		".socrates/tests/web/browse.md": "---\ntitle: Browse\n---\n## Steps\n\n1. Browse.\n   Expect: a list.\n",
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

// A chat gets the test tools when its repository has a catalog, or when it
// was started for them; a test session and a chat elsewhere do not.
func TestAChatIsOfferedTheTestToolsWhereThereIsACatalog(t *testing.T) {
	repo := catalogRepo(t)
	chat := func(workdir string, options json.RawMessage) *store.Session {
		return &store.Session{ID: "c", Kind: store.KindChat, Workdir: workdir, Options: options}
	}
	if !offersTestTools(chat(filepath.Join(repo, "sub"), nil)) {
		t.Error("a chat below a repository with a catalog")
	}
	if offersTestTools(chat(t.TempDir(), nil)) || offersTestTools(chat("", nil)) {
		t.Error("a chat in a folder without a catalog")
	}
	if !offersTestTools(chat(t.TempDir(), withOption(nil, testToolsOption, true))) {
		t.Error("a chat started to write a test")
	}
	cell := chat(repo, withOption(withOption(nil, testToolsOption, true), store.TestOption, map[string]string{"kind": "test-cell"}))
	if offersTestTools(cell) {
		t.Error("a test session")
	}
	term := &store.Session{ID: "t", Kind: "terminal", Workdir: repo}
	if offersTestTools(term) {
		t.Error("a terminal")
	}
}

// run_tests' arguments become the run's selection, cleaned up.
func TestRunTestsArgumentsAreTheSelection(t *testing.T) {
	var a chattools.RunTestsArgs
	raw := `{"areas":["web"," "],"tests":["web/buy.md"],"cells":["web/buy@a"],"tags":["smoke"],"priorities":["p0"],
		"dims":{"device":["a",""],"locale":[]},"problems_of":" latest ","name":"after the fix"}`
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	sel := selectionOf(a)
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
	if got := capReport("short\n", 100, true); got != "short\n" {
		t.Fatalf("short: %q", got)
	}
	long := strings.Repeat("a line of the report\n", 5000)
	got := capReport(long, chattools.MaxResults, true)
	body, note, ok := strings.Cut(got, "\n\n[The report is cut here at 40 KB")
	if !ok || len(body) > chattools.MaxResults || !strings.HasSuffix(body, "a line of the report") || !strings.HasSuffix(note, "]") {
		t.Fatalf("capped: %d bytes, ends %q", len(got), got[len(got)-200:])
	}
	if got := capReport(long, 1000, false); !strings.Contains(got, "problems_only true") {
		t.Fatalf("not problems only: %q", got[len(got)-200:])
	}
}

// tests_format is the format reference; tests_check lists the catalog's
// problems, areas, tests and cells, or says there is none; run_tests and
// test_results say when the runner is not there.
func TestTheTestToolsAnswer(t *testing.T) {
	s := &Server{}
	repo := catalogRepo(t)
	row := &store.Session{ID: "c", Kind: store.KindChat, Workdir: filepath.Join(repo, "sub"), Title: "Fix"}
	if res := s.testChatCall(row, chattools.TestsFormat, nil); res.Error || res.Text != catalog.Format {
		t.Fatalf("format: %+v", res.Error)
	}
	res := s.testChatCall(row, chattools.TestsCheck, nil)
	for _, want := range []string{"The test catalog of Shop", "1 area(s), 2 test(s), 4 cell(s); 0 error(s), 1 warning(s)",
		".socrates/tests/web/browse.md: no priority", "Area web (Web); device: a, b", "- web/buy [P0; tags smoke] Buy: 1 step(s), cells web/buy@a web/buy@b"} {
		if res.Error || !strings.Contains(res.Text, want) {
			t.Errorf("check lacks %q:\n%s", want, res.Text)
		}
	}
	none := &store.Session{ID: "d", Kind: store.KindChat, Workdir: t.TempDir()}
	if res := s.testChatCall(none, chattools.TestsCheck, nil); res.Error || !strings.Contains(res.Text, "has no test catalog") ||
		!strings.Contains(res.Text, chattools.TestsFormat) {
		t.Fatalf("no catalog: %+v", res)
	}
	for _, tool := range []string{chattools.RunTests, chattools.TestResults} {
		if res := s.testChatCall(row, tool, nil); !res.Error || !strings.Contains(res.Text, "not running") {
			t.Fatalf("%s without a runner: %+v", tool, res)
		}
	}

	// With a runner: run_tests refuses a folder without a catalog with a
	// pointer to tests_format, and test_results says there is no run yet.
	s.tests = testmode.New(t.TempDir(), testHost{s})
	if res := s.testChatCall(none, chattools.RunTests, json.RawMessage(`{"tests":["web/buy"]}`)); !res.Error ||
		!strings.Contains(res.Text, "no test catalog") || !strings.Contains(res.Text, chattools.TestsFormat) {
		t.Fatalf("run_tests without a catalog: %+v", res)
	}
	if res := s.testChatCall(row, chattools.RunTests, json.RawMessage(`{"tests":["web/nope"]}`)); !res.Error ||
		!strings.Contains(res.Text, `there is no test "web/nope"`) {
		t.Fatalf("run_tests of an unknown test: %+v", res)
	}
	if res := s.testChatCall(row, chattools.TestResults, nil); !res.Error || !strings.Contains(res.Text, "no test run yet") {
		t.Fatalf("test_results without a run: %+v", res)
	}
}
