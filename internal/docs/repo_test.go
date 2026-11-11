package docs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Owner/Repo.git":          "github.com/owner/repo",
		"https://user:tok@github.com/o/r":            "github.com/o/r",
		"git@github.com:o/r.git":                     "github.com/o/r",
		"ssh://git@gitlab.example.com:2222/g/sub/r/": "gitlab.example.com/g/sub/r",
		"/srv/git/r.git":                             "",
		"file:///srv/git/r.git":                      "",
		"https://github.com/onlyowner":               "",
		"":                                           "",
	} {
		if got := normalizeRemote(in); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoOf(t *testing.T) {
	root := t.TempDir()
	mk := func(path, text string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A repository with two remotes: origin wins, wherever it stands.
	mk("main/.git/config", "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = https://github.com/up/r.git\n[remote \"origin\"]\n\turl = git@github.com:Me/R.git\n")
	mk("main/docs/x.md", "# x\n")
	// A worktree: .git is a file naming its directory, which names the shared one.
	mk("main/.git/worktrees/wt/commondir", "../..\n")
	mk("wt/.git", "gitdir: "+filepath.Join(root, "main/.git/worktrees/wt")+"\n")
	// A repository without a remote, and a folder in none.
	mk("local/.git/config", "[core]\n\tbare = false\n")
	mk("none/a.md", "# a\n")

	for folder, want := range map[string]Repo{
		"main":      {Remote: "github.com/me/r"},
		"main/docs": {Remote: "github.com/me/r", Prefix: "docs"},
		"wt":        {Remote: "github.com/me/r"},
		"local":     {},
	} {
		if got := RepoOf(filepath.Join(root, folder)); got != want {
			t.Errorf("RepoOf(%s) = %+v, want %+v", folder, got, want)
		}
	}
	// A folder in no repository: zero, unless the temp dir itself is in one.
	if got := RepoOf(filepath.Join(root, "none")); got.Prefix == "" && got.Remote != "" {
		t.Errorf("RepoOf(none) = %+v", got)
	}
}
