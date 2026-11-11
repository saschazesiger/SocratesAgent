// Package repos is a chat's GitHub repository on this server: Socrates' own
// bare clone of it (the cache), one git worktree per chat on a branch of the
// chat's own, the push of that branch, and the worktree's end when the chat is
// done or deleted (docs/design/REPOS.md §4).
//
//	<root>/.repos/<owner>/<name>.git   the cache, bare, shared by every chat
//	<root>/<name>-<id8>                a chat's worktree
//
// Everything here runs git as a program. The operations of one repository
// run one at a time (Manager.lock): two chats made at once must not fetch into
// the same cache together, and a worktree must not be added while another is
// being removed.
package repos

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Remote is a repository as GitHub describes it to Socrates: what the
// GitHub App reaches (internal/github's Repo, without what this package does
// not need).
type Remote struct {
	FullName      string
	DefaultBranch string
	Private       bool
}

// PullRequest is a pull request GitHub opened, or the one a branch already
// had (Existing).
type PullRequest struct {
	Number   int
	URL      string
	Existing bool
}

// Branch is one branch of the repository as the cache last fetched it.
type Branch struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
	// Date is the time of the branch's last commit, in milliseconds since
	// the epoch.
	Date int64 `json:"date"`
}

// Checkout is a chat's worktree: where it is, its branch and base, and
// whether the branch was on GitHub already (a chat that continues an existing
// branch rather than making one).
type Checkout struct {
	Path     string
	Branch   string
	Base     string
	Existing bool
}

// ErrBranchInUse is a branch that is checked out in another worktree already:
// git refuses to check one branch out twice, and two chats on one branch is
// what this design exists to prevent.
type ErrBranchInUse struct {
	Branch string
	// Path is the worktree that has it, which names the chat to the caller.
	Path string
}

func (e *ErrBranchInUse) Error() string {
	return fmt.Sprintf("the branch %s is checked out in %s already", e.Branch, e.Path)
}

// gitTimeout bounds one git command: a fetch of a large repository over a
// slow line takes a while, a git that hangs on a network that is gone must
// not hold the repository's lock for ever.
const gitTimeout = 10 * time.Minute

// Manager runs the repositories of one Socrates.
type Manager struct {
	// Root is the workspace root (settings.workspace.root), read at every
	// call so that a changed setting counts from the next chat.
	Root func() string
	// URL is the clone URL of a repository (internal/github CloneURL):
	// https://github.com/<owner>/<name>.git, or a file:// URL in the tests.
	URL func(fullName string) string
	// Token is an installation token for the repository, for the git
	// commands that reach GitHub; nil or "" is none (a file:// remote needs
	// none). It is only ever passed to git in its environment, never written
	// to disk.
	Token func(ctx context.Context, fullName string) (string, error)

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lock takes the repository's lock and returns its release.
func (m *Manager) lock(fullName string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = map[string]*sync.Mutex{}
	}
	l := m.locks[fullName]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[fullName] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// ValidName says whether fullName is a repository's owner/name as GitHub
// spells them: letters, digits, '-', '_' and '.', and neither part a dot path.
// The name becomes part of folder names, so nothing else gets through.
func ValidName(fullName string) bool {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok {
		return false
	}
	for _, part := range []string{owner, name} {
		if part == "" || len(part) > 100 || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

func (m *Manager) root() string { return filepath.Clean(m.Root()) }

// CacheDir is where the repository's cache is.
func (m *Manager) CacheDir(fullName string) string {
	owner, name, _ := strings.Cut(fullName, "/")
	return filepath.Join(m.root(), ".repos", owner, name+".git")
}

// WorktreeDir is where a chat's worktree of the repository goes:
// <root>/<name>-<id8>.
func (m *Manager) WorktreeDir(fullName, chatID string) string {
	_, name, _ := strings.Cut(fullName, "/")
	id8 := chatID
	if len(id8) > 8 {
		id8 = id8[:8]
	}
	return filepath.Join(m.root(), name+"-"+id8)
}

// CheckBranch says whether name is a branch name git accepts
// (`git check-ref-format --branch`), and one that cannot be read as an
// option.
func CheckBranch(ctx context.Context, name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\n") {
		return fmt.Errorf("%q is not a branch name", name)
	}
	cmd := exec.CommandContext(ctx, "git", "check-ref-format", "--branch", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(bytes.TrimSpace(out)) > 0 || errors.As(err, new(*exec.ExitError)) {
			return fmt.Errorf("%q is not a branch name git accepts", name)
		}
		return err
	}
	return nil
}

// ------------------------------------------------------------ running git

// gitError is a git command that failed, with what it said.
type gitError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	what := "git " + strings.Join(e.args, " ")
	if msg := strings.TrimSpace(e.stderr); msg != "" {
		return what + ": " + lastLines(msg, 6)
	}
	return what + ": " + e.err.Error()
}

func (e *gitError) Unwrap() error { return e.err }

// lastLines is the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// gitEnv is the environment of a git the server runs: the server's own,
// without what would point git elsewhere, never asking at a terminal, and,
// with a token, the token as an Authorization header for an https remote.
// The header goes in through GIT_CONFIG_* rather than `-c` so that it is in
// no process listing; the credential helpers are switched off at the same
// time, so that root's own login (gh) is never asked instead.
func gitEnv(token string) []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch {
		case key == "GIT_CONFIG_COUNT", key == "GIT_CONFIG_PARAMETERS", strings.HasPrefix(key, "GIT_CONFIG_KEY_"),
			strings.HasPrefix(key, "GIT_CONFIG_VALUE_"), key == "GIT_DIR", key == "GIT_WORK_TREE", key == "GIT_INDEX_FILE",
			key == "GIT_COMMON_DIR", key == "GIT_TERMINAL_PROMPT", key == "GIT_ASKPASS", key == "SSH_ASKPASS":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		env = append(env, "GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
			"GIT_CONFIG_KEY_1=http.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Basic "+basic)
	}
	return env
}

// run runs git in dir and returns what it printed.
func run(ctx context.Context, env []string, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &gitError{args: args, stderr: stderr.String(), err: err}
	}
	return stdout.String(), nil
}

// local runs a git that needs no network.
func (m *Manager) local(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, gitEnv(""), dir, args...)
}

// remote runs a git that reaches the repository's remote, with its token
// when the remote is https.
func (m *Manager) remote(ctx context.Context, fullName, dir string, args ...string) (string, error) {
	token := ""
	if u, err := url.Parse(m.URL(fullName)); err == nil && u.Scheme == "https" && m.Token != nil {
		t, err := m.Token(ctx, fullName)
		if err != nil {
			return "", fmt.Errorf("no token for %s: %w", fullName, err)
		}
		token = t
	}
	return run(ctx, gitEnv(token), dir, args...)
}

// ------------------------------------------------------------- the cache

// Fetch brings the repository's cache up to date with GitHub, making it the
// first time.
func (m *Manager) Fetch(ctx context.Context, fullName string) error {
	if !ValidName(fullName) {
		return fmt.Errorf("%q is not a repository", fullName)
	}
	defer m.lock(fullName)()
	return m.fetchLocked(ctx, fullName)
}

func (m *Manager) fetchLocked(ctx context.Context, fullName string) error {
	cache := m.CacheDir(fullName)
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err != nil {
		if err := m.initCache(ctx, fullName, cache); err != nil {
			return err
		}
	} else if out, _ := m.local(ctx, cache, "config", "--get", "remote.origin.url"); strings.TrimSpace(out) != m.URL(fullName) {
		// GitHub's address changed (SOCRATES_GITHUB_GIT): the cache follows.
		if _, err := m.local(ctx, cache, "config", "remote.origin.url", m.URL(fullName)); err != nil {
			return err
		}
	}
	_, err := m.remote(ctx, fullName, cache, "fetch", "--prune", "--quiet", "origin")
	return err
}

// initCache makes an empty bare repository whose fetch puts GitHub's branches
// under refs/remotes/origin/, never onto a local branch: the local branches
// are the chats', and a fetch must never move one of them.
func (m *Manager) initCache(ctx context.Context, fullName, cache string) error {
	// A half-made cache of a start that failed is made again.
	_ = os.RemoveAll(cache)
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return err
	}
	if _, err := m.local(ctx, filepath.Dir(cache), "init", "--quiet", "--bare", cache); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", m.URL(fullName)},
		{"config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		// A chat's first `git push` makes its branch on GitHub without
		// being told where to (git push -u origin <branch> works as well).
		{"config", "push.autoSetupRemote", "true"},
		// The objects are shared by every worktree; gc --auto is run by the
		// hourly maintenance, not in the middle of a chat's commit.
		{"config", "gc.autoDetach", "false"},
	} {
		if _, err := m.local(ctx, cache, args...); err != nil {
			_ = os.RemoveAll(cache)
			return err
		}
	}
	return nil
}

// Branches fetches the repository and lists its branches on GitHub, the one
// with the newest commit first.
func (m *Manager) Branches(ctx context.Context, fullName string) ([]Branch, error) {
	if !ValidName(fullName) {
		return nil, fmt.Errorf("%q is not a repository", fullName)
	}
	defer m.lock(fullName)()
	if err := m.fetchLocked(ctx, fullName); err != nil {
		return nil, err
	}
	out, err := m.local(ctx, m.CacheDir(fullName), "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%09%(objectname)%09%(committerdate:unix)", "refs/remotes/origin/")
	if err != nil {
		return nil, err
	}
	list := []Branch{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "refs/remotes/origin/")
		if name == "HEAD" || name == fields[0] {
			continue
		}
		secs, _ := strconv.ParseInt(fields[2], 10, 64)
		list = append(list, Branch{Name: name, SHA: fields[1], Date: secs * 1000})
	}
	return list, nil
}

// hasRef says whether the cache has the ref.
func (m *Manager) hasRef(ctx context.Context, cache, ref string) bool {
	_, err := m.local(ctx, cache, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// ---------------------------------------------------------- the worktrees

// Create fetches the repository and makes a chat's worktree: on a new branch
// made from base, or - when GitHub has the branch already - on that branch,
// tracking it. A branch another worktree has checked out is an
// *ErrBranchInUse.
func (m *Manager) Create(ctx context.Context, fullName, chatID, branch, base string) (Checkout, error) {
	if !ValidName(fullName) {
		return Checkout{}, fmt.Errorf("%q is not a repository", fullName)
	}
	if err := CheckBranch(ctx, branch); err != nil {
		return Checkout{}, err
	}
	defer m.lock(fullName)()
	if err := m.fetchLocked(ctx, fullName); err != nil {
		return Checkout{}, err
	}
	return m.addLocked(ctx, fullName, m.WorktreeDir(fullName, chatID), branch, base)
}

// Ensure makes sure a chat's worktree is there before its agent starts: one
// that was removed when the chat was done is made again, from the local branch
// if the cache still has it and from GitHub's otherwise.
func (m *Manager) Ensure(ctx context.Context, fullName, path, branch, base string) (Checkout, error) {
	if !ValidName(fullName) {
		return Checkout{}, fmt.Errorf("%q is not a repository", fullName)
	}
	// Under the lock: a done chat's worktree being removed right now is
	// made again once that is over, not taken for one that is there.
	defer m.lock(fullName)()
	if isWorktree(path) {
		return Checkout{Path: path, Branch: branch, Base: base}, nil
	}
	if err := m.fetchLocked(ctx, fullName); err != nil {
		return Checkout{}, err
	}
	return m.addLocked(ctx, fullName, path, branch, base)
}

// isWorktree says whether path is a checkout: a folder with a .git in it.
func isWorktree(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func (m *Manager) addLocked(ctx context.Context, fullName, path, branch, base string) (Checkout, error) {
	cache := m.CacheDir(fullName)
	// A worktree whose folder went (removed by hand, a restore without it)
	// still holds its branch until it is pruned.
	if _, err := m.local(ctx, cache, "worktree", "prune"); err != nil {
		return Checkout{}, err
	}
	if other, err := m.checkedOut(ctx, cache, branch); err != nil {
		return Checkout{}, err
	} else if other != "" && filepath.Clean(other) != filepath.Clean(path) {
		return Checkout{}, &ErrBranchInUse{Branch: branch, Path: other}
	}
	if _, err := os.Stat(path); err == nil {
		return Checkout{}, fmt.Errorf("%s is in the way: it exists, and is not a worktree of %s", path, fullName)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Checkout{}, err
	}
	out := Checkout{Path: path, Branch: branch, Base: base}
	local, remote := "refs/heads/"+branch, "refs/remotes/origin/"+branch
	var args []string
	switch {
	case m.hasRef(ctx, cache, local):
		// The chat's own branch, kept in the cache while its worktree was
		// not there: it may hold commits GitHub does not have yet.
		args = []string{"worktree", "add", "--quiet", path, branch}
		out.Existing = m.hasRef(ctx, cache, remote)
	case m.hasRef(ctx, cache, remote):
		args = []string{"worktree", "add", "--quiet", "--track", "-b", branch, path, "origin/" + branch}
		out.Existing = true
	default:
		if base == "" {
			return Checkout{}, fmt.Errorf("the branch %s is not on GitHub, and there is no base to make it from", branch)
		}
		if !m.hasRef(ctx, cache, "refs/remotes/origin/"+base) {
			return Checkout{}, fmt.Errorf("the base branch %s is not on GitHub", base)
		}
		// No upstream: the branch is not on GitHub yet, and its first push
		// sets one.
		args = []string{"worktree", "add", "--quiet", "--no-track", "-b", branch, path, "origin/" + base}
	}
	if _, err := m.local(ctx, cache, args...); err != nil {
		return Checkout{}, err
	}
	return out, nil
}

// checkedOut is the worktree that has the branch checked out, "" for none.
func (m *Manager) checkedOut(ctx context.Context, cache, branch string) (string, error) {
	out, err := m.local(ctx, cache, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	path := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return path, nil
		}
	}
	return "", nil
}

// Worktrees is every worktree of the repository and the branch it has
// checked out ("" for a detached one), the cache itself left out.
func (m *Manager) Worktrees(ctx context.Context, fullName string) (map[string]string, error) {
	cache := m.CacheDir(fullName)
	out, err := m.local(ctx, cache, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	list := map[string]string{}
	path := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
			if filepath.Clean(path) != filepath.Clean(cache) {
				list[path] = ""
			}
		case strings.HasPrefix(line, "branch refs/heads/") && path != "" && filepath.Clean(path) != filepath.Clean(cache):
			list[path] = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	return list, nil
}

// ------------------------------------------------------------ the branch

// ahead says whether the branch has commits GitHub does not have, as of the
// cache's last look at GitHub: commits not on its own branch there, or, for a
// branch GitHub does not have yet, commits on no branch there.
func (m *Manager) ahead(ctx context.Context, cache, branch string) (bool, error) {
	local := "refs/heads/" + branch
	if !m.hasRef(ctx, cache, local) {
		return false, nil
	}
	args := []string{"rev-list", "--count", local, "--not"}
	if remote := "refs/remotes/origin/" + branch; m.hasRef(ctx, cache, remote) {
		args = append(args, remote)
	} else {
		args = append(args, "--remotes=origin")
	}
	out, err := m.local(ctx, cache, args...)
	if err != nil {
		return false, err
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n > 0, nil
}

// Discard removes a worktree that was just made and holds nothing yet, with
// its branch: the chat it was made for could not be stored.
func (m *Manager) Discard(ctx context.Context, fullName, path, branch string) error {
	defer m.lock(fullName)()
	cache := m.CacheDir(fullName)
	// A local branch the worktree was made from may hold commits GitHub
	// does not have: it stays.
	if ahead, err := m.ahead(ctx, cache, branch); err != nil || ahead {
		_, rerr := m.local(ctx, cache, "worktree", "remove", "--force", path)
		return errors.Join(err, rerr)
	}
	return m.removeLocked(ctx, cache, path, branch)
}

// OnGitHub says whether GitHub has the branch, as of the cache's last look.
func (m *Manager) OnGitHub(ctx context.Context, fullName, branch string) bool {
	defer m.lock(fullName)()
	return m.hasRef(ctx, m.CacheDir(fullName), "refs/remotes/origin/"+branch)
}

// Ahead says whether the chat's branch has commits GitHub does not have.
func (m *Manager) Ahead(ctx context.Context, fullName, branch string) (bool, error) {
	defer m.lock(fullName)()
	return m.ahead(ctx, m.CacheDir(fullName), branch)
}

// Push pushes the chat's branch when it has commits GitHub does not have, and
// says whether it did. The push is --force-with-lease: the branch is the
// chat's own and a rebase onto its base rewrites it, but a commit somebody
// else pushed to it on GitHub since the cache last saw it is never
// overwritten - that push fails instead. Uncommitted changes are left alone:
// committing is the agent's work.
func (m *Manager) Push(ctx context.Context, fullName, branch string) (bool, error) {
	defer m.lock(fullName)()
	return m.pushLocked(ctx, fullName, branch)
}

func (m *Manager) pushLocked(ctx context.Context, fullName, branch string) (bool, error) {
	cache := m.CacheDir(fullName)
	ahead, err := m.ahead(ctx, cache, branch)
	if err != nil || !ahead {
		return false, err
	}
	ref := "refs/heads/" + branch
	if _, err := m.remote(ctx, fullName, cache, "push", "--quiet", "--force-with-lease="+ref, "origin", ref+":"+ref); err != nil {
		return false, err
	}
	return true, nil
}

// dirty says whether the worktree has changes that are not committed,
// untracked files included (ignored ones are not: they are what a setup
// installs).
func (m *Manager) dirty(ctx context.Context, path string) (bool, error) {
	out, err := m.local(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Done is what filing a chat as done does to its worktree: the branch is
// pushed, and a worktree that is clean and has everything on GitHub goes,
// with the local branch (GitHub has it). Otherwise the worktree stays, and
// kept says why, in words for the chat. mayRemove, when it is set, is asked
// under the repository's lock just before the worktree goes: a chat picked up
// again meanwhile keeps it, and kept is "".
func (m *Manager) Done(ctx context.Context, fullName, path, branch string, mayRemove func() bool) (removed bool, kept string, err error) {
	defer m.lock(fullName)()
	cache := m.CacheDir(fullName)
	if !isWorktree(path) {
		return true, "", nil
	}
	if _, err := m.pushLocked(ctx, fullName, branch); err != nil {
		return false, "its branch could not be pushed to GitHub", err
	}
	if d, err := m.dirty(ctx, path); err != nil {
		return false, "", err
	} else if d {
		return false, "it has changes that are not committed", nil
	}
	if a, err := m.ahead(ctx, cache, branch); err != nil {
		return false, "", err
	} else if a {
		return false, "its branch has commits that are not on GitHub", nil
	}
	if mayRemove != nil && !mayRemove() {
		return false, "", nil
	}
	if err := m.removeLocked(ctx, cache, path, branch); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// WIPMessage is the commit a deleted chat's uncommitted changes are kept in.
func WIPMessage(title string) string {
	return fmt.Sprintf("WIP: uncommitted changes of chat %q", title)
}

// Delete is what deleting a chat does to its worktree: uncommitted changes
// are committed to the branch (WIPMessage), the branch is pushed, and the
// worktree and the local branch go; GitHub keeps the branch. Nothing is lost
// silently: when the commit or the push fails, the worktree and the branch
// stay where they are and the error says why.
func (m *Manager) Delete(ctx context.Context, fullName, path, branch, title string) error {
	defer m.lock(fullName)()
	cache := m.CacheDir(fullName)
	if isWorktree(path) {
		d, err := m.dirty(ctx, path)
		if err != nil {
			return err
		}
		if d {
			if err := m.commitAll(ctx, path, WIPMessage(title)); err != nil {
				return err
			}
		}
	}
	if _, err := m.pushLocked(ctx, fullName, branch); err != nil {
		return err
	}
	if isWorktree(path) {
		return m.removeLocked(ctx, cache, path, branch)
	}
	_, _ = m.local(ctx, cache, "worktree", "prune")
	if m.hasRef(ctx, cache, "refs/heads/"+branch) {
		if other, _ := m.checkedOut(ctx, cache, branch); other == "" {
			_, err := m.local(ctx, cache, "branch", "--quiet", "-D", branch)
			return err
		}
	}
	return nil
}

// commitAll commits everything in the worktree. Hooks are not run - a WIP
// commit is a rescue, not a contribution - and a machine without a git
// identity commits as Socrates.
func (m *Manager) commitAll(ctx context.Context, path, message string) error {
	if _, err := m.local(ctx, path, "add", "--all"); err != nil {
		return err
	}
	args := []string{"commit", "--quiet", "--no-verify", "-m", message}
	if out, _ := m.local(ctx, path, "config", "user.email"); strings.TrimSpace(out) == "" {
		args = append([]string{"-c", "user.name=Socrates", "-c", "user.email=socrates@localhost"}, args...)
	}
	_, err := m.local(ctx, path, args...)
	return err
}

// removeLocked removes a worktree and the chat's local branch. The caller has
// made sure that nothing in it is lost: --force only takes the ignored files
// (what a setup installed) with it.
func (m *Manager) removeLocked(ctx context.Context, cache, path, branch string) error {
	if _, err := m.local(ctx, cache, "worktree", "remove", "--force", path); err != nil {
		return err
	}
	if m.hasRef(ctx, cache, "refs/heads/"+branch) {
		if _, err := m.local(ctx, cache, "branch", "--quiet", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}

// Maintain is the hourly care of every cache: the worktrees whose folders are
// gone are pruned, and the objects are packed when git thinks it is time.
func (m *Manager) Maintain(ctx context.Context) {
	base := filepath.Join(m.root(), ".repos")
	owners, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, owner := range owners {
		repos, err := os.ReadDir(filepath.Join(base, owner.Name()))
		if err != nil {
			continue
		}
		for _, repo := range repos {
			name, ok := strings.CutSuffix(repo.Name(), ".git")
			fullName := owner.Name() + "/" + name
			if !ok || !ValidName(fullName) || ctx.Err() != nil {
				continue
			}
			func() {
				defer m.lock(fullName)()
				cache := m.CacheDir(fullName)
				_, _ = m.local(ctx, cache, "worktree", "prune")
				_, _ = m.local(ctx, cache, "gc", "--auto", "--quiet")
			}()
		}
	}
}
