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

func TestWorktrees(t *testing.T) {
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
	at := func(p string) string { return filepath.Join(root, filepath.FromSlash(p)) }
	mk("main/.git/config", "[core]\n\tbare = false\n")
	mk("main/docs/a.md", "# a\n")
	// Two worktrees made by git: each names its .git in the repository.
	for _, wt := range []string{"wt/one", "wt/two"} {
		name := filepath.Base(wt)
		mk("main/.git/worktrees/"+name+"/commondir", "../..\n")
		mk("main/.git/worktrees/"+name+"/gitdir", at(wt+"/.git")+"\n")
		mk(wt+"/.git", "gitdir: "+at("main/.git/worktrees/"+name)+"\n")
		mk(wt+"/docs/b.md", "# b\n")
	}
	// A stale entry whose tree is gone, and one whose .git leads elsewhere.
	mk("main/.git/worktrees/gone/gitdir", at("gone/.git")+"\n")
	mk("main/.git/worktrees/other/gitdir", at("other/.git")+"\n")
	mk("other/.git/config", "[core]\n")

	same := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if got, want := Worktrees(at("main")), []string{at("main"), at("wt/one"), at("wt/two")}; !same(got, want) {
		t.Errorf("Worktrees(main) = %v, want %v", got, want)
	}
	if got, want := Worktrees(at("wt/two")), []string{at("wt/two"), at("main"), at("wt/one")}; !same(got, want) {
		t.Errorf("Worktrees(wt/two) = %v, want %v", got, want)
	}
	// A folder inside the repository: the same folder in every tree.
	if got, want := Worktrees(at("main/docs")), []string{at("main/docs"), at("wt/one/docs"), at("wt/two/docs")}; !same(got, want) {
		t.Errorf("Worktrees(main/docs) = %v, want %v", got, want)
	}
	if got := Worktrees(at("other")); !same(got, []string{at("other")}) {
		t.Errorf("Worktrees(other) = %v", got)
	}
}

// Only the trees the repository names count: a .git that points at a
// repository from elsewhere makes no tree, and a symbolic link in one tree
// leads nowhere outside it.
func TestWorktreesRefusals(t *testing.T) {
	root := t.TempDir()
	at := func(p string) string { return filepath.Join(root, filepath.FromSlash(p)) }
	mk := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(at(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(at(path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("main/.git/config", "[core]\n")
	mk("main/link/a.md", "# a real folder in main\n")
	// A tree whose gitdir entry is relative (worktree.useRelativePaths).
	mk("main/.git/worktrees/rel/commondir", "../..\n")
	mk("main/.git/worktrees/rel/gitdir", "../../../../rel/.git\n")
	mk("rel/.git", "gitdir: ../main/.git/worktrees/rel\n")
	mk("secret/s.md", "# secret\n")
	if err := os.Symlink(at("secret"), at("rel/link")); err != nil {
		t.Fatal(err)
	}
	// A folder that claims the repository without being one of its trees.
	mk("stranger/.git", "gitdir: "+at("main/.git")+"\n")
	mk("stranger/x.md", "# x\n")

	if got := Worktrees(at("main")); len(got) != 2 || got[1] != at("rel") {
		t.Errorf("Worktrees(main) = %v, want main and rel", got)
	}
	if got := Worktrees(at("stranger")); got != nil {
		t.Errorf("Worktrees(stranger) = %v, want nil", got)
	}
	if got := Worktrees(at("main/link")); len(got) != 1 || got[0] != at("main/link") {
		t.Errorf("Worktrees(main/link) = %v, want only main/link", got)
	}
	if got := Worktrees(at("rel/link")); got != nil {
		t.Errorf("Worktrees(rel/link) = %v, want nil", got)
	}
}
