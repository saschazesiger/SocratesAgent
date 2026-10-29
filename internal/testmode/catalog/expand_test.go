package catalog

import (
	"reflect"
	"strings"
	"testing"
)

// cellIDs lists the ids of cells.
func cellIDs(cells []Cell) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = c.ID()
	}
	return out
}

// A test runs on the values it selects, in the dimension's order: all of
// them, a list (extra values only when listed), all but some, or one per
// combination of the others, rotated by the test's index. Cells come by
// priority, then area, then test.
func TestTestsExpandIntoCells(t *testing.T) {
	area := `---
dimensions:
  device:
    values: {a: A, b: B, c: C}
    extra: {x: X}
  locale:
    values: {en: English, de: German, fr: French}
    default: rotate
---
`
	c, err := Load(writeRepo(t, map[string]string{
		"TESTS.md":  "",
		"x/AREA.md": area,
		"x/t0.md":   testFile("title: t0\npriority: P1"),
		"x/t1.md":   testFile("title: t1\npriority: P2\ndevice: [x, c, a]"),
		"x/t2.md":   testFile("title: t2\npriority: P1\ndevice: {except: [b]}\nlocale: [fr, en]"),
		"x/t3.md":   testFile("title: t3\npriority: P0\ndevice: rotate\nlocale: all"),
		"a/AREA.md": "",
		"a/one.md":  testFile("title: one\npriority: P1"),
	}))
	if err != nil || len(c.Errors) != 0 {
		t.Fatalf("%v %v", c.Errors, err)
	}
	for id, want := range map[string][]string{
		"x/t0":  {"x/t0@a.en", "x/t0@b.de", "x/t0@c.fr"},
		"x/t1":  {"x/t1@a.de", "x/t1@c.fr", "x/t1@x.en"},
		"x/t2":  {"x/t2@a.en", "x/t2@a.fr", "x/t2@c.en", "x/t2@c.fr"},
		"x/t3":  {"x/t3@a.en", "x/t3@b.de", "x/t3@c.fr"},
		"a/one": {"a/one"},
	} {
		if got := cellIDs(c.CellsOf(c.Test(id))); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", id, got, want)
		}
	}
	cell := c.CellsOf(c.Test("x/t1"))[2]
	if cell.Key != "x.en" || !reflect.DeepEqual(cell.Dims, []DimValue{{"device", "x", "X"}, {"locale", "en", "English"}}) {
		t.Errorf("cell: %+v", cell)
	}
	if one := c.CellsOf(c.Test("a/one"))[0]; one.Key != "" || len(one.Dims) != 0 {
		t.Errorf("a cell without dimensions: %+v", one)
	}
	got := strings.Join(cellIDs(c.Cells()), " ")
	want := "x/t3@a.en x/t3@b.de x/t3@c.fr " + // P0
		"a/one x/t0@a.en x/t0@b.de x/t0@c.fr x/t2@a.en x/t2@a.fr x/t2@c.en x/t2@c.fr " + // P1: area a before x
		"x/t1@a.de x/t1@c.fr x/t1@x.en" // P2
	if got != want {
		t.Errorf("order:\n got %s\nwant %s", got, want)
	}
}

// The rotated dimension counts the combinations of all the others, in their
// order, the first dimension outermost.
func TestRotationCountsTheCombinationsOfTheOtherDimensions(t *testing.T) {
	c, err := Load(writeRepo(t, map[string]string{
		"TESTS.md": "",
		"z/AREA.md": `---
dimensions:
  os: {values: [l, w]}
  locale: {values: [en, de, fr], default: rotate}
  device: {values: [p, q]}
---
`,
		"z/a.md": testFile("title: a\npriority: P1"),
		"z/b.md": testFile("title: b\npriority: P1"),
	}))
	if err != nil || len(c.Errors) != 0 {
		t.Fatalf("%v %v", c.Errors, err)
	}
	if got := strings.Join(cellIDs(c.CellsOf(c.Test("z/a"))), " "); got != "z/a@l.en.p z/a@l.de.q z/a@w.fr.p z/a@w.en.q" {
		t.Errorf("a: %s", got)
	}
	if got := strings.Join(cellIDs(c.CellsOf(c.Test("z/b"))), " "); got != "z/b@l.de.p z/b@l.fr.q z/b@w.en.p z/b@w.de.q" {
		t.Errorf("b: %s", got)
	}
	if c.Areas[0].Columns != "os" {
		t.Errorf("columns default to the first dimension: %q", c.Areas[0].Columns)
	}
}

func TestCellID(t *testing.T) {
	if got := CellID("web-browser/header-navigation", "pixel.es"); got != "web-browser/header-navigation@pixel.es" {
		t.Fatal(got)
	}
	if got := CellID("api/ping", ""); got != "api/ping" {
		t.Fatal(got)
	}
}

// browser.lol's Device QA converted (TEST-MODE.md, "Converting browser.lol's
// Device QA"): the catalog is clean and gives the cells qa's eval/flows.yaml
// gave, 14 of them. qa rotated the locales by a flow's position in its YAML
// file; Socrates counts a test's position in its area sorted by path, so
// language-switch and sign-in-validation keep qa's locales and the two other
// pairs (header-navigation and create-form, not-found and pricing-toggle)
// swap theirs.
func TestBrowserCatalog(t *testing.T) {
	c, err := Load("testdata/browser")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Errors) != 0 || len(c.Warnings) != 0 {
		t.Fatalf("errors %v, warnings %v", c.Errors, c.Warnings)
	}
	if c.Title != "browser.lol" || len(c.Areas) != 1 || c.TestCount() != 6 {
		t.Fatalf("catalog: %q %d areas %d tests", c.Title, len(c.Areas), c.TestCount())
	}
	web := c.Areas[0]
	dev := web.Dimensions[0]
	if web.Columns != "device" || len(dev.Values) != 9 || len(dev.Extra) != 2 || dev.Values[5] != (Value{"samsung", "Galaxy S25 · Samsung Internet"}) ||
		dev.Extra[1] != (Value{"iphone-se-ios15", "iPhone SE · iOS 15"}) || web.Dimensions[1].Default.Kind != SelectRotate ||
		len(web.Dimensions[1].Values) != 7 || web.Params["consent"] != "accept" {
		t.Fatalf("area: %+v", web)
	}
	nav := c.Test("web-browser/header-navigation")
	if nav.Priority != P0 || !reflect.DeepEqual(nav.Tags, []string{"eval", "probe"}) || len(nav.Steps) != 3 ||
		nav.Steps[2] != (Step{3, "Go back, then open the plans from the header (on a phone through the menu).", "/upgrade with the plans and their prices."}) {
		t.Fatalf("header-navigation: %+v", nav)
	}
	if s := c.Test("web-browser/create-form").Steps[0].Expect; !strings.HasSuffix(s, "(find it, do not start a session).") {
		t.Fatalf("an Expect over two lines: %q", s)
	}
	for id, steps := range map[string]int{"create-form": 4, "language-switch": 3, "pricing-toggle": 3, "not-found": 2, "sign-in-validation": 3} {
		if n := len(c.Test("web-browser/" + id).Steps); n != steps {
			t.Errorf("%s: %d steps, want %d", id, n, steps)
		}
	}

	cells := c.Cells()
	want := []string{
		// P0
		"web-browser/create-form@iphone.en", "web-browser/create-form@pixel.de", "web-browser/create-form@win-chrome.fr",
		"web-browser/header-navigation@iphone.de", "web-browser/header-navigation@pixel.fr", "web-browser/header-navigation@win-chrome.es",
		// P1
		"web-browser/language-switch@iphone.fr", "web-browser/language-switch@win-chrome.es", // as qa
		"web-browser/pricing-toggle@pixel.it", "web-browser/pricing-toggle@win-chrome.pt",
		"web-browser/sign-in-validation@pixel.pt", "web-browser/sign-in-validation@win-chrome.ja", // as qa
		// P2
		"web-browser/not-found@iphone.es", "web-browser/not-found@win-chrome.it",
	}
	if got := cellIDs(cells); !reflect.DeepEqual(got, want) {
		t.Fatalf("cells:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	bs := c.Tools["browserstack"]
	if bs == nil || bs.Title != "BrowserStack devices" || bs.Setup != "npm ci --no-audit --no-fund" || bs.End != "bin/qa stop" ||
		bs.Secrets.Command != "infra/secretctl show beta device-qa" || len(bs.Secrets.Names) != 7 ||
		bs.Secrets.Names[0] != (SecretName{"BROWSERSTACK_ACCOUNTS", false}) || bs.Secrets.Names[6] != (SecretName{"QA_MS_TOTP_SECRET", true}) ||
		*bs.Pool != (Pool{Secret: "BROWSERSTACK_ACCOUNTS", Separator: ",", PerMember: 1, Status: "bin/qa pool-status"}) {
		t.Fatalf("browserstack: %+v", bs)
	}
	if tools := c.ToolsOf(nav); len(tools) != 1 || tools[0] != bs {
		t.Fatalf("tools of a test: %v", tools)
	}
}
