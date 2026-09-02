package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestVersionIsAShortStableHash(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(version) {
		t.Fatalf("version %q is not a 12 character hash", version)
	}
	raw := readAll()
	if again := hashOf(raw); again != version {
		t.Fatalf("hash is not stable: %q then %q", version, again)
	}
	// A changed file has to move every address that names it.
	raw["js/session.js"] = append(raw["js/session.js"], byte('\n'))
	if changed := hashOf(raw); changed == version {
		t.Fatal("a changed file left the version alone")
	}
}

// The whole point of the stamp: a document can only ask for the scripts of its
// own build, so a script kept from an earlier one is never handed to it.
func TestPagesAskForStampedAssets(t *testing.T) {
	for _, page := range []string{"index.html", "admin.html", "login.html", "setup.html"} {
		rec := httptest.NewRecorder()
		ServePage(rec, httptest.NewRequest(http.MethodGet, "/", nil), page)
		body := rec.Body.String()
		for _, ref := range regexp.MustCompile(`["'](/static/[^"']*|/favicon\.png[^"']*)["']`).FindAllStringSubmatch(body, -1) {
			if !strings.Contains(ref[1], "?v="+version) {
				t.Errorf("%s asks for %s without the build stamp", page, ref[1])
			}
		}
		if !strings.Contains(body, "/static/css/app.css?v="+version) {
			t.Errorf("%s does not carry a stamped stylesheet", page)
		}
	}
}

// A relative import does not inherit the query of the file it is written in,
// so every module has to be stamped or only the entry point is versioned.
func TestModuleImportsAreStamped(t *testing.T) {
	relative := regexp.MustCompile(`from\s+["'](\.[^"']+)["']`)
	seen := 0
	for name, data := range assets {
		if !strings.HasSuffix(name, ".js") || name == "sw.js" {
			continue
		}
		for _, m := range relative.FindAllStringSubmatch(string(data), -1) {
			seen++
			if !strings.Contains(m[1], "?v="+version) {
				t.Errorf("%s imports %s without the build stamp", name, m[1])
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no module imports at all, so this proves nothing")
	}
	if body := get(t, "/js/session.js"); !strings.Contains(body, "from './api.js?v="+version+"'") {
		t.Error("session.js does not import api.js at a stamped address")
	}
}

func TestServiceWorkerCarriesTheVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	ServiceWorker(rec, httptest.NewRequest(http.MethodGet, "/sw.js", nil))
	body := rec.Body.String()
	if strings.Contains(body, "__VERSION__") {
		t.Fatal("the worker went out with its placeholder unreplaced")
	}
	if !strings.Contains(body, "'"+version+"'") {
		t.Fatalf("the worker does not name this build:\n%s", body[:200])
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("the worker must be revalidated, got %q", got)
	}
}

// The offline shell is only whole if it holds the entire import graph the
// session page boots from. A module that is imported but not precached turns
// "open Socrates in a tunnel" back into the browser's error page, which is the
// one thing the worker exists to prevent - and it is the kind of omission a
// new import makes silently.
func TestServiceWorkerShellHoldsTheWholeChatImportGraph(t *testing.T) {
	worker := string(assets["sw.js"])
	relative := regexp.MustCompile(`from\s+["']\./([^"'?]+)`)
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		data, ok := assets["js/"+name]
		if !ok {
			t.Fatalf("session.js's import graph names js/%s, which is not embedded", name)
		}
		for _, m := range relative.FindAllStringSubmatch(string(data), -1) {
			walk(m[1])
		}
	}
	walk("session.js")
	if len(seen) < 5 {
		t.Fatalf("walked only %d modules, so this proves nothing", len(seen))
	}
	for name := range seen {
		if !strings.Contains(worker, "'/static/js/"+name+"'") {
			t.Errorf("sw.js does not precache /static/js/%s, which the session page imports", name)
		}
	}
	// The page itself and its stylesheet are what everything else hangs off.
	for _, path := range []string{"'/'", "'/static/css/app.css'"} {
		if !strings.Contains(worker, path) {
			t.Errorf("sw.js does not precache %s", path)
		}
	}
}

// The terminal is vendored, not fetched from a CDN, so every file index.html
// loads with a <script> or <link> tag has to be embedded, stamped and
// precached. A vendored file that is not in SHELL is an app that cannot open
// a terminal offline, which is most of what the offline story is for.
func TestVendoredTerminalIsStampedAndPrecached(t *testing.T) {
	rec := httptest.NewRecorder()
	ServePage(rec, httptest.NewRequest(http.MethodGet, "/", nil), "index.html")
	page := rec.Body.String()
	worker := string(assets["sw.js"])

	refs := regexp.MustCompile(`(?:src|href)="(/static/vendor/[^"?]+)\?v=([0-9a-f]{12})"`).FindAllStringSubmatch(page, -1)
	if len(refs) < 7 {
		t.Fatalf("index.html loads %d vendored files; the terminal is six scripts and a stylesheet", len(refs))
	}
	for _, ref := range refs {
		if ref[2] != version {
			t.Errorf("%s carries the stamp %s, not this build's %s", ref[1], ref[2], version)
		}
		if assets[strings.TrimPrefix(ref[1], "/static/")] == nil {
			t.Errorf("%s is referenced but not embedded", ref[1])
		}
		if !strings.Contains(worker, "'"+ref[1]+"'") {
			t.Errorf("sw.js does not precache %s, which index.html loads", ref[1])
		}
	}
	// The maps are 1.9 MB for xterm alone and would be precached with
	// everything else, so they are deliberately not shipped.
	for name := range assets {
		if strings.HasPrefix(name, "vendor/") && strings.HasSuffix(name, ".js.map") {
			t.Errorf("%s is embedded; the source maps are not shipped", name)
		}
	}
}

// The old web chat's modules went with it. A file that survived a deletion is
// how a page ends up importing half a product.
//
// js/chat.js is not among them any more: the name has come back for something
// else entirely - the conversation beside a terminal, which asks a model about
// the pane and can have it typed into. What was deleted was a chat that *was*
// the product; this one is a panel on the session page.
func TestTheChatModulesAreGone(t *testing.T) {
	for _, name := range []string{"js/markdown.js", "js/models.js", "js/agents.js"} {
		if assets[name] != nil {
			t.Errorf("%s is still embedded", name)
		}
	}
}

// A stamped address names one exact version of one file and can never mean
// anything else; an unstamped one has to be asked about every time.
func TestStampedAssetsAreKeptUnstampedOnesRevalidated(t *testing.T) {
	stamped := do(t, "/js/session.js?v="+version)
	if got := stamped.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("stamped asset is not cacheable for good, got %q", got)
	}
	plain := do(t, "/js/session.js")
	if got := plain.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("unstamped asset must be revalidated, got %q", got)
	}
	if stamped.Body.String() != plain.Body.String() {
		t.Fatal("the stamp changed what was served")
	}
}

// The documents have their own routes, and those routes are where signing in
// is decided. Serving them from here as well would be a way around it.
func TestPagesAreNotServedAsAssets(t *testing.T) {
	for _, page := range []string{"/index.html", "/admin.html", "/login.html", "/setup.html"} {
		if code := do(t, page).Code; code != http.StatusNotFound {
			t.Errorf("GET /static%s = %d, want 404: a page has to come from its own route", page, code)
		}
	}
	// The assets the login and setup pages need are public, or nobody could
	// ever get as far as signing in.
	for _, file := range []string{"/css/app.css", "/js/net.js", "/img/logo.png"} {
		if code := do(t, file).Code; code != http.StatusOK {
			t.Errorf("GET /static%s = %d, want 200", file, code)
		}
	}
}

func TestMissingAssetIsNotFound(t *testing.T) {
	if code := do(t, "/js/nothing.js").Code; code != http.StatusNotFound {
		t.Fatalf("want 404 for a file that is not there, got %d", code)
	}
	if code := do(t, "/../embed.go").Code; code != http.StatusNotFound {
		t.Fatalf("want 404 for a path that climbs out, got %d", code)
	}
}

func do(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	http.StripPrefix("/static/", Static()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static"+path, nil))
	return rec
}

func get(t *testing.T, path string) string {
	t.Helper()
	rec := do(t, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", path, rec.Code)
	}
	return rec.Body.String()
}
