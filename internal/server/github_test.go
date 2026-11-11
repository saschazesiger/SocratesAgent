package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// fakeGitHubAPI is GitHub's API as Admin's GitHub card uses it: the manifest
// conversion of an App alice owns, its installations - on alice with two
// repositories, on the organisation acme and on mallory with one each - and
// tokens, and uninstalling.
func fakeGitHubAPI(t *testing.T) *httptest.Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	answer := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	installs := []map[string]any{
		{"id": 6, "account": map[string]any{"login": "acme", "type": "Organization"}},
		{"id": 5, "account": map[string]any{"login": "alice", "type": "User"}},
		{"id": 8, "account": map[string]any{"login": "mallory", "type": "User"}},
	}
	repos := map[string][]map[string]any{
		"ghs_5": {
			{"full_name": "alice/One", "default_branch": "main", "private": true},
			{"full_name": "alice/two", "default_branch": "trunk", "private": false},
		},
		"ghs_6": {{"full_name": "acme/web", "default_branch": "main", "private": true}},
		"ghs_8": {{"full_name": "mallory/trap", "default_branch": "main", "private": false}},
	}
	var mu sync.Mutex
	uninstalled := map[string]bool{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/app-manifests/good-code/conversions":
			answer(w, 201, map[string]any{
				"id": 99, "slug": "socrates-test", "name": "Socrates test", "html_url": "https://web.example/apps/socrates-test",
				"client_id": "Iv1.x", "client_secret": "client-secret-value", "webhook_secret": "hook-secret-value", "pem": pemText,
				"owner": map[string]any{"login": "alice", "type": "User"},
			})
		case r.Method == "GET" && r.URL.Path == "/apps/socrates-test":
			// The App is public: GitHub shows it without authentication.
			if r.Header.Get("Authorization") != "" {
				answer(w, 400, map[string]any{"message": "no authentication expected"})
				return
			}
			answer(w, 200, map[string]any{"id": 99, "slug": "socrates-test"})
		case r.Method == "GET" && r.URL.Path == "/app/installations":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				answer(w, 401, map[string]any{"message": "no JWT"})
				return
			}
			list := []map[string]any{}
			for _, in := range installs {
				if !uninstalled[fmt.Sprint(in["id"])] {
					list = append(list, in)
				}
			}
			answer(w, 200, list)
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/app/installations/") && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/app/installations/"), "/access_tokens")
			if _, ok := repos["ghs_"+id]; !ok || uninstalled[id] {
				answer(w, 404, map[string]any{"message": "Not Found"})
				return
			}
			answer(w, 201, map[string]any{"token": "ghs_" + id, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/app/installations/"):
			id := strings.TrimPrefix(r.URL.Path, "/app/installations/")
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				answer(w, 401, map[string]any{"message": "no JWT"})
				return
			}
			if _, ok := repos["ghs_"+id]; !ok || uninstalled[id] {
				answer(w, 404, map[string]any{"message": "Not Found"})
				return
			}
			uninstalled[id] = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && r.URL.Path == "/installation/repositories":
			list, ok := repos[strings.TrimPrefix(r.Header.Get("Authorization"), "token ")]
			if !ok {
				answer(w, 401, map[string]any{"message": "Bad credentials"})
				return
			}
			answer(w, 200, map[string]any{"repositories": list})
		default:
			answer(w, 404, map[string]any{"message": "Not Found"})
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func githubEnv(t *testing.T) *testEnv {
	t.Helper()
	api := fakeGitHubAPI(t)
	t.Setenv("SOCRATES_GITHUB_API", api.URL)
	t.Setenv("SOCRATES_GITHUB_WEB", "https://web.example")
	t.Setenv("SOCRATES_GITHUB_GIT", "file:///srv/git")
	env := adminEnv(t)
	// The data directory's key, which seals the App's secrets, is made in it.
	if err := os.MkdirAll(env.srv.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestGitHubConnectRepositoriesAndDisconnect(t *testing.T) {
	env := githubEnv(t)

	// Signed out there is nothing to read.
	if res, _ := env.do(t, env.anon, "GET", "/api/github", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
	_, view := env.do(t, env.client, "GET", "/api/github", "")
	if view["connected"] != false || view["problem"] != "" || view["public"] != nil {
		t.Fatalf("before connecting: %#v", view)
	}

	// Start: the form's target and the manifest for this origin.
	res, start := env.do(t, env.client, "POST", "/api/github/app/start", `{"org":""}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start: %d %#v", res.StatusCode, start)
	}
	state, _ := start["state"].(string)
	if state == "" || start["action"] != "https://web.example/settings/apps/new?state="+url.QueryEscape(state) {
		t.Fatalf("start: %#v", start)
	}
	manifest := start["manifest"].(map[string]any)
	if manifest["redirect_url"] != env.server.URL+"/api/github/app/created" ||
		manifest["setup_url"] != env.server.URL+"/api/github/app/installed" || manifest["public"] != true ||
		manifest["default_permissions"].(map[string]any)["contents"] != "write" {
		t.Fatalf("manifest: %#v", manifest)
	}
	if _, org := env.do(t, env.client, "POST", "/api/github/app/start", `{"org":"acme"}`); !strings.HasPrefix(org["action"].(string), "https://web.example/organizations/acme/settings/apps/new?state=") {
		t.Fatalf("organisation: %#v", org)
	}
	if res, _ := env.do(t, env.client, "POST", "/api/github/app/start", `{"org":"no/such"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad organisation: %d", res.StatusCode)
	}

	// A state this server did not hand out is refused, back to Admin.
	res, _ = env.do(t, env.client, "GET", "/api/github/app/created?code=good-code&state=forged", "")
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/admin?github_error=") || !strings.HasSuffix(loc, "#github") {
		t.Fatalf("forged state: %d %s", res.StatusCode, loc)
	}
	// GitHub's redirect: the App is kept and the browser goes on to install it.
	res, _ = env.do(t, env.client, "GET", "/api/github/app/created?code=good-code&state="+url.QueryEscape(state), "")
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "https://web.example/apps/socrates-test/installations/new?state=") {
		t.Fatalf("created: %d %s", res.StatusCode, loc)
	}
	// The state was good once.
	res, _ = env.do(t, env.client, "GET", "/api/github/app/created?code=good-code&state="+url.QueryEscape(state), "")
	if !strings.Contains(res.Header.Get("Location"), "github_error=") {
		t.Fatalf("a state used twice: %s", res.Header.Get("Location"))
	}

	// The facts are in the settings, the secrets sealed in the kv table.
	g := env.srv.Settings().GitHub
	if g.AppID != 99 || g.Slug != "socrates-test" || g.Owner != "alice" || g.OwnerType != "User" {
		t.Fatalf("settings.github = %+v", g)
	}
	_, settings := env.do(t, env.client, "GET", "/api/settings", "")
	raw, _ := json.Marshal(settings)
	for _, secret := range []string{"PRIVATE KEY", "client-secret-value", "hook-secret-value"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the settings carry %q", secret)
		}
	}
	for _, key := range []string{kvGitHubPEM, kvGitHubClientSecret, kvGitHubWebhookSecret} {
		v, err := env.store.GetKV(key)
		if err != nil || v == "" || strings.Contains(v, "PRIVATE") || strings.Contains(v, "secret-value") {
			t.Fatalf("kv %s = %q, %v", key, v, err)
		}
	}
	if secret, err := env.srv.openGitHubSecret(kvGitHubClientSecret); err != nil || string(secret) != "client-secret-value" {
		t.Fatalf("client secret %q %v", secret, err)
	}
	// A restart finds the App again.
	if !env.srv.newGitHub().Connected() {
		t.Fatal("the App is not loaded from the data directory")
	}

	// The card: the App, its installation and the repositories.
	_, view = env.do(t, env.client, "GET", "/api/github?refresh=1", "")
	if view["connected"] != true || view["problem"] != "" || view["public"] != true {
		t.Fatalf("connected: %#v", view)
	}
	app := view["app"].(map[string]any)
	if app["name"] != "Socrates test" || app["settings_url"] != "https://web.example/settings/apps/socrates-test" {
		t.Fatalf("app: %#v", app)
	}
	// alice's own installation is accepted on its own; the others wait.
	installs := view["installations"].([]any)
	if len(installs) != 1 || installs[0].(map[string]any)["settings_url"] != "https://web.example/settings/installations/5" {
		t.Fatalf("installations: %#v", installs)
	}
	if pending := view["pending"].([]any); len(pending) != 2 || pending[0].(map[string]any)["account"] != "acme" {
		t.Fatalf("pending: %#v", pending)
	}
	if app["advanced_url"] != "https://web.example/settings/apps/socrates-test/advanced" || app["install_url"] != "/api/github/app/install" {
		t.Fatalf("advanced URL: %#v", app)
	}
	repos := view["repos"].([]any)
	if len(repos) != 2 || repos[0].(map[string]any)["full_name"] != "alice/One" || repos[1].(map[string]any)["default_branch"] != "trunk" {
		t.Fatalf("repos: %#v", repos)
	}
	if env.srv.ghApp.CloneURL("alice/two") != "file:///srv/git/alice/two.git" {
		t.Fatal("clone URL does not follow SOCRATES_GITHUB_GIT")
	}

	// A second App is not created over the first.
	if res, _ := env.do(t, env.client, "POST", "/api/github/app/start", `{}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("start while connected: %d", res.StatusCode)
	}

	// A repository's setup: kept under GitHub's name, CRLF made LF.
	res, saved := env.do(t, env.client, "PUT", "/api/github/repos/alice/one/settings", `{"setup":"npm ci\r\ncp /etc/x/.env .env\r\n"}`)
	if res.StatusCode != http.StatusOK || saved["full_name"] != "alice/One" || saved["setup"] != "npm ci\ncp /etc/x/.env .env\n" {
		t.Fatalf("setup: %d %#v", res.StatusCode, saved)
	}
	_, view = env.do(t, env.client, "GET", "/api/github", "")
	if view["repos"].([]any)[0].(map[string]any)["setup"] != "npm ci\ncp /etc/x/.env .env\n" {
		t.Fatalf("setup not shown: %#v", view["repos"])
	}
	if res, _ := env.do(t, env.client, "PUT", "/api/github/repos/bob/none/settings", `{"setup":"x"}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a repository the App does not reach: %d", res.StatusCode)
	}
	if res, _ := env.do(t, env.client, "PUT", "/api/github/repos/alice/..%2Fx/settings", `{"setup":"x"}`); res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
		t.Fatalf("a bad name: %d", res.StatusCode)
	}

	// Saving Admin's page with a document from before the App keeps the App.
	res, _ = putSettings(t, env, func(s map[string]any) { s["github"] = map[string]any{} })
	if res.StatusCode != http.StatusOK || env.srv.Settings().GitHub.AppID != 99 || env.srv.Settings().GitHub.RepoSetup("alice/One") == "" {
		t.Fatalf("a settings save changed GitHub: %d %+v", res.StatusCode, env.srv.Settings().GitHub)
	}

	// GitHub's setup redirect goes back to the card.
	res, _ = env.do(t, env.client, "GET", "/api/github/app/installed?installation_id=5&setup_action=install", "")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin#github" {
		t.Fatalf("installed: %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	// An empty script removes it.
	if _, saved := env.do(t, env.client, "PUT", "/api/github/repos/alice/One/settings", `{"setup":"  "}`); saved["setup"] != "" {
		t.Fatalf("emptied setup: %#v", saved)
	}
	env.do(t, env.client, "PUT", "/api/github/repos/alice/two/settings", `{"setup":"make"}`)

	// Disconnect forgets the App and its keys, and keeps the scripts.
	res, view = env.do(t, env.client, "DELETE", "/api/github/app", "")
	if res.StatusCode != http.StatusOK || view["connected"] != false {
		t.Fatalf("disconnect: %d %#v", res.StatusCode, view)
	}
	if env.srv.ghApp.Connected() || env.srv.Settings().GitHub.Connected() {
		t.Fatal("still connected")
	}
	if env.srv.Settings().GitHub.RepoSetup("alice/two") != "make" {
		t.Fatal("the setup scripts went with the App")
	}
	for _, key := range []string{kvGitHubPEM, kvGitHubClientSecret, kvGitHubWebhookSecret} {
		if v, _ := env.store.GetKV(key); v != "" {
			t.Fatalf("kv %s kept after disconnect", key)
		}
	}
	if res, _ := env.do(t, env.client, "PUT", "/api/github/repos/alice/two/settings", `{"setup":"x"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("setup without an App: %d", res.StatusCode)
	}
}

// connectGitHub creates the App through the manifest flow, without
// installing it.
func connectGitHub(t *testing.T, env *testEnv) {
	t.Helper()
	_, start := env.do(t, env.client, "POST", "/api/github/app/start", `{}`)
	state, _ := start["state"].(string)
	res, _ := env.do(t, env.client, "GET", "/api/github/app/created?code=good-code&state="+url.QueryEscape(state), "")
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/installations/new?state=") {
		t.Fatalf("created: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

// installState is the state of an install page the server sends the
// browser to (Add an organisation).
func installState(t *testing.T, env *testEnv) string {
	t.Helper()
	res, _ := env.do(t, env.client, "GET", "/api/github/app/install", "")
	loc, err := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusSeeOther || err != nil || loc.Path != "/apps/socrates-test/installations/new" || loc.Query().Get("state") == "" {
		t.Fatalf("install: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	return loc.Query().Get("state")
}

func githubNames(view map[string]any, key, field string) string {
	var names []string
	for _, one := range view[key].([]any) {
		names = append(names, fmt.Sprint(one.(map[string]any)[field]))
	}
	return strings.Join(names, ",")
}

func TestGitHubInstallationsAcceptPendingAndRemove(t *testing.T) {
	env := githubEnv(t)
	connectGitHub(t, env)
	ctx := context.Background()

	// Coming back from installing on acme signed out accepts nothing.
	res, _ := env.do(t, env.anon, "GET", "/api/github/app/installed?installation_id=6&setup_action=install", "")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin#github" || env.srv.ghApp.Accepted(6) {
		t.Fatalf("signed out: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// The owner's own installation is accepted on its own and kept.
	_, view := env.do(t, env.client, "GET", "/api/github?refresh=1", "")
	if got := githubNames(view, "installations", "account"); got != "alice" {
		t.Fatalf("installations %s", got)
	}
	if got := githubNames(view, "pending", "account"); got != "acme,mallory" {
		t.Fatalf("pending %s", got)
	}
	if got := githubNames(view, "repos", "full_name"); got != "alice/One,alice/two" {
		t.Fatalf("repos %s", got)
	}
	if got := env.srv.Settings().GitHub.AcceptedInstallations; len(got) != 1 || got[0] != 5 {
		t.Fatalf("accepted %v", got)
	}
	// A pending installation's repositories are not used.
	if _, ok, err := env.srv.ghApp.Reaches(ctx, "acme/web"); ok || err != nil {
		t.Fatalf("acme/web reached while pending: %v %v", ok, err)
	}
	if _, err := env.srv.ghApp.RepoToken(ctx, "mallory/trap"); err == nil {
		t.Fatal("a token for a pending installation's repository")
	}
	if res, _ := env.do(t, env.client, "PUT", "/api/github/repos/acme/web/settings", `{"setup":"x"}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("setup of a pending installation's repository: %d", res.StatusCode)
	}

	// Signed in but without the state of an install page this server sent
	// the browser to (a link someone made up), or with one it did not hand
	// out, nothing is accepted: the installation stays pending.
	for _, query := range []string{"installation_id=6&setup_action=install", "installation_id=6&setup_action=install&state=forged"} {
		res, _ = env.do(t, env.client, "GET", "/api/github/app/installed?"+query, "")
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin#github" || env.srv.ghApp.Accepted(6) {
			t.Fatalf("%s: %d %s", query, res.StatusCode, res.Header.Get("Location"))
		}
	}
	// Signed out, a good state accepts nothing either, and keeps the state.
	state := installState(t, env)
	res, _ = env.do(t, env.anon, "GET", "/api/github/app/installed?installation_id=6&state="+url.QueryEscape(state), "")
	if res.StatusCode != http.StatusSeeOther || env.srv.ghApp.Accepted(6) {
		t.Fatalf("signed out with a state: %d", res.StatusCode)
	}
	// With the state, GitHub's redirect accepts the installation once GitHub
	// lists it; an id it does not know is refused.
	res, _ = env.do(t, env.client, "GET", "/api/github/app/installed?installation_id=999&setup_action=install&state="+url.QueryEscape(state), "")
	if !strings.Contains(res.Header.Get("Location"), "github_error=") || env.srv.ghApp.Accepted(999) {
		t.Fatalf("an unknown installation: %s", res.Header.Get("Location"))
	}
	// A state is good for one redirect.
	res, _ = env.do(t, env.client, "GET", "/api/github/app/installed?installation_id=6&state="+url.QueryEscape(state), "")
	if env.srv.ghApp.Accepted(6) {
		t.Fatalf("a state used twice accepted: %s", res.Header.Get("Location"))
	}
	state = installState(t, env)
	res, _ = env.do(t, env.client, "GET", "/api/github/app/installed?installation_id=6&setup_action=install&state="+url.QueryEscape(state), "")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin#github" || !env.srv.ghApp.Accepted(6) {
		t.Fatalf("installed on acme: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if _, ok, err := env.srv.ghApp.Reaches(ctx, "acme/web"); !ok || err != nil {
		t.Fatalf("acme/web not reached once accepted: %v %v", ok, err)
	}

	// Accept on the card.
	if res, _ := env.do(t, env.client, "POST", "/api/github/installations/999/accept", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("accept an unknown installation: %d", res.StatusCode)
	}
	if res, _ := env.do(t, env.client, "POST", "/api/github/installations/x/accept", ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("accept a bad id: %d", res.StatusCode)
	}
	if res, _ := env.do(t, env.anon, "POST", "/api/github/installations/8/accept", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("accept signed out: %d", res.StatusCode)
	}
	res, view = env.do(t, env.client, "POST", "/api/github/installations/8/accept", "")
	if res.StatusCode != http.StatusOK || githubNames(view, "installations", "account") != "acme,alice,mallory" ||
		githubNames(view, "pending", "account") != "" || githubNames(view, "repos", "full_name") != "acme/web,alice/One,alice/two,mallory/trap" {
		t.Fatalf("accept: %d %#v", res.StatusCode, view)
	}
	if got := env.srv.Settings().GitHub.AcceptedInstallations; fmt.Sprint(got) != "[5 6 8]" {
		t.Fatalf("accepted %v", got)
	}
	// A restart keeps them.
	if again := env.srv.newGitHub(); !again.Accepted(6) || !again.Accepted(8) {
		t.Fatal("the accepted installations are not loaded again")
	}

	// Remove uninstalls it on GitHub and forgets it.
	res, view = env.do(t, env.client, "DELETE", "/api/github/installations/8", "")
	if res.StatusCode != http.StatusOK || githubNames(view, "installations", "account") != "acme,alice" ||
		githubNames(view, "pending", "account") != "" || strings.Contains(githubNames(view, "repos", "full_name"), "mallory") {
		t.Fatalf("remove: %d %#v", res.StatusCode, view)
	}
	if got := env.srv.Settings().GitHub.AcceptedInstallations; fmt.Sprint(got) != "[5 6]" || env.srv.ghApp.Accepted(8) {
		t.Fatalf("accepted after the removal %v", got)
	}
	// Gone on GitHub already is no error.
	if res, _ := env.do(t, env.client, "DELETE", "/api/github/installations/8", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("remove twice: %d", res.StatusCode)
	}

	// A document from before installations were accepted: the owner's is
	// accepted again on its own, the others wait.
	if err := env.srv.editGitHub(func(g *config.GitHubSettings) { g.AcceptedInstallations = nil }); err != nil {
		t.Fatal(err)
	}
	env.srv.ghApp = env.srv.newGitHub()
	_, view = env.do(t, env.client, "GET", "/api/github", "")
	if githubNames(view, "installations", "account") != "alice" || githubNames(view, "pending", "account") != "acme" {
		t.Fatalf("adopted: %#v", view)
	}
	if got := env.srv.Settings().GitHub.AcceptedInstallations; fmt.Sprint(got) != "[5]" {
		t.Fatalf("adopted kept as %v", got)
	}

	// Disconnect forgets what was accepted.
	env.do(t, env.client, "DELETE", "/api/github/app", "")
	if got := env.srv.Settings().GitHub.AcceptedInstallations; got != nil || env.srv.ghApp.Accepted(5) {
		t.Fatalf("accepted after disconnect %v", got)
	}
}

func TestGitHubInstallStatesExpire(t *testing.T) {
	var f githubFlow
	now := time.Now()
	f.addInstall("old", now.Add(-16*time.Minute))
	f.addInstall("new", now.Add(-14*time.Minute))
	if f.takeInstall("old", now) || !f.takeInstall("new", now) || f.takeInstall("new", now) || f.takeInstall("", now) {
		t.Fatal("install states")
	}
	// A manifest state is no install state.
	f.add("manifest", now)
	if f.takeInstall("manifest", now) || !f.take("manifest", now) {
		t.Fatal("the two kinds of state mix")
	}
}
