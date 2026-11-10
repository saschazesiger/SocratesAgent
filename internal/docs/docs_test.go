package docs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// tree writes files (path -> content) under a new directory and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func paths(l Listing) []string {
	out := []string{}
	for _, f := range l.Files {
		out = append(out, f.Path)
	}
	return out
}

// A scan lists the project's own Markdown, any case of the extension,
// sorted, and leaves out hidden and dependency directories, dot files,
// other files and symbolic links.
func TestScanListsTheProjectsMarkdown(t *testing.T) {
	root := tree(t, map[string]string{
		"README.md":                     "# Hi",
		"CHANGES.MARKDOWN":              "x",
		"docs/design/ARCH.md":           "x",
		"docs/notes.Md":                 "x",
		"docs/img.png":                  "x",
		"main.go":                       "x",
		".github/PULL_REQUEST.md":       "x",
		".hidden.md":                    "x",
		"node_modules/pkg/README.md":    "x",
		"vendor/x/README.md":            "x",
		"web/dist/README.md":            "x",
		"py/__pycache__/x.md":           "x",
		"py/venv/lib/README.md":         "x",
		"third_party/a.md":              "x",
		"rust/target/doc.md":            "x",
		"coverage/report.md":            "x",
		"elsewhere/outside-of-root.txt": "x",
	})
	outside := tree(t, map[string]string{"secret.md": "x", "dir/x.md": "x"})
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	l, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"CHANGES.MARKDOWN", "README.md", "docs/design/ARCH.md", "docs/notes.Md"}
	if !reflect.DeepEqual(paths(l), want) || l.Truncated {
		t.Fatalf("scan = %v (truncated %v), want %v", paths(l), l.Truncated, want)
	}
	if f := l.Files[1]; f.Size != 4 || f.Mtime == 0 {
		t.Fatalf("README's size and time: %+v", f)
	}
}

// A root reached through a symbolic link is scanned as the folder it is;
// a root that is not there is an error wrapping fs.ErrNotExist.
func TestScanRoot(t *testing.T) {
	root := tree(t, map[string]string{"a.md": "x"})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if l, err := Scan(link); err != nil || len(l.Files) != 1 {
		t.Fatalf("scan through a link: %v %v", l, err)
	}
	if _, err := Scan(filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing root: %v", err)
	}
	if _, err := Scan(filepath.Join(root, "a.md")); err == nil {
		t.Fatal("a file is not a folder")
	}
}

// More Markdown than MaxFiles is listed in part, and so is a tree deeper
// than MaxDepth.
func TestScanTruncates(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < MaxFiles+5; i++ {
		files[fmt.Sprintf("d%d/f%04d.md", i%7, i)] = ""
	}
	l, err := Scan(tree(t, files))
	if err != nil || len(l.Files) != MaxFiles || !l.Truncated {
		t.Fatalf("many files: %d truncated %v err %v", len(l.Files), l.Truncated, err)
	}

	deep := strings.Repeat("d/", MaxDepth+1) + "deep.md"
	l, err = Scan(tree(t, map[string]string{"top.md": "", deep: ""}))
	if err != nil || !reflect.DeepEqual(paths(l), []string{"top.md"}) || !l.Truncated {
		t.Fatalf("deep tree: %v truncated %v err %v", paths(l), l.Truncated, err)
	}
	ok := strings.Repeat("d/", MaxDepth) + "ok.md"
	l, _ = Scan(tree(t, map[string]string{ok: ""}))
	if !reflect.DeepEqual(paths(l), []string{ok}) || l.Truncated {
		t.Fatalf("a tree exactly MaxDepth deep: %v truncated %v", paths(l), l.Truncated)
	}
}

func TestHas(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{"top", map[string]string{"README.md": ""}, true},
		{"nested", map[string]string{"a/b/c/notes.markdown": ""}, true},
		{"none", map[string]string{"main.go": "", "a/b.txt": ""}, false},
		{"only hidden", map[string]string{".github/x.md": "", "node_modules/a/README.md": ""}, false},
		{"empty", map[string]string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Has(tree(t, c.files)); got != c.want {
				t.Fatalf("Has = %v, want %v", got, c.want)
			}
		})
	}
	if Has(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("a missing folder has no docs")
	}
}

func TestResolve(t *testing.T) {
	root := tree(t, map[string]string{
		"README.md":                  "# Top",
		"docs/ARCH.md":               "# Arch",
		"docs/img/a.PNG":             "png",
		"docs/pic.svg":               "<svg/>",
		"docs/guide/Readme.markdown": "x",
		"docs/site/index.md":         "x",
		"docs/empty/x.txt":           "x",
		"main.go":                    "x",
		".env":                       "SECRET=1",
		".git/config":                "x",
		".github/x.md":               "x",
		"node_modules/a/r.md":        "x",
	})
	outside := tree(t, map[string]string{"secret.md": "x"})
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "out.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "docs", "ARCH.md"), filepath.Join(root, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".github", "x.md"), filepath.Join(root, "sneaky.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "main.go"), filepath.Join(root, "code.md")); err != nil {
		t.Fatal(err)
	}

	type want struct {
		rel      string
		redirect bool
		kind     Kind
		err      error
	}
	cases := []struct {
		in string
		want
	}{
		{"README.md", want{rel: "README.md", kind: Markdown}},
		{"./docs//ARCH.md", want{rel: "docs/ARCH.md", kind: Markdown}},
		{"docs/img/a.PNG", want{rel: "docs/img/a.PNG", kind: Image}},
		{"docs/pic.svg", want{rel: "docs/pic.svg", kind: Image}},
		{"alias.md", want{rel: "alias.md", kind: Markdown}},
		{"docs/guide", want{rel: "docs/guide/Readme.markdown", redirect: true}},
		{"docs/guide/", want{rel: "docs/guide/Readme.markdown", redirect: true}},
		{"docs/site", want{rel: "docs/site/index.md", redirect: true}},
		{"docs/empty", want{err: fs.ErrNotExist}},
		{"docs/missing.md", want{err: fs.ErrNotExist}},
		{"main.go", want{err: ErrNotServed}},
		{"code.md", want{err: ErrNotServed}},
		{"", want{err: ErrNotServed}},
		{".", want{err: ErrNotServed}},
		{"/etc/passwd", want{err: ErrNotServed}},
		{"../secret.md", want{err: ErrNotServed}},
		{"docs/../../secret.md", want{err: ErrNotServed}},
		{"docs/../README.md", want{err: ErrNotServed}},
		{".env", want{err: ErrNotServed}},
		{".git/config", want{err: ErrNotServed}},
		{".github/x.md", want{err: ErrNotServed}},
		{"node_modules/a/r.md", want{err: ErrNotServed}},
		{"out.md", want{err: ErrNotServed}},
		{"sneaky.md", want{err: ErrNotServed}},
		{"docs\\ARCH.md", want{err: ErrNotServed}},
		{"a\x00.md", want{err: ErrNotServed}},
	}
	for _, c := range cases {
		got, err := Resolve(root, c.in)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("Resolve(%q) error = %v, want %v", c.in, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("Resolve(%q): %v", c.in, err)
			continue
		}
		if got.Rel != c.rel || got.Redirect != c.redirect || got.Kind != c.kind {
			t.Errorf("Resolve(%q) = %+v, want %+v", c.in, got, c.want)
		}
		if !c.redirect && (got.Abs == "" || got.Size == 0 || got.Mtime == 0) {
			t.Errorf("Resolve(%q) has no file: %+v", c.in, got)
		}
	}
}

// A file over its bound is refused, and Open refuses a file swapped after
// Resolve checked it.
func TestResolveBoundsAndOpen(t *testing.T) {
	root := tree(t, map[string]string{"a.md": "# A", "b.md": "# B"})
	if err := os.WriteFile(filepath.Join(root, "big.md"), make([]byte, MaxMarkdownSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, "big.md"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big Markdown: %v", err)
	}
	tg, err := Resolve(root, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	f, _, err := tg.Open()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Rename(filepath.Join(root, "b.md"), filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tg.Open(); err == nil {
		t.Fatal("a file replaced after Resolve was opened")
	}
}

func TestRootReadme(t *testing.T) {
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"readme.MD": "", "index.md": ""}, "readme.MD"},
		{map[string]string{"README.markdown": "", "index.md": ""}, "README.markdown"},
		{map[string]string{"INDEX.md": ""}, "INDEX.md"},
		{map[string]string{"docs/README.md": ""}, ""},
	} {
		if got := RootReadme(tree(t, c.files)); got != c.want {
			t.Errorf("RootReadme(%v) = %q, want %q", c.files, got, c.want)
		}
	}
}

// The page is the kit's Markdown document with the text escaped inside it.
func TestPage(t *testing.T) {
	page := string(Page("A <b>.md", "docs/a&b.md", []byte("# Hi\n</pre><script>alert(1)</script>"), "v1"))
	for _, want := range []string{
		`<html lang="en">`,
		`<title>A &lt;b&gt;.md</title>`,
		`href="/artifact-kit/kit.css?v=v1"`,
		`src="/artifact-kit/kit.js?v=v1"`,
		`<body data-docs="docs/a&amp;b.md">`,
		`<main class="page" data-format="markdown"><pre id="artifact-markdown"`,
		"# Hi\n&lt;/pre&gt;&lt;script&gt;alert(1)&lt;/script&gt;</pre></main>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
}

// The walk stops at its bound of entries, and says so: past a few thousand
// entries Has gives up rather than read a whole disk.
func TestWalkBound(t *testing.T) {
	files := map[string]string{"sub/found.md": ""}
	for i := 0; i < hasEntries; i++ {
		files[fmt.Sprintf("f%05d.txt", i)] = ""
	}
	root := tree(t, files)
	if Has(root) {
		t.Fatal("Has looked past its bound")
	}
	n := 0
	if !walk(root, 10, func(string, fs.DirEntry) bool { n++; return true }) || n != 0 {
		t.Fatalf("a walk cut short by its bound: visited %d", n)
	}
	if walk(root, hasEntries+10, func(string, fs.DirEntry) bool { n++; return true }) || n != 1 {
		t.Fatalf("a whole walk: visited %d", n)
	}
}
