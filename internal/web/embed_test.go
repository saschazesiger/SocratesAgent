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
	raw["js/chat.js"] = append(raw["js/chat.js"], byte('\n'))
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
	if body := get(t, "/js/chat.js"); !strings.Contains(body, "from './api.js?v="+version+"'") {
		t.Error("chat.js does not import api.js at a stamped address")
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

// The offline shell is only whole if it holds the entire import graph the chat
// page boots from. A module that is imported but not precached turns "open
// Socrates in a tunnel" back into the browser's error page, which is the one
// thing the worker exists to prevent - and it is the kind of omission a new
// import makes silently.
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
			t.Fatalf("chat.js's import graph names js/%s, which is not embedded", name)
		}
		for _, m := range relative.FindAllStringSubmatch(string(data), -1) {
			walk(m[1])
		}
	}
	walk("chat.js")
	if len(seen) < 5 {
		t.Fatalf("walked only %d modules, so this proves nothing", len(seen))
	}
	for name := range seen {
		if !strings.Contains(worker, "'/static/js/"+name+"'") {
			t.Errorf("sw.js does not precache /static/js/%s, which the chat page imports", name)
		}
	}
	// The page itself and its stylesheet are what everything else hangs off.
	for _, path := range []string{"'/'", "'/static/css/app.css'"} {
		if !strings.Contains(worker, path) {
			t.Errorf("sw.js does not precache %s", path)
		}
	}
}

// A stamped address names one exact version of one file and can never mean
// anything else; an unstamped one has to be asked about every time.
func TestStampedAssetsAreKeptUnstampedOnesRevalidated(t *testing.T) {
	stamped := do(t, "/js/chat.js?v="+version)
	if got := stamped.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("stamped asset is not cacheable for good, got %q", got)
	}
	plain := do(t, "/js/chat.js")
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
