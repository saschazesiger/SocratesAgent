package browserstack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/browserstack/driver"
)

// fakeBrowserStack is the hub and the REST API, as far as a session that
// starts and stops needs them, on one test server.
type fakeBrowserStack struct {
	mu      sync.Mutex
	created []map[string]any
	quit    []string
	auth    []string
	refused string // a user it refuses
	plans   map[string][2]int
}

func (f *fakeBrowserStack) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		f.mu.Lock()
		f.auth = append(f.auth, user)
		refused := user == f.refused
		plan := f.plans[user]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if refused {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
			return
		}
		switch {
		case r.URL.Path == "/plan.json":
			_ = json.NewEncoder(w).Encode(map[string]int{"parallel_sessions_running": plan[0], "parallel_sessions_max_allowed": max(1, plan[1])})
		case r.Method == http.MethodPost && r.URL.Path == "/wd/hub/session":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.created = append(f.created, body)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"value":{"sessionId":"s-1","capabilities":{}}}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/wd/hub/session/"):
			f.mu.Lock()
			f.quit = append(f.quit, strings.TrimPrefix(r.URL.Path, "/wd/hub/session/"))
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"value":null}`))
		case strings.HasPrefix(r.URL.Path, "/wd/hub/session/"):
			_, _ = w.Write([]byte(`{"value":null}`))
		case strings.HasPrefix(r.URL.Path, "/sessions/"):
			_, _ = w.Write([]byte(`{"automation_session":{"video_url":"https://video/1","public_url":"https://pub/?token=abc"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := [3]string{driver.HubURL, driver.SessionAPI, driver.PlanAPI}
	driver.HubURL, driver.SessionAPI, driver.PlanAPI = srv.URL+"/wd/hub", srv.URL+"/sessions", srv.URL+"/plan.json"
	t.Cleanup(func() { driver.HubURL, driver.SessionAPI, driver.PlanAPI = old[0], old[1], old[2] })
}

func newServer(t *testing.T, accounts ...mcpserver.Account) *Server {
	t.Helper()
	s := New(Deps{DataDir: t.TempDir(), Accounts: func() []mcpserver.Account { return accounts }})
	// No test asks BrowserStack which accounts are busy.
	s.pool.planOf = func(context.Context, driver.Account) (*driver.Plan, error) { return nil, errors.New("offline") }
	return s
}

func acct(id, user string) mcpserver.Account {
	return mcpserver.Account{ID: id, Values: map[string]string{"user": user, "key": "k-" + user}}
}

func text(r mcpserver.Result) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestTheAccountsAreLeasedOneHolderEach(t *testing.T) {
	s := newServer(t, acct("a", "ann"), acct("b", "bob"), mcpserver.Account{ID: "c", Values: map[string]string{"user": "cid"},
		Check: &mcpserver.Check{State: mcpserver.AccountInvalid}})
	first, why := s.Lease("cell-1")
	second, _ := s.Lease("chat-2")
	again, _ := s.Lease("cell-1")
	if first.Account == "" || why != "" || second.Account == "" || first.Account == second.Account || again != first {
		t.Fatalf("leases: %+v %+v %+v %q", first, second, again, why)
	}
	// The account BrowserStack refused is never handed out.
	if third, why := s.Lease("chat-3"); third.Account != "" || !strings.Contains(why, "every BrowserStack account is in use") {
		t.Fatalf("a third lease: %+v %q", third, why)
	}
	s.Release("cell-1")
	if third, _ := s.Lease("chat-3"); third.Account != first.Account {
		t.Fatalf("the account given back: %+v", third)
	}
	if none, why := newServer(t).Lease("x"); none.Account != "" || !strings.Contains(why, "Admin") {
		t.Fatalf("without accounts: %q", why)
	}
}

func TestAccountsAreCheckedWithTheirPlan(t *testing.T) {
	f := &fakeBrowserStack{refused: "eve", plans: map[string][2]int{"ann": {1, 2}}}
	f.start(t)
	s := newServer(t)
	if c := s.CheckAccount(context.Background(), map[string]string{"user": "ann", "key": "k"}); c.State != mcpserver.AccountValid || !strings.Contains(c.Detail, "1 of 2") {
		t.Fatalf("ann: %+v", c)
	}
	if c := s.CheckAccount(context.Background(), map[string]string{"user": "eve", "key": "k"}); c.State != mcpserver.AccountInvalid {
		t.Fatalf("eve: %+v", c)
	}
}

// A chat starts a device on a free account with the repository's site,
// calls go to it, and stop closes it, writes its links and gives the
// account back.
func TestAChatStartsUsesAndStopsADevice(t *testing.T) {
	f := &fakeBrowserStack{}
	f.start(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := `{"base_url": "https://beta.example.com/", "locales": ["en", "de"], "project": "Example QA",
		"secrets": {"command": "printf 'QA_PASSWORD=hunter2\\nOTHER=x\\n'", "names": ["QA_PASSWORD", "QA_TOTP?"]}}`
	if err := os.MkdirAll(filepath.Join(repo, ".socrates", "mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, RepoFile), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newServer(t, acct("a", "ann"))
	sess := mcpserver.Session{ID: "chat-1", Title: "Try the site", Workdir: filepath.Join(repo, "sub")}
	ctx := context.Background()
	if r := s.Call(ctx, sess, "tap", json.RawMessage(`{"text":"Go"}`)); !r.IsError || !strings.Contains(text(r), "call start first") {
		t.Fatalf("a call before start: %v", text(r))
	}
	if r := s.Call(ctx, sess, "start", json.RawMessage(`{}`)); !r.IsError || !strings.Contains(text(r), "needs a device") {
		t.Fatalf("start without a device: %v", text(r))
	}
	if r := s.Call(ctx, sess, "start", json.RawMessage(`{"device":"win-chrome","locale":"fr"}`)); !r.IsError || !strings.Contains(text(r), "locale fr") {
		t.Fatalf("a locale the site has not: %v", text(r))
	}
	r := s.Call(ctx, sess, "start", json.RawMessage(`{"device":"win-chrome"}`))
	if r.IsError || !strings.Contains(text(r), "device ready: Windows · Chrome (desktop), locale en, consent accept, BrowserStack account ann, session s-1") {
		t.Fatalf("start: %v", text(r))
	}
	caps := f.created[0]["capabilities"].(map[string]any)["alwaysMatch"].(map[string]any)
	opts := caps["bstack:options"].(map[string]any)
	if opts["projectName"] != "Example QA" || opts["sessionName"] != "Try the site" || caps["browserName"] != "chrome" {
		t.Fatalf("capabilities: %v", caps)
	}
	if uses := s.Uses(); len(uses) != 1 || uses[0].Account != "a" || uses[0].Session != "chat-1" {
		t.Fatalf("uses: %+v", uses)
	}
	if other, why := s.Lease("chat-2"); other.Account != "" || why == "" {
		t.Fatal("the account of a running device was leased again")
	}
	if r := s.Call(ctx, sess, "stop", nil); !strings.Contains(text(r), "the BrowserStack session s-1 is closed") {
		t.Fatalf("stop: %v", text(r))
	}
	if len(f.quit) != 1 || f.quit[0] != "s-1" {
		t.Fatalf("quit: %v", f.quit)
	}
	links, err := os.ReadFile(filepath.Join(s.chatEvidence("chat-1"), "links.json"))
	if err != nil || !strings.Contains(string(links), "https://video/1") || strings.Contains(string(links), "abc") {
		t.Fatalf("links: %s %v", links, err)
	}
	if again, _ := s.Lease("chat-2"); again.Account != "a" {
		t.Fatal("the account was not given back")
	}
	s.Release("chat-2")
	// The chat is deleted: what it recorded goes.
	s.End(ctx, "chat-1")
	if _, err := os.Stat(s.chatEvidence("chat-1")); !os.IsNotExist(err) {
		t.Fatalf("the chat's evidence is still there: %v", err)
	}
}

func TestTheRepositorysMailboxIsReadThroughItsCommand(t *testing.T) {
	repo := t.TempDir()
	script := filepath.Join(repo, "mail")
	body := `#!/bin/sh
echo '[{"received":"2026-10-29T10:00:00","from":"a@b.c","to":"'"$2"'","title":"Your code 123456","code":null,"text":"Open https://beta.example.com/x?token=1 or https://evil.test/","html":""}]'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	m := repoMail{root: repo, conf: &RepoMail{Command: "mail", LinkHosts: []string{"example.com"}}}
	mails, err := m.Read(context.Background(), "qa1-beta@goog.re", 60, 3)
	if err != nil || len(mails) != 1 || mails[0].Code != "123456" || mails[0].To != "qa1-beta@goog.re" ||
		len(mails[0].Links) != 1 || !strings.HasPrefix(mails[0].Links[0], "https://beta.example.com/") {
		t.Fatalf("mails: %+v %v", mails, err)
	}
	if _, err := m.Read(context.Background(), "x; rm -rf /", 60, 3); err == nil {
		t.Fatal("a bad address reached the command")
	}
}

func TestTheRepositoryFileIsStrict(t *testing.T) {
	repo := t.TempDir()
	_ = os.MkdirAll(filepath.Join(repo, ".socrates", "mcp"), 0o755)
	if c, err := LoadRepo(repo); err != nil || c.BaseURL != "" {
		t.Fatalf("no file: %+v %v", c, err)
	}
	_ = os.WriteFile(filepath.Join(repo, RepoFile), []byte(`{"base_ur": "x"}`), 0o644)
	if _, err := LoadRepo(repo); err == nil {
		t.Fatal("an unknown key was taken")
	}
	_ = os.WriteFile(filepath.Join(repo, RepoFile), []byte(`{"base_url": "ftp://x"}`), 0o644)
	if _, err := LoadRepo(repo); err == nil {
		t.Fatal("a base URL that is not http was taken")
	}
	_ = os.WriteFile(filepath.Join(repo, RepoFile), []byte(`{"secrets": {"command": "printf 'A=1\\n'", "names": ["A", "B"]}}`), 0o644)
	c, _ := LoadRepo(repo)
	if _, err := c.readSecrets(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "did not print B") {
		t.Fatalf("a missing secret: %v", err)
	}
}

func TestEveryToolIsDescribed(t *testing.T) {
	s := newServer(t)
	seen := map[string]bool{}
	for _, tool := range s.Tools() {
		if tool.Description == "" || tool.Schema["type"] != "object" || seen[tool.Name] {
			t.Fatalf("tool %+v", tool)
		}
		seen[tool.Name] = true
		if tool.Name != "devices" && !strings.Contains(s.Instructions(), tool.Name) {
			t.Errorf("the instructions do not name %s", tool.Name)
		}
	}
	for _, want := range []string{"start", "open", "find", "tap", "type", "key", "keyboard", "scroll", "rotate", "back", "forward", "reload", "wait", "look", "shot", "env", "address", "mail", "logs", "stop"} {
		if !seen[want] {
			t.Errorf("no tool %s (qa %s)", want, want)
		}
	}
}
