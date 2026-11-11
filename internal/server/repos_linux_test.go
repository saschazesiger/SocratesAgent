//go:build linux

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/accounts"
	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/repos"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// fakeGitHub stands in for internal/github: a folder of bare repositories
// (dir/<owner>/<name>.git) that the App reaches, a token per repository, and
// the pull requests it was asked for. base is the clone URLs' base: file://
// for the most part, an HTTP server in front of the folder for the
// credential helper.
type fakeGitHub struct {
	dir  string
	base string

	mu  sync.Mutex
	prs []string
}

func (f *fakeGitHub) Connected() bool { return true }

// Reaches finds the repository whatever the case of its name, and names it
// as it is spelled, as GitHub does.
func (f *fakeGitHub) Reaches(_ context.Context, fullName string) (repos.Remote, bool, error) {
	owners, _ := os.ReadDir(f.dir)
	for _, owner := range owners {
		names, _ := os.ReadDir(filepath.Join(f.dir, owner.Name()))
		for _, name := range names {
			if canonical := owner.Name() + "/" + strings.TrimSuffix(name.Name(), ".git"); strings.EqualFold(canonical, fullName) {
				return repos.Remote{FullName: canonical, DefaultBranch: "main"}, true, nil
			}
		}
	}
	return repos.Remote{}, false, nil
}

func (f *fakeGitHub) RepoToken(_ context.Context, fullName string) (string, error) {
	return "tok-" + strings.ReplaceAll(fullName, "/", "-"), nil
}

func (f *fakeGitHub) OpenPullRequest(_ context.Context, fullName, head, base, title, body string, draft bool) (repos.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, seen := range f.prs {
		if seen == fullName+":"+head {
			return repos.PullRequest{Number: i + 1, URL: "https://github.example/" + fullName + "/pull/1", Existing: true}, nil
		}
	}
	f.prs = append(f.prs, fullName+":"+head)
	return repos.PullRequest{Number: len(f.prs), URL: "https://github.example/" + fullName + "/pull/1"}, nil
}

func (f *fakeGitHub) CloneURL(fullName string) string { return f.base + "/" + fullName + ".git" }

// testGit runs git for a test with an identity, failing it when git fails.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Tester", "-c", "user.email=tester@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newFakeGitHub makes octo/app (main, one commit) and octo/other.
func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	f := &fakeGitHub{dir: t.TempDir()}
	f.base = "file://" + f.dir
	work := t.TempDir()
	testGit(t, work, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, work, "add", ".")
	testGit(t, work, "commit", "--quiet", "-m", "first")
	for _, name := range []string{"octo/app", "octo/other"} {
		bare := filepath.Join(f.dir, name+".git")
		testGit(t, f.dir, "init", "--quiet", "--bare", "-b", "main", bare)
		// The HTTP fake lets an authenticated push in (receive-pack).
		testGit(t, bare, "config", "http.receivepack", "true")
		testGit(t, work, "push", "--quiet", bare, "main")
	}
	return f
}

// remoteBranch is the commit the fake GitHub has for a branch, "" for none.
func (f *fakeGitHub) remoteBranch(name, branch string) string {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = filepath.Join(f.dir, name+".git")
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// newRepoEnv is a chat server connected to the fake GitHub, with a setup
// script for octo/app.
func newRepoEnv(t *testing.T, f *fakeGitHub, setup string) *chatEnv {
	t.Helper()
	c := newChatEnv(t, time.Hour)
	next := c.srv.Settings()
	next.Workspace.Root = t.TempDir()
	if err := c.srv.saveSettings(next); err != nil {
		t.Fatal(err)
	}
	c.srv.UseGitHub(f, func(fullName string) string {
		if fullName == "octo/app" {
			return setup
		}
		return ""
	})
	return c
}

func (c *chatEnv) createIn(t *testing.T, clientID string, body map[string]any) (int, map[string]any) {
	t.Helper()
	body["client_id"], body["harness"] = clientID, "fake"
	return c.post(t, "/api/chats", body)
}

func sessionOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	s, ok := body["session"].(map[string]any)
	if !ok {
		t.Fatalf("no session in %v", body)
	}
	return s
}

// A chat in a repository gets its own worktree on its own branch, named from
// its first message, with the repository's setup run before its agent's first
// turn; a second chat on the same branch is refused naming the first; a
// preset or a typed folder is refused, and a chat without a repository
// still gets a folder of its own.
func TestAChatInARepositoryGetsItsOwnWorktree(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, `echo "set up $SOCRATES_REPO on $SOCRATES_BRANCH from $SOCRATES_BASE"; touch .setup-ran`)
	code, body := c.createIn(t, "repo-1", map[string]any{"repo": "octo/app", "title": "Login",
		"message": map[string]any{"client_id": "m1", "text": "Fix the login bug, please!"}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	session := sessionOf(t, body)
	id, workdir := session["id"].(string), session["workdir"].(string)
	if session["repo"] != "octo/app" || session["base"] != "main" || session["branch"] != "socrates/fix-the-login-bug-please" ||
		session["workdir_mode"] != store.WorkdirRepo {
		t.Fatalf("the chat: %v", session)
	}
	if workdir != filepath.Join(c.srv.Settings().Workspace.Root, "app-"+id[:8]) {
		t.Fatalf("the worktree is at %s", workdir)
	}
	if got := testGit(t, workdir, "branch", "--show-current"); got != "socrates/fix-the-login-bug-please" {
		t.Fatalf("the worktree is on %q", got)
	}
	c.waitJournal(t, id, 15*time.Second, "the first turn", turnsEnded(1))
	if _, err := os.Stat(filepath.Join(workdir, ".setup-ran")); err != nil {
		t.Fatal("the setup did not run in the worktree")
	}
	row, _ := c.srv.store.GetSession(id)
	if st := readSetup(row.Options); st.State != setupDone || st.At == 0 {
		t.Fatalf("the setup's state: %+v", st)
	}
	if log, _ := os.ReadFile(setupLogPath(c.data, id)); !strings.Contains(string(log), "set up octo/app on socrates/fix-the-login-bug-please from main") {
		t.Fatalf("the setup log: %s", log)
	}
	// The chat's git asks Socrates for its credentials: its global
	// configuration is the chat's own.
	if cfg, err := os.ReadFile(filepath.Join(agenthost.ChatDir(c.data, id), "gitconfig")); err != nil || !strings.Contains(string(cfg), "[include]") {
		t.Fatalf("the chat's git configuration: %s %v", cfg, err)
	}
	spec, err := agenthost.ReadSpec(c.data, id)
	if err != nil || spec.ChatTools.Repo == nil || spec.ChatTools.Repo.Branch != "socrates/fix-the-login-bug-please" ||
		spec.ChatTools.Repo.Worktree != workdir || spec.Workdir != workdir {
		t.Fatalf("the launch spec: %+v %v", spec.ChatTools.Repo, err)
	}

	code, body = c.createIn(t, "repo-2", map[string]any{"repo": "octo/app", "branch": "socrates/fix-the-login-bug-please"})
	if msg, _ := body["error"].(string); code != http.StatusConflict || !strings.Contains(msg, "“Login”") {
		t.Fatalf("a branch in use: %d %v", code, body)
	}
	code, body = c.createIn(t, "repo-3", map[string]any{"repo": "octo/app", "branch": "no..way"})
	if code != http.StatusBadRequest {
		t.Fatalf("a bad branch name: %d %v", code, body)
	}
	code, body = c.createIn(t, "repo-4", map[string]any{"repo": "octo/unknown"})
	if code != http.StatusBadRequest {
		t.Fatalf("a repository the App does not reach: %d %v", code, body)
	}
	// Without a message the branch is named after the chat's id.
	code, body = c.createIn(t, "repo-5", map[string]any{"repo": "octo/app"})
	if s := sessionOf(t, body); code != http.StatusCreated || s["branch"] != "socrates/"+s["id"].(string)[:8] {
		t.Fatalf("a chat without a message: %d %v", code, body)
	}

	t.Setenv("SOCRATES_FOLDER_CHATS", "")
	for _, mode := range []string{"custom", "preset"} {
		code, body = c.createIn(t, "folder-"+mode, map[string]any{"workdir_mode": mode, "workdir": c.data})
		if msg, _ := body["error"].(string); code != http.StatusBadRequest || !strings.Contains(msg, "gone") {
			t.Fatalf("a %s folder: %d %v", mode, code, body)
		}
	}
	code, body = c.createIn(t, "dynamic", map[string]any{"workdir_mode": "dynamic"})
	if code != http.StatusCreated || sessionOf(t, body)["repo"] != nil {
		t.Fatalf("a chat without a repository: %d %v", code, body)
	}
	// "Point to folder" is not for a chat in a repository.
	res, _ := c.do(t, c.client, "PUT", "/api/sessions/"+id+"/workdir", `{"workdir":"/tmp"}`)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("pointing a repository chat to a folder: %d", res.StatusCode)
	}

	// Without GitHub, a repository is refused with a sentence.
	c.srv.UseGitHub(nil, nil)
	code, body = c.createIn(t, "repo-6", map[string]any{"repo": "octo/app"})
	if msg, _ := body["error"].(string); code != http.StatusConflict || !strings.Contains(msg, "GitHub is not connected") {
		t.Fatalf("without GitHub: %d %v", code, body)
	}
}

// A setup that fails is a notice in the chat with its log, and the agent's
// instructions say so; the agent starts all the same.
func TestAFailedSetupIsSaid(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, "echo npm went wrong; exit 3")
	code, body := c.createIn(t, "repo-1", map[string]any{"repo": "octo/app",
		"message": map[string]any{"client_id": "m1", "text": "Go"}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := sessionOf(t, body)["id"].(string)
	events := c.waitJournal(t, id, 15*time.Second, "the first turn", turnsEnded(1))
	found := false
	for _, ev := range events {
		if ev.Type == agent.TypeNotice && ev.Code == agent.NoticeRepository && strings.Contains(ev.Text, "npm went wrong") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no notice of the failed setup in %+v", events)
	}
	spec, _ := agenthost.ReadSpec(c.data, id)
	if spec.ChatTools.Repo == nil || !strings.Contains(spec.ChatTools.Repo.SetupFailure, "exit status 3") ||
		!strings.Contains(spec.ChatTools.Guidance("", false), "setup failed") {
		t.Fatalf("the agent is not told: %+v", spec.ChatTools.Repo)
	}
}

// The branch is pushed at the end of a turn when it has new commits; done
// takes the clean worktree away and a new message brings it back; a delete
// keeps the uncommitted work on GitHub as a WIP commit.
func TestTheBranchOutlivesTheWorktree(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, "")
	code, body := c.createIn(t, "repo-1", map[string]any{"repo": "octo/app", "branch": "socrates/work", "title": "Work",
		"message": map[string]any{"client_id": "m1", "text": "Start"}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id, workdir := sessionOf(t, body)["id"].(string), sessionOf(t, body)["workdir"].(string)
	c.waitJournal(t, id, 15*time.Second, "the first turn", turnsEnded(1))
	if f.remoteBranch("octo/app", "socrates/work") != "" {
		t.Fatal("a branch without commits was pushed")
	}
	// The agent commits; the turn ends.
	if err := os.WriteFile(filepath.Join(workdir, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, workdir, "add", ".")
	testGit(t, workdir, "commit", "--quiet", "-m", "work")
	head := testGit(t, workdir, "rev-parse", "HEAD")
	c.send(t, id, "m2", "Go on")
	c.waitJournal(t, id, 15*time.Second, "the second turn", turnsEnded(2))
	waitForCond(t, 15*time.Second, "the push at the turn's end", func() bool { return f.remoteBranch("octo/app", "socrates/work") == head })

	// open_pull_request pushes and opens; a second call names the same one.
	ep, err := c.srv.chatToolsEndpoint(id)
	if err != nil {
		t.Fatal(err)
	}
	res := ep.Call(context.Background(), chattools.PullRequest, json.RawMessage(`{"title":"Work","body":"Does the work"}`))
	if res.Error || !strings.Contains(res.Text, "Opened pull request #1") {
		t.Fatalf("open_pull_request: %+v", res)
	}
	res = ep.Call(context.Background(), chattools.PullRequest, json.RawMessage(`{"title":"Work"}`))
	if res.Error || !strings.Contains(res.Text, "has a pull request already") {
		t.Fatalf("open_pull_request again: %+v", res)
	}
	row, _ := c.srv.store.GetSession(id)
	var opts struct {
		PR struct {
			Number int    `json:"number"`
			URL    string `json:"url"`
		} `json:"pr"`
	}
	_ = json.Unmarshal(row.Options, &opts)
	if opts.PR.Number != 1 || opts.PR.URL == "" {
		t.Fatalf("options.pr: %s", row.Options)
	}

	// Done: the host parks, the clean worktree goes with its local branch.
	res2, _ := c.do(t, c.client, "POST", "/api/sessions/"+id+"/file", `{"status":"done"}`)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("done: %d", res2.StatusCode)
	}
	waitForCond(t, 15*time.Second, "the host to park", func() bool { alive, _ := c.srv.chats.launcher.Alive(id); return !alive })
	waitForCond(t, 15*time.Second, "the worktree to go", func() bool {
		c.srv.sweep(context.Background(), time.Now())
		_, err := os.Stat(workdir)
		return os.IsNotExist(err)
	})
	// Picked up again: the worktree is back, from GitHub's branch.
	c.send(t, id, "m3", "One more thing")
	c.waitJournal(t, id, 15*time.Second, "the third turn", turnsEnded(3))
	if got := testGit(t, workdir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the worktree came back at %s, not %s", got, head)
	}

	// Deleted with uncommitted work: it is on GitHub as WIP.
	if err := os.WriteFile(filepath.Join(workdir, "half.txt"), []byte("half\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res3, _ := c.do(t, c.client, "DELETE", "/api/sessions/"+id, "")
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", res3.StatusCode)
	}
	waitForCond(t, 15*time.Second, "the WIP commit on GitHub", func() bool {
		tip := f.remoteBranch("octo/app", "socrates/work")
		return tip != "" && tip != head
	})
	waitForCond(t, 15*time.Second, "the worktree to go", func() bool { _, err := os.Stat(workdir); return os.IsNotExist(err) })
	bare := filepath.Join(f.dir, "octo/app.git")
	if msg := testGit(t, bare, "log", "-1", "--format=%s", "socrates/work"); msg != repos.WIPMessage("Work") {
		t.Fatalf("the last commit on GitHub: %q", msg)
	}
}

// The branch list of the new-chat sheet, newest first, with the default.
func TestTheBranchList(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, "")
	res, body := c.do(t, c.client, "GET", "/api/github/repos/octo/app/branches", "")
	list, _ := body["branches"].([]any)
	if res.StatusCode != http.StatusOK || body["default"] != "main" || len(list) != 1 || list[0].(map[string]any)["name"] != "main" ||
		list[0].(map[string]any)["sha"] == "" {
		t.Fatalf("branches: %d %v", res.StatusCode, body)
	}
	if res, _ := c.do(t, c.client, "GET", "/api/github/repos/octo/nope/branches", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a repository the App does not reach: %d", res.StatusCode)
	}
	if res, _ := c.do(t, c.server.Client(), "GET", "/api/github/repos/octo/app/branches", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
}

// The chats of a repository reach each other whatever their worktree, each
// named with its branch; a chat in a folder does not see them.
func TestTheChatsOfARepositoryAreItsPeers(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, "")
	newChat := func(clientID string, body map[string]any) string {
		code, answer := c.createIn(t, clientID, body)
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", clientID, code, answer)
		}
		id := sessionOf(t, answer)["id"].(string)
		c.send(t, id, "hello-"+id, "hello")
		c.waitJournal(t, id, 15*time.Second, "the first turn", turnsEnded(1))
		return id
	}
	a := newChat("a", map[string]any{"repo": "octo/app", "branch": "socrates/a", "title": "A"})
	b := newChat("b", map[string]any{"repo": "octo/app", "branch": "socrates/b", "title": "B"})
	other := newChat("o", map[string]any{"repo": "octo/other", "title": "Other"})
	folder := newChat("f", map[string]any{"workdir_mode": "custom", "workdir": c.data, "title": "Folder"})
	ep, _ := c.srv.chatToolsEndpoint(a)
	res := ep.Call(context.Background(), chattools.List, json.RawMessage(`{}`))
	if res.Error || !strings.Contains(res.Text, "id "+b) || !strings.Contains(res.Text, "on branch socrates/b") ||
		strings.Contains(res.Text, other) || strings.Contains(res.Text, folder) || !strings.Contains(res.Text, "(octo/app)") {
		t.Fatalf("list_chats: %+v", res)
	}
	row, _ := c.srv.store.GetSession(a)
	if note := c.srv.chats.tools.peersNote(row); !strings.Contains(note, "socrates/b") || strings.Contains(note, "working tree that you did not make") {
		t.Fatalf("the peers note: %s", note)
	}
	// A repository's board is keyed by the repository.
	if folder, err := boardFolder("github:octo/app"); err != nil || folder != "github:octo/app" {
		t.Fatalf("the repository's board: %q %v", folder, err)
	}
	if _, err := boardFolder("github:../x"); err == nil {
		t.Fatal("a board of no repository was taken")
	}
}

// The agent's git gets the chat's own repository's token from `socrates
// git-credential`, and a push through it reaches GitHub with the token; a
// remote of another repository gets nothing, and its push fails.
func TestTheCredentialHelperHandsOutTheRepositorysTokenOnly(t *testing.T) {
	f := newFakeGitHub(t)
	backend := filepath.Join(strings.TrimSpace(testGit(t, f.dir, "--exec-path")), "git-http-backend")
	var mu sync.Mutex
	var pushedWith []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack" {
			user, pass, ok := r.BasicAuth()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="fake GitHub"`)
				http.Error(w, "authenticate", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			pushedWith = append(pushedWith, r.URL.Path+" "+user+":"+pass)
			mu.Unlock()
			repo := strings.TrimPrefix(strings.SplitN(r.URL.Path, ".git", 2)[0], "/")
			if user != "x-access-token" || pass != "tok-"+strings.ReplaceAll(repo, "/", "-") {
				http.Error(w, "bad credentials", http.StatusForbidden)
				return
			}
		}
		h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + f.dir, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=x-access-token"}}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	f.base = srv.URL
	c := newRepoEnv(t, f, "")
	code, body := c.createIn(t, "repo-1", map[string]any{"repo": "octo/app", "branch": "socrates/creds"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id, workdir := sessionOf(t, body)["id"].(string), sessionOf(t, body)["workdir"].(string)
	waitForCond(t, 15*time.Second, "the host's launch spec", func() bool {
		spec, err := agenthost.ReadSpec(c.data, id)
		return err == nil && spec.ChatTools.Usable()
	})
	// The chat's git configuration as its host writes it, with this test
	// binary as `socrates` (TestMain runs git-credential).
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	text := agenthost.RepoGitConfig(nil, srv.URL+"/octo/app.git", os.Args[0], c.data, id)
	if err := os.WriteFile(cfg, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, workdir, "add", ".")
	testGit(t, workdir, "commit", "--quiet", "-m", "x")
	push := func(remote string) (string, error) {
		cmd := exec.Command("git", "push", remote, "HEAD:refs/heads/socrates/creds")
		cmd.Dir = workdir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+cfg, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := push("origin"); err != nil {
		t.Fatalf("the push through the helper: %v\n%s", err, out)
	}
	if f.remoteBranch("octo/app", "socrates/creds") != testGit(t, workdir, "rev-parse", "HEAD") {
		t.Fatal("GitHub does not have the commit")
	}
	out, err := push(srv.URL + "/octo/other.git")
	if err == nil {
		t.Fatalf("a push to another repository went through:\n%s", out)
	}
	if !strings.Contains(out, "credentials are for octo/app only") {
		t.Fatalf("the helper did not say why:\n%s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, seen := range pushedWith {
		if strings.Contains(seen, "octo/other") && strings.Contains(seen, "tok-") {
			t.Fatalf("another repository saw a token: %v", pushedWith)
		}
	}
	if len(pushedWith) == 0 || !strings.Contains(pushedWith[0], "x-access-token:tok-octo-app") {
		t.Fatalf("the pushes GitHub saw: %v", pushedWith)
	}
}

// Claude Code keys a worktree's memory by its repository's common git
// directory, the cache: what the chats of an older folder of the repository
// remembered is brought over to it once per account.
func TestClaudeMemoryIsBroughtOverOnce(t *testing.T) {
	if got := claudeProjectDir("/root/.cache/repos-ws-tmp/memtest/cache/o/r.git"); got != "-root--cache-repos-ws-tmp-memtest-cache-o-r-git" {
		t.Fatalf("Claude Code's name of a project: %s", got)
	}
	e := signedIn(t)
	srv := e.srv
	next := srv.Settings()
	next.Workspace.Root = t.TempDir()
	if err := srv.saveSettings(next); err != nil {
		t.Fatal(err)
	}
	legacy := t.TempDir()
	testGit(t, legacy, "init", "--quiet")
	testGit(t, legacy, "remote", "add", "origin", "git@github.com:Octo/App.git")
	if err := srv.store.CreateSession(&store.Session{ID: "old", Harness: "claude", Workdir: filepath.Join(legacy), WorkdirMode: store.WorkdirCustom,
		Kind: store.KindChat}); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(accounts.Folder(srv.dataDir, accounts.Claude, "a1"), "projects")
	source := filepath.Join(projects, claudeProjectDir(legacy), "memory")
	if err := os.MkdirAll(filepath.Join(source, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"MEMORY.md": "- [Push](push.md)\n", "notes/push.md": "push to main\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	row := &store.Session{ID: "new", Harness: "claude", Repo: "octo/app", Kind: store.KindChat, Workdir: srv.repos.WorktreeDir("octo/app", "new")}
	srv.seedClaudeMemory(row, "a1")
	target := filepath.Join(projects, claudeProjectDir(srv.repos.CacheDir("octo/app")), "memory")
	if got, err := os.ReadFile(filepath.Join(target, "notes", "push.md")); err != nil || string(got) != "push to main\n" {
		t.Fatalf("the memory brought over: %q %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(source, "MEMORY.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.seedClaudeMemory(row, "a1")
	if got, _ := os.ReadFile(filepath.Join(target, "MEMORY.md")); string(got) != "- [Push](push.md)\n" {
		t.Fatalf("the memory was brought over twice: %q", got)
	}
}

// A worktree made again (the chat was done, then goes on) runs the setup
// again: what it installed went with the old one.
func TestTheSetupRunsAgainInAWorktreeMadeAgain(t *testing.T) {
	f := newFakeGitHub(t)
	counter := filepath.Join(t.TempDir(), "setups")
	c := newRepoEnv(t, f, "echo ran >> "+counter)
	code, body := c.createIn(t, "repo-1", map[string]any{"repo": "octo/app", "branch": "socrates/again",
		"message": map[string]any{"client_id": "m1", "text": "Start"}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id, workdir := sessionOf(t, body)["id"].(string), sessionOf(t, body)["workdir"].(string)
	c.waitJournal(t, id, 15*time.Second, "the first turn", turnsEnded(1))
	res, _ := c.do(t, c.client, "POST", "/api/sessions/"+id+"/file", `{"status":"done"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("done: %d", res.StatusCode)
	}
	waitForCond(t, 15*time.Second, "the host to park", func() bool { alive, _ := c.srv.chats.launcher.Alive(id); return !alive })
	waitForCond(t, 15*time.Second, "the worktree to go", func() bool {
		c.srv.sweep(context.Background(), time.Now())
		_, err := os.Stat(workdir)
		return os.IsNotExist(err)
	})
	c.send(t, id, "m2", "Go on")
	c.waitJournal(t, id, 15*time.Second, "the second turn", turnsEnded(2))
	if raw, _ := os.ReadFile(counter); strings.Count(string(raw), "ran") != 2 {
		t.Fatalf("the setup ran %q", raw)
	}
}

// Somebody else's commit on the chat's branch is never overwritten: the turn
// end's push is refused, and the chat says to fetch and rebase.
func TestAPushOverSomebodysCommitIsANotice(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, "")
	code, body := c.createIn(t, "repo-1", map[string]any{"repo": "octo/app", "branch": "socrates/shared",
		"message": map[string]any{"client_id": "m1", "text": "Start"}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id, workdir := sessionOf(t, body)["id"].(string), sessionOf(t, body)["workdir"].(string)
	c.waitJournal(t, id, 15*time.Second, "the first turn", turnsEnded(1))
	other := t.TempDir()
	testGit(t, other, "clone", "--quiet", f.base+"/octo/app.git", ".")
	if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, other, "add", ".")
	testGit(t, other, "commit", "--quiet", "-m", "theirs")
	testGit(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/socrates/shared")
	theirs := f.remoteBranch("octo/app", "socrates/shared")
	// Another chat's fetch brings their commit into the cache.
	if res, _ := c.do(t, c.client, "GET", "/api/github/repos/octo/app/branches", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("branches: %d", res.StatusCode)
	}
	if err := os.WriteFile(filepath.Join(workdir, "mine.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, workdir, "add", ".")
	testGit(t, workdir, "commit", "--quiet", "-m", "mine")
	c.send(t, id, "m2", "Go on")
	c.waitJournal(t, id, 15*time.Second, "the notice", func(events []agent.Event) bool {
		for _, ev := range events {
			if ev.Type == agent.TypeNotice && ev.Code == agent.NoticeRepository && strings.Contains(ev.Text, "fetch and rebase") {
				return true
			}
		}
		return false
	})
	if f.remoteBranch("octo/app", "socrates/shared") != theirs {
		t.Fatal("their commit was overwritten")
	}
}

// A create keeps going when the browser gives up on it, a retry with the
// same client_id gets the chat the first one made, a generated branch name
// that is taken gets the chat's id8, and the repository is named as GitHub
// spells it.
func TestCreatingAChatInARepositoryIsRobust(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	made, status, msg := c.srv.makeRepoWorktree(ctx, &createChatBody{Repo: "octo/app", Branch: "socrates/cancelled"}, "cccccccc11112222")
	if status != 0 || made == nil {
		t.Fatalf("a create whose request went away: %d %s", status, msg)
	}

	var wg sync.WaitGroup
	ids := make([]string, 2)
	codes := make([]int, 2)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, body := c.post(t, "/api/chats", map[string]any{"client_id": "same", "harness": "fake", "repo": "OCTO/App",
				"message": map[string]any{"client_id": "m1", "text": "Fix it"}})
			codes[i] = code
			if s, ok := body["session"].(map[string]any); ok {
				ids[i], _ = s["id"].(string)
			}
		}(i)
	}
	wg.Wait()
	if ids[0] == "" || ids[0] != ids[1] || codes[0]+codes[1] != http.StatusCreated+http.StatusOK {
		t.Fatalf("a retried create: %v %v", codes, ids)
	}
	row, err := c.srv.store.GetSession(ids[0])
	if err != nil || row.Repo != "octo/app" || row.Branch != "socrates/fix-it" || readLease(row.Options) == "" {
		t.Fatalf("the chat: %+v %v", row, err)
	}
	code, body := c.createIn(t, "another", map[string]any{"repo": "octo/app", "message": map[string]any{"client_id": "m1", "text": "Fix it"}})
	if s := sessionOf(t, body); code != http.StatusCreated || s["branch"] != "socrates/fix-it-"+s["id"].(string)[:8] {
		t.Fatalf("a generated name that is taken: %d %v", code, body)
	}
}

// Admin's setup of a repository is found whatever the case of its name.
func TestTheSetupIsFoundWhateverTheCase(t *testing.T) {
	e := signedIn(t)
	next := e.srv.Settings()
	next.GitHub.Repos = map[string]config.RepoSettings{"Octo/App": {Setup: "npm ci"}}
	if err := e.srv.saveSettings(next); err != nil {
		t.Fatal(err)
	}
	e.srv.useGitHubApp()
	if got := e.srv.setupScript("octo/app"); got != "npm ci" {
		t.Fatalf("the setup of octo/app: %q", got)
	}
}

// A delete that fails keeps the worktree and is tried again by the hourly
// care until it goes through.
func TestAFailedDeleteIsTriedAgain(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, "")
	code, body := c.createIn(t, "repo-1", map[string]any{"repo": "octo/app", "branch": "socrates/retry", "title": "Retry",
		"message": map[string]any{"client_id": "m1", "text": "Start"}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id, workdir := sessionOf(t, body)["id"].(string), sessionOf(t, body)["workdir"].(string)
	c.waitJournal(t, id, 15*time.Second, "the first turn", turnsEnded(1))
	if err := os.WriteFile(filepath.Join(workdir, "half.txt"), []byte("half\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(f.dir, "octo", "app.git")
	if err := os.Rename(bare, bare+".away"); err != nil {
		t.Fatal(err)
	}
	if res, _ := c.do(t, c.client, "DELETE", "/api/sessions/"+id, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	waitForCond(t, 15*time.Second, "the delete to be kept for later", func() bool {
		raw, err := os.ReadFile(c.srv.repoDeletesFile())
		return err == nil && strings.Contains(string(raw), id)
	})
	if _, err := os.Stat(filepath.Join(workdir, "half.txt")); err != nil {
		t.Fatal("the worktree went without its work on GitHub")
	}
	if err := os.Rename(bare+".away", bare); err != nil {
		t.Fatal(err)
	}
	c.srv.retryRepoDeletes(context.Background())
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatal("the worktree is still there")
	}
	if _, err := os.Stat(c.srv.repoDeletesFile()); !os.IsNotExist(err) {
		t.Fatal("the delete is still kept for later")
	}
	if tip := f.remoteBranch("octo/app", "socrates/retry"); tip == "" || testGit(t, bare, "log", "-1", "--format=%s", tip) != repos.WIPMessage("Retry") {
		t.Fatal("the WIP commit is not on GitHub")
	}
}
