package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeRepo writes a repository holding files (paths below .socrates/tests,
// or below the repository with a leading "/"; a path ending in / is an
// empty folder) and returns its root.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, ".socrates", "tests", filepath.FromSlash(name))
		if strings.HasPrefix(name, "/") {
			p = filepath.Join(root, filepath.FromSlash(name[1:]))
		}
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		// The mode is set past the umask, for the copies' modes below.
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil || os.Chmod(p, 0o644) != nil {
			t.Fatal(err)
		}
	}
	return root
}

// testFile is a test with front matter fm and a single step.
func testFile(fm string) string {
	return "---\n" + fm + "\n---\nContext.\n\n## Steps\n\n1. Do it.\n   Expect: it is done.\n"
}

// baseRepo is a valid catalog of two areas and a tool, which the problem
// cases below break one file at a time.
func baseRepo() map[string]string {
	return map[string]string{
		"TESTS.md":  "---\ntitle: Shop\n---\nBeta only.\n",
		"README.md": "For people.",
		"web/AREA.md": `---
title: Web app
tools: [driver]
dimensions:
  device:
    values: {phone: Phone, desk: Desktop}
    extra: {old: Old phone}
  locale:
    values: [en, de]
    default: rotate
params: {consent: accept, speed: fast}
timeout: 30m
---
Web instructions.
`,
		"web/home.md":           testFile("title: Home\npriority: P0\nparams: {speed: slow}\ntimeout: 1h30m\ntags: [smoke]"),
		"web/sign-in/form.md":   testFile("title: Sign-in form\npriority: P2\ndevice: [old, phone]\ntools: [mailer, driver]\nexclusive: [mailbox]"),
		"web/sign-in/README.md": "ignored",
		"web/.draft.md":         "ignored, hidden",
		"web/.hidden/x.md":      "ignored, hidden",
		"web/notes.txt":         "not a test",
		"api/AREA.md":           "No front matter at all.\n",
		"api/ping.md":           testFile("title: Ping\npriority: P1"),
		"tools/README.md":       "not a tool",
		"tools/driver/TOOL.md": `---
title: Driver
setup: npm ci
end: bin/drv stop
secrets:
  command: secrets show
  names: [ACCOUNTS, LOGIN?]
pool: {secret: ACCOUNTS, per_member: 2}
---
Run drv.
`,
		"tools/driver/bin/":    "",
		"tools/mailer/TOOL.md": "---\nbin: cmd\n---\nRead mail.\n",
		"tools/mailer/cmd/":    "",
	}
}

// A catalog is read with every default filled in: areas and tests in id
// order, README.md and hidden files left out, a test's params and timeout
// on top of its area's, every dimension's selection known.
func TestLoadReadsACatalog(t *testing.T) {
	c, err := Load(writeRepo(t, baseRepo()))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Errors) != 0 {
		t.Fatalf("errors: %v", c.Errors)
	}
	if c.Title != "Shop" || c.Instructions != "Beta only." || !filepath.IsAbs(c.Dir) {
		t.Fatalf("catalog: %q %q %q", c.Title, c.Instructions, c.Dir)
	}
	if len(c.Areas) != 2 || c.Areas[0].ID != "api" || c.Areas[1].ID != "web" {
		t.Fatalf("areas: %+v", c.Areas)
	}
	api, web := c.Areas[0], c.Areas[1]
	if api.Title != "api" || api.Instructions != "No front matter at all." || api.Evidence != EvidenceScreenshot ||
		api.Timeout != DefaultTimeout || api.Columns != "" || len(api.Dimensions) != 0 {
		t.Fatalf("api: %+v", api)
	}
	if web.Title != "Web app" || web.Columns != "device" || web.Timeout != 30*time.Minute ||
		!reflect.DeepEqual(web.ParamOrder, []string{"consent", "speed"}) {
		t.Fatalf("web: %+v", web)
	}
	dev, loc := web.Dimensions[0], web.Dimensions[1]
	if dev.Name != "device" || !reflect.DeepEqual(dev.Values, []Value{{"phone", "Phone"}, {"desk", "Desktop"}}) ||
		!reflect.DeepEqual(dev.Extra, []Value{{"old", "Old phone"}}) || dev.Default.Kind != SelectAll {
		t.Fatalf("device: %+v", dev)
	}
	if !reflect.DeepEqual(loc.Values, []Value{{"en", "en"}, {"de", "de"}}) || loc.Default.Kind != SelectRotate {
		t.Fatalf("locale: %+v", loc)
	}
	var ids []string
	for _, x := range web.Tests {
		ids = append(ids, x.ID)
	}
	if !reflect.DeepEqual(ids, []string{"web/home", "web/sign-in/form"}) {
		t.Fatalf("tests: %v", ids)
	}
	home, form := c.Test("web/home"), c.Test("web/sign-in/form")
	if home.Index != 0 || form.Index != 1 || home.File != "web/home.md" || home.Area != "web" {
		t.Fatalf("index: %+v %+v", home, form)
	}
	if home.Title != "Home" || home.Priority != P0 || home.Timeout != 90*time.Minute ||
		!reflect.DeepEqual(home.Params, map[string]string{"consent": "accept", "speed": "slow"}) ||
		!reflect.DeepEqual(home.Tags, []string{"smoke"}) || home.Context != "Context." ||
		!reflect.DeepEqual(home.Steps, []Step{{1, "Do it.", "it is done."}}) {
		t.Fatalf("home: %+v", home)
	}
	if !reflect.DeepEqual(home.Select, map[string]Selection{"device": {Kind: SelectAll}, "locale": {Kind: SelectRotate}}) {
		t.Fatalf("home select: %+v", home.Select)
	}
	if form.Timeout != 30*time.Minute || !reflect.DeepEqual(form.Select["device"], Selection{SelectList, []string{"old", "phone"}}) ||
		!reflect.DeepEqual(form.Exclusive, []string{"mailbox"}) {
		t.Fatalf("form: %+v", form)
	}
	var tools []string
	for _, tool := range c.ToolsOf(form) {
		tools = append(tools, tool.Name)
	}
	if !reflect.DeepEqual(tools, []string{"driver", "mailer"}) {
		t.Fatalf("tools of form: %v", tools)
	}
	if c.Test("web/sign-in/README") != nil || c.Test("web/.draft") != nil || c.TestCount() != 3 {
		t.Fatalf("README or hidden files read as tests")
	}

	drv, mail := c.Tools["driver"], c.Tools["mailer"]
	wantDrv := &Tool{
		Name: "driver", Dir: filepath.Join(c.Dir, "tools", "driver"), Title: "Driver", Bin: "bin",
		Setup: "npm ci", End: "bin/drv stop", Instructions: "Run drv.",
		Secrets: Secrets{Command: "secrets show", Names: []SecretName{{"ACCOUNTS", false}, {"LOGIN", true}}},
		Pool:    &Pool{Secret: "ACCOUNTS", Separator: ",", PerMember: 2},
	}
	if !reflect.DeepEqual(drv, wantDrv) {
		t.Fatalf("driver:\n%+v\nwant\n%+v", drv, wantDrv)
	}
	if mail.Title != "mailer" || mail.Bin != "cmd" || mail.Pool != nil || len(c.Tools) != 2 {
		t.Fatalf("mailer: %+v", mail)
	}

	// The only warning: api/ping and web/home name their priority, the
	// form names P2; api's test names it too, so none is about priority.
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings: %v", c.Warnings)
	}
}

// Without a catalog there is nothing to check: an error, not a Problem.
func TestLoadNeedsACatalog(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no test catalog") {
		t.Fatalf("no folder: %v", err)
	}
	root := writeRepo(t, map[string]string{"web/": ""})
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "no TESTS.md") {
		t.Fatalf("no TESTS.md: %v", err)
	}
	c, err := Load(writeRepo(t, map[string]string{"TESTS.md": "Hello."}))
	if err != nil || c.Title == "" || c.Title != filepath.Base(c.Root) || len(c.Areas) != 0 {
		t.Fatalf("a catalog of TESTS.md alone: %+v %v", c, err)
	}
}

// Every mistake in a catalog is an error with its file and line, and the
// rest of the catalog is still read.
func TestCatalogProblems(t *testing.T) {
	big := strings.Repeat("x", MaxPromptBytes)
	for _, tc := range []struct {
		name string
		edit map[string]string // "" removes the file
		file string
		line int
		want string
	}{
		{"yaml error", map[string]string{"web/home.md": "---\ntitle: Home\ntags: [a\n---\n## Steps\n1. a\n   Expect: b\n"}, "web/home.md", 3, "not closed"},
		{"unknown test key", map[string]string{"web/home.md": testFile("title: Home\npriority: P0\nlocales: rotate")}, "web/home.md", 4, `unknown key "locales"`},
		{"dimension of another area", map[string]string{"api/ping.md": testFile("title: Ping\npriority: P1\ndevice: [phone]")}, "api/ping.md", 4, `unknown key "device"`},
		{"unknown area key", map[string]string{"api/AREA.md": "---\ntitel: API\n---\n"}, "api/AREA.md", 2, `unknown key "titel"`},
		{"unknown tool key", map[string]string{"tools/mailer/TOOL.md": "---\nbin: cmd\nsetupp: x\n---\n"}, "tools/mailer/TOOL.md", 3, `unknown key "setupp"`},
		{"unknown tests key", map[string]string{"TESTS.md": "---\nname: x\n---\n"}, "TESTS.md", 2, `unknown key "name"`},
		{"folder without AREA.md", map[string]string{"docs/x.md": "x"}, "docs/", 0, "needs an AREA.md"},
		{"AREA.md in a sub-folder", map[string]string{"web/sign-in/AREA.md": "x"}, "web/sign-in/AREA.md", 0, "only the area's own folder"},
		{"no title", map[string]string{"api/ping.md": testFile("priority: P1")}, "api/ping.md", 2, "no title"},
		{"no steps", map[string]string{"api/ping.md": "---\ntitle: Ping\npriority: P1\n---\nNothing.\n"}, "api/ping.md", 0, "no ## Steps"},
		{"no expect", map[string]string{"api/ping.md": "---\ntitle: Ping\npriority: P1\n---\n## Steps\n1. Do.\n"}, "api/ping.md", 6, "no line starting with Expect:"},
		{"bad priority", map[string]string{"api/ping.md": testFile("title: Ping\npriority: high")}, "api/ping.md", 3, "P0, P1, P2"},
		{"bad timeout", map[string]string{"api/ping.md": testFile("title: Ping\npriority: P1\ntimeout: 45")}, "api/ping.md", 4, "duration"},
		{"bad tags", map[string]string{"api/ping.md": testFile("title: Ping\npriority: P1\ntags: {a: b}")}, "api/ping.md", 4, "a list"},
		{"bad exclusive", map[string]string{"api/ping.md": testFile("title: Ping\npriority: P1\nexclusive: [Mail Box]")}, "api/ping.md", 4, "not a name"},
		{"unknown value", map[string]string{"web/home.md": testFile("title: Home\npriority: P0\ndevice: [phone, tablet]")}, "web/home.md", 4, `"tablet" is not a value of device (phone, desk, old)`},
		{"except unknown", map[string]string{"web/home.md": testFile("title: Home\npriority: P0\ndevice: {except: [tv]}")}, "web/home.md", 4, "not a value"},
		{"except leaves none", map[string]string{"web/home.md": testFile("title: Home\npriority: P0\ndevice: {except: [phone, desk]}")}, "web/home.md", 4, "leaves no value"},
		{"bad selection", map[string]string{"web/home.md": testFile("title: Home\npriority: P0\ndevice: {only: [phone]}")}, "web/home.md", 4, "all, rotate"},
		{"empty selection", map[string]string{"web/home.md": testFile("title: Home\npriority: P0\ndevice: []")}, "web/home.md", 4, "selects no value"},
		{"two rotations", map[string]string{"web/home.md": testFile("title: Home\npriority: P0\ndevice: rotate")}, "web/home.md", 4, "only one dimension may rotate"},
		{"two rotations in the area", map[string]string{"api/AREA.md": "---\ndimensions:\n  a:\n    values: [x]\n    default: rotate\n  b:\n    values: [y]\n    default: rotate\n---\n"}, "api/AREA.md", 8, "only one dimension may rotate"},
		{"columns unknown", map[string]string{"api/AREA.md": "---\ncolumns: device\n---\n"}, "api/AREA.md", 2, "columns names a dimension"},
		{"dimension without values", map[string]string{"api/AREA.md": "---\ndimensions:\n  os: {default: all}\n---\n"}, "api/AREA.md", 3, "has no values"},
		{"dimension named like a key", map[string]string{"api/AREA.md": "---\ndimensions:\n  tags: {values: [a]}\n---\n"}, "api/AREA.md", 3, "key of every test"},
		{"dimension name", map[string]string{"api/AREA.md": "---\ndimensions:\n  Device: {values: [a]}\n---\n"}, "api/AREA.md", 3, "not a dimension's name"},
		{"dimension with a token", map[string]string{"api/AREA.md": "---\ndimensions:\n  api_token: {values: [a]}\n---\n"}, "api/AREA.md", 3, "KEY, SECRET or TOKEN"},
		{"value with a dot", map[string]string{"api/AREA.md": "---\ndimensions:\n  os:\n    values: {a.b: A}\n---\n"}, "api/AREA.md", 4, "not a value id"},
		{"value named all", map[string]string{"api/AREA.md": "---\ndimensions:\n  os:\n    values: [all, b]\n---\n"}, "api/AREA.md", 4, "word of the selections"},
		{"extra twice", map[string]string{"api/AREA.md": "---\ndimensions:\n  os:\n    values: {a: A}\n    extra: {a: B}\n---\n"}, "api/AREA.md", 5, "defined twice"},
		{"param with a key", map[string]string{"api/AREA.md": "---\nparams:\n  api_key: x\n---\n"}, "api/AREA.md", 3, "KEY, SECRET or TOKEN"},
		{"param nested", map[string]string{"api/AREA.md": "---\nparams:\n  a: [x]\n---\n"}, "api/AREA.md", 3, "single value"},
		{"bad evidence", map[string]string{"api/AREA.md": "---\nevidence: video\n---\n"}, "api/AREA.md", 2, "screenshot, any, none"},
		{"unknown area tool", map[string]string{"api/AREA.md": "---\ntools: [nope]\n---\n"}, "api/AREA.md", 2, `no tool "nope"`},
		{"unknown test tool", map[string]string{"api/ping.md": testFile("title: Ping\npriority: P1\ntools: [nope]")}, "api/ping.md", 4, `no tool "nope"`},
		{"mcp without .mcp.json", map[string]string{"api/AREA.md": "---\nmcp: [db]\n---\n"}, "api/AREA.md", 2, `no server "db"`},
		{"mcp unknown", map[string]string{"api/AREA.md": "---\nmcp: [db]\n---\n", "/.mcp.json": `{"mcpServers": {"web": {}}}`}, "api/AREA.md", 2, `no server "db"`},
		{"previous is a test", map[string]string{"api/ping.md": testFile("title: Ping\npriority: P1\nprevious: web/home")}, "api/ping.md", 4, "is a test of the catalog"},
		{"previous twice", map[string]string{"api/ping.md": testFile("title: Ping\npriority: P1\nprevious: old/x"), "api/pong.md": testFile("title: Pong\npriority: P1\nprevious: old/x")}, "api/pong.md", 4, "already carries on"},
		{"previous is itself", map[string]string{"api/ping.md": testFile("title: Ping\npriority: P1\nprevious: api/ping")}, "api/ping.md", 4, "the test itself"},
		{"bad file name", map[string]string{"api/a b.md": testFile("title: x")}, "api/a b.md", 0, "file name is part of its id"},
		{"bad tool name", map[string]string{"tools/My_Tool/TOOL.md": "---\n---\n"}, "tools/My_Tool/", 0, "lower-case letters"},
		{"tool without TOOL.md", map[string]string{"tools/other/bin/x": "x"}, "tools/other/", 0, "needs a TOOL.md"},
		{"bin missing", map[string]string{"tools/mailer/TOOL.md": "---\nbin: nope\n---\n"}, "tools/mailer/TOOL.md", 2, "no folder nope"},
		{"bin outside", map[string]string{"tools/mailer/TOOL.md": "---\nbin: ../driver/bin\n---\n"}, "tools/mailer/TOOL.md", 2, "inside the tool's own"},
		{"default bin missing", map[string]string{"tools/mailer/TOOL.md": "---\ntitle: Mail\n---\n"}, "tools/mailer/TOOL.md", 2, "no folder bin"},
		{"pool secret not named", map[string]string{"tools/driver/TOOL.md": "---\nsecrets: {command: s, names: [A]}\npool: {secret: B}\n---\n"}, "tools/driver/TOOL.md", 3, "not in secrets.names"},
		{"pool secret optional", map[string]string{"tools/driver/TOOL.md": "---\nsecrets: {command: s, names: [A?]}\npool: {secret: A}\n---\n"}, "tools/driver/TOOL.md", 3, "optional"},
		{"pool without secret", map[string]string{"tools/driver/TOOL.md": "---\npool: {separator: ;}\n---\n"}, "tools/driver/TOOL.md", 2, "pool.secret is missing"},
		{"per_member", map[string]string{"tools/driver/TOOL.md": "---\nsecrets: {command: s, names: [A]}\npool: {secret: A, per_member: 0}\n---\n"}, "tools/driver/TOOL.md", 3, "at least 1"},
		{"secrets without command", map[string]string{"tools/driver/TOOL.md": "---\nsecrets:\n  names: [A]\n---\n"}, "tools/driver/TOOL.md", 3, "secrets.command is missing"},
		{"secrets without names", map[string]string{"tools/driver/TOOL.md": "---\nsecrets:\n  command: x\n---\n"}, "tools/driver/TOOL.md", 3, "secrets.names is missing"},
		{"bad secret name", map[string]string{"tools/driver/TOOL.md": "---\nsecrets:\n  command: x\n  names: [A-B]\n---\n"}, "tools/driver/TOOL.md", 4, "not a secret's name"},
		{"prompt too large", map[string]string{"tools/mailer/TOOL.md": "---\nbin: cmd\n---\n" + big}, "web/AREA.md", 0, "more than the 56.0 KiB"},
	} {
		files := baseRepo()
		for name, content := range tc.edit {
			files[name] = content
		}
		c, err := Load(writeRepo(t, files))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		var found bool
		for _, p := range c.Errors {
			if p.File == tc.file && p.Line == tc.line && strings.Contains(p.Message, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %v, want %s:%d with %q", tc.name, c.Errors, tc.file, tc.line, tc.want)
		}
	}
}

// Two ids that differ only in case would be one file on a checkout on
// macOS or Windows.
func TestIDsDifferingOnlyInCase(t *testing.T) {
	files := baseRepo()
	files["api/Ping.md"] = testFile("title: Ping\npriority: P1")
	root := writeRepo(t, files)
	if entries, _ := os.ReadDir(filepath.Join(root, ".socrates", "tests", "api")); len(entries) != 3 {
		t.Skip("the file system ignores case")
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Errors) != 1 || c.Errors[0].String() != "api/ping.md: the id differs from api/Ping only in case" {
		t.Fatalf("errors: %v", c.Errors)
	}
}

// An MCP server the repository's .mcp.json has may be named.
func TestAreaNamesAnMCPServerOfTheRepository(t *testing.T) {
	files := baseRepo()
	files["api/AREA.md"] = "---\nmcp: [db]\n---\n"
	files["/.mcp.json"] = `{"mcpServers": {"db": {"command": "db-mcp"}}}`
	c, err := Load(writeRepo(t, files))
	if err != nil || len(c.Errors) != 0 || !reflect.DeepEqual(c.Areas[0].MCP, []string{"db"}) {
		t.Fatalf("%v %v", c.Errors, err)
	}
}

// Warnings do not stop a run: an area without tests, a test without a
// priority, a tool no test uses, a test file outside an area, a link.
func TestCatalogWarnings(t *testing.T) {
	files := baseRepo()
	files["empty/AREA.md"] = "---\ntitle: Empty\n---\n"
	files["api/ping.md"] = testFile("title: Ping")
	files["tools/unused/TOOL.md"] = "---\n---\n"
	files["tools/unused/bin/"] = ""
	files["stray.md"] = "x"
	root := writeRepo(t, files)
	if err := os.Symlink(filepath.Join(root, ".socrates/tests/api/ping.md"), filepath.Join(root, ".socrates/tests/api/link.md")); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil || len(c.Errors) != 0 {
		t.Fatalf("%v %v", c.Errors, err)
	}
	var got []string
	for _, w := range c.Warnings {
		got = append(got, w.String())
	}
	want := []string{
		"stray.md: not part of an area: a test goes into an area's folder",
		"api/link.md: a symbolic link is not part of the catalog (it is not copied into a run)",
		"empty/AREA.md: the area has no tests",
		"api/ping.md: no priority: the test runs as P1 (priority: P0, P1 or P2)",
		"tools/unused/TOOL.md: no test uses the tool",
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("missing warning %q in\n%s", w, strings.Join(got, "\n"))
		}
	}
	if len(got) != len(want) {
		t.Errorf("warnings:\n%s", strings.Join(got, "\n"))
	}
}

// A problem prints as a compiler's would.
func TestProblemString(t *testing.T) {
	if s := (Problem{"web/a.md", 3, "x"}).String(); s != "web/a.md:3: x" {
		t.Fatal(s)
	}
	if s := (Problem{"web/", 0, "x"}).String(); s != "web/: x" {
		t.Fatal(s)
	}
}
