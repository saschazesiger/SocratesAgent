package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub is GitHub's API as far as the service uses it: two
// installations, the repositories each grants, tokens, a manifest code and
// pull requests. Every request that needs the App's JWT has it checked
// against the public key.
type fakeGitHub struct {
	t     *testing.T
	appID int64
	pub   *rsa.PublicKey
	pem   string
	now   func() time.Time

	mu sync.Mutex
	// repos by installation: full name → private.
	repos map[int64][]string
	// tokens handed out: token → installation and the repositories asked.
	tokens map[string]tokenGrant
	minted int
	listed int
	// pulls open by "owner/name:head".
	pulls map[string]int
	// lastPull is the body of the last pull request asked for.
	lastPull map[string]any
	// uninstalled are the installations removed with DELETE.
	uninstalled map[int64]bool
	// appStatus is how GET /apps/{slug} answers without authentication:
	// 200 for a public App, 0 (404) for a private one. appAsked counts it.
	appStatus int
	appAsked  int
}

type tokenGrant struct {
	installation int64
	repos        []string
}

func newFake(t *testing.T, now func() time.Time) (*fakeGitHub, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return &fakeGitHub{
		t: t, appID: 4242, pub: &key.PublicKey, pem: string(pemBytes), now: now,
		repos: map[int64][]string{
			11: {"alice/one", "alice/two", "alice/three"},
			22: {"acme/web"},
		},
		tokens:      map[string]tokenGrant{},
		pulls:       map[string]int{},
		uninstalled: map[int64]bool{},
	}, key
}

// verifyJWT checks the App's JWT the way GitHub does: the signature with the
// public key, the issuer and the times.
func (f *fakeGitHub) verifyJWT(r *http.Request) error {
	jwt, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return errors.New("no bearer token")
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	enc := base64.RawURLEncoding
	head, _ := enc.DecodeString(parts[0])
	var h struct{ Alg, Typ string }
	if err := json.Unmarshal(head, &h); err != nil || h.Alg != "RS256" {
		return fmt.Errorf("header %s", head)
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	raw, _ := enc.DecodeString(parts[1])
	var c struct{ Iat, Exp, Iss int64 }
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	now := f.now().Unix()
	if c.Iss != f.appID || c.Iat > now || c.Exp <= now || c.Exp-c.Iat > 600 {
		return fmt.Errorf("claims %s at %d", raw, now)
	}
	return nil
}

func (f *fakeGitHub) grant(r *http.Request) (tokenGrant, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "token ")
	if !ok {
		return tokenGrant{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.tokens[tok]
	return g, ok
}

func page(r *http.Request, n int) (int, int) {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if per <= 0 {
		per = 30
	}
	if p <= 0 {
		p = 1
	}
	from := min((p-1)*per, n)
	return from, min(from+per, n)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == "GET" && path == "/app/installations":
		if err := f.verifyJWT(r); err != nil {
			writeJSON(w, 401, map[string]any{"message": err.Error()})
			return
		}
		all := []map[string]any{}
		f.mu.Lock()
		for _, in := range []map[string]any{
			{"id": 22, "account": map[string]any{"login": "acme", "type": "Organization"}},
			{"id": 11, "account": map[string]any{"login": "alice", "type": "User"}},
		} {
			if !f.uninstalled[int64(in["id"].(int))] {
				all = append(all, in)
			}
		}
		f.mu.Unlock()
		from, to := page(r, len(all))
		writeJSON(w, 200, all[from:to])
	case r.Method == "DELETE" && strings.HasPrefix(path, "/app/installations/"):
		if err := f.verifyJWT(r); err != nil {
			writeJSON(w, 401, map[string]any{"message": err.Error()})
			return
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/app/installations/"), 10, 64)
		f.mu.Lock()
		_, known := f.repos[id]
		gone := f.uninstalled[id]
		f.uninstalled[id] = true
		f.mu.Unlock()
		if !known || gone {
			writeJSON(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "POST" && strings.HasPrefix(path, "/app/installations/") && strings.HasSuffix(path, "/access_tokens"):
		if err := f.verifyJWT(r); err != nil {
			writeJSON(w, 401, map[string]any{"message": err.Error()})
			return
		}
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/app/installations/"), "/access_tokens"), 10, 64)
		var body struct {
			Repositories []string `json:"repositories"`
		}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		f.mu.Lock()
		owned := f.repos[id]
		for _, want := range body.Repositories {
			found := false
			for _, full := range owned {
				if strings.HasSuffix(full, "/"+want) {
					found = true
				}
			}
			if !found {
				f.mu.Unlock()
				writeJSON(w, 422, map[string]any{"message": "There is at least one repository that does not exist or is not accessible"})
				return
			}
		}
		f.minted++
		tok := fmt.Sprintf("ghs_%d_%d", id, f.minted)
		f.tokens[tok] = tokenGrant{installation: id, repos: body.Repositories}
		f.mu.Unlock()
		writeJSON(w, 201, map[string]any{"token": tok, "expires_at": f.now().Add(time.Hour).UTC().Format(time.RFC3339)})
	case r.Method == "GET" && path == "/installation/repositories":
		g, ok := f.grant(r)
		if !ok || len(g.repos) > 0 {
			writeJSON(w, 401, map[string]any{"message": "Bad credentials"})
			return
		}
		f.mu.Lock()
		f.listed++
		owned := f.repos[g.installation]
		f.mu.Unlock()
		from, to := page(r, len(owned))
		var list []map[string]any
		for _, full := range owned[from:to] {
			list = append(list, map[string]any{"full_name": full, "default_branch": "main", "private": strings.HasPrefix(full, "acme/")})
		}
		writeJSON(w, 200, map[string]any{"total_count": len(owned), "repositories": list})
	case r.Method == "GET" && path == "/apps/socrates-example":
		f.mu.Lock()
		f.appAsked++
		status := f.appStatus
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			writeJSON(w, 400, map[string]any{"message": "no authentication expected"})
			return
		}
		switch status {
		case 0:
			writeJSON(w, 404, map[string]any{"message": "Not Found"})
		case 200:
			writeJSON(w, 200, map[string]any{"id": f.appID, "slug": "socrates-example"})
		default:
			writeJSON(w, status, map[string]any{"message": "API rate limit exceeded"})
		}
	case r.Method == "POST" && strings.HasPrefix(path, "/app-manifests/"):
		if r.Header.Get("Authorization") != "" {
			writeJSON(w, 400, map[string]any{"message": "no authentication expected"})
			return
		}
		if path != "/app-manifests/good-code/conversions" {
			writeJSON(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		writeJSON(w, 201, map[string]any{
			"id": f.appID, "slug": "socrates-example", "name": "Socrates example.org", "html_url": "https://github.com/apps/socrates-example",
			"client_id": "Iv1.abc", "client_secret": "shh", "webhook_secret": "hook", "pem": f.pem,
			"owner": map[string]any{"login": "alice", "type": "User"},
		})
	case strings.HasPrefix(path, "/repos/") && strings.HasSuffix(path, "/pulls"):
		full := strings.TrimSuffix(strings.TrimPrefix(path, "/repos/"), "/pulls")
		g, ok := f.grant(r)
		_, name, _ := strings.Cut(full, "/")
		if !ok || len(g.repos) != 1 || g.repos[0] != name {
			writeJSON(w, 403, map[string]any{"message": "Resource not accessible by integration"})
			return
		}
		if r.Method == "GET" {
			head := r.URL.Query().Get("head")
			owner, _, _ := strings.Cut(full, "/")
			if r.URL.Query().Get("state") != "open" || !strings.HasPrefix(head, owner+":") {
				writeJSON(w, 400, map[string]any{"message": "bad query " + r.URL.RawQuery})
				return
			}
			f.mu.Lock()
			n, ok := f.pulls[full+":"+strings.TrimPrefix(head, owner+":")]
			f.mu.Unlock()
			list := []map[string]any{}
			if ok {
				list = append(list, map[string]any{"number": n, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d", full, n)})
			}
			writeJSON(w, 200, list)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.lastPull = body
		key := full + ":" + fmt.Sprint(body["head"])
		if _, exists := f.pulls[key]; exists {
			f.mu.Unlock()
			writeJSON(w, 422, map[string]any{"message": "Validation Failed", "errors": []map[string]any{
				{"resource": "PullRequest", "code": "custom", "message": "A pull request already exists for " + key + "."}}})
			return
		}
		n := 7 + len(f.pulls)
		f.pulls[key] = n
		f.mu.Unlock()
		writeJSON(w, 201, map[string]any{"number": n, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d", full, n)})
	default:
		writeJSON(w, 404, map[string]any{"message": "Not Found: " + r.Method + " " + path})
	}
}

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*Service, *fakeGitHub, *clock) {
	t.Helper()
	c := &clock{t: time.Now()}
	f, _ := newFake(t, c.now)
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	s := New(Config{API: ts.URL, Web: "https://web.example", Git: "file:///srv/git/", Now: c.now})
	if err := s.SetApp(&App{ID: f.appID, Slug: "socrates-example", PEM: []byte(f.pem)}); err != nil {
		t.Fatal(err)
	}
	s.SetAccepted([]int64{11, 22})
	old := pageSize
	pageSize = 2
	t.Cleanup(func() { pageSize = old })
	return s, f, c
}

func TestJWTVerifiesWithThePublicKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f, key := newFake(t, func() time.Time { return now })
	jwt, err := AppJWT(key, f.appID, now)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+jwt)
	if err := f.verifyJWT(r); err != nil {
		t.Fatalf("JWT refused: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(jwt, ".")[1])
	var c map[string]int64
	_ = json.Unmarshal(raw, &c)
	if c["iat"] != now.Unix()-60 || c["exp"] != now.Unix()+540 || c["iss"] != f.appID {
		t.Fatalf("claims %v", c)
	}
	// Another key's signature does not pass.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	bad, _ := AppJWT(other, f.appID, now)
	r.Header.Set("Authorization", "Bearer "+bad)
	if f.verifyJWT(r) == nil {
		t.Fatal("a JWT of another key passed")
	}
}

func TestListingPagesAndCaches(t *testing.T) {
	s, f, c := setup(t)
	ctx := context.Background()
	repos, err := s.Repos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range repos {
		names = append(names, r.FullName)
	}
	if got := strings.Join(names, ","); got != "acme/web,alice/one,alice/three,alice/two" {
		t.Fatalf("repos %s", got)
	}
	if repos[0].Installation != 22 || !repos[0].Private || repos[1].Installation != 11 || repos[1].DefaultBranch != "main" {
		t.Fatalf("repo facts %+v", repos)
	}
	installs, _ := s.Installations(ctx)
	if len(installs) != 2 || installs[0].Account != "acme" || installs[0].AccountType != "Organization" {
		t.Fatalf("installations %+v", installs)
	}
	if got := s.InstallationSettingsURL(installs[0]); got != "https://web.example/organizations/acme/settings/installations/22" {
		t.Fatalf("org settings URL %s", got)
	}
	if got := s.InstallationSettingsURL(installs[1]); got != "https://web.example/settings/installations/11" {
		t.Fatalf("user settings URL %s", got)
	}
	// alice's three repositories took two pages, acme's one.
	listed := f.listed
	if listed != 3 {
		t.Fatalf("%d repository pages asked", listed)
	}
	// Within the minute the listing is not asked again.
	c.add(30 * time.Second)
	if _, err := s.Repos(ctx); err != nil || f.listed != listed {
		t.Fatalf("asked again within the minute: %d, %v", f.listed, err)
	}
	// After it, it is; and a refresh asks at once.
	c.add(31 * time.Second)
	_, _ = s.Repos(ctx)
	if f.listed != listed*2 {
		t.Fatalf("not asked after the minute: %d", f.listed)
	}
	_, _, _ = s.Refresh(ctx)
	if f.listed != listed*3 {
		t.Fatalf("refresh did not ask: %d", f.listed)
	}
}

func TestReachesAsksAgainForAnUnknownRepository(t *testing.T) {
	s, f, c := setup(t)
	ctx := context.Background()
	if r, ok, err := s.Reaches(ctx, "ALICE/Two"); err != nil || !ok || r.FullName != "alice/two" {
		t.Fatalf("Reaches = %+v %v %v", r, ok, err)
	}
	// Granted on GitHub a moment ago: the next question finds it.
	f.mu.Lock()
	f.repos[22] = append(f.repos[22], "acme/api")
	f.mu.Unlock()
	c.add(5 * time.Second)
	if _, ok, err := s.Reaches(ctx, "acme/api"); err != nil || !ok {
		t.Fatalf("a newly granted repository is not reached: %v %v", ok, err)
	}
	c.add(5 * time.Second)
	if _, ok, err := s.Reaches(ctx, "bob/none"); err != nil || ok {
		t.Fatalf("an ungranted repository is reached: %v %v", ok, err)
	}
}

func TestRepoTokenIsScopedAndCached(t *testing.T) {
	s, f, c := setup(t)
	ctx := context.Background()
	tok, err := s.RepoToken(ctx, "alice/two")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	g := f.tokens[tok]
	f.mu.Unlock()
	if g.installation != 11 || len(g.repos) != 1 || g.repos[0] != "two" {
		t.Fatalf("token grant %+v", g)
	}
	minted := f.minted
	// The same repository within the hour: the same token.
	c.add(50 * time.Minute)
	if again, err := s.RepoToken(ctx, "alice/two"); err != nil || again != tok || f.minted != minted {
		t.Fatalf("not cached: %s %v minted %d", again, err, f.minted)
	}
	// Another repository: a token of its own.
	other, err := s.RepoToken(ctx, "alice/one")
	if err != nil || other == tok {
		t.Fatalf("another repository shares the token: %v", err)
	}
	// Five minutes before the expiry it is replaced.
	c.add(6 * time.Minute)
	if fresh, err := s.RepoToken(ctx, "alice/two"); err != nil || fresh == tok {
		t.Fatalf("an expiring token is still handed out: %v", err)
	}
	// A repository no installation grants gets none.
	if _, err := s.RepoToken(ctx, "bob/none"); !errors.Is(err, ErrNotReached) {
		t.Fatalf("ungranted repository: %v", err)
	}
	// Without the App there is nothing.
	_ = s.SetApp(nil)
	if s.Connected() {
		t.Fatal("connected without an App")
	}
	if _, err := s.RepoToken(ctx, "alice/two"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("without an App: %v", err)
	}
}

func TestManifestAndConversion(t *testing.T) {
	s, f, _ := setup(t)
	m := NewManifest("https://socrates.example.org/")
	if m.Name != "Socrates socrates.example.org" || m.RedirectURL != "https://socrates.example.org/api/github/app/created" ||
		m.SetupURL != "https://socrates.example.org/api/github/app/installed" || !m.SetupOnUpdate || !m.Public ||
		m.HookAttributes.Active || m.DefaultPermissions["workflows"] != "write" || m.DefaultPermissions["metadata"] != "read" {
		t.Fatalf("manifest %+v", m)
	}
	if long := NewManifest("https://a-very-long-host-name.example.organisation.net"); len(long.Name) > maxAppName {
		t.Fatalf("name too long: %q", long.Name)
	}
	if got := s.ManifestAction("", "st&1"); got != "https://web.example/settings/apps/new?state=st%261" {
		t.Fatalf("action %s", got)
	}
	if got := s.ManifestAction("acme", "x"); got != "https://web.example/organizations/acme/settings/apps/new?state=x" {
		t.Fatalf("org action %s", got)
	}
	conv, err := s.ConvertManifest(context.Background(), "good-code")
	if err != nil {
		t.Fatal(err)
	}
	if conv.ID != f.appID || conv.Slug != "socrates-example" || conv.Owner.Login != "alice" || conv.ClientSecret != "shh" || conv.WebhookSecret != "hook" {
		t.Fatalf("conversion %+v", conv)
	}
	app := conv.App()
	if app.Owner != "alice" || string(app.PEM) != f.pem {
		t.Fatalf("app %+v", app)
	}
	var apiErr *APIError
	if _, err := s.ConvertManifest(context.Background(), "stale"); !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("a stale code: %v", err)
	}
}

func TestOpenPullRequestAndTheExistingOne(t *testing.T) {
	s, f, _ := setup(t)
	ctx := context.Background()
	pr, err := s.OpenPullRequest(ctx, "alice/two", "socrates/fix", "main", "Fix it", "Body", true)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || pr.Existing || pr.URL != "https://github.com/alice/two/pull/7" {
		t.Fatalf("pull request %+v", pr)
	}
	if f.lastPull["base"] != "main" || f.lastPull["draft"] != true || f.lastPull["title"] != "Fix it" {
		t.Fatalf("asked for %v", f.lastPull)
	}
	again, err := s.OpenPullRequest(ctx, "alice/two", "socrates/fix", "main", "Fix it", "Body", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Number != 7 || !again.Existing || again.URL != pr.URL {
		t.Fatalf("existing pull request %+v", again)
	}
	if _, err := s.OpenPullRequest(ctx, "bob/none", "x", "main", "t", "", false); !errors.Is(err, ErrNotReached) {
		t.Fatalf("ungranted repository: %v", err)
	}
}

func TestCloneURLAndConfig(t *testing.T) {
	s := New(Config{Git: "file:///srv/git/"})
	if got := s.CloneURL("alice/two"); got != "file:///srv/git/alice/two.git" {
		t.Fatalf("clone URL %s", got)
	}
	if s.Connected() {
		t.Fatal("connected without an App")
	}
	if _, err := s.Repos(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Repos without an App: %v", err)
	}
	t.Setenv("SOCRATES_GITHUB_API", "http://127.0.0.1:1/")
	t.Setenv("SOCRATES_GITHUB_WEB", "")
	cfg := New(ConfigFromEnv()).Config()
	if cfg.API != "http://127.0.0.1:1" || cfg.Web != DefaultWeb || cfg.Git != DefaultGit {
		t.Fatalf("config %+v", cfg)
	}
	if New(Config{}).CloneURL("a/b") != "https://github.com/a/b.git" {
		t.Fatal("default clone URL")
	}
	if err := s.SetApp(&App{ID: 1, PEM: []byte("nope")}); err == nil {
		t.Fatal("a broken key was taken")
	}
}

// tokensFor is how many tokens the fake handed out for installation id.
func (f *fakeGitHub) tokensFor(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, g := range f.tokens {
		if g.installation == id {
			n++
		}
	}
	return n
}

func TestOnlyAcceptedInstallationsCount(t *testing.T) {
	s, f, c := setup(t)
	ctx := context.Background()
	s.SetAccepted([]int64{11})
	repos, err := s.Repos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range repos {
		names = append(names, r.FullName)
	}
	if got := strings.Join(names, ","); got != "alice/one,alice/three,alice/two" {
		t.Fatalf("repos %s", got)
	}
	// The installation not accepted is pending, and asked no token for.
	installs, _ := s.Installations(ctx)
	pending, _ := s.Pending(ctx)
	if len(installs) != 1 || installs[0].ID != 11 || len(pending) != 1 || pending[0].ID != 22 || pending[0].Account != "acme" {
		t.Fatalf("installations %+v, pending %+v", installs, pending)
	}
	if n := f.tokensFor(22); n != 0 {
		t.Fatalf("%d tokens of the pending installation", n)
	}
	// Its repositories are not reached, not even after asking again.
	c.add(5 * time.Second)
	if _, ok, err := s.Reaches(ctx, "acme/web"); err != nil || ok {
		t.Fatalf("a pending installation's repository is reached: %v %v", ok, err)
	}
	if _, err := s.RepoToken(ctx, "acme/web"); !errors.Is(err, ErrNotReached) {
		t.Fatalf("token of a pending installation's repository: %v", err)
	}
	if _, err := s.OpenPullRequest(ctx, "acme/web", "x", "main", "t", "", false); !errors.Is(err, ErrNotReached) {
		t.Fatalf("pull request in a pending installation's repository: %v", err)
	}
	if n := f.tokensFor(22); n != 0 {
		t.Fatalf("%d tokens of the pending installation", n)
	}
	// Installation finds either kind, and nothing GitHub does not have.
	if in, ok, err := s.Installation(ctx, 22); err != nil || !ok || in.AccountType != "Organization" {
		t.Fatalf("Installation(22) = %+v %v %v", in, ok, err)
	}
	if _, ok, err := s.Installation(ctx, 99); err != nil || ok {
		t.Fatalf("Installation(99) = %v %v", ok, err)
	}
	// Accepted, it counts.
	s.SetAccepted([]int64{11, 22})
	if !s.Accepted(22) {
		t.Fatal("22 not accepted")
	}
	if _, ok, err := s.Reaches(ctx, "acme/web"); err != nil || !ok {
		t.Fatalf("an accepted installation's repository is not reached: %v %v", ok, err)
	}
	if pending, _ := s.Pending(ctx); len(pending) != 0 {
		t.Fatalf("pending %+v", pending)
	}
}

func TestTheOwnersInstallationIsAdopted(t *testing.T) {
	s, f, _ := setup(t)
	ctx := context.Background()
	var adopted [][]int64
	s.OnAdopt(func(ids []int64) { adopted = append(adopted, ids) })

	// An App of bob's: nothing on his account, nothing accepted.
	if err := s.SetApp(&App{ID: f.appID, Slug: "socrates-example", Owner: "bob", PEM: []byte(f.pem)}); err != nil {
		t.Fatal(err)
	}
	s.SetAccepted(nil)
	if repos, err := s.Repos(ctx); err != nil || len(repos) != 0 || len(adopted) != 0 {
		t.Fatalf("bob's App: %+v %v, adopted %v", repos, err, adopted)
	}

	// alice's: her installation is accepted, acme's stays pending.
	if err := s.SetApp(&App{ID: f.appID, Slug: "socrates-example", Owner: "Alice", PEM: []byte(f.pem)}); err != nil {
		t.Fatal(err)
	}
	repos, err := s.Repos(ctx)
	if err != nil || len(repos) != 3 || !s.Accepted(11) || s.Accepted(22) {
		t.Fatalf("alice's App: %+v %v", repos, err)
	}
	if len(adopted) != 1 || len(adopted[0]) != 1 || adopted[0][0] != 11 {
		t.Fatalf("adopted %v", adopted)
	}
	// Once something is accepted, nothing is adopted again.
	if _, _, err := s.Refresh(ctx); err != nil || len(adopted) != 1 {
		t.Fatalf("adopted again: %v %v", adopted, err)
	}
	s.SetAccepted([]int64{22})
	if _, _, err := s.Refresh(ctx); err != nil || len(adopted) != 1 || s.Accepted(11) {
		t.Fatalf("adopted beside an accepted one: %v %v", adopted, err)
	}
}

func TestUninstall(t *testing.T) {
	s, f, _ := setup(t)
	ctx := context.Background()
	if pending, _ := s.Installations(ctx); len(pending) != 2 {
		t.Fatalf("installations %+v", pending)
	}
	if err := s.Uninstall(ctx, 22); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	gone := f.uninstalled[22]
	f.mu.Unlock()
	if !gone {
		t.Fatal("not uninstalled on GitHub")
	}
	// The listing is asked again at once.
	if installs, _ := s.Installations(ctx); len(installs) != 1 || installs[0].ID != 11 {
		t.Fatalf("installations after the uninstall %+v", installs)
	}
	// One GitHub no longer has is gone already.
	if err := s.Uninstall(ctx, 22); err != nil {
		t.Fatalf("uninstalled twice: %v", err)
	}
	_ = s.SetApp(nil)
	if err := s.Uninstall(ctx, 11); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("without an App: %v", err)
	}
}

func TestPublicAsksWithoutAuthenticationAndCaches(t *testing.T) {
	s, f, c := setup(t)
	ctx := context.Background()
	asked := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.appAsked
	}
	set := func(status int) {
		f.mu.Lock()
		f.appStatus = status
		f.mu.Unlock()
	}

	// Private: GitHub answers 404 without authentication.
	if public, err := s.Public(ctx); err != nil || public {
		t.Fatalf("private App: %v, %v", public, err)
	}
	// Made public on GitHub, the answer is believed for the minute...
	set(200)
	c.add(30 * time.Second)
	if public, err := s.Public(ctx); err != nil || public || asked() != 1 {
		t.Fatalf("asked again within the minute: %v, %v, %d", public, err, asked())
	}
	// ...and Refresh asks again.
	if _, _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if public, err := s.Public(ctx); err != nil || !public || asked() != 2 {
		t.Fatalf("after Refresh: %v, %v, %d", public, err, asked())
	}
	// After the minute it is asked again on its own.
	set(0)
	c.add(61 * time.Second)
	if public, err := s.Public(ctx); err != nil || public || asked() != 3 {
		t.Fatalf("after the minute: %v, %v, %d", public, err, asked())
	}

	// Any other answer is not known, and believed for a few seconds only.
	set(403)
	c.add(61 * time.Second)
	if _, err := s.Public(ctx); err == nil {
		t.Fatal("a rate limit counted as an answer")
	}
	if _, err := s.Public(ctx); err == nil || asked() != 4 {
		t.Fatalf("the failure was not kept for a moment: %v, %d", err, asked())
	}
	set(200)
	c.add(6 * time.Second)
	if public, err := s.Public(ctx); err != nil || !public || asked() != 5 {
		t.Fatalf("after the failure: %v, %v, %d", public, err, asked())
	}

	// GitHub not reached is not known either.
	off := New(Config{API: "http://127.0.0.1:1", Now: c.now})
	if err := off.SetApp(&App{ID: f.appID, Slug: "socrates-example", PEM: []byte(f.pem)}); err != nil {
		t.Fatal(err)
	}
	if _, err := off.Public(ctx); err == nil {
		t.Fatal("an unreachable GitHub gave an answer")
	}
	// Without an App there is nothing to ask.
	_ = s.SetApp(nil)
	if _, err := s.Public(ctx); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("without an App: %v", err)
	}
}
