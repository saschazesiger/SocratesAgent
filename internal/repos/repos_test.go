package repos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeGitHub is a folder of bare repositories that stands in for GitHub: the
// clone URL of owner/name is file://<dir>/owner/name.git.
type fakeGitHub struct {
	dir string
}

func (f fakeGitHub) url(fullName string) string {
	return "file://" + filepath.Join(f.dir, fullName+".git")
}

// git runs git for a test, failing it when git fails.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// testEnv gives git an identity and no configuration of this machine's.
func testEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Tester")
	t.Setenv("GIT_AUTHOR_EMAIL", "tester@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Tester")
	t.Setenv("GIT_COMMITTER_EMAIL", "tester@example.com")
}

// newFake makes octo/app on the fake GitHub: main with one commit, and the
// branch feature one commit further on, a day later.
func newFake(t *testing.T) fakeGitHub {
	t.Helper()
	testEnv(t)
	f := fakeGitHub{dir: t.TempDir()}
	bare := filepath.Join(f.dir, "octo", "app.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "init", "--quiet", "--bare", "-b", "main", bare)
	work := t.TempDir()
	git(t, work, "init", "--quiet", "-b", "main")
	writeFile(t, filepath.Join(work, "README.md"), "app\n")
	git(t, work, "add", ".")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z")
	git(t, work, "commit", "--quiet", "-m", "first")
	git(t, work, "checkout", "--quiet", "-b", "feature")
	writeFile(t, filepath.Join(work, "feature.txt"), "feature\n")
	git(t, work, "add", ".")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-02T00:00:00Z")
	git(t, work, "commit", "--quiet", "-m", "feature")
	os.Unsetenv("GIT_COMMITTER_DATE")
	git(t, work, "push", "--quiet", bare, "main", "feature")
	return f
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// chatOf is a chat on a checkout, with its lease kept in memory.
func chatOf(co Checkout, id string) Chat {
	lease := co.Start
	return Chat{FullName: "octo/app", ID: id, Path: co.Path, Branch: co.Branch, Base: co.Base,
		Lease: func() string { return lease }, SetLease: func(sha string) { lease = sha }}
}

func newManager(t *testing.T, f fakeGitHub) *Manager {
	root := t.TempDir()
	return &Manager{Root: func() string { return root }, URL: f.url}
}

// remoteHas is the commit the fake GitHub has for a branch, "" for none.
func remoteHas(t *testing.T, f fakeGitHub, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = filepath.Join(f.dir, "octo", "app.git")
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func TestValidNames(t *testing.T) {
	for name, ok := range map[string]bool{
		"octo/app": true, "Octo-1/app.js": true, "a_b/c": true,
		"octo": false, "octo/": false, "/app": false, "../app": false, "octo/..": false, "octo/.git": false,
		"octo/app/x": false, "octo/a b": false,
	} {
		if ValidName(name) != ok {
			t.Errorf("ValidName(%q) = %v", name, !ok)
		}
	}
	ctx := context.Background()
	for name, ok := range map[string]bool{"socrates/fix-it": true, "feature": true, "-x": false, "a..b": false, "a b": false, "": false, "x.lock": false} {
		if err := CheckBranch(ctx, name); (err == nil) != ok {
			t.Errorf("CheckBranch(%q) = %v", name, err)
		}
	}
}

// The first look at a repository makes its cache and lists GitHub's
// branches, the newest commit first; a fetch never moves a local branch.
func TestTheCacheIsMadeAndListsTheBranches(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	list, err := m.Branches(ctx, "octo/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "feature" || list[1].Name != "main" || list[0].SHA == "" || list[0].Date <= list[1].Date {
		t.Fatalf("branches %+v", list)
	}
	cache := m.CacheDir("octo/app")
	if got := git(t, cache, "config", "remote.origin.fetch"); got != "+refs/heads/*:refs/remotes/origin/*" {
		t.Fatalf("refspec %q", got)
	}
	if got := git(t, cache, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Fatalf("the cache is not bare: %s", got)
	}
	// A second look fetches into the same cache.
	if _, err := m.Branches(ctx, "octo/app"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Branches(ctx, "../evil"); err == nil {
		t.Fatal("a name that is no repository was listed")
	}
}

// A new branch is made from its base, without an upstream; a branch GitHub
// has is checked out tracking it; a branch another worktree has is refused.
func TestWorktreesForNewAndExistingBranches(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/fix-it", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if co.Path != filepath.Join(m.Root(), "app-abcdef12") || co.Existing {
		t.Fatalf("checkout %+v", co)
	}
	if got := git(t, co.Path, "branch", "--show-current"); got != "socrates/fix-it" {
		t.Fatalf("branch %q", got)
	}
	if _, err := os.Stat(filepath.Join(co.Path, "README.md")); err != nil {
		t.Fatal("the worktree is not main's")
	}
	if cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "@{u}"); func() error { cmd.Dir = co.Path; return cmd.Run() }() == nil {
		t.Fatal("a new branch has an upstream before its first push")
	}

	ex, err := m.Create(ctx, "octo/app", "1111111199999999", "feature", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.Existing || git(t, ex.Path, "rev-parse", "--abbrev-ref", "@{u}") != "origin/feature" {
		t.Fatalf("existing branch %+v", ex)
	}
	if _, err := os.Stat(filepath.Join(ex.Path, "feature.txt")); err != nil {
		t.Fatal("the worktree is not the branch's")
	}

	_, err = m.Create(ctx, "octo/app", "2222222299999999", "feature", "main", false)
	var inUse *ErrBranchInUse
	if !errors.As(err, &inUse) || inUse.Path != ex.Path {
		t.Fatalf("a branch in use: %v", err)
	}
	if _, err := m.Create(ctx, "octo/app", "3333333399999999", "socrates/new", "nope", false); err == nil {
		t.Fatal("a base GitHub does not have was taken")
	}
	if _, err := m.Create(ctx, "octo/app", "4444444499999999", "bad..name", "main", false); err == nil {
		t.Fatal("a branch name git refuses was taken")
	}
	list, err := m.Worktrees(ctx, "octo/app")
	if err != nil || list[co.Path] != "socrates/fix-it" || list[ex.Path] != "feature" || len(list) != 2 {
		t.Fatalf("worktrees %v %v", list, err)
	}
}

// newEmptyFake makes octo/app on the fake GitHub as GitHub makes a new
// repository without a README: no commit and no branch.
func newEmptyFake(t *testing.T) fakeGitHub {
	t.Helper()
	testEnv(t)
	f := fakeGitHub{dir: t.TempDir()}
	bare := filepath.Join(f.dir, "octo", "app.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "init", "--quiet", "--bare", "-b", "main", bare)
	return f
}

// A repository with no branch at all gets its base made on GitHub, as an
// empty first commit, and the chat's branch starts there; a base GitHub does
// not have in a repository that has branches is still refused.
func TestAnEmptyRepositoryGetsItsFirstBranch(t *testing.T) {
	f := newEmptyFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	list, err := m.Branches(ctx, "octo/app")
	if err != nil || len(list) != 0 {
		t.Fatalf("the branches of an empty repository: %+v %v", list, err)
	}
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/first", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	seed := remoteHas(t, f, "main")
	if seed == "" || co.Start != seed || co.Existing || co.Base != "main" {
		t.Fatalf("checkout %+v, main on GitHub %q", co, seed)
	}
	if got := git(t, co.Path, "log", "--format=%s", "HEAD"); got != SeedMessage {
		t.Fatalf("the worktree's history: %q", got)
	}
	if got := git(t, co.Path, "ls-tree", "HEAD"); got != "" {
		t.Fatalf("the first commit is not empty: %q", got)
	}
	if got := git(t, co.Path, "branch", "--show-current"); got != "socrates/first" {
		t.Fatalf("branch %q", got)
	}
	if remoteHas(t, f, "socrates/first") != "" {
		t.Fatal("the chat's branch was pushed before it had a commit")
	}

	// The chat's work goes to GitHub on its own branch, made from main.
	writeFile(t, filepath.Join(co.Path, "hello.txt"), "hello\n")
	git(t, co.Path, "add", ".")
	git(t, co.Path, "commit", "--quiet", "-m", "hello")
	if pushed, err := m.Push(ctx, chatOf(co, "abcdef1234567890")); err != nil || !pushed {
		t.Fatalf("push: %v %v", pushed, err)
	}
	if got := git(t, co.Path, "rev-parse", "HEAD"); remoteHas(t, f, "socrates/first") != got {
		t.Fatal("the chat's commit is not on GitHub")
	}
	if remoteHas(t, f, "main") != seed {
		t.Fatal("main moved")
	}

	// The next chat finds main and starts from it, without a new seed.
	next, err := m.Create(ctx, "octo/app", "1111111199999999", "socrates/second", "main", false)
	if err != nil || next.Start != seed {
		t.Fatalf("the second chat: %+v %v", next, err)
	}
	// Now that the repository has branches, a base it does not have is
	// refused.
	if _, err := m.Create(ctx, "octo/app", "2222222299999999", "socrates/third", "develop", false); err == nil {
		t.Fatal("a base GitHub does not have was taken")
	}
}

// A repository with no branch and no default branch named starts on main.
func TestAnEmptyRepositoryWithoutABaseStartsOnMain(t *testing.T) {
	f := newEmptyFake(t)
	m := newManager(t, f)
	co, err := m.Create(context.Background(), "octo/app", "abcdef1234567890", "socrates/first", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if co.Base != "main" || remoteHas(t, f, "main") == "" || co.Start != remoteHas(t, f, "main") {
		t.Fatalf("checkout %+v", co)
	}
}

// When main cannot be made on GitHub, no worktree is made and the error
// says why.
func TestAnEmptyRepositoryThatRefusesThePush(t *testing.T) {
	f := newEmptyFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	// GitHub refusing it: the fake takes no pack at all.
	git(t, filepath.Join(f.dir, "octo", "app.git"), "config", "receive.maxInputSize", "1")
	_, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/first", "main", false)
	if err == nil || !strings.Contains(err.Error(), "the repository is empty") {
		t.Fatalf("the error: %v", err)
	}
	if _, serr := os.Stat(m.WorktreeDir("octo/app", "abcdef1234567890")); serr == nil {
		t.Fatal("a worktree was made")
	}
}

// A main somebody pushed after the cache's fetch is never overwritten: the
// chat starts from it.
func TestAnEmptyRepositorysFirstBranchIsNeverOverwritten(t *testing.T) {
	f := newEmptyFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	cache := m.CacheDir("octo/app")
	if err := m.Fetch(ctx, "octo/app"); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	git(t, work, "init", "--quiet", "-b", "main")
	writeFile(t, filepath.Join(work, "README.md"), "theirs\n")
	git(t, work, "add", ".")
	git(t, work, "commit", "--quiet", "-m", "theirs")
	git(t, work, "push", "--quiet", filepath.Join(f.dir, "octo", "app.git"), "main")
	theirs := remoteHas(t, f, "main")
	co, err := m.addLocked(ctx, "octo/app", cache, m.WorktreeDir("octo/app", "abcdef1234567890"), "socrates/first", "main")
	if err != nil {
		t.Fatal(err)
	}
	if remoteHas(t, f, "main") != theirs || co.Start != theirs || git(t, co.Path, "rev-parse", "HEAD") != theirs {
		t.Fatalf("main was overwritten or not used: %+v", co)
	}
}

// The branch is pushed when it has commits GitHub does not have, and only
// then; a commit somebody else pushed meanwhile is never overwritten.
func TestPushOnlyWhenAhead(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/work", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	if pushed, err := m.Push(ctx, chat); err != nil || pushed {
		t.Fatalf("a branch without commits was pushed: %v %v", pushed, err)
	}
	writeFile(t, filepath.Join(co.Path, "a.txt"), "a\n")
	if pushed, err := m.Push(ctx, chat); err != nil || pushed {
		t.Fatalf("uncommitted changes were pushed: %v %v", pushed, err)
	}
	git(t, co.Path, "add", ".")
	git(t, co.Path, "commit", "--quiet", "-m", "a")
	if pushed, err := m.Push(ctx, chat); err != nil || !pushed {
		t.Fatalf("the commit was not pushed: %v %v", pushed, err)
	}
	if remoteHas(t, f, co.Branch) != git(t, co.Path, "rev-parse", "HEAD") {
		t.Fatal("GitHub does not have the commit")
	}
	if pushed, err := m.Push(ctx, chat); err != nil || pushed {
		t.Fatalf("pushed again: %v %v", pushed, err)
	}
	// A rebase rewrites the branch: the push is forced, with a lease.
	git(t, co.Path, "commit", "--quiet", "--amend", "-m", "a, amended")
	if pushed, err := m.Push(ctx, chat); err != nil || !pushed {
		t.Fatalf("the rewritten branch was not pushed: %v %v", pushed, err)
	}
	// Somebody pushes to the branch on GitHub; the chat commits on: the
	// lease fails rather than throwing their commit away.
	other := t.TempDir()
	git(t, other, "clone", "--quiet", "-b", co.Branch, f.url("octo/app"), ".")
	writeFile(t, filepath.Join(other, "b.txt"), "b\n")
	git(t, other, "add", ".")
	git(t, other, "commit", "--quiet", "-m", "theirs")
	git(t, other, "push", "--quiet")
	theirs := git(t, other, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(co.Path, "c.txt"), "c\n")
	git(t, co.Path, "add", ".")
	git(t, co.Path, "commit", "--quiet", "-m", "c")
	if _, err := m.Push(ctx, chat); err == nil {
		t.Fatal("the push overwrote a commit GitHub had")
	}
	if remoteHas(t, f, co.Branch) != theirs {
		t.Fatal("their commit is gone")
	}
}

// Done removes a clean worktree whose branch GitHub has, with its local
// branch, and keeps one with uncommitted changes; a chat started again gets
// its worktree back from GitHub.
func TestDoneAndReopen(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/done", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	writeFile(t, filepath.Join(co.Path, "a.txt"), "a\n")
	git(t, co.Path, "add", ".")
	git(t, co.Path, "commit", "--quiet", "-m", "a")
	head := git(t, co.Path, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(co.Path, "loose.txt"), "not committed\n")

	removed, kept, err := m.Done(ctx, chat, nil)
	if err != nil || removed || !strings.Contains(kept, "not committed") {
		t.Fatalf("a dirty worktree: removed %v, kept %q, %v", removed, kept, err)
	}
	if remoteHas(t, f, co.Branch) != head {
		t.Fatal("done did not push the branch")
	}
	if _, err := os.Stat(co.Path); err != nil {
		t.Fatal("the dirty worktree went")
	}

	os.Remove(filepath.Join(co.Path, "loose.txt"))
	// What a setup installs is ignored, and goes with the worktree.
	writeFile(t, filepath.Join(co.Path, ".gitignore"), "node_modules/\n")
	git(t, co.Path, "add", ".gitignore")
	git(t, co.Path, "commit", "--quiet", "-m", "ignore")
	writeFile(t, filepath.Join(co.Path, "node_modules", "x.js"), "x\n")
	removed, kept, err = m.Done(ctx, chat, nil)
	if err != nil || !removed || kept != "" {
		t.Fatalf("a clean worktree: removed %v, kept %q, %v", removed, kept, err)
	}
	if _, err := os.Stat(co.Path); !os.IsNotExist(err) {
		t.Fatal("the worktree is still there")
	}
	if cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/socrates/done"); func() error {
		cmd.Dir = m.CacheDir("octo/app")
		return cmd.Run()
	}() == nil {
		t.Fatal("the local branch is still there")
	}
	tip := remoteHas(t, f, co.Branch)

	again, err := m.Ensure(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	if again.Path != co.Path || git(t, co.Path, "rev-parse", "HEAD") != tip || git(t, co.Path, "branch", "--show-current") != co.Branch {
		t.Fatalf("reopened %+v", again)
	}
	// An existing worktree is left as it is.
	if _, err := m.Ensure(ctx, chat); err != nil {
		t.Fatal(err)
	}
}

// A worktree whose branch is not on GitHub and whose folder went is made
// again from the local branch, which kept its commits.
func TestEnsureKeepsTheLocalBranch(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/local", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	writeFile(t, filepath.Join(co.Path, "a.txt"), "a\n")
	git(t, co.Path, "add", ".")
	git(t, co.Path, "commit", "--quiet", "-m", "local only")
	head := git(t, co.Path, "rev-parse", "HEAD")
	if err := os.RemoveAll(co.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(ctx, chat); err != nil {
		t.Fatal(err)
	}
	if git(t, co.Path, "rev-parse", "HEAD") != head {
		t.Fatal("the local commit is lost")
	}
}

// Deleting a chat commits its uncommitted changes as WIP and pushes them,
// then the worktree and the local branch go; GitHub keeps the branch.
func TestDeleteKeepsTheWorkOnGitHub(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/wip", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	writeFile(t, filepath.Join(co.Path, "draft.txt"), "half done\n")
	writeFile(t, filepath.Join(co.Path, "README.md"), "changed\n")
	if err := m.Delete(ctx, chat, `Fix "it"`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(co.Path); !os.IsNotExist(err) {
		t.Fatal("the worktree is still there")
	}
	tip := remoteHas(t, f, co.Branch)
	if tip == "" {
		t.Fatal("GitHub does not have the branch")
	}
	bare := filepath.Join(f.dir, "octo", "app.git")
	if msg := git(t, bare, "log", "-1", "--format=%s", tip); msg != WIPMessage(`Fix "it"`) {
		t.Fatalf("the commit says %q", msg)
	}
	if got := git(t, bare, "show", tip+":draft.txt"); got != "half done" {
		t.Fatalf("draft.txt is %q", got)
	}

	// A clean chat with nothing new leaves no branch behind on GitHub.
	clean, err := m.Create(ctx, "octo/app", "9999999912345678", "socrates/clean", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, chatOf(clean, "9999999912345678"), "Clean"); err != nil {
		t.Fatal(err)
	}
	if remoteHas(t, f, clean.Branch) != "" {
		t.Fatal("an empty branch was pushed")
	}
}

// A delete whose push fails keeps the worktree: nothing is lost silently.
func TestDeleteKeepsTheWorktreeWhenThePushFails(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/stuck", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	writeFile(t, filepath.Join(co.Path, "draft.txt"), "x\n")
	// GitHub goes away.
	if err := os.Rename(filepath.Join(f.dir, "octo", "app.git"), filepath.Join(f.dir, "octo", "gone.git")); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, chat, "Stuck"); err == nil {
		t.Fatal("the delete went on without the push")
	}
	if _, err := os.Stat(filepath.Join(co.Path, "draft.txt")); err != nil {
		t.Fatal("the worktree went without its work on GitHub")
	}
	removed, kept, err := m.Done(ctx, chat, nil)
	if removed || kept == "" || err == nil {
		t.Fatalf("done without GitHub: removed %v, kept %q, %v", removed, kept, err)
	}
}

// The hourly care prunes the worktrees whose folders are gone.
func TestMaintainPrunes(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/prune", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(co.Path)
	m.Maintain(ctx)
	list, err := m.Worktrees(ctx, "octo/app")
	if err != nil || len(list) != 0 {
		t.Fatalf("worktrees after the care: %v %v", list, err)
	}
}

func TestSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the setup runs sh")
	}
	ctx := context.Background()
	dir := t.TempDir()
	logs := t.TempDir()

	// Without a script and without .socrates/setup there is nothing to run.
	if ran, err := RunSetup(ctx, Setup{Dir: dir, Log: filepath.Join(logs, "none.log")}); ran || err != nil {
		t.Fatalf("nothing to run: %v %v", ran, err)
	}

	env := append(os.Environ(), "SOCRATES_REPO=octo/app")
	ok := filepath.Join(logs, "ok.log")
	ran, err := RunSetup(ctx, Setup{Script: "echo \"setting up $SOCRATES_REPO\"\ntouch made", Dir: dir, Log: ok, Env: env})
	if !ran || err != nil {
		t.Fatalf("the script: %v %v", ran, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "made")); err != nil {
		t.Fatal("the script did not run in the worktree")
	}
	if tail := LogTail(ok, 5); !strings.Contains(tail, "setting up octo/app") || !strings.Contains(tail, "# done") {
		t.Fatalf("the log: %q", tail)
	}

	// sh -e: the first command that fails ends it.
	failed := filepath.Join(logs, "failed.log")
	ran, err = RunSetup(ctx, Setup{Script: "echo before\nfalse\necho after", Dir: dir, Log: failed, Env: env})
	if !ran || err == nil {
		t.Fatalf("a failing script: %v %v", ran, err)
	}
	if log := LogTail(failed, 10); !strings.Contains(log, "before") || strings.Contains(log, "\nafter\n") || !strings.Contains(log, "# failed") {
		t.Fatalf("the log: %q", log)
	}

	slow := filepath.Join(logs, "slow.log")
	start := time.Now()
	ran, err = RunSetup(ctx, Setup{Script: "sleep 30 & sleep 30", Dir: dir, Log: slow, Env: env, Timeout: 300 * time.Millisecond})
	if !ran || !errors.Is(err, ErrSetupTimeout) || time.Since(start) > 10*time.Second {
		t.Fatalf("a script that does not end: %v %v after %s", ran, err, time.Since(start))
	}
	if !strings.Contains(LogTail(slow, 3), "# stopped") {
		t.Fatalf("the log: %q", LogTail(slow, 3))
	}

	// The repository's own setup, when it is executable and Admin has none.
	writeFile(t, filepath.Join(dir, SetupFile), "#!/bin/sh\necho own > own.txt\n")
	if ran, err := RunSetup(ctx, Setup{Dir: dir, Log: filepath.Join(logs, "plain.log"), Env: env}); ran || err != nil {
		t.Fatalf("a setup file that is not executable ran: %v %v", ran, err)
	}
	if err := os.Chmod(filepath.Join(dir, SetupFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if ran, err := RunSetup(ctx, Setup{Dir: dir, Log: filepath.Join(logs, "own.log"), Env: env}); !ran || err != nil {
		t.Fatalf("the repository's setup: %v %v", ran, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "own.txt")); err != nil {
		t.Fatal("the repository's setup did not run")
	}
}

// commit commits a file in a worktree and returns the new HEAD.
func commit(t *testing.T, dir, file, text string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, file), text)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "--quiet", "-m", file)
	return git(t, dir, "rev-parse", "HEAD")
}

// The push's lease is what the chat has, not what the cache last fetched: a
// commit somebody pushed and another chat's fetch brought into the cache is
// not overwritten. The agent's own pushes, and commits it took in from
// GitHub, are the chat's.
func TestTheLeaseIsWhatTheChatHas(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/lease", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if co.Start == "" || co.Start != remoteHas(t, f, "main") {
		t.Fatalf("the branch's start %q", co.Start)
	}
	chat := chatOf(co, "abcdef1234567890")
	commit(t, co.Path, "a.txt", "a\n")
	if pushed, err := m.Push(ctx, chat); err != nil || !pushed {
		t.Fatalf("the first push: %v %v", pushed, err)
	}
	// Somebody pushes to the branch; another chat's fetch brings it in.
	other := t.TempDir()
	git(t, other, "clone", "--quiet", "-b", co.Branch, f.url("octo/app"), ".")
	theirs := commit(t, other, "theirs.txt", "theirs\n")
	git(t, other, "push", "--quiet")
	if _, err := m.Branches(ctx, "octo/app"); err != nil {
		t.Fatal(err)
	}
	commit(t, co.Path, "c.txt", "c\n")
	_, err = m.Push(ctx, chat)
	var rejected *ErrRejected
	if !errors.As(err, &rejected) || !strings.Contains(err.Error(), "fetch and rebase") {
		t.Fatalf("a push over their commit: %v", err)
	}
	if remoteHas(t, f, co.Branch) != theirs {
		t.Fatal("their commit is gone")
	}
	// The agent takes it in: now the push goes.
	git(t, co.Path, "fetch", "--quiet", "origin")
	git(t, co.Path, "rebase", "--quiet", "origin/"+co.Branch)
	if pushed, err := m.Push(ctx, chat); err != nil || !pushed {
		t.Fatalf("the push after the rebase: %v %v", pushed, err)
	}
	if remoteHas(t, f, co.Branch) != git(t, co.Path, "rev-parse", "HEAD") {
		t.Fatal("GitHub does not have the rebased branch")
	}

	// The agent pushes a new branch itself, then amends without pushing:
	// what it pushed is the chat's, and is overwritten.
	co2, err := m.Create(ctx, "octo/app", "2222222212345678", "socrates/self", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat2 := chatOf(co2, "2222222212345678")
	commit(t, co2.Path, "s.txt", "s\n")
	git(t, co2.Path, "push", "--quiet", "-u", "origin", co2.Branch)
	if pushed, err := m.Push(ctx, chat2); err != nil || pushed {
		t.Fatalf("a branch the agent pushed was pushed again: %v %v", pushed, err)
	}
	git(t, co2.Path, "commit", "--quiet", "--amend", "-m", "s, amended")
	if pushed, err := m.Push(ctx, chat2); err != nil || !pushed {
		t.Fatalf("the amended branch: %v %v", pushed, err)
	}
	if remoteHas(t, f, co2.Branch) != git(t, co2.Path, "rev-parse", "HEAD") {
		t.Fatal("GitHub does not have the amended commit")
	}
}

// A branch GitHub squash-merged and deleted is not pushed back while the
// chat has nothing new, and done takes its worktree; a new commit is pushed.
func TestAMergedAndDeletedBranchIsNotPushedBack(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/merged", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	commit(t, co.Path, "m.txt", "m\n")
	if pushed, err := m.Push(ctx, chat); err != nil || !pushed {
		t.Fatalf("the push: %v %v", pushed, err)
	}
	git(t, filepath.Join(f.dir, "octo", "app.git"), "branch", "-D", co.Branch)
	if _, err := m.Branches(ctx, "octo/app"); err != nil {
		t.Fatal(err)
	}
	if pushed, err := m.Push(ctx, chat); err != nil || pushed || remoteHas(t, f, co.Branch) != "" {
		t.Fatalf("the deleted branch was pushed back: %v %v", pushed, err)
	}
	commit(t, co.Path, "n.txt", "n\n")
	if pushed, err := m.Push(ctx, chat); err != nil || !pushed || remoteHas(t, f, co.Branch) == "" {
		t.Fatalf("a new commit after the merge: %v %v", pushed, err)
	}
	git(t, filepath.Join(f.dir, "octo", "app.git"), "branch", "-D", co.Branch)
	if _, err := m.Branches(ctx, "octo/app"); err != nil {
		t.Fatal(err)
	}
	removed, kept, err := m.Done(ctx, chat, nil)
	if err != nil || !removed || remoteHas(t, f, co.Branch) != "" {
		t.Fatalf("done with a merged branch: removed %v, kept %q, %v", removed, kept, err)
	}
}

// Done keeps a worktree that is not on its branch or has an operation under
// way; delete keeps such a state on a rescue branch on GitHub, and the cache
// logs every ref update.
func TestDoneAndDeleteNeedTheChatsBranch(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/off", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	if got := git(t, m.CacheDir("octo/app"), "config", "core.logAllRefUpdates"); got != "true" {
		t.Fatalf("core.logAllRefUpdates %q", got)
	}
	head := commit(t, co.Path, "a.txt", "a\n")
	git(t, co.Path, "checkout", "--quiet", "--detach")
	removed, kept, err := m.Done(ctx, chat, nil)
	if err != nil || removed || !strings.Contains(kept, "detached") {
		t.Fatalf("done, detached: removed %v, kept %q, %v", removed, kept, err)
	}
	if remoteHas(t, f, co.Branch) != head {
		t.Fatal("done did not push the branch")
	}
	git(t, co.Path, "checkout", "--quiet", co.Branch)

	// A rebase that stopped on a conflict.
	commit(t, co.Path, "README.md", "ours\n")
	other := t.TempDir()
	git(t, other, "clone", "--quiet", f.url("octo/app"), ".")
	commit(t, other, "README.md", "theirs\n")
	git(t, other, "push", "--quiet", "origin", "main")
	git(t, co.Path, "fetch", "--quiet", "origin")
	if err := exec.Command("git", "-C", co.Path, "rebase", "origin/main").Run(); err == nil {
		t.Fatal("the rebase did not stop")
	}
	removed, kept, err = m.Done(ctx, chat, nil)
	if err != nil || removed || !strings.Contains(kept, "rebase is in progress") {
		t.Fatalf("done, rebasing: removed %v, kept %q, %v", removed, kept, err)
	}
	writeFile(t, filepath.Join(co.Path, "loose.txt"), "loose\n")
	if err := m.Delete(ctx, chat, "Off"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(co.Path); !os.IsNotExist(err) {
		t.Fatal("the worktree is still there")
	}
	rescue := remoteHas(t, f, RescueBranch(chat.ID))
	if rescue == "" || RescueBranch(chat.ID) != "socrates/rescue/abcdef12" {
		t.Fatalf("no rescue branch on GitHub")
	}
	bare := filepath.Join(f.dir, "octo", "app.git")
	if got := git(t, bare, "show", rescue+":loose.txt"); got != "loose" {
		t.Fatalf("loose.txt on the rescue branch: %q", got)
	}
	if msg := git(t, bare, "log", "-1", "--format=%B", rescue); !strings.Contains(msg, "rebase is in progress") {
		t.Fatalf("the rescue commit says %q", msg)
	}
	if remoteHas(t, f, co.Branch) != git(t, bare, "rev-parse", co.Branch) || remoteHas(t, f, co.Branch) == head {
		t.Fatal("the chat's branch was not pushed as it was")
	}
}

// A chat made before the workspace root changed keeps the cache it was made
// with: its pushes reach GitHub, its worktree is made again from its local
// branch; a chat whose cache is gone says so.
func TestAChatKeepsItsCacheWhenTheRootChanges(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/moved", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	newRoot := t.TempDir()
	m.Root = func() string { return newRoot }
	head := commit(t, co.Path, "a.txt", "a\n")
	if pushed, err := m.Push(ctx, chat); err != nil || !pushed || remoteHas(t, f, co.Branch) != head {
		t.Fatalf("the push after the root changed: %v %v", pushed, err)
	}
	local := commit(t, co.Path, "b.txt", "b\n")
	if err := os.RemoveAll(co.Path); err != nil {
		t.Fatal(err)
	}
	again, err := m.Ensure(ctx, chat)
	if err != nil || !again.Added || again.Start != "" {
		t.Fatalf("made again: %+v %v", again, err)
	}
	if git(t, co.Path, "rev-parse", "HEAD") != local {
		t.Fatal("the local commit is lost")
	}
	if _, err := os.Stat(filepath.Join(newRoot, ".repos")); !os.IsNotExist(err) {
		t.Fatal("a cache was made under the new root")
	}
	if again, err := m.Ensure(ctx, chat); err != nil || again.Added {
		t.Fatalf("a worktree that is there: %+v %v", again, err)
	}
	// The cache goes: nothing is pushed, and the error says why.
	if err := os.RemoveAll(filepath.Join(filepath.Dir(co.Path), ".repos")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Push(ctx, chat); !errors.Is(err, ErrNoCache) {
		t.Fatalf("a push without a cache: %v", err)
	}
}

// A cache whose HEAD is missing is made again only when no worktree hangs
// on it.
func TestACacheWithWorktreesIsNeverDeleted(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	if _, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/keep", "main", false); err != nil {
		t.Fatal(err)
	}
	cache := m.CacheDir("octo/app")
	if err := os.Remove(filepath.Join(cache, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := m.Fetch(ctx, "octo/app"); err == nil {
		t.Fatal("a cache without HEAD but with worktrees was fetched into")
	}
	if _, err := os.Stat(filepath.Join(cache, "worktrees")); err != nil {
		t.Fatal("the cache was deleted")
	}
	// A half-made one without worktrees is made again.
	if err := os.RemoveAll(filepath.Join(cache, "worktrees")); err != nil {
		t.Fatal(err)
	}
	if err := m.Fetch(ctx, "octo/app"); err != nil {
		t.Fatal(err)
	}
}

// The server's git runs no hook of the cache's, puts the remote back as it
// should be, and sends the token to the remote's host only.
func TestTheServersGitIsHardened(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/hard", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	cache := m.CacheDir("octo/app")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, hook := range []string{"post-checkout", "pre-push", "reference-transaction"} {
		writeFile(t, filepath.Join(cache, "hooks", hook), "#!/bin/sh\ntouch "+marker+"\n")
		if err := os.Chmod(filepath.Join(cache, "hooks", hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	git(t, f.dir, "init", "--quiet", "--bare", elsewhere)
	git(t, cache, "config", "remote.origin.pushurl", "file://"+elsewhere)
	git(t, cache, "config", "remote.origin.url", "file://"+elsewhere)
	head := commit(t, co.Path, "a.txt", "a\n")
	os.Remove(marker)
	if pushed, err := m.Push(ctx, chat); err != nil || !pushed {
		t.Fatalf("the push: %v %v", pushed, err)
	}
	if _, err := m.Create(ctx, "octo/app", "2222222212345678", "socrates/hard2", "main", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a hook of the cache ran")
	}
	if remoteHas(t, f, co.Branch) != head {
		t.Fatal("the push went elsewhere")
	}
	if out, err := exec.Command("git", "-C", cache, "config", "--get", "remote.origin.pushurl").Output(); err == nil {
		t.Fatalf("the push URL is still there: %s", out)
	}
	env := strings.Join(gitEnv("tok", "https://github.com/octo/app.git"), "\n")
	if !strings.Contains(env, "GIT_CONFIG_KEY_1=http.https://github.com/.extraHeader") || strings.Contains(env, "KEY_1=http.extraHeader") {
		t.Fatalf("the header is not scoped: %s", env)
	}
}

// A branch name Socrates made up that GitHub or the cache has already gets
// the chat's id8; a name the person gave continues that branch.
func TestGeneratedBranchNamesDoNotCollide(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "feature", "main", true)
	if err != nil || co.Branch != "feature-abcdef12" || co.Existing {
		t.Fatalf("a generated name GitHub has: %+v %v", co, err)
	}
	a, err := m.Create(ctx, "octo/app", "1111111112345678", "socrates/same", "main", true)
	if err != nil || a.Branch != "socrates/same" {
		t.Fatalf("the first: %+v %v", a, err)
	}
	b, err := m.Create(ctx, "octo/app", "2222222212345678", "socrates/same", "main", true)
	if err != nil || b.Branch != "socrates/same-22222222" {
		t.Fatalf("the second: %+v %v", b, err)
	}
}

// The WIP commit of a deleted chat leaves files that look like secrets and
// big files out, lists them, and keeps the worktree.
func TestTheWIPCommitLeavesSecretsOut(t *testing.T) {
	f := newFake(t)
	m := newManager(t, f)
	ctx := context.Background()
	co, err := m.Create(ctx, "octo/app", "abcdef1234567890", "socrates/secrets", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	chat := chatOf(co, "abcdef1234567890")
	writeFile(t, filepath.Join(co.Path, "notes.txt"), "notes\n")
	writeFile(t, filepath.Join(co.Path, "README.md"), "changed\n")
	for _, name := range []string{".env", ".env.local", "conf/server.pem", "id_ed25519", "x.key", "cert.p12"} {
		writeFile(t, filepath.Join(co.Path, name), "secret\n")
	}
	big, err := os.Create(filepath.Join(co.Path, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(11 << 20); err != nil {
		t.Fatal(err)
	}
	big.Close()
	err = m.Delete(ctx, chat, "Secrets")
	var left *ErrLeftOut
	if !errors.As(err, &left) || len(left.Files) != 7 {
		t.Fatalf("the delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(co.Path, ".env")); err != nil {
		t.Fatal("the worktree went with files that were not committed")
	}
	tip := remoteHas(t, f, co.Branch)
	bare := filepath.Join(f.dir, "octo", "app.git")
	files := git(t, bare, "ls-tree", "-r", "--name-only", tip)
	if !strings.Contains(files, "notes.txt") || strings.Contains(files, ".env") || strings.Contains(files, "big.bin") ||
		strings.Contains(files, ".pem") || strings.Contains(files, "id_ed25519") {
		t.Fatalf("the WIP commit has %s", files)
	}
	if got := git(t, bare, "show", tip+":README.md"); got != "changed" {
		t.Fatalf("README.md is %q", got)
	}
	if msg := git(t, bare, "log", "-1", "--format=%B", tip); !strings.Contains(msg, "- .env.local") || !strings.Contains(msg, "big.bin") {
		t.Fatalf("the commit does not list what was left out: %q", msg)
	}
}
