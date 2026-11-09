package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var base = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

type event struct{ chat, id, what string }

// newStore is a store under a temporary folder whose clock moves a second
// on with every reading, and the changes it told.
func newStore(t *testing.T) (*Store, string, func() []event) {
	t.Helper()
	root := t.TempDir()
	s := New(func(chat string) string { return filepath.Join(root, "chats", chat) })
	var ticks atomic.Int64
	s.now = func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Second) }
	var (
		mu     sync.Mutex
		events []event
	)
	s.SetOnChange(func(chat, id, what string) {
		mu.Lock()
		events = append(events, event{chat, id, what})
		mu.Unlock()
	})
	return s, root, func() []event {
		mu.Lock()
		defer mu.Unlock()
		out := events
		events = nil
		return out
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateReadUpdateDelete(t *testing.T) {
	s, root, events := newStore(t)
	src := t.TempDir()
	png := writeFile(t, src, "chart.png", "PNG")

	a, err := s.Create("c1", Fields{Title: "  How a  request flows ", Description: "Browser to DB", Content: "<h1>Flow</h1><img src=\"chart.png\">", Files: []string{png}})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "A1" || a.Title != "How a request flows" || a.Format != HTML || len(a.Key) != 32 || a.Latest().N != 1 {
		t.Fatalf("created %+v", a)
	}
	if got := events(); len(got) != 1 || got[0] != (event{"c1", "A1", Created}) {
		t.Fatalf("events %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "chats", "c1", "artifacts", "pages", "A1", "1.html")); err != nil {
		t.Fatal(err)
	}
	p, err := s.FilePath("c1", "A1", a.Key, "chart.png")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "PNG" {
		t.Fatalf("file %q", b)
	}
	if _, err := s.FilePath("c1", "A1", strings.Repeat("0", 32), "chart.png"); err == nil {
		t.Fatal("a wrong key found the file")
	}
	if _, err := s.FilePath("c1", "a1", a.Key, "chart.png"); err == nil {
		t.Fatal("an id in lower case is an address too")
	}

	// Edits make version 2, with its note.
	a, ch, err := s.Update("c1", "A1", Patch{Edits: []Edit{{Old: "Flow", New: "Request flow"}}, Note: "Clearer heading"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Version != 2 || a.Latest().Note != "Clearer heading" {
		t.Fatalf("update %+v %+v", ch, a.Latest())
	}
	_, v, content, err := s.Content("c1", "How a request flows", 0)
	if err != nil || v.N != 2 || string(content) != "<h1>Request flow</h1><img src=\"chart.png\">" {
		t.Fatalf("content %v %d %q", err, v.N, content)
	}
	_, _, old, err := s.Content("c1", "a1", 1)
	if err != nil || string(old) != "<h1>Flow</h1><img src=\"chart.png\">" {
		t.Fatalf("version 1 %v %q", err, old)
	}
	if got := events(); len(got) != 1 || got[0].what != Updated {
		t.Fatalf("events %v", got)
	}

	// The same content again changes nothing and tells nothing.
	same := "<h1>Request flow</h1><img src=\"chart.png\">"
	if _, ch, err := s.Update("c1", "A1", Patch{Content: &same}); err != nil || ch.Version != 0 {
		t.Fatalf("same content %v %+v", err, ch)
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("events %v", got)
	}

	// A file replaced and one removed.
	png2 := writeFile(t, t.TempDir(), "chart.png", "PNG2")
	csv := writeFile(t, src, "data.csv", "a,b")
	a, ch, err = s.Update("c1", "A1", Patch{Files: []string{png2, csv}})
	if err != nil || len(ch.Attached) != 2 || len(a.Files) != 2 {
		t.Fatalf("attach %v %+v %+v", err, ch, a.Files)
	}
	p, _ = s.FilePath("c1", "A1", a.Key, "chart.png")
	if b, _ := os.ReadFile(p); string(b) != "PNG2" {
		t.Fatalf("replaced file %q", b)
	}
	a, _, err = s.Update("c1", "A1", Patch{RemoveFiles: []string{"data.csv"}})
	if err != nil || len(a.Files) != 1 {
		t.Fatalf("remove %v %+v", err, a.Files)
	}
	if _, err := os.Stat(filepath.Join(s.FilesDir("c1", "A1"), "data.csv")); !os.IsNotExist(err) {
		t.Fatalf("removed file still there: %v", err)
	}

	gone, err := s.Delete("c1", "A1")
	if err != nil || gone.ID != "A1" {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.FilesDir("c1", "A1")); !os.IsNotExist(err) {
		t.Fatal("files left after delete")
	}
	events()
	// Ids are never given twice.
	b, err := s.Create("c1", Fields{Title: "Next", Content: "x"})
	if err != nil || b.ID != "A2" {
		t.Fatalf("next id %v %s", err, b.ID)
	}
}

func TestEditsRefuse(t *testing.T) {
	s, _, _ := newStore(t)
	if _, err := s.Create("c", Fields{Title: "T", Content: "a b a", Format: "md"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		p    Patch
		want string
	}{
		{Patch{Edits: []Edit{{Old: "a", New: "c"}}}, "2 times"},
		{Patch{Edits: []Edit{{Old: "zz", New: "c"}}}, "not in the page"},
		{Patch{Edits: []Edit{{Old: "", New: "c"}}}, "no old text"},
		{Patch{Edits: []Edit{{Old: "a", New: "c"}}, Content: new(string)}, "not both"},
		{Patch{Format: ptr("pdf")}, "html or markdown"},
	}
	for _, c := range cases {
		if _, _, err := s.Update("c", "A1", c.p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.p, err, c.want)
		}
	}
	a, _, err := s.Update("c", "A1", Patch{Edits: []Edit{{Old: "a", New: "c", All: true}}})
	if err != nil || a.Format != Markdown {
		t.Fatal(err, a.Format)
	}
	if _, _, c, _ := s.Content("c", "A1", 0); string(c) != "c b c" {
		t.Fatalf("%q", c)
	}
}

func ptr(s string) *string { return &s }

func TestVersionsPruned(t *testing.T) {
	s, _, _ := newStore(t)
	if _, err := s.Create("c", Fields{Title: "T", Content: "v1"}); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= MaxVersions+3; i++ {
		c := "v" + string(rune('a'+i))
		if _, _, err := s.Update("c", "A1", Patch{Content: &c}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := s.Get("c", "A1")
	if len(a.Versions) != MaxVersions || a.Versions[0].N != 4 || a.Latest().N != MaxVersions+3 {
		t.Fatalf("versions %d, first %d, latest %d", len(a.Versions), a.Versions[0].N, a.Latest().N)
	}
	if _, err := os.Stat(s.contentPath("c", "A1", Version{N: 1, Format: HTML})); !os.IsNotExist(err) {
		t.Fatal("a pruned version's content is left")
	}
	if _, _, _, err := s.Content("c", "A1", 2); err == nil || !strings.Contains(err.Error(), "versions 4 to 23") {
		t.Fatalf("pruned version: %v", err)
	}
}

func TestFilesRefused(t *testing.T) {
	s, _, _ := newStore(t)
	dir := t.TempDir()
	bad := writeFile(t, dir, "my chart.png", "x")
	link := filepath.Join(dir, "link.png")
	os.Symlink(bad, link)
	for _, p := range []string{bad, link, dir, "rel.png", filepath.Join(dir, "missing.png")} {
		if _, err := s.Create("c", Fields{Title: "T", Content: "x", Files: []string{p}}); err == nil {
			t.Errorf("%s was attached", p)
		}
	}
	if l, _ := s.List("c"); len(l) != 0 {
		t.Fatalf("a refused create kept %d", len(l))
	}
}

func TestPage(t *testing.T) {
	a := Artifact{Title: "A <b>"}
	doc := []byte("<!-- x -->\n<!DOCTYPE html><html><body>hi</body></html>")
	if got := Page(a, Version{Format: HTML}, doc, "s1"); string(got) != string(doc) {
		t.Fatalf("a whole document was wrapped: %s", got)
	}
	frag := Page(a, Version{Format: HTML}, []byte("<h1>Hi</h1>"), "")
	if !strings.Contains(string(frag), "<title>A &lt;b&gt;</title>") || !strings.Contains(string(frag), "<main class=\"page\">\n<h1>Hi</h1>") ||
		!strings.Contains(string(frag), KitPath+"kit.js\"") {
		t.Fatalf("fragment page: %s", frag)
	}
	// Markdown is its text, escaped, in a pre the kit renders: readable
	// even when the kit cannot load, and nothing in it is markup.
	md := string(Page(a, Version{Format: Markdown}, []byte("\n# T & \"q\"\n</pre><script>alert(1)</script>"), ""))
	if strings.Contains(md, "<script>alert") || strings.Contains(md, "</pre><script>") ||
		!strings.Contains(md, "<pre id=\"artifact-markdown\" style=\"white-space:pre-wrap;overflow-wrap:anywhere\">\n\n# T &amp; &#34;q&#34;\n&lt;/pre&gt;&lt;script&gt;alert(1)&lt;/script&gt;</pre></main>") {
		t.Fatalf("markdown page: %s", md)
	}
	// Markdown that looks like a document is still Markdown.
	if md := string(Page(a, Version{Format: Markdown}, []byte("<html>"), "")); !strings.Contains(md, "artifact-markdown") {
		t.Fatalf("markdown document: %s", md)
	}
}

func TestReadText(t *testing.T) {
	s, _, _ := newStore(t)
	a, _ := s.Create("c", Fields{Title: "T", Content: "a ``` b"})
	a, v, c, _ := s.Content("c", "A1", 0)
	text := ReadText(a, v, c, s.FilesDir("c", "A1"))
	if !strings.Contains(text, "````html\na ``` b\n````") {
		t.Fatalf("%s", text)
	}
	if !strings.Contains(ListText([]Artifact{a}), "A1 “T”: HTML, version 1") {
		t.Fatal(ListText([]Artifact{a}))
	}
}

func TestBadChat(t *testing.T) {
	s, _, _ := newStore(t)
	for _, chat := range []string{"", "..", "a/b"} {
		if _, err := s.Create(chat, Fields{Title: "T", Content: "x"}); err == nil {
			t.Errorf("chat %q was taken", chat)
		}
	}
}

func TestEditsCannotBlowUpThePage(t *testing.T) {
	s, _, _ := newStore(t)
	if _, err := s.Create("c", Fields{Title: "T", Content: strings.Repeat("a", 1000)}); err != nil {
		t.Fatal(err)
	}
	edits := make([]Edit, MaxEdits)
	for i := range edits {
		edits[i] = Edit{Old: "a", New: "aa", All: true}
	}
	if _, _, err := s.Update("c", "A1", Patch{Edits: edits}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("doubling edits: %v", err)
	}
}

func TestAddressesOfNoChatTakeNoLock(t *testing.T) {
	s, _, _ := newStore(t)
	for _, chat := range []string{"nothing", strings.Repeat("x", 200), "a.b"} {
		if _, err := s.FilePath(chat, "A1", strings.Repeat("0", 32), "f.png"); err == nil {
			t.Fatalf("%s found a file", chat)
		}
	}
	if len(s.locks) != 0 {
		t.Fatalf("%d locks for made-up addresses", len(s.locks))
	}
}

func TestRemoveAndAttachTheSameName(t *testing.T) {
	s, _, _ := newStore(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "chart.png", "one")
	a, _ := s.Create("c", Fields{Title: "T", Content: "x", Files: []string{p}})
	p2 := writeFile(t, t.TempDir(), "chart.png", "two")
	a, ch, err := s.Update("c", "A1", Patch{RemoveFiles: []string{"chart.png"}, Files: []string{p2}})
	if err != nil || len(ch.Removed) != 0 || len(ch.Attached) != 1 || len(a.Files) != 1 {
		t.Fatalf("%v %+v %+v", err, ch, a.Files)
	}
	path, _ := s.FilePath("c", "A1", a.Key, "chart.png")
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Fatalf("%q", b)
	}
	if left, _ := os.ReadDir(s.FilesDir("c", "A1")); len(left) != 1 {
		t.Fatalf("left behind: %v", left)
	}
}
