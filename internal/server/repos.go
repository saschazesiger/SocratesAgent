package server

// Chats in GitHub repositories (docs/design/REPOS.md): a chat made with a
// repository gets a worktree of its own on a branch of its own
// (internal/repos), its setup before the agent's first turn, its branch pushed
// at the end of every turn, and its worktree taken away when it is done or
// deleted, its work kept on GitHub.
//
// GitHub itself - the App, its installations, its tokens and its pull
// requests - is internal/github's. This file reaches it through repoGitHub,
// which the server is given once GitHub is connected (Server.gh): nil is a
// Socrates without GitHub, which makes chats without a repository only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/saschazesiger/SocratesAgent/internal/accounts"
	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/repos"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// repoGitHub is what the chats in repositories need of GitHub: whether it is
// connected, whether the App reaches a repository, an installation token for
// one repository, a pull request, and a repository's clone URL. The service
// of internal/github has all of it, with its own Repo and PullRequest types,
// which a small adapter turns into repos.Remote and repos.PullRequest.
type repoGitHub interface {
	Connected() bool
	Reaches(ctx context.Context, fullName string) (repos.Remote, bool, error)
	RepoToken(ctx context.Context, fullName string) (string, error)
	OpenPullRequest(ctx context.Context, fullName, head, base, title, body string, draft bool) (repos.PullRequest, error)
	CloneURL(fullName string) string
}

// UseGitHub connects the chats in repositories to GitHub, and setup names
// a repository's setup script from Admin (settings.github.repos[…].setup), ""
// for none. Called once, before the server serves.
func (s *Server) UseGitHub(gh repoGitHub, setup func(fullName string) string) {
	s.ghState.Store(&githubState{gh: gh, setup: setup})
}

// githubState is GitHub as UseGitHub last set it.
type githubState struct {
	gh    repoGitHub
	setup func(fullName string) string
}

// github is GitHub, nil while it is not connected.
func (s *Server) github() repoGitHub {
	if st := s.ghState.Load(); st != nil {
		return st.gh
	}
	return nil
}

// setupScript is Admin's setup script of a repository, "" for none.
func (s *Server) setupScript(fullName string) string {
	if st := s.ghState.Load(); st != nil && st.setup != nil {
		return st.setup(fullName)
	}
	return ""
}

// newRepoManager is the repositories of this server, under the workspace
// root as the settings have it at every call.
func (s *Server) newRepoManager() *repos.Manager {
	return &repos.Manager{
		Root: func() string { return s.Settings().Workspace.Root },
		URL:  s.repoURL,
		Token: func(ctx context.Context, fullName string) (string, error) {
			gh := s.github()
			if gh == nil {
				return "", errors.New("GitHub is not connected")
			}
			return gh.RepoToken(ctx, fullName)
		},
	}
}

// repoURL is a repository's clone URL: GitHub's own, or the base the
// environment names (SOCRATES_GITHUB_GIT, as internal/github reads it) for a
// server without GitHub, whose repositories nobody can make a chat in anyway.
func (s *Server) repoURL(fullName string) string {
	if gh := s.github(); gh != nil {
		return gh.CloneURL(fullName)
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("SOCRATES_GITHUB_GIT")), "/")
	if base == "" {
		base = "https://github.com"
	}
	return base + "/" + fullName + ".git"
}

// githubReady is GitHub when chats can be made in repositories, and why not
// otherwise.
func (s *Server) githubReady() (repoGitHub, error) {
	gh := s.github()
	if gh == nil || !gh.Connected() {
		return nil, errors.New("GitHub is not connected to Socrates: connect it in Admin → GitHub, or start the chat without a repository")
	}
	return gh, nil
}

// folderChatsAllowed is whether POST /api/chats still takes a preset or a
// typed folder (SOCRATES_FOLDER_CHATS=1): for the test suites, whose chats
// work in folders they made, and for a server that has to make one by hand.
// Everyone else picks a repository or none (REPOS.md §4.2).
func folderChatsAllowed() bool { return os.Getenv("SOCRATES_FOLDER_CHATS") == "1" }

// branchSlugMax is how long the words of a default branch name may be.
const branchSlugMax = 40

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// defaultBranch is the branch a chat gets when it names none: socrates/ and
// the first words of its first message, or of its id when it has none.
func defaultBranch(message, id string) string {
	var slug string
	for _, word := range strings.Fields(strings.ToLower(message)) {
		word = strings.Trim(notSlug.ReplaceAllString(word, "-"), "-")
		if word == "" {
			continue
		}
		next := word
		if slug != "" {
			next = slug + "-" + word
		}
		if len(next) > branchSlugMax {
			if slug == "" {
				slug = strings.Trim(word[:branchSlugMax], "-")
			}
			break
		}
		slug = next
	}
	if slug == "" {
		slug = id
		if len(slug) > 8 {
			slug = slug[:8]
		}
	}
	return "socrates/" + slug
}

// repoChat is a new chat's repository as POST /api/chats has it.
type repoChat struct {
	checkout repos.Checkout
	repo     string
}

// repoFetchTimeout bounds the git work of a request that makes a cache or
// fetches it: a first clone of a large repository takes minutes.
const repoFetchTimeout = 15 * time.Minute

// detached is the request's context without its cancellation, for the git
// work that must not stop half way when the browser gives up waiting: a
// cache cut off in its first fetch is made from the start again next time.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), repoFetchTimeout)
}

// keyedLocks is one mutex per key, there while somebody holds or waits for
// it.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu    sync.Mutex
	users int
}

// lock takes the key's lock and returns its release.
func (k *keyedLocks) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.users++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		if l.users--; l.users == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// makeRepoWorktree checks a new chat's repository, base and branch and makes
// its worktree; the status is the request's answer when it fails. The
// repository is named as GitHub spells it (Reaches), whatever the request
// wrote, so that its cache and lock are one per repository.
func (s *Server) makeRepoWorktree(ctx context.Context, body *createChatBody, id string) (*repoChat, int, string) {
	repo := strings.TrimSpace(body.Repo)
	gh, err := s.githubReady()
	if err != nil {
		return nil, http.StatusConflict, err.Error()
	}
	if !repos.ValidName(repo) {
		return nil, http.StatusBadRequest, fmt.Sprintf("%q is not a repository: name it as owner/name", repo)
	}
	remote, ok, err := gh.Reaches(ctx, repo)
	switch {
	case err != nil:
		return nil, http.StatusBadGateway, "GitHub could not be asked about " + repo + ": " + err.Error()
	case !ok:
		return nil, http.StatusBadRequest, "Socrates' GitHub App does not reach " + repo + ": choose it on the App's installation page on GitHub"
	}
	if repos.ValidName(remote.FullName) {
		repo = remote.FullName
	}
	gitCtx, cancel := detached(ctx)
	defer cancel()
	base := strings.TrimSpace(body.Base)
	if base == "" {
		base = remote.DefaultBranch
	}
	branch := strings.TrimSpace(body.Branch)
	generated := branch == ""
	if generated {
		text := ""
		if body.Message != nil {
			text = body.Message.Text
		}
		branch = defaultBranch(text, id)
	}
	if err := repos.CheckBranch(gitCtx, branch); err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}
	if base != "" {
		if err := repos.CheckBranch(gitCtx, base); err != nil {
			return nil, http.StatusBadRequest, "the base: " + err.Error()
		}
	}
	co, err := s.repos.Create(gitCtx, repo, id, branch, base, generated)
	var inUse *repos.ErrBranchInUse
	switch {
	case errors.As(err, &inUse):
		return nil, http.StatusConflict, s.branchInUse(inUse)
	case err != nil:
		return nil, http.StatusBadGateway, "the worktree could not be made: " + err.Error()
	}
	return &repoChat{checkout: co, repo: repo}, 0, ""
}

// discardWorktree removes the worktree made for a chat that was not stored.
func (s *Server) discardWorktree(made *repoChat) {
	if made == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.repos.Discard(ctx, made.repo, made.checkout.Path, made.checkout.Branch); err != nil {
		log.Printf("repos: the worktree %s of a chat that was not made stays: %v", made.checkout.Path, err)
	}
}

// branchInUse says which chat has a branch checked out.
func (s *Server) branchInUse(e *repos.ErrBranchInUse) string {
	if rows, err := s.store.ListSessions(); err == nil {
		for _, row := range rows {
			if row.IsChat() && filepath.Clean(row.Workdir) == filepath.Clean(e.Path) {
				return fmt.Sprintf("The branch %s is checked out in the chat “%s” already: continue there, or pick another branch.", e.Branch, row.Title)
			}
		}
	}
	return fmt.Sprintf("The branch %s is checked out in %s already: pick another branch.", e.Branch, e.Path)
}

// ---------------------------------------------------------------- options

// repoSetupState is options.setup: how the repository's setup of the chat's
// worktree went (REPOS.md §8).
type repoSetupState struct {
	// State is pending until the first launch runs it, then running, and
	// done, failed or none (the repository has no setup).
	State string `json:"state"`
	At    int64  `json:"at,omitempty"`
	// Error is why it failed.
	Error string `json:"error,omitempty"`
}

// The setup's states.
const (
	setupPending = "pending"
	setupRunning = "running"
	setupDone    = "done"
	setupFailed  = "failed"
	setupNone    = "none"
)

func readSetup(raw json.RawMessage) repoSetupState {
	var o struct {
		Setup repoSetupState `json:"setup"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o.Setup
}

// setSetup writes options.setup.
func (s *Server) setSetup(id string, st repoSetupState) {
	st.At = time.Now().UnixMilli()
	if _, err := s.store.UpdateChatOptions(id, func(raw json.RawMessage) json.RawMessage {
		return withOption(raw, "setup", st)
	}); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("chat %s: could not record its setup: %v", id, err)
	}
	s.events.poke()
}

// readLease is options.lease: the commit Socrates last pushed the chat's
// branch at, or made it from (repos.Chat.Lease).
func readLease(raw json.RawMessage) string {
	var o struct {
		Lease string `json:"lease"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o.Lease
}

// repoChatOf is a chat in a repository as internal/repos works on it, its
// lease read from the chat's options at every call and kept there. A chat
// that is gone (deleted, its worktree still being dealt with) keeps the
// lease it had.
func (s *Server) repoChatOf(row *store.Session) repos.Chat {
	id, lease := row.ID, readLease(row.Options)
	return repos.Chat{FullName: row.Repo, ID: row.ID, Path: row.Workdir, Branch: row.Branch, Base: row.Base,
		Lease: func() string {
			if now, err := s.store.GetSession(id); err == nil {
				lease = readLease(now.Options)
			}
			return lease
		},
		SetLease: func(sha string) {
			lease = sha
			if _, err := s.store.UpdateChatOptions(id, func(raw json.RawMessage) json.RawMessage {
				return withOption(raw, "lease", sha)
			}); err != nil && !errors.Is(err, store.ErrNotFound) {
				log.Printf("chat %s: could not record its branch's lease: %v", id, err)
			}
		},
	}
}

// readDoneAt is options.worktree_done: the status_at of the done-filing whose
// worktree was dealt with, so that the sweep, which parks a done chat every
// minute, deals with it once.
func readDoneAt(raw json.RawMessage) int64 {
	var o struct {
		At int64 `json:"worktree_done"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o.At
}

// ---------------------------------------------------------------- launch

// prepareRepo readies a chat in a repository for its host: the worktree is
// made again if it went (the chat was done, a restore without it), the
// setup runs before the first launch, and the chat tools learn the
// repository. It runs in the pump, not in a request, so a setup of minutes
// keeps nobody waiting: the first message waits in the outbox, and the chat
// says that it is being set up (options.setup).
func (p *chatPeer) prepareRepo(ctx context.Context, row *store.Session, tools *chattools.Endpoint) error {
	s := p.hub.srv
	chat := s.repoChatOf(row)
	co, err := s.repos.Ensure(ctx, chat)
	if err != nil {
		var inUse *repos.ErrBranchInUse
		if errors.As(err, &inUse) {
			return errors.New(s.branchInUse(inUse))
		}
		if errors.Is(err, repos.ErrNoCache) {
			p.notice(agent.NoticeRepository, "The worktree "+row.Workdir+" cannot be used: "+err.Error()+".")
		}
		return fmt.Errorf("the chat's worktree of %s: %w", row.Repo, err)
	}
	if co.Start != "" {
		chat.SetLease(co.Start)
	}
	setup := readSetup(row.Options)
	// A worktree made again is a fresh checkout: what the setup installed
	// went with the old one.
	if co.Added && setup.State != setupPending {
		setup = repoSetupState{State: setupPending}
		s.setSetup(row.ID, setup)
	}
	if setup.State == setupPending || setup.State == setupRunning {
		setup = p.runSetup(ctx, row)
	}
	repo := &chattools.Repo{FullName: row.Repo, Branch: row.Branch, Base: row.Base, Worktree: row.Workdir, URL: s.repoURL(row.Repo)}
	if setup.State == setupFailed {
		repo.SetupFailure, repo.SetupLog = setup.Error, setupLogPath(s.dataDir, row.ID)
	}
	tools.Repo = repo
	return nil
}

// setupLogPath is where a chat's setup writes its output.
func setupLogPath(dataDir, chatID string) string {
	return filepath.Join(agenthost.ChatDir(dataDir, chatID), "setup.log")
}

// runSetup runs the repository's setup in the chat's worktree (REPOS.md
// §4.3) and records how it went. A failure is a notice in the chat with the
// log's last lines; the agent starts either way, and its instructions say
// what failed.
func (p *chatPeer) runSetup(ctx context.Context, row *store.Session) repoSetupState {
	s := p.hub.srv
	s.setSetup(row.ID, repoSetupState{State: setupRunning})
	script := s.setupScript(row.Repo)
	logPath := setupLogPath(s.dataDir, row.ID)
	env := append(os.Environ(), "SOCRATES_REPO="+row.Repo, "SOCRATES_BRANCH="+row.Branch, "SOCRATES_BASE="+row.Base,
		"SOCRATES_WORKTREE="+row.Workdir)
	// The setup reaches GitHub as the agent does, through the credential
	// helper, so that `npm ci` against the owner's private packages works.
	if tools, err := s.chatToolsEndpoint(row.ID); err == nil {
		for k, v := range tools.Env() {
			env = append(env, k+"="+v)
		}
		env = append(env, setupGitEnv(s.repoURL(row.Repo), row.ID, s.dataDir)...)
	}
	ran, err := repos.RunSetup(ctx, repos.Setup{Script: script, Dir: row.Workdir, Log: logPath, Env: env})
	switch {
	case ctx.Err() != nil:
		// The server is stopping: the next start runs it again.
		st := repoSetupState{State: setupPending}
		s.setSetup(row.ID, st)
		return st
	case err != nil:
		st := repoSetupState{State: setupFailed, Error: err.Error()}
		s.setSetup(row.ID, st)
		text := "The repository's setup failed (" + err.Error() + "). The agent starts all the same; what the setup installs may be missing."
		if tail := repos.LogTail(logPath, 12); tail != "" {
			text += "\n\n```\n" + tail + "\n```"
		}
		p.notice(agent.NoticeRepository, text)
		return st
	case !ran:
		st := repoSetupState{State: setupNone}
		s.setSetup(row.ID, st)
		return st
	}
	st := repoSetupState{State: setupDone}
	s.setSetup(row.ID, st)
	return st
}

// setupGitEnv points the setup's git at the credential helper, through
// GIT_CONFIG_COUNT: the setup is the server's own child, whose environment no
// agent filters.
func setupGitEnv(cloneURL, chatID, dataDir string) []string {
	u, err := url.Parse(cloneURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	prefix := "credential." + u.Scheme + "://" + u.Host
	helper := "!'" + strings.ReplaceAll(exe, "'", `'\''`) + "' git-credential --data '" +
		strings.ReplaceAll(dataDir, "'", `'\''`) + "' --chat " + chatID
	return []string{"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=" + prefix + ".helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=" + prefix + ".helper", "GIT_CONFIG_VALUE_1=" + helper,
		"GIT_CONFIG_KEY_2=" + prefix + ".useHttpPath", "GIT_CONFIG_VALUE_2=true"}
}

// notice says a line in the chat: at once when its host is there and new
// enough, else when the next host connects (serve).
func (p *chatPeer) notice(code, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gone {
		return
	}
	if p.conn != nil && p.conn.Hello.Proto >= agenthost.NoticeProtocol {
		if p.conn.Send(agenthost.Command{Op: agenthost.OpNotice, Notice: code, Text: text}) == nil {
			return
		}
	}
	p.notices = append(p.notices, agenthost.Command{Op: agenthost.OpNotice, Notice: code, Text: text})
}

// sendNoticesLocked hands the notices that waited for a host to the one
// that is connected now.
func (p *chatPeer) sendNoticesLocked(conn *agenthost.Conn) {
	if len(p.notices) == 0 || conn.Hello.Proto < agenthost.NoticeProtocol {
		return
	}
	for _, cmd := range p.notices {
		_ = conn.Send(cmd)
	}
	p.notices = nil
}

// ------------------------------------------------------ the chat's lifetime

// pushAfterTurn pushes a chat's branch when a turn has ended with commits
// GitHub does not have (REPOS.md §4.4). A push that fails is a notice.
func (p *chatPeer) pushAfterTurn() {
	s := p.hub.srv
	row, err := s.store.GetSession(p.id)
	if err != nil || row.Repo == "" || row.Branch == "" {
		return
	}
	ctx, cancel := context.WithTimeout(p.hub.ctx, 5*time.Minute)
	defer cancel()
	if _, err := s.repos.Push(ctx, s.repoChatOf(row)); err != nil && ctx.Err() == nil {
		log.Printf("chat %s: the push of %s failed: %v", p.id, row.Branch, err)
		p.notice(agent.NoticeRepository, pushFailure(row.Branch, err))
	}
}

// pushFailure says in the chat why its branch was not pushed.
func pushFailure(branch string, err error) string {
	var rejected *repos.ErrRejected
	switch {
	case errors.As(err, &rejected):
		return "The branch " + branch + " was not pushed: " + err.Error() + "."
	case errors.Is(err, repos.ErrNoCache):
		return "The branch " + branch + " was not pushed: " + err.Error() + ". Nothing of this chat reaches GitHub until it is found."
	}
	return "The branch " + branch + " could not be pushed to GitHub: " + err.Error()
}

// repoDone deals with the worktree of a chat that is done (REPOS.md §4.4),
// once the host has parked: the turn-end push, then the worktree goes when it
// is clean and everything is on GitHub, and the chat says why it stays
// otherwise. A host still running is left alone; the sweep parks the chat
// again a minute later, and comes back here. Each done-filing is dealt with
// once (options.worktree_done).
func (s *Server) repoDone(ctx context.Context, row *store.Session) {
	if row.Repo == "" || row.Status != store.StatusDone || readDoneAt(row.Options) == row.StatusAt {
		return
	}
	if alive, _ := s.chats.launcher.Alive(row.ID); alive {
		return
	}
	// One at a time per chat: the sweep's next park may come while a slow
	// push of this one is still going.
	if _, busy := s.chats.repoBusy.LoadOrStore(row.ID, true); busy {
		return
	}
	defer s.chats.repoBusy.Delete(row.ID)
	// Still done and still without a host when the worktree goes: a message
	// that picked the chat up again meanwhile keeps it.
	stillDone := func() bool {
		now, err := s.store.GetSession(row.ID)
		alive, _ := s.chats.launcher.Alive(row.ID)
		return err == nil && now.Status == store.StatusDone && now.StatusAt == row.StatusAt && !alive
	}
	removed, kept, err := s.repos.Done(ctx, s.repoChatOf(row), stillDone)
	if ctx.Err() != nil || (!removed && kept == "" && err == nil) {
		return
	}
	if _, werr := s.store.UpdateChatOptions(row.ID, func(raw json.RawMessage) json.RawMessage {
		return withOption(raw, "worktree_done", row.StatusAt)
	}); werr != nil {
		log.Printf("chat %s: %v", row.ID, werr)
	}
	if removed {
		return
	}
	text := "The worktree " + row.Workdir + " stays"
	if kept != "" {
		text += ": " + kept
	}
	if err != nil {
		log.Printf("chat %s: done with its worktree: %v", row.ID, err)
		text += " (" + err.Error() + ")"
	}
	text += ". It is made again from the branch if the chat goes on."
	if p, perr := s.chats.peer(row.ID); perr == nil {
		p.notice(agent.NoticeRepository, text)
	}
}

// repoDelete is what deleting a chat in a repository does to its worktree:
// uncommitted changes are committed as WIP and pushed, then the worktree and
// the local branch go; GitHub keeps the branch. It runs in the background,
// after the chat's host is gone; a failure leaves the worktree where it is,
// so nothing is lost silently, is logged - the chat is gone - and is tried
// again every hour (repoDeletesFile). A worktree kept for untracked files
// that were not committed (repos.ErrLeftOut) is not: it stays for somebody to
// look at.
func (s *Server) repoDelete(row *store.Session) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	d := pendingDelete{ID: row.ID, Repo: row.Repo, Workdir: row.Workdir, Branch: row.Branch, Base: row.Base,
		Title: row.Title, Lease: readLease(row.Options)}
	if err := s.deleteWorktree(ctx, d); err != nil && ctx.Err() == nil {
		s.updateRepoDeletes(func(list []pendingDelete) []pendingDelete { return append(withoutDelete(list, d.ID), d) })
	}
}

// deleteWorktree runs one delete and logs how it failed; a delete that is
// not worth trying again is not an error.
func (s *Server) deleteWorktree(ctx context.Context, d pendingDelete) error {
	lease := d.Lease
	chat := repos.Chat{FullName: d.Repo, ID: d.ID, Path: d.Workdir, Branch: d.Branch, Base: d.Base,
		Lease: func() string { return lease }, SetLease: func(sha string) { lease = sha }}
	err := s.repos.Delete(ctx, chat, d.Title)
	var leftOut *repos.ErrLeftOut
	switch {
	case err == nil:
		return nil
	case errors.As(err, &leftOut):
		log.Printf("chat %s: %v", d.ID, err)
		return nil
	}
	log.Printf("chat %s: its worktree %s stays, and its delete is tried again within the hour: %v", d.ID, d.Workdir, err)
	return err
}

// pendingDelete is a deleted chat whose worktree could not be dealt with
// yet: what repos.Delete needs, as the chat's row had it.
type pendingDelete struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Workdir string `json:"workdir"`
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	Title   string `json:"title"`
	Lease   string `json:"lease,omitempty"`
}

// repoDeletesFile is the list of the deletes to try again.
func (s *Server) repoDeletesFile() string { return filepath.Join(s.dataDir, "repo-deletes.json") }

func withoutDelete(list []pendingDelete, id string) []pendingDelete {
	out := list[:0:0]
	for _, d := range list {
		if d.ID != id {
			out = append(out, d)
		}
	}
	return out
}

// updateRepoDeletes changes the list of the deletes to try again.
func (s *Server) updateRepoDeletes(change func([]pendingDelete) []pendingDelete) {
	s.chats.repoDeletes.Lock()
	defer s.chats.repoDeletes.Unlock()
	var list []pendingDelete
	if raw, err := os.ReadFile(s.repoDeletesFile()); err == nil {
		_ = json.Unmarshal(raw, &list)
	}
	list = change(list)
	if len(list) == 0 {
		if err := os.Remove(s.repoDeletesFile()); err != nil && !os.IsNotExist(err) {
			log.Printf("repos: %v", err)
		}
		return
	}
	raw, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.repoDeletesFile() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("repos: the deletes to try again could not be kept: %v", err)
		return
	}
	if err := os.Rename(tmp, s.repoDeletesFile()); err != nil {
		log.Printf("repos: the deletes to try again could not be kept: %v", err)
	}
}

// retryRepoDeletes tries every delete that failed before once more.
func (s *Server) retryRepoDeletes(ctx context.Context) {
	var list []pendingDelete
	s.updateRepoDeletes(func(l []pendingDelete) []pendingDelete { list = append(list, l...); return l })
	for _, d := range list {
		if ctx.Err() != nil {
			return
		}
		dctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := s.deleteWorktree(dctx, d)
		cancel()
		if err == nil {
			log.Printf("chat %s: its worktree %s is dealt with now", d.ID, d.Workdir)
			s.updateRepoDeletes(func(l []pendingDelete) []pendingDelete { return withoutDelete(l, d.ID) })
		}
	}
}

// repoCareEvery is how often the caches are looked after.
const repoCareEvery = time.Hour

// repoCare prunes and packs every cache once an hour, and tries the deletes
// that failed again.
func (h *chatHub) repoCare() {
	t := time.NewTicker(repoCareEvery)
	defer t.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-t.C:
			h.srv.repos.Maintain(h.ctx)
			h.srv.retryRepoDeletes(h.ctx)
		}
	}
}

// ------------------------------------------------------- the branch list

// handleRepoBranches lists a repository's branches for the new-chat sheet:
//
//	GET /api/github/repos/{owner}/{name}/branches  {"default", "branches": [{"name", "sha", "date"}]}
//
// It fetches the cache, making it the first time.
func (s *Server) handleRepoBranches(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("owner") + "/" + r.PathValue("name")
	gh, err := s.githubReady()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if !repos.ValidName(repo) {
		writeError(w, http.StatusBadRequest, "that is not a repository")
		return
	}
	remote, ok, err := gh.Reaches(r.Context(), repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, "GitHub could not be asked: "+err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "Socrates' GitHub App does not reach "+repo+".")
		return
	}
	if repos.ValidName(remote.FullName) {
		repo = remote.FullName
	}
	gitCtx, cancel := detached(r.Context())
	defer cancel()
	list, err := s.repos.Branches(gitCtx, repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, "the branches could not be fetched: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"default": remote.DefaultBranch, "branches": list})
}

// ----------------------------------------------------- the agent's tools

// credential answers `socrates git-credential` (chattools.Credential): the
// installation token of the chat's own repository, and nothing for any other
// remote (REPOS.md §5.2).
func (t *chatToolServer) credential(ctx context.Context, row *store.Session, args json.RawMessage) chattools.Response {
	s := t.srv
	if row.Repo == "" {
		return chattools.Response{Text: "This chat works in no GitHub repository.", Error: true}
	}
	var req agenthost.CredentialRequest
	_ = json.Unmarshal(args, &req)
	if !sameRemote(s.repoURL(row.Repo), req) {
		return chattools.Response{Text: "This chat's credentials are for " + row.Repo + " only.", Error: true}
	}
	gh, err := s.githubReady()
	if err != nil {
		return chattools.Response{Text: err.Error(), Error: true}
	}
	token, err := gh.RepoToken(ctx, row.Repo)
	if err != nil {
		return chattools.Response{Text: "GitHub gave no token for " + row.Repo + ": " + err.Error(), Error: true}
	}
	return chattools.Response{Text: token}
}

// sameRemote says whether git's credential request is for the repository
// at cloneURL: the same scheme and host, and the same path with or without
// .git (GitHub's names do not care about case).
func sameRemote(cloneURL string, req agenthost.CredentialRequest) bool {
	u, err := url.Parse(cloneURL)
	if err != nil || u.Host == "" {
		return false
	}
	norm := func(path string) string {
		return strings.ToLower(strings.TrimSuffix(strings.Trim(path, "/"), ".git"))
	}
	return req.Protocol == u.Scheme && strings.EqualFold(req.Host, u.Host) && req.Path != "" && norm(req.Path) == norm(u.Path)
}

// pullRequest is open_pull_request: the branch pushed, and a pull request
// from it into the chat's base opened, or the one it has already named.
func (t *chatToolServer) pullRequest(ctx context.Context, row *store.Session, args json.RawMessage) chattools.Response {
	s := t.srv
	if row.Repo == "" {
		return chattools.Response{Text: "This chat works in no GitHub repository: there is nothing to open a pull request in.", Error: true}
	}
	gh, err := s.githubReady()
	if err != nil {
		return chattools.Response{Text: err.Error(), Error: true}
	}
	var a struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
	}
	_ = json.Unmarshal(args, &a)
	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" {
		return chattools.Response{Text: chattools.PullRequest + ` takes {"title": "…", "body": "…", "draft": false}; the title is missing.`, Error: true}
	}
	chat := s.repoChatOf(row)
	if _, err := s.repos.Push(ctx, chat); err != nil {
		return chattools.Response{Text: pushFailure(row.Branch, err), Error: true}
	}
	if !s.repos.OnGitHub(ctx, chat) {
		return chattools.Response{Text: "The branch " + row.Branch + " has no commits beyond " + row.Base + " yet: commit your work first.", Error: true}
	}
	pr, err := gh.OpenPullRequest(ctx, row.Repo, row.Branch, row.Base, a.Title, a.Body, a.Draft)
	if err != nil {
		return chattools.Response{Text: "GitHub did not open the pull request: " + err.Error(), Error: true}
	}
	if _, err := s.store.UpdateChatOptions(row.ID, func(raw json.RawMessage) json.RawMessage {
		return withOption(raw, "pr", map[string]any{"number": pr.Number, "url": pr.URL})
	}); err != nil {
		log.Printf("chat %s: could not record its pull request: %v", row.ID, err)
	}
	s.events.poke()
	if pr.Existing {
		return chattools.Response{Text: fmt.Sprintf("The branch %s has a pull request already: #%d, %s. It shows the pushed commits.", row.Branch, pr.Number, pr.URL)}
	}
	return chattools.Response{Text: fmt.Sprintf("Opened pull request #%d from %s into %s: %s", pr.Number, row.Branch, row.Base, pr.URL)}
}

// ---------------------------------------------------- Claude Code's memory

// Claude Code keeps a project's auto memory under
// <config>/projects/<the project's path, every character but letters and
// digits a dash>/memory, and the project of a linked worktree is its
// repository's common git directory: every worktree of one cache shares the
// cache's memory, as every chat in one folder shared the folder's (checked
// against Claude Code 2.1.322, REPOS.md §7). Nothing has to be linked.
//
// What a repository's chats in a folder of their own remembered before is
// brought over once, the first time a Claude chat of the repository starts on
// an account: the memory of the folder of an older chat whose checkout's
// origin is the same GitHub repository.

// claudeProjectDir is the folder name Claude Code gives a project path.
func claudeProjectDir(path string) string {
	return strings.Map(func(r rune) rune {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return '-'
	}, path)
}

// seedClaudeMemory brings the repository's memory over from an older folder
// chat, once per account.
func (s *Server) seedClaudeMemory(row *store.Session, account string) {
	if row.Harness != config.HarnessClaude || row.Repo == "" || account == "" {
		return
	}
	cache, err := s.repos.CacheOf(context.Background(), s.repoChatOf(row))
	if err != nil {
		return
	}
	if real, err := filepath.EvalSymlinks(cache); err == nil {
		cache = real
	}
	projects := filepath.Join(accounts.Folder(s.dataDir, accounts.Claude, account), "projects")
	target := filepath.Join(projects, claudeProjectDir(cache), "memory")
	if _, err := os.Stat(target); err == nil {
		return
	}
	rows, err := s.store.ListSessions()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, other := range rows {
		if !other.IsChat() || other.Repo != "" || other.Workdir == "" {
			continue
		}
		folder := chatScope(other.Workdir)
		if seen[folder] {
			continue
		}
		seen[folder] = true
		if !strings.EqualFold(originRepo(folder), row.Repo) {
			continue
		}
		source := filepath.Join(projects, claudeProjectDir(folder), "memory")
		if fi, err := os.Stat(source); err != nil || !fi.IsDir() {
			continue
		}
		if err := copyTree(source, target); err != nil {
			log.Printf("chat %s: the memory of %s could not be brought over: %v", row.ID, folder, err)
			return
		}
		log.Printf("chat %s: brought Claude Code's memory of %s over to %s", row.ID, folder, row.Repo)
		return
	}
	// Nothing to bring: the folder marks that the look was taken.
	_ = os.MkdirAll(target, 0o700)
}

// githubRemote matches a GitHub remote URL's owner/name.
var githubRemote = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9._-]+/[A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// originRepo is the GitHub repository (owner/name) of a checkout's origin,
// "" when it has none.
func originRepo(dir string) string {
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if m := githubRemote.FindStringSubmatch(strings.TrimSpace(string(out))); m != nil {
		return m[1]
	}
	return ""
}

// copyTree copies the files under src to dst, which it makes.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, raw, 0o600)
	})
}
