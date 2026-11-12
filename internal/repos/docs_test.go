package repos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// docsWant is what the server takes: Markdown and pictures outside hidden
// folders (docs.Listed, without the size bounds).
func docsWant(rel string, size int64) bool {
	if strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") || strings.HasPrefix(rel, "node_modules/") {
		return false
	}
	return strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, ".png")
}

// A repository's docs are its Markdown files and pictures at the branch's
// newest commit, follow the branch once it is fetched again, keep the time
// of a file that did not change, and keep only the newest few commits.
func TestTheDocsOfARepository(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	sha := pushCommit(t, f, "main", map[string]string{
		"README.md":                   "# App\n",
		"docs/DESIGN.md":              "# Design\n",
		"docs/pic.png":                "png",
		"main.go":                     "package main\n",
		".github/CONTRIBUTING.md":     "hidden\n",
		"node_modules/x/README.md":    "theirs\n",
		".env":                        "SECRET=1\n",
		"docs/deep/nested/CHANGES.md": "## 1.0\n",
	})
	dir, got, err := m.Docs(ctx, "octo/app", "main", time.Minute, docsWant)
	if err != nil {
		t.Fatal(err)
	}
	if got != sha || dir != filepath.Join(m.DocsDir("octo/app"), sha) {
		t.Fatalf("docs at %s in %s, want %s", got, dir, sha)
	}
	want := []string{docsManifest, "README.md", "docs/DESIGN.md", "docs/deep/nested/CHANGES.md", "docs/pic.png"}
	if files := filesUnder(t, dir); !reflect.DeepEqual(files, want) {
		t.Fatalf("docs hold %v, want %v", files, want)
	}
	if text, _ := os.ReadFile(filepath.Join(dir, "docs/DESIGN.md")); string(text) != "# Design\n" {
		t.Fatalf("DESIGN.md is %q", text)
	}
	if m.DocsShown("OCTO/app") != dir {
		t.Fatalf("DocsShown is %q, want %q", m.DocsShown("octo/app"), dir)
	}
	readme, _ := os.Stat(filepath.Join(dir, "README.md"))

	// A new commit: not seen while the fetch is fresh, seen with maxAge 0.
	next := pushCommit(t, f, "main", map[string]string{"docs/DESIGN.md": "# Design 2\n"})
	if again, _, err := m.Docs(ctx, "octo/app", "main", time.Hour, docsWant); err != nil || again != dir {
		t.Fatalf("a fresh fetch is not repeated: %s, %v", again, err)
	}
	if again, _, err := m.Docs(ctx, "octo/app", "main", -1, docsWant); err != nil || again != dir {
		t.Fatalf("a negative age never fetches: %s, %v", again, err)
	}
	dir2, got, err := m.Docs(ctx, "octo/app", "main", 0, docsWant)
	if err != nil || got != next {
		t.Fatalf("docs at %s (%v), want %s", got, err, next)
	}
	if text, _ := os.ReadFile(filepath.Join(dir2, "docs/DESIGN.md")); string(text) != "# Design 2\n" {
		t.Fatalf("DESIGN.md is %q", text)
	}
	if readme2, _ := os.Stat(filepath.Join(dir2, "README.md")); !readme2.ModTime().Equal(readme.ModTime()) {
		t.Fatalf("an unchanged file's time moved: %v, was %v", readme2.ModTime(), readme.ModTime())
	}
	if m.DocsShown("octo/app") != dir2 {
		t.Fatalf("DocsShown is %q, want %q", m.DocsShown("octo/app"), dir2)
	}

	// Only the newest few commits are kept.
	for i := 0; i < docsKeep+1; i++ {
		pushCommit(t, f, "main", map[string]string{"README.md": "# App " + string(rune('a'+i)) + "\n"})
		if _, _, err := m.Docs(ctx, "octo/app", "main", 0, docsWant); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(m.DocsDir("octo/app"))
	if len(entries) != docsKeep {
		t.Fatalf("%d docs folders kept, want %d", len(entries), docsKeep)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the oldest docs are still there: %v", err)
	}
}

// A branch GitHub does not have is ErrNoBranch; a fetch that fails once the
// docs were made leaves them as they were.
func TestTheDocsWithoutGitHub(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	if _, _, err := m.Docs(ctx, "octo/app", "nope", time.Minute, docsWant); !errors.Is(err, ErrNoBranch) {
		t.Fatalf("a missing branch: %v", err)
	}
	pushCommit(t, f, "main", map[string]string{"README.md": "# App\n"})
	dir, _, err := m.Docs(ctx, "octo/app", "main", 0, docsWant)
	if err != nil {
		t.Fatal(err)
	}
	m.URL = func(string) string { return "file:///nowhere/at/all.git" }
	again, _, err := m.Docs(ctx, "octo/app", "main", 0, docsWant)
	if err != nil || again != dir {
		t.Fatalf("docs without GitHub: %s, %v; want %s", again, err, dir)
	}
	if _, _, err := m.Docs(ctx, "octo/app", "../x", 0, docsWant); err == nil {
		t.Fatal("a branch that is no branch name was taken")
	}
	if _, _, err := m.Docs(ctx, "../app", "main", 0, docsWant); err == nil {
		t.Fatal("a repository that is no name was taken")
	}
}
