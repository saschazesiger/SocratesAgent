package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeGitHubAPI is GitHub's API as Admin's GitHub card uses it: the manifest
// conversion, one installation on alice with two repositories, and tokens.
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/app-manifests/good-code/conversions":
			answer(w, 201, map[string]any{
				"id": 99, "slug": "socrates-test", "name": "Socrates test", "html_url": "https://web.example/apps/socrates-test",
				"client_id": "Iv1.x", "client_secret": "client-secret-value", "webhook_secret": "hook-secret-value", "pem": pemText,
				"owner": map[string]any{"login": "alice", "type": "User"},
			})
		case r.Method == "GET" && r.URL.Path == "/app/installations":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				answer(w, 401, map[string]any{"message": "no JWT"})
				return
			}
			answer(w, 200, []map[string]any{{"id": 5, "account": map[string]any{"login": "alice", "type": "User"}}})
		case r.Method == "POST" && r.URL.Path == "/app/installations/5/access_tokens":
			answer(w, 201, map[string]any{"token": "ghs_test", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case r.Method == "GET" && r.URL.Path == "/installation/repositories":
			if r.Header.Get("Authorization") != "token ghs_test" {
				answer(w, 401, map[string]any{"message": "Bad credentials"})
				return
			}
			answer(w, 200, map[string]any{"repositories": []map[string]any{
				{"full_name": "alice/One", "default_branch": "main", "private": true},
				{"full_name": "alice/two", "default_branch": "trunk", "private": false},
			}})
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
	if view["connected"] != false || view["problem"] != "" {
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
		manifest["setup_url"] != env.server.URL+"/api/github/app/installed" || manifest["public"] != false ||
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
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || loc != "https://web.example/apps/socrates-test/installations/new" {
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
	if view["connected"] != true || view["problem"] != "" {
		t.Fatalf("connected: %#v", view)
	}
	app := view["app"].(map[string]any)
	if app["name"] != "Socrates test" || app["settings_url"] != "https://web.example/settings/apps/socrates-test" {
		t.Fatalf("app: %#v", app)
	}
	installs := view["installations"].([]any)
	if len(installs) != 1 || installs[0].(map[string]any)["settings_url"] != "https://web.example/settings/installations/5" {
		t.Fatalf("installations: %#v", installs)
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
