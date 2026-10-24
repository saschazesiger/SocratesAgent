package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
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
	for _, page := range []string{"index.html", "admin.html", "costs.html", "login.html", "setup.html"} {
		rec := httptest.NewRecorder()
		ServePage(rec, httptest.NewRequest(http.MethodGet, "/", nil), page)
		body := rec.Body.String()
		for _, ref := range regexp.MustCompile(`["'](/static/[^"']*|/favicon\.png[^"']*|/apple-touch-icon[^"']*)["']`).FindAllStringSubmatch(body, -1) {
			if !strings.Contains(ref[1], "?v="+version) {
				t.Errorf("%s asks for %s without the build stamp", page, ref[1])
			}
		}
		// Every page wears Dashboard-Style the documented way: its stylesheet
		// and its one classic runtime, then the project's theme and shared
		// components on top.
		for _, want := range []string{
			"/static/vendor/dashboard-style/ds.css", "/static/vendor/dashboard-style/ds.js",
			"/static/css/theme.css", "/static/css/so-shared.css",
		} {
			if !strings.Contains(body, want+"?v="+version) {
				t.Errorf("%s does not ask for a stamped %s", page, want)
			}
		}
		if strings.Contains(body, "/static/css/app.css") {
			t.Errorf("%s still asks for the old app.css", page)
		}
	}
}

func TestTerminalFontsAreVersionedAndPrecached(t *testing.T) {
	css := string(asset("css/theme.css"))
	worker := string(asset("sw.js"))
	for _, name := range []string{"SourceCodePro-Regular.woff2", "SourceCodePro-Bold.woff2"} {
		if !strings.Contains(css, "/static/fonts/"+name+"?v="+version) {
			t.Errorf("font %s has no versioned CSS URL", name)
		}
		if !strings.Contains(worker, "'/static/fonts/"+name+"'") {
			t.Errorf("font %s is not precached", name)
		}
		if data := asset("fonts/" + name); len(data) < 4 || string(data[:4]) != "wOF2" {
			t.Errorf("font %s is not an embedded WOFF2 file", name)
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
	// Modules import each other as ./name.js and the vendored ones as
	// ../vendor/...: both are part of the graph.
	relative := regexp.MustCompile(`from\s+["'](\./|\.\./vendor/)([^"'?]+)`)
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		data, ok := assets[name]
		if !ok {
			t.Fatalf("session.js's import graph names %s, which is not embedded", name)
		}
		for _, m := range relative.FindAllStringSubmatch(string(data), -1) {
			if m[1] == "./" {
				walk(path.Join(path.Dir(name), m[2]))
			} else {
				walk("vendor/" + m[2])
			}
		}
	}
	walk("js/session.js")
	if len(seen) < 5 {
		t.Fatalf("walked only %d modules, so this proves nothing", len(seen))
	}
	for name := range seen {
		if !strings.Contains(worker, "'/static/"+name+"'") {
			t.Errorf("sw.js does not precache /static/%s, which the session page imports", name)
		}
	}
	// The page itself and its stylesheets are what everything else hangs off.
	for _, path := range []string{"'/'", "'/static/css/theme.css'", "'/static/css/so-shared.css'"} {
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
	if len(refs) < 6 {
		t.Fatalf("index.html loads %d vendored files; the terminal is five scripts and a stylesheet", len(refs))
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

// Opening the session page in a tunnel needs every file its head and body
// load, not only the modules it imports: a stylesheet that is not in SHELL is
// a page that comes up unstyled offline, and a font fonts.css names but the
// worker does not hold is text in a fallback face. The list is checked against
// what index.html actually asks for, so a new <link> or <script> cannot be
// added without the worker hearing of it.
func TestSessionPageAssetsArePrecached(t *testing.T) {
	rec := httptest.NewRecorder()
	ServePage(rec, httptest.NewRequest(http.MethodGet, "/", nil), "index.html")
	page := rec.Body.String()
	worker := string(assets["sw.js"])

	tags := regexp.MustCompile(`<link rel="stylesheet" href="([^"?]+)|<script[^>]* src="([^"?]+)`).FindAllStringSubmatch(page, -1)
	if len(tags) < 10 {
		t.Fatalf("index.html loads only %d stylesheets and scripts, so this proves nothing", len(tags))
	}
	for _, tag := range tags {
		path := tag[1] + tag[2]
		if !strings.Contains(worker, "'"+path+"'") {
			t.Errorf("sw.js does not precache %s, which index.html loads", path)
		}
	}

	fonts := string(assets["vendor/dashboard-style/fonts/fonts.css"])
	urls := regexp.MustCompile(`url\(['"]?([^)'"?]+)`).FindAllStringSubmatch(fonts, -1)
	if len(urls) == 0 {
		t.Fatal("fonts.css names no font files, so this proves nothing")
	}
	for _, u := range urls {
		path := "/static/vendor/dashboard-style/fonts/" + u[1]
		if assets[strings.TrimPrefix(path, "/static/")] == nil {
			t.Errorf("fonts.css names %s, which is not embedded", path)
		}
		if !strings.Contains(worker, "'"+path+"'") {
			t.Errorf("sw.js does not precache %s, which fonts.css names", path)
		}
	}
}

// The old web chat's modules went with it, and so did the panel that later
// took the name: nothing on the session page asks a model to type any more.
// A file that survived a deletion is how a page ends up importing half a
// product.
func TestTheChatModulesAreGone(t *testing.T) {
	for _, name := range []string{"js/markdown.js", "js/models.js", "js/agents.js", "js/chat.js"} {
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
	for _, page := range []string{"/index.html", "/admin.html", "/costs.html", "/login.html", "/setup.html"} {
		if code := do(t, page).Code; code != http.StatusNotFound {
			t.Errorf("GET /static%s = %d, want 404: a page has to come from its own route", page, code)
		}
	}
	// The assets the login and setup pages need are public, or nobody could
	// ever get as far as signing in.
	for _, file := range []string{
		"/css/theme.css", "/css/so-shared.css", "/css/so-pages.css",
		"/vendor/dashboard-style/ds.css", "/vendor/dashboard-style/ds.js", "/vendor/dashboard-style/fonts/fonts.css",
		"/js/net.js", "/js/ui.js", "/img/logo.png",
	} {
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

// iOS asks the root for the home screen icon on its own, and it gets the very
// bytes the pages link to, as a PNG, under both of the names it asks by.
func TestAppleTouchIconAtTheRoot(t *testing.T) {
	want := asset("img/apple-touch-icon.png")
	if want == nil {
		t.Skip("the home screen icon is not in the build")
	}
	for _, path := range []string{"/apple-touch-icon.png", "/apple-touch-icon-precomposed.png"} {
		rec := httptest.NewRecorder()
		AppleTouchIcon(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("%s = %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		if !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("%s is not the icon the pages link to", path)
		}
	}
}

// Dashboard-Style is vendored, not edited: the copy has to match the
// SHA256SUMS that vendor.sh wrote next to it, with no file missing and none
// added, or `make vendor-ds` would silently throw a local change away.
func TestVendoredDashboardStyleIsUntouched(t *testing.T) {
	const dir = "static/vendor/dashboard-style"
	sums, err := files.ReadFile(dir + "/SHA256SUMS")
	if err != nil {
		t.Fatalf("no vendored Dashboard-Style: %v", err)
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		want, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("SHA256SUMS line %q is not in sha256sum format", line)
		}
		listed[name] = true
		data, err := files.ReadFile(dir + "/" + name)
		if err != nil {
			t.Errorf("%s is listed but missing", name)
			continue
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
			t.Errorf("%s was edited: the vendored copy is replaced as a whole by make vendor-ds", name)
		}
	}
	for name := range assets {
		rel, ok := strings.CutPrefix(name, "vendor/dashboard-style/")
		if ok && rel != "SHA256SUMS" && !listed[rel] {
			t.Errorf("%s was added to the vendored copy", name)
		}
	}
	if !listed["ds.css"] || !listed["ds.js"] {
		t.Fatal("the vendored copy has no ds.css or ds.js")
	}
}

// The chat view's Markdown renderer and sanitizer are vendored as published:
// the copy has to match vendor/markdown/SHA256SUMS, with nothing missing and
// nothing added, or `make vendor-markdown` would throw a local change away -
// and a sanitizer edited by hand is not the sanitizer that was reviewed.
func TestVendoredMarkdownIsUntouched(t *testing.T) {
	const dir = "static/vendor/markdown"
	sums, err := files.ReadFile(dir + "/SHA256SUMS")
	if err != nil {
		t.Fatalf("no vendored Markdown renderer: %v", err)
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		want, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("SHA256SUMS line %q is not in sha256sum format", line)
		}
		listed[name] = true
		data, err := files.ReadFile(dir + "/" + name)
		if err != nil {
			t.Errorf("%s is listed but missing", name)
			continue
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
			t.Errorf("%s was edited: the vendored copy is replaced as a whole by make vendor-markdown", name)
		}
	}
	for name := range assets {
		rel, ok := strings.CutPrefix(name, "vendor/markdown/")
		if ok && rel != "SHA256SUMS" && rel != "VERSIONS" && !listed[rel] {
			t.Errorf("%s was added to the vendored copy", name)
		}
	}
	for _, name := range []string{"marked.esm.js", "purify.es.js", "LICENSE-marked", "LICENSE-dompurify"} {
		if !listed[name] {
			t.Errorf("SHA256SUMS does not cover %s", name)
		}
	}
}

// The vendored fonts.css names its files relative to itself, and a relative
// address does not inherit the stylesheet's query: it has to be stamped too.
// A data: mask in ds.css must come through untouched.
func TestDashboardStyleURLsAreStamped(t *testing.T) {
	fonts := string(asset("vendor/dashboard-style/fonts/fonts.css"))
	if !strings.Contains(fonts, "google-sans-latin-opsz-normal.woff2?v="+version) {
		t.Error("fonts.css names its font without the build stamp")
	}
	ds := string(asset("vendor/dashboard-style/ds.css"))
	if strings.Contains(ds, "data:image/svg+xml") && regexp.MustCompile(`data:[^)'"]*\?v=`).MatchString(ds) {
		t.Error("a data: URL in ds.css was stamped")
	}
}
