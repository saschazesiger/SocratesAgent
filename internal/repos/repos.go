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
	// Added says that the worktree was made now (Ensure: it was not there).
	Added bool
	// Start is the commit a worktree made now starts at when it was made
	// from GitHub's branch or the base, "" when it was made from the chat's
	// own local branch: the caller keeps it as the chat's lease (Chat).
	Start string
}

// Chat is a chat's worktree as the operations after its creation need it.
type Chat struct {
	FullName string
	// ID is the chat's id; its first eight characters name RescueBranch.
	ID     string
	Path   string
	Branch string
	Base   string
	// Lease is the commit Socrates last pushed the branch at, or made it
	// from (Checkout.Start), "" when it is not known; SetLease keeps a new
	// one. Both are called under the repository's lock: the caller keeps
	// the lease where it lasts (the chat's options) and reads it from there
	// at every call, never from a copy taken earlier. nil is none.
	Lease    func() string
	SetLease func(sha string)
}

func (c Chat) lease() string {
	if c.Lease == nil {
		return ""
	}
	return c.Lease()
}

func (c Chat) setLease(sha string) {
	if c.SetLease != nil && sha != "" && sha != c.lease() {
		c.SetLease(sha)
	}
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
	// GitHub's names do not care about case, and neither does the lock.
	fullName = strings.ToLower(fullName)
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
// with a token, the token as an Authorization header for the remote's scheme
// and host only (remoteURL): a remote URL somebody changed to another host
// never sees it. The header goes in through GIT_CONFIG_* rather than `-c` so
// that it is in no process listing; the credential helpers are switched off
// at the same time, so that root's own login (gh) is never asked instead.
func gitEnv(token, remoteURL string) []string {
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
	if u, err := url.Parse(remoteURL); token != "" && err == nil && u.Scheme != "" && u.Host != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		env = append(env, "GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
			"GIT_CONFIG_KEY_1=http."+u.Scheme+"://"+u.Host+"/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Basic "+basic)
	}
	return env
}

// safeConfig goes before every git command the server runs: the cache and
// its worktrees are the agent's to write, so nothing in them that git would
// run - a hook, an fsmonitor - is run by the server.
var safeConfig = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}

// run runs git in dir and returns what it printed; stdin, when it is not
// empty, is its input.
func run(ctx context.Context, env []string, dir, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, safeConfig...), args...)...)
	cmd.Dir = dir
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &gitError{args: args, stderr: stderr.String(), err: err}
	}
	return stdout.String(), nil
}

// local runs a git that needs no network.
func (m *Manager) local(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, gitEnv("", ""), dir, "", args...)
}

// remote runs a git that reaches the repository's remote, with its token
// when the remote is https.
func (m *Manager) remote(ctx context.Context, fullName, dir string, args ...string) (string, error) {
	token := ""
	remoteURL := m.URL(fullName)
	if u, err := url.Parse(remoteURL); err == nil && u.Scheme == "https" && m.Token != nil {
		t, err := m.Token(ctx, fullName)
		if err != nil {
			return "", fmt.Errorf("no token for %s: %w", fullName, err)
		}
		token = t
	}
	return run(ctx, gitEnv(token, remoteURL), dir, "", args...)
}

// ------------------------------------------------------------- the cache

// ErrNoCache is a chat whose repository's cache cannot be found: its
// worktree's .git names one that is gone, or neither is there.
var ErrNoCache = errors.New("the chat's repository cache cannot be found")

// cacheOf is the cache of a chat's worktree: the worktree's common git
// directory when the worktree is there, so that a chat made before the
// workspace root changed keeps its own cache, and otherwise the one beside
// where the worktree was (<root>/.repos/<owner>/<name>.git, with the root the
// chat was made under).
func (m *Manager) cacheOf(ctx context.Context, c Chat) (string, error) {
	if isWorktree(c.Path) {
		out, err := m.local(ctx, c.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return "", fmt.Errorf("%w: %s is no worktree of it (%v)", ErrNoCache, c.Path, err)
		}
		return filepath.Clean(strings.TrimSpace(out)), nil
	}
	owner, name, _ := strings.Cut(c.FullName, "/")
	return filepath.Join(filepath.Dir(filepath.Clean(c.Path)), ".repos", owner, name+".git"), nil
}

// existingCache is cacheOf, and ErrNoCache when that cache is not there.
func (m *Manager) existingCache(ctx context.Context, c Chat) (string, error) {
	cache, err := m.cacheOf(ctx, c)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err != nil {
		return "", fmt.Errorf("%w: %s is not there", ErrNoCache, cache)
	}
	return cache, nil
}

// CacheOf is the cache of a chat's worktree (cacheOf).
func (m *Manager) CacheOf(ctx context.Context, c Chat) (string, error) {
	return m.cacheOf(ctx, c)
}

// Fetch brings the repository's cache up to date with GitHub, making it the
// first time.
func (m *Manager) Fetch(ctx context.Context, fullName string) error {
	if !ValidName(fullName) {
		return fmt.Errorf("%q is not a repository", fullName)
	}
	defer m.lock(fullName)()
	return m.fetchLocked(ctx, fullName, m.CacheDir(fullName))
}

func (m *Manager) fetchLocked(ctx context.Context, fullName, cache string) error {
	_, err := os.Stat(filepath.Join(cache, "HEAD"))
	switch {
	case err == nil:
	case os.IsNotExist(err):
		// A cache without HEAD is a start that failed half way - unless
		// worktrees hang on it, whose branches may hold commits GitHub
		// does not have: that one is nobody's to delete.
		if _, err := os.Stat(filepath.Join(cache, "worktrees")); err == nil {
			return fmt.Errorf("the cache %s has worktrees but no HEAD: it is left as it is for somebody to look at", cache)
		}
		if err := m.initCache(ctx, fullName, cache); err != nil {
			return err
		}
	default:
		return err
	}
	if err := m.configure(ctx, fullName, cache); err != nil {
		return err
	}
	_, err = m.remote(ctx, fullName, cache, "fetch", "--prune", "--quiet", "origin")
	return err
}

// configure puts the cache's configuration back as Socrates wants it, every
// time it reaches GitHub: the cache's configuration is shared with the
// worktrees, where an agent may have changed it. The remote is the
// repository's URL with no push URL of its own (GitHub's address may change,
// SOCRATES_GITHUB_GIT), the fetch never moves a local branch, and every ref
// update is logged, so that a branch's earlier tips are known (pushLocked)
// and a rewrite can be undone.
func (m *Manager) configure(ctx context.Context, fullName, cache string) error {
	for _, args := range [][]string{
		{"config", "remote.origin.url", m.URL(fullName)},
		{"config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"config", "core.logAllRefUpdates", "true"},
	} {
		if _, err := m.local(ctx, cache, args...); err != nil {
			return err
		}
	}
	// Exit status 5 is "there was none".
	if _, err := m.local(ctx, cache, "config", "--unset-all", "remote.origin.pushurl"); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 5 {
			return err
		}
	}
	return nil
}

// initCache makes an empty bare repository whose fetch puts GitHub's branches
// under refs/remotes/origin/, never onto a local branch: the local branches
// are the chats', and a fetch must never move one of them.
func (m *Manager) initCache(ctx context.Context, fullName, cache string) error {
	// A half-made cache of a start that failed is made again (the caller
	// has made sure that it has no worktrees).
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
	if err := m.fetchLocked(ctx, fullName, m.CacheDir(fullName)); err != nil {
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
	_, ok := m.resolve(ctx, cache, ref)
	return ok
}

// resolve is the commit a ref names.
func (m *Manager) resolve(ctx context.Context, dir, ref string) (string, bool) {
	out, err := m.local(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// ---------------------------------------------------------- the worktrees

// Create fetches the repository and makes a chat's worktree: on a new branch
// made from base, or - when GitHub has the branch already - on that branch,
// tracking it. A branch another worktree has checked out is an
// *ErrBranchInUse. generated says that Socrates named the branch, not the
// person: a generated name that GitHub or the cache has already gets the
// chat's id8 appended instead of continuing that branch.
func (m *Manager) Create(ctx context.Context, fullName, chatID, branch, base string, generated bool) (Checkout, error) {
	if !ValidName(fullName) {
		return Checkout{}, fmt.Errorf("%q is not a repository", fullName)
	}
	if err := CheckBranch(ctx, branch); err != nil {
		return Checkout{}, err
	}
	defer m.lock(fullName)()
	cache := m.CacheDir(fullName)
	if err := m.fetchLocked(ctx, fullName, cache); err != nil {
		return Checkout{}, err
	}
	if generated && (m.hasRef(ctx, cache, "refs/heads/"+branch) || m.hasRef(ctx, cache, "refs/remotes/origin/"+branch)) {
		branch += "-" + id8(chatID)
		if err := CheckBranch(ctx, branch); err != nil {
			return Checkout{}, err
		}
	}
	return m.addLocked(ctx, fullName, cache, m.WorktreeDir(fullName, chatID), branch, base)
}

// id8 is the first eight characters of a chat id.
func id8(chatID string) string {
	if len(chatID) > 8 {
		return chatID[:8]
	}
	return chatID
}

// Ensure makes sure a chat's worktree is there before its agent starts: one
// that was removed when the chat was done is made again, from the local branch
// if the cache still has it and from GitHub's otherwise (Checkout.Added).
// The cache is the one the chat was made with (cacheOf), whatever the
// workspace root is now.
func (m *Manager) Ensure(ctx context.Context, c Chat) (Checkout, error) {
	if !ValidName(c.FullName) {
		return Checkout{}, fmt.Errorf("%q is not a repository", c.FullName)
	}
	// Under the lock: a done chat's worktree being removed right now is
	// made again once that is over, not taken for one that is there.
	defer m.lock(c.FullName)()
	cache, err := m.cacheOf(ctx, c)
	if err != nil {
		return Checkout{}, err
	}
	if isWorktree(c.Path) {
		return Checkout{Path: c.Path, Branch: c.Branch, Base: c.Base}, nil
	}
	if err := m.fetchLocked(ctx, c.FullName, cache); err != nil {
		return Checkout{}, err
	}
	return m.addLocked(ctx, c.FullName, cache, c.Path, c.Branch, c.Base)
}

// isWorktree says whether path is a checkout: a folder with a .git in it.
func isWorktree(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func (m *Manager) addLocked(ctx context.Context, fullName, cache, path, branch, base string) (Checkout, error) {
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
	out := Checkout{Path: path, Branch: branch, Base: base, Added: true}
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
		out.Start, _ = m.resolve(ctx, cache, remote)
	default:
		empty, err := m.emptyRemote(ctx, cache)
		if err != nil {
			return Checkout{}, err
		}
		if base == "" && empty {
			base = firstBranch
			out.Base = base
		}
		if base == "" {
			return Checkout{}, fmt.Errorf("the branch %s is not on GitHub, and there is no base to make it from", branch)
		}
		start, ok := m.resolve(ctx, cache, "refs/remotes/origin/"+base)
		if !ok && empty {
			// A new repository: its first branch is made, so that the
			// chat's branch has something to start from and a base to be
			// merged into.
			if start, err = m.seedLocked(ctx, fullName, cache, base); err != nil {
				return Checkout{}, err
			}
			ok = true
		}
		if !ok {
			return Checkout{}, fmt.Errorf("the base branch %s is not on GitHub", base)
		}
		// No upstream: the branch is not on GitHub yet, and its first push
		// sets one.
		args = []string{"worktree", "add", "--quiet", "--no-track", "-b", branch, path, "origin/" + base}
		out.Start = start
	}
	if _, err := m.local(ctx, cache, args...); err != nil {
		return Checkout{}, err
	}
	return out, nil
}

// firstBranch is the base of a chat in a repository that has no branch at
// all and names no default one.
const firstBranch = "main"

// SeedMessage is the message of the first commit Socrates makes in a
// repository that has none.
const SeedMessage = "Initial commit"

// emptyRemote says whether GitHub has no branch of the repository at all, as
// of the cache's last fetch: a repository made on GitHub without a README.
func (m *Manager) emptyRemote(ctx context.Context, cache string) (bool, error) {
	out, err := m.local(ctx, cache, "for-each-ref", "--count=1", "--format=%(refname)", "refs/remotes/origin/")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// seedLocked makes the first branch of a repository that has none: an empty
// commit (SeedMessage), pushed to GitHub as base, which GitHub then takes for
// the repository's default branch. The push only makes the branch, never
// moves one: when somebody pushed meanwhile, their branch is fetched and used
// instead. It returns the base's commit.
func (m *Manager) seedLocked(ctx context.Context, fullName, cache, base string) (string, error) {
	tree, err := m.local(ctx, cache, "mktree")
	if err != nil {
		return "", err
	}
	args := append(m.identity(ctx, cache), "commit-tree", strings.TrimSpace(tree), "-m", SeedMessage)
	sha, err := m.local(ctx, cache, args...)
	if err != nil {
		return "", err
	}
	sha = strings.TrimSpace(sha)
	ref := "refs/heads/" + base
	if _, perr := m.remote(ctx, fullName, cache, "push", "--quiet", "--force-with-lease="+ref+":", "origin", sha+":"+ref); perr != nil {
		if err := m.fetchLocked(ctx, fullName, cache); err == nil {
			if theirs, ok := m.resolve(ctx, cache, "refs/remotes/origin/"+base); ok {
				return theirs, nil
			}
		}
		return "", fmt.Errorf("the repository is empty, and its first branch %s could not be made on GitHub: %w", base, perr)
	}
	if _, err := m.local(ctx, cache, "update-ref", "refs/remotes/origin/"+base, sha); err != nil {
		return "", err
	}
	return sha, nil
}

// identity is what goes before a commit Socrates makes itself: nothing when
// git has an identity in dir, Socrates' own when it has none.
func (m *Manager) identity(ctx context.Context, dir string) []string {
	if out, _ := m.local(ctx, dir, "config", "user.email"); strings.TrimSpace(out) != "" {
		return nil
	}
	return []string{"-c", "user.name=Socrates", "-c", "user.email=socrates@localhost"}
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

// OnGitHub says whether GitHub has the chat's branch, as of the cache's last
// look.
func (m *Manager) OnGitHub(ctx context.Context, c Chat) bool {
	defer m.lock(c.FullName)()
	cache, err := m.cacheOf(ctx, c)
	return err == nil && m.hasRef(ctx, cache, "refs/remotes/origin/"+c.Branch)
}

// ErrRejected is a push that would have thrown away a commit GitHub has and
// the chat does not: somebody else pushed to the chat's branch. Socrates
// never forces it; the agent fetches and rebases.
type ErrRejected struct{ Branch string }

func (e *ErrRejected) Error() string {
	return "the branch " + e.Branch + " on GitHub has commits this chat does not have: fetch and rebase (git fetch origin && git rebase origin/" +
		e.Branch + "), and it is pushed again"
}

// Push pushes the chat's branch when it has moved since Socrates last pushed
// it or made it (Chat.Lease), and says whether it did (pushLocked).
// Uncommitted changes are left alone: committing is the agent's work.
func (m *Manager) Push(ctx context.Context, c Chat) (bool, error) {
	defer m.lock(c.FullName)()
	cache, err := m.existingCache(ctx, c)
	if err != nil {
		return false, err
	}
	if err := m.configure(ctx, c.FullName, cache); err != nil {
		return false, err
	}
	return m.pushLocked(ctx, c, cache)
}

// pushLocked pushes the chat's branch when its tip is not the commit
// Socrates last pushed or made it from. A branch whose tip is that commit is
// not pushed even when GitHub no longer has it: a branch squash-merged and
// deleted on GitHub is not brought back unless the chat committed after it.
//
// The push is forced, with an explicit lease: the branch is the chat's own and
// a rebase onto its base rewrites it, but a commit on GitHub that the chat
// does not have is never overwritten (pushRef). That is an *ErrRejected, and
// is never retried with force. A chat made before the lease was kept
// (Lease "") is pushed when it has commits GitHub does not have.
func (m *Manager) pushLocked(ctx context.Context, c Chat, cache string) (bool, error) {
	ref := "refs/heads/" + c.Branch
	tip, ok := m.resolve(ctx, cache, ref)
	if !ok {
		return false, nil
	}
	lease := c.lease()
	if lease == tip {
		return false, nil
	}
	if lease == "" {
		if ahead, err := m.ahead(ctx, cache, c.Branch); err != nil || !ahead {
			return false, err
		}
	}
	pushed, err := m.pushRef(ctx, c.FullName, cache, c.Branch, tip, lease)
	if err != nil {
		return false, err
	}
	c.setLease(tip)
	return pushed, nil
}

// pushRef pushes tip to the branch on GitHub unless GitHub has it there
// already. What GitHub has is asked (ls-remote), not taken from the cache's
// last fetch, and it may only be overwritten when the chat has it: the commit
// Socrates last pushed or made the branch from (lease), one the branch's tip
// contains, or one the branch pointed at before (its reflog: the agent pushed
// it itself, then rewrote it). The push's lease is that commit, so a push to
// GitHub in between fails rather than being overwritten.
func (m *Manager) pushRef(ctx context.Context, fullName, cache, branch, tip, lease string) (bool, error) {
	ref := "refs/heads/" + branch
	out, err := m.remote(ctx, fullName, cache, "ls-remote", "origin", ref)
	if err != nil {
		return false, err
	}
	theirs := ""
	for _, line := range strings.Split(out, "\n") {
		if sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && name == ref {
			theirs = sha
		}
	}
	switch {
	case theirs == tip:
		// Pushed already, by the agent: the cache learns it.
		_, _ = m.local(ctx, cache, "update-ref", "refs/remotes/origin/"+branch, tip)
		return false, nil
	case theirs != "" && theirs != lease && !m.chatHas(ctx, cache, branch, theirs, tip):
		return false, &ErrRejected{Branch: branch}
	}
	if _, err := m.remote(ctx, fullName, cache, "push", "--quiet", "--force-with-lease="+ref+":"+theirs, "origin", ref+":"+ref); err != nil {
		var ge *gitError
		if errors.As(err, &ge) && (strings.Contains(ge.stderr, "stale info") || strings.Contains(ge.stderr, "[rejected]")) {
			return false, &ErrRejected{Branch: branch}
		}
		return false, err
	}
	return true, nil
}

// chatHas says whether the commit is one the chat's branch has: in its tip's
// history, or a tip it had before (the branch's reflog).
func (m *Manager) chatHas(ctx context.Context, cache, branch, sha, tip string) bool {
	if _, err := m.local(ctx, cache, "merge-base", "--is-ancestor", sha, tip); err == nil {
		return true
	}
	out, err := m.local(ctx, cache, "reflog", "show", "--format=%H", "refs/heads/"+branch, "--")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == sha {
			return true
		}
	}
	return false
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

// inProgress are the files in a worktree's git directory that say an
// operation is under way, with what it is.
var inProgress = []struct{ file, what string }{
	{"rebase-merge", "a rebase"}, {"rebase-apply", "a rebase"}, {"MERGE_HEAD", "a merge"},
	{"CHERRY_PICK_HEAD", "a cherry-pick"}, {"REVERT_HEAD", "a revert"}, {"BISECT_LOG", "a bisect"},
}

// offBranch says why the worktree is not simply on the chat's branch, ""
// when it is: an operation under way (a rebase, a merge ...), or HEAD
// detached or on another branch. Committing or removing such a worktree
// would lose or misplace what is in it.
func (m *Manager) offBranch(ctx context.Context, path, branch string) (string, error) {
	gitDir, err := m.local(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	for _, p := range inProgress {
		if _, err := os.Stat(filepath.Join(strings.TrimSpace(gitDir), p.file)); err == nil {
			return p.what + " is in progress in it", nil
		}
	}
	out, err := m.local(ctx, path, "symbolic-ref", "-q", "HEAD")
	head := strings.TrimSpace(out)
	switch {
	case err != nil || head == "":
		return "its HEAD is detached, not on its branch " + branch, nil
	case head != "refs/heads/"+branch:
		return "it is on " + strings.TrimPrefix(head, "refs/heads/") + ", not on its branch " + branch, nil
	}
	return "", nil
}

// Done is what filing a chat as done does to its worktree: the branch is
// pushed, and a worktree that is on its branch, clean and has everything on
// GitHub goes, with the local branch (GitHub has it). Otherwise the worktree
// stays, and kept says why, in words for the chat. mayRemove, when it is set,
// is asked under the repository's lock just before the worktree goes: a chat
// picked up again meanwhile keeps it, and kept is "".
func (m *Manager) Done(ctx context.Context, c Chat, mayRemove func() bool) (removed bool, kept string, err error) {
	defer m.lock(c.FullName)()
	if !isWorktree(c.Path) {
		return true, "", nil
	}
	cache, err := m.existingCache(ctx, c)
	if err != nil {
		return false, "its repository's cache cannot be found, so nothing was pushed", err
	}
	if err := m.configure(ctx, c.FullName, cache); err != nil {
		return false, "", err
	}
	if _, err := m.pushLocked(ctx, c, cache); err != nil {
		var rejected *ErrRejected
		if errors.As(err, &rejected) {
			return false, rejected.Error(), nil
		}
		return false, "its branch could not be pushed to GitHub", err
	}
	if why, err := m.offBranch(ctx, c.Path, c.Branch); err != nil {
		return false, "", err
	} else if why != "" {
		return false, why, nil
	}
	if d, err := m.dirty(ctx, c.Path); err != nil {
		return false, "", err
	} else if d {
		return false, "it has changes that are not committed", nil
	}
	// Everything is on GitHub when the tip is what Socrates last pushed
	// (GitHub may have merged and deleted the branch since), or GitHub's
	// branches have it.
	if tip, _ := m.resolve(ctx, cache, "refs/heads/"+c.Branch); tip == "" || tip != c.lease() {
		if a, err := m.ahead(ctx, cache, c.Branch); err != nil {
			return false, "", err
		} else if a {
			return false, "its branch has commits that are not on GitHub", nil
		}
	}
	if mayRemove != nil && !mayRemove() {
		return false, "", nil
	}
	if err := m.removeLocked(ctx, cache, c.Path, c.Branch); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// WIPMessage is the commit a deleted chat's uncommitted changes are kept in.
func WIPMessage(title string) string {
	return fmt.Sprintf("WIP: uncommitted changes of chat %q", title)
}

// RescueBranch is the branch a deleted chat's worktree is kept on when it
// was not simply on the chat's branch (a rebase under way, HEAD detached), or
// when GitHub's branch has moved on without it.
func RescueBranch(chatID string) string { return "socrates/rescue/" + id8(chatID) }

// ErrLeftOut is a deleted chat's worktree that stays because untracked files
// in it were not committed: they look like secrets, or are too big.
type ErrLeftOut struct {
	Path  string
	Files []string
}

func (e *ErrLeftOut) Error() string {
	return fmt.Sprintf("the worktree %s stays: %d untracked file(s) were not committed, as they look like secrets or are over %d MB: %s",
		e.Path, len(e.Files), wipMaxSize>>20, strings.Join(e.Files, ", "))
}

// Delete is what deleting a chat does to its worktree: uncommitted changes
// are committed to the branch (WIPMessage), the branch is pushed, and the
// worktree and the local branch go; GitHub keeps the branch. A worktree that
// is not simply on its branch (offBranch) is kept on RescueBranch instead,
// and so is a branch GitHub has moved on without. Nothing is lost silently:
// when the commit or the push fails, the worktree and the branch stay where
// they are and the error says why, and untracked files that look like
// secrets or are big are not committed, and keep the worktree (*ErrLeftOut).
func (m *Manager) Delete(ctx context.Context, c Chat, title string) error {
	defer m.lock(c.FullName)()
	if !isWorktree(c.Path) {
		return m.deleteBranchLocked(ctx, c)
	}
	cache, err := m.existingCache(ctx, c)
	if err != nil {
		return err
	}
	if err := m.configure(ctx, c.FullName, cache); err != nil {
		return err
	}
	why, err := m.offBranch(ctx, c.Path, c.Branch)
	if err != nil {
		return err
	}
	var left []string
	rescue := RescueBranch(c.ID)
	if why != "" {
		// The state as it is goes to a branch of its own: the chat's
		// branch is pushed as it is.
		sha, l, err := m.snapshot(ctx, c.Path, WIPMessage(title)+"\n\nThe worktree was kept as it was: "+why+".")
		if err != nil {
			return err
		}
		left = l
		if err := m.pushRescue(ctx, c, cache, rescue, sha); err != nil {
			return err
		}
		rescue += "-branch"
	} else {
		d, err := m.dirty(ctx, c.Path)
		if err != nil {
			return err
		}
		if d {
			old, _ := m.resolve(ctx, cache, "refs/heads/"+c.Branch)
			sha, l, err := m.snapshot(ctx, c.Path, WIPMessage(title))
			if err != nil {
				return err
			}
			left = l
			if sha != old {
				if _, err := m.local(ctx, c.Path, "update-ref", "-m", "socrates: WIP", "refs/heads/"+c.Branch, sha, old); err != nil {
					return err
				}
			}
		}
	}
	if _, err := m.pushLocked(ctx, c, cache); err != nil {
		var rejected *ErrRejected
		if !errors.As(err, &rejected) {
			return err
		}
		tip, _ := m.resolve(ctx, cache, "refs/heads/"+c.Branch)
		if err := m.pushRescue(ctx, c, cache, rescue, tip); err != nil {
			return err
		}
	}
	if len(left) > 0 {
		return &ErrLeftOut{Path: c.Path, Files: left}
	}
	if err := m.removeLocked(ctx, cache, c.Path, c.Branch); err != nil {
		return err
	}
	for _, b := range []string{RescueBranch(c.ID), RescueBranch(c.ID) + "-branch"} {
		if m.hasRef(ctx, cache, "refs/heads/"+b) {
			_, _ = m.local(ctx, cache, "branch", "--quiet", "-D", b)
		}
	}
	return nil
}

// deleteBranchLocked is Delete for a chat whose worktree is gone already:
// its local branch, if the cache still has it, is pushed and goes.
func (m *Manager) deleteBranchLocked(ctx context.Context, c Chat) error {
	cache, err := m.existingCache(ctx, c)
	if err != nil {
		// No cache, no local branch: nothing here is lost.
		return nil
	}
	if err := m.configure(ctx, c.FullName, cache); err != nil {
		return err
	}
	if _, err := m.pushLocked(ctx, c, cache); err != nil {
		var rejected *ErrRejected
		if !errors.As(err, &rejected) {
			return err
		}
		tip, _ := m.resolve(ctx, cache, "refs/heads/"+c.Branch)
		if err := m.pushRescue(ctx, c, cache, RescueBranch(c.ID), tip); err != nil {
			return err
		}
	}
	_, _ = m.local(ctx, cache, "worktree", "prune")
	if m.hasRef(ctx, cache, "refs/heads/"+c.Branch) {
		if other, _ := m.checkedOut(ctx, cache, c.Branch); other == "" {
			_, err := m.local(ctx, cache, "branch", "--quiet", "-D", c.Branch)
			return err
		}
	}
	return nil
}

// pushRescue keeps sha on GitHub as the branch rescue.
func (m *Manager) pushRescue(ctx context.Context, c Chat, cache, rescue, sha string) error {
	if sha == "" {
		return nil
	}
	if _, err := m.local(ctx, cache, "update-ref", "-m", "socrates: rescue", "refs/heads/"+rescue, sha); err != nil {
		return err
	}
	_, err := m.pushRef(ctx, c.FullName, cache, rescue, sha, "")
	return err
}

// wipMaxSize is the largest untracked file a WIP commit takes.
const wipMaxSize = 10 << 20

// secretNames are the untracked files a WIP commit never takes: they are
// what a secret is usually kept in, and a WIP commit goes to GitHub.
var secretNames = []string{".env", ".env.*", "*.pem", "*.key", "id_rsa*", "id_ed25519*", "*.p12"}

// leaveOut says whether an untracked file stays out of a WIP commit.
func leaveOut(worktree, file string) bool {
	base := filepath.Base(strings.TrimSuffix(file, "/"))
	for _, pattern := range secretNames {
		if ok, _ := filepath.Match(pattern, base); ok {
			return true
		}
	}
	fi, err := os.Lstat(filepath.Join(worktree, file))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > wipMaxSize
}

// snapshot commits everything in the worktree that is worth keeping and
// returns the commit, made on HEAD without moving it or any branch: tracked
// changes, and untracked files except those that look like secrets or are
// over wipMaxSize, which it returns (they are listed in the commit's
// message). A worktree with nothing to commit is its HEAD. Hooks are not run
// - a WIP commit is a rescue, not a contribution - and a machine without a
// git identity commits as Socrates.
func (m *Manager) snapshot(ctx context.Context, path, message string) (string, []string, error) {
	head, ok := m.resolve(ctx, path, "HEAD")
	if !ok {
		return "", nil, fmt.Errorf("%s has no HEAD commit", path)
	}
	if _, err := m.local(ctx, path, "add", "--update"); err != nil {
		return "", nil, err
	}
	out, err := m.local(ctx, path, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return "", nil, err
	}
	var take, left []string
	for _, file := range strings.Split(out, "\x00") {
		switch {
		case file == "":
		case leaveOut(path, file):
			left = append(left, file)
		default:
			take = append(take, file)
		}
	}
	if len(take) > 0 {
		if _, err := run(ctx, gitEnv("", ""), path, strings.Join(take, "\x00"),
			"--literal-pathspecs", "add", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return "", nil, err
		}
	}
	tree, err := m.local(ctx, path, "write-tree")
	if err != nil {
		return "", nil, err
	}
	tree = strings.TrimSpace(tree)
	if headTree, _ := m.resolveTree(ctx, path, head); headTree == tree {
		return head, left, nil
	}
	if len(left) > 0 {
		message += "\n\nLeft out (they look like secrets or are over 10 MB), still in " + path + ":\n- " + strings.Join(left, "\n- ")
	}
	args := append(m.identity(ctx, path), "commit-tree", tree, "-p", head, "-m", message)
	sha, err := m.local(ctx, path, args...)
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(sha), left, nil
}

// resolveTree is the tree of a commit.
func (m *Manager) resolveTree(ctx context.Context, dir, commit string) (string, bool) {
	out, err := m.local(ctx, dir, "rev-parse", "--verify", "--quiet", commit+"^{tree}")
	return strings.TrimSpace(out), err == nil
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
