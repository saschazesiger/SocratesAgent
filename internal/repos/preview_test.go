package repos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// previewWant is what the server copies: OpenCode's config and agents and
// the MCP files.
func previewWant(rel string) bool {
	switch rel {
	case "opencode.json", ".opencode/opencode.json", ".mcp.json", ".socrates/mcp.json":
		return true
	}
	return strings.HasPrefix(rel, ".opencode/agent/") && strings.HasSuffix(rel, ".md")
}

var previewRootsForTest = []string{"opencode.json", ".opencode", ".mcp.json", ".socrates/mcp.json"}

// filesUnder is every file under dir, by its slash path.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// pushCommit commits files (path → content) on top of branch of the fake
// GitHub's octo/app and pushes it.
func pushCommit(t *testing.T, f fakeGitHub, branch string, files map[string]string) string {
	t.Helper()
	work := t.TempDir()
	git(t, work, "clone", "--quiet", "--branch", branch, f.url("octo/app"), ".")
	for path, text := range files {
		writeFile(t, filepath.Join(work, filepath.FromSlash(path)), text)
	}
	git(t, work, "add", "-A")
	git(t, work, "commit", "--quiet", "-m", "config")
	git(t, work, "push", "--quiet", "origin", branch)
	return git(t, work, "rev-parse", "HEAD")
}

// The preview of a branch holds its agent configuration and nothing else,
// is made once per commit, follows the branch once the cache knows its new
// commit, and keeps only the newest few.
func TestThePreviewHoldsTheConfigurationOnly(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	sha := pushCommit(t, f, "main", map[string]string{
		"opencode.json":                  `{"model":"lab/qwen"}`,
		".opencode/agent/review.md":      "---\nmodel: lab/big\n---\nReview.\n",
		".opencode/agent/deep/nested.md": "nested\n",
		".opencode/agent/notes.txt":      "not an agent\n",
		".opencode/plugin/run.js":        "process.exit(1)\n",
		".mcp.json":                      `{"mcpServers":{}}`,
		".socrates/mcp.json":             `{"off":["x"]}`,
		".socrates/setup":                "#!/bin/sh\nexit 1\n",
		"src/opencode.json":              "{}",
		".env":                           "SECRET=1\n",
	})

	// No cache yet: the preview makes it.
	dir, got, err := m.Preview(ctx, "octo/app", "main", previewRootsForTest, previewWant)
	if err != nil {
		t.Fatal(err)
	}
	if got != sha || dir != filepath.Join(m.PreviewDir("octo/app"), sha) {
		t.Fatalf("the preview of %s is %s at %s", sha, got, dir)
	}
	want := []string{".mcp.json", ".opencode/agent/deep/nested.md", ".opencode/agent/review.md", ".socrates/mcp.json", "opencode.json"}
	if files := filesUnder(t, dir); strings.Join(files, " ") != strings.Join(want, " ") {
		t.Fatalf("the preview holds %v", files)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "opencode.json")); string(data) != `{"model":"lab/qwen"}` {
		t.Fatalf("opencode.json: %q", data)
	}

	// The same commit is the same folder, not written again.
	marker := filepath.Join(dir, "marker")
	writeFile(t, marker, "kept")
	if again, _, err := m.Preview(ctx, "octo/app", "main", previewRootsForTest, previewWant); err != nil || again != dir {
		t.Fatalf("again: %s %v", again, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the preview of the same commit was made again")
	}

	// Another branch the cache knows; a branch nobody has is an error.
	if fdir, fsha, err := m.Preview(ctx, "octo/app", "feature", previewRootsForTest, previewWant); err != nil || fsha == sha ||
		len(filesUnder(t, fdir)) != 0 {
		t.Fatalf("feature: %s %s %v", fdir, fsha, err)
	}
	if _, _, err := m.Preview(ctx, "octo/app", "nope", previewRootsForTest, previewWant); !errors.Is(err, ErrNoBranch) {
		t.Fatalf("a branch GitHub does not have: %v", err)
	}
	if _, _, err := m.Preview(ctx, "octo/app", "-x", previewRootsForTest, previewWant); err == nil {
		t.Fatal("a branch that reads as an option is taken")
	}
	if _, _, err := m.Preview(ctx, "../app", "main", previewRootsForTest, previewWant); err == nil {
		t.Fatal("a name that is no repository is taken")
	}

	// New commits: the cache is fetched by the branch list (not by the
	// preview, which knows the branch), and the newest previews are kept.
	var dirs []string
	for i := 0; i < previewKeep+1; i++ {
		pushCommit(t, f, "main", map[string]string{"opencode.json": `{"n":` + string(rune('0'+i)) + `}`})
		if err := m.Fetch(ctx, "octo/app"); err != nil {
			t.Fatal(err)
		}
		d, _, err := m.Preview(ctx, "octo/app", "main", previewRootsForTest, previewWant)
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	entries, _ := os.ReadDir(m.PreviewDir("octo/app"))
	if len(entries) != previewKeep {
		t.Fatalf("%d previews are kept", len(entries))
	}
	for _, d := range dirs[1:] {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("a newest preview went: %s", d)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dirs[len(dirs)-1], "opencode.json")); string(data) != `{"n":4}` {
		t.Fatalf("the newest preview: %q", data)
	}
}

// gitIn runs git with stdin, failing the test when git fails.
func gitIn(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// writeBlob stores text in the repository and names its object.
func writeBlob(t *testing.T, dir, text string) string {
	return gitIn(t, dir, text, "hash-object", "-w", "--stdin")
}

// A tree whose names would leave the preview's folder - "..", ".git" - or a
// symlink in it writes nothing outside it, and nothing through the link.
func TestThePreviewRefusesPathsOutOfIt(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	bare := filepath.Join(f.dir, "octo", "app.git")
	mk := func(entries string) string { return gitIn(t, bare, entries, "mktree") }
	evil := writeBlob(t, bare, "evil\n")
	leaf := mk("100644 blob " + evil + "\tevil.md\n")
	up := mk("040000 tree " + leaf + "\t..\n")
	upUp := mk("040000 tree " + up + "\t..\n")
	dotgit := mk("040000 tree " + leaf + "\t.git\n")
	agent := mk("040000 tree " + upUp + "\tx\n" + "040000 tree " + dotgit + "\ty\n" +
		"120000 blob " + writeBlob(t, bare, "/etc") + "\tlink\n" + "100644 blob " + evil + "\tok.md\n")
	opencode := mk("040000 tree " + agent + "\tagent\n")
	root := mk("040000 tree " + opencode + "\t.opencode\n" + "120000 blob " + writeBlob(t, bare, "/etc/passwd") + "\topencode.json\n")
	parent := git(t, bare, "rev-parse", "refs/heads/main")
	commit := git(t, bare, "commit-tree", root, "-p", parent, "-m", "evil")
	git(t, bare, "update-ref", "refs/heads/evil", commit)

	dir, _, err := m.Preview(ctx, "octo/app", "evil", previewRootsForTest, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if files := filesUnder(t, dir); strings.Join(files, " ") != ".opencode/agent/ok.md" {
		t.Fatalf("the preview holds %v", files)
	}
	for _, outside := range []string{filepath.Join(filepath.Dir(dir), "evil.md"), filepath.Join(filepath.Dir(dir), "agent", "evil.md"),
		filepath.Join(dir, ".opencode", "evil.md")} {
		if _, err := os.Stat(outside); err == nil {
			t.Fatalf("written outside the preview: %s", outside)
		}
	}
	for rel, ok := range map[string]bool{
		"opencode.json": true, ".opencode/agent/a.md": true,
		"": false, "../x": false, "a/../b": false, "/etc/passwd": false, "a//b": false, "./a": false,
		".git/config": false, "a/.GIT/x": false, `a\b`: false,
	} {
		if safeRel(rel) != ok {
			t.Errorf("safeRel(%q) = %v", rel, !ok)
		}
	}
}
