package server

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// docsEnv is a signed-in server whose workspace root is a directory of the
// test's, with a project in it, and a session working in a folder outside
// the root. It returns the project and the session's folder.
func docsEnv(t *testing.T) (*testEnv, string, string) {
	t.Helper()
	e := boardEnv(t)
	ws := filepath.Join(t.TempDir(), "workspace")
	project := filepath.Join(ws, "project")
	writeTree(t, project, map[string]string{
		"README.md":           "# Project\n\n<script>alert(1)</script>\n",
		"docs/ARCH.md":        "# Architecture\n\n```mermaid\ngraph TD; A-->B\n```\n",
		"docs/guide/index.md": "# Guide",
		"docs/img/flow.svg":   `<svg xmlns="http://www.w3.org/2000/svg"/>`,
		"docs/img/shot.png":   "\x89PNG\r\n\x1a\nfake",
		"main.go":             "package main",
		".env":                "SECRET=1",
		".git/config":         "[core]",
		".git/notes.md":       "# hidden",
	})
	e.srv.mu.Lock()
	e.srv.settings.Workspace.Root = ws
	e.srv.mu.Unlock()

	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	writeTree(t, elsewhere, map[string]string{"NOTES.md": "# Notes"})
	if err := e.store.CreateSession(&store.Session{ID: "s1", Title: "A chat", Harness: "claude",
		Workdir: elsewhere, WorkdirMode: "custom"}); err != nil {
		t.Fatal(err)
	}
	return e, project, elsewhere
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// docsBase lists a folder's docs and returns the address its pages are at.
func docsBase(t *testing.T, e *testEnv, folder string) (string, map[string]any) {
	t.Helper()
	res, body := e.do(t, e.client, "GET", "/api/docs?folder="+url.QueryEscape(folder), "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list %s: %d %v", folder, res.StatusCode, body)
	}
	return body["base"].(string), body
}

// get asks for an address without a cookie, as a sandboxed page does.
func get(t *testing.T, e *testEnv, path string, header ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", e.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := e.anon.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

// The sidebar learns which folders have docs: a project inside the root and
// a session's folder do, a folder without Markdown does not, and neither
// does one Socrates does not work in.
func TestDocsProbe(t *testing.T) {
	e, project, elsewhere := docsEnv(t)
	bare := filepath.Join(filepath.Dir(project), "bare")
	writeTree(t, bare, map[string]string{"main.go": ""})
	stranger := t.TempDir()
	writeTree(t, stranger, map[string]string{"README.md": ""})
	// The project is a GitHub checkout: the probe says where its files are.
	writeTree(t, project, map[string]string{".git/config": "[remote \"origin\"]\n\turl = https://github.com/Acme/Guide.git\n"})

	body := `{"folders":["` + project + `/","` + elsewhere + `","` + bare + `","` + stranger + `","#more",""]}`
	for round := 0; round < 2; round++ {
		res, got := e.do(t, e.client, "POST", "/api/docs/probe", body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("probe: %d %v", res.StatusCode, got)
		}
		m := got["docs"].(map[string]any)
		if m[project+"/"] != true || m[elsewhere] != true || m[bare] != false || m[stranger] != false ||
			m["#more"] != false || len(m) != 6 {
			t.Fatalf("probe round %d: %v", round, m)
		}
		repos := got["repos"].(map[string]any)
		repo, _ := repos[project+"/"].(map[string]any)
		if repo == nil || repo["remote"] != "github.com/acme/guide" || repo["prefix"] != "" || repos[bare] != nil || repos[stranger] != nil {
			t.Fatalf("probe round %d repos: %v", round, repos)
		}
	}

	many := `{"folders":[` + strings.TrimSuffix(strings.Repeat(`"/x",`, maxDocsProbe+1), ",") + `]}`
	if res, _ := e.do(t, e.client, "POST", "/api/docs/probe", many); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("too many folders: %d", res.StatusCode)
	}
	if res, _ := e.do(t, e.anon, "GET", "/api/docs?folder="+url.QueryEscape(project), ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("listing without a login: %d", res.StatusCode)
	}
}

// A folder's listing is its Markdown, outside hidden directories, with the
// address of its pages and the page it opens on.
func TestDocsListing(t *testing.T) {
	e, project, elsewhere := docsEnv(t)
	base, body := docsBase(t, e, project+"/")
	if body["folder"] != project || body["readme"] != "README.md" || body["truncated"] != false ||
		!strings.HasPrefix(base, "/docs/") || !strings.HasSuffix(base, "/") {
		t.Fatalf("listing: %v", body)
	}
	files := body["files"].([]any)
	var names []string
	for _, f := range files {
		m := f.(map[string]any)
		names = append(names, m["path"].(string))
		if m["mtime"].(float64) == 0 || m["size"].(float64) == 0 {
			t.Fatalf("file without size or time: %v", m)
		}
	}
	if strings.Join(names, ",") != "README.md,docs/ARCH.md,docs/guide/index.md" {
		t.Fatalf("files: %v", names)
	}

	// Without a README, the first file is the home page.
	_, body = docsBase(t, e, elsewhere)
	if body["readme"] != "NOTES.md" {
		t.Fatalf("home without a README: %v", body["readme"])
	}

	for _, folder := range []string{t.TempDir(), filepath.Join(project, "missing"), filepath.Join(project, ".git"),
		filepath.Dir(project), "relative/path"} {
		res, _ := e.do(t, e.client, "GET", "/api/docs?folder="+url.QueryEscape(folder), "")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("folder %s: %d, want 404", folder, res.StatusCode)
		}
	}
	if res, _ := e.do(t, e.client, "GET", "/api/docs?folder=", ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("no folder: %d", res.StatusCode)
	}
}

// A Markdown page goes out like an artifact's page, sandboxed, with its text
// escaped inside the kit's document; ?raw=1 is the text itself.
func TestDocsPage(t *testing.T) {
	e, project, _ := docsEnv(t)
	base, _ := docsBase(t, e, project)

	res, page := get(t, e, base+"README.md")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("page: %d %s", res.StatusCode, page)
	}
	for k, v := range map[string]string{
		"Content-Type":           "text/html; charset=utf-8",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "private, no-cache",
	} {
		if got := res.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// A doc's own HTML runs nothing: scripts only from the kit and Mermaid,
	// no request of its own.
	host := strings.TrimPrefix(e.server.URL, "http://")
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads;",
		"frame-ancestors 'self'", "script-src 'self' " + host + "/artifact-kit/ https://cdn.jsdelivr.net/npm/mermaid@11/;",
		"connect-src 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("the page's policy %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("the page's policy %q lets the page out", csp)
	}
	for _, want := range []string{`<title>README.md</title>`, `<body data-docs="README.md">`,
		`<main class="page" data-format="markdown">`, "&lt;script&gt;alert(1)&lt;/script&gt;", "/artifact-kit/kit.js"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>alert") {
		t.Fatal("the Markdown went into the page unescaped")
	}

	res, raw := get(t, e, base+"docs/ARCH.md?raw=1")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		res.Header.Get("Content-Security-Policy") != "sandbox" || res.Header.Get("Access-Control-Allow-Origin") != "" ||
		!strings.HasPrefix(raw, "# Architecture") {
		t.Fatalf("raw: %d %v %q", res.StatusCode, res.Header, raw)
	}

	if res, _ := get(t, e, base+"docs/ARCH.md"); res.StatusCode != http.StatusOK {
		t.Fatalf("a nested page: %d", res.StatusCode)
	}
	req, _ := http.NewRequest("HEAD", e.server.URL+base+"README.md", nil)
	if res, err := e.anon.Do(req); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: %v %v", res, err)
	}
	req, _ = http.NewRequest("POST", e.server.URL+base+"README.md", nil)
	if res, err := e.anon.Do(req); err != nil || res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %v %v", res, err)
	}
}

// An image goes as its type, a sandbox any origin may read, with ranges and
// conditional requests.
func TestDocsImage(t *testing.T) {
	e, project, _ := docsEnv(t)
	base, _ := docsBase(t, e, project)
	res, body := get(t, e, base+"docs/img/flow.svg")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/svg+xml" ||
		res.Header.Get("Content-Security-Policy") != "sandbox" || res.Header.Get("Access-Control-Allow-Origin") != "*" ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Cache-Control") != "private, no-cache" ||
		!strings.HasPrefix(body, "<svg") {
		t.Fatalf("svg: %d %v %q", res.StatusCode, res.Header, body)
	}
	res, body = get(t, e, base+"docs/img/shot.png", "Range", "bytes=0-3")
	if res.StatusCode != http.StatusPartialContent || res.Header.Get("Content-Type") != "image/png" || body != "\x89PNG" {
		t.Fatalf("range: %d %v %q", res.StatusCode, res.Header, body)
	}
	res, _ = get(t, e, base+"docs/img/shot.png", "If-Modified-Since", res.Header.Get("Last-Modified"))
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional: %d", res.StatusCode)
	}
}

// A directory and the folder itself open on their README; a directory
// without one is not found.
func TestDocsRedirects(t *testing.T) {
	e, project, _ := docsEnv(t)
	base, _ := docsBase(t, e, project)
	for path, want := range map[string]string{
		"":            base + "README.md",
		"docs/guide":  base + "docs/guide/index.md",
		"docs/guide/": base + "docs/guide/index.md",
	} {
		res, _ := get(t, e, base+path)
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != want {
			t.Errorf("%q: %d -> %q, want %q", path, res.StatusCode, res.Header.Get("Location"), want)
		}
	}
	writeTree(t, project, map[string]string{"my docs/Über uns.md": "x", "my docs/README.md": "x"})
	res, _ := get(t, e, base+"my%20docs")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != base+"my%20docs/README.md" {
		t.Fatalf("escaped redirect: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := get(t, e, base+"my%20docs/%C3%9Cber%20uns.md"); res.StatusCode != http.StatusOK {
		t.Fatalf("an escaped name: %d", res.StatusCode)
	}
	if res, _ := get(t, e, base+"docs/img"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a directory without a README: %d", res.StatusCode)
	}
}

// Nothing outside the folder's Markdown and images is reached: not a path
// out of it, a hidden file, another kind of file, a link pointing out, nor
// anything under a token the server did not sign or a folder no longer
// shown.
func TestDocsRefusals(t *testing.T) {
	e, project, _ := docsEnv(t)
	base, _ := docsBase(t, e, project)
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"secret.md": "# secret"})
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(project, "out.md")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../project/README.md", "..%2fproject%2fREADME.md", "docs/..%2f..%2fREADME.md",
		".env", ".git/config", ".git/notes.md", "main.go", "out.md", "missing.md", "%2e%2e/README.md"} {
		res, body := get(t, e, base+p)
		// The router cleans a path with ".." by a redirect to the
		// cleaned one, which then names no docs.
		if loc := res.Header.Get("Location"); res.StatusCode >= 300 && res.StatusCode < 400 && loc != "" {
			res, body = get(t, e, loc)
		}
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d %q", p, res.StatusCode, body)
		}
		if strings.Contains(body, "SECRET") || strings.Contains(body, "secret") || strings.Contains(body, "[core]") {
			t.Errorf("%s leaked: %q", p, body)
		}
	}

	token := strings.TrimSuffix(strings.TrimPrefix(base, "/docs/"), "/")
	dot := strings.LastIndex(token, ".")
	forged := token[:dot+1] + strings.Repeat("A", docsSigLen)
	other, _ := e.srv.docsToken(outside)
	for _, bad := range []string{forged, "x", token[:dot], token + "A", "!!!." + token[dot+1:], other} {
		res, body := get(t, e, "/docs/"+bad+"/README.md")
		if res.StatusCode != http.StatusNotFound || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("token %q: %d %q", bad, res.StatusCode, body)
		}
	}

	// The token outlives a folder that is no longer shown, the page does not.
	e.srv.mu.Lock()
	e.srv.settings.Workspace.Root = t.TempDir()
	e.srv.mu.Unlock()
	if res, _ := get(t, e, base+"README.md"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a folder no longer shown: %d", res.StatusCode)
	}
}

// One file's size and time, for a page reloading what changed on disk.
func TestDocsStat(t *testing.T) {
	e, project, _ := docsEnv(t)
	q := func(path string) string {
		return "/api/docs/stat?folder=" + url.QueryEscape(project) + "&path=" + url.QueryEscape(path)
	}
	res, body := e.do(t, e.client, "GET", q("docs/ARCH.md"), "")
	if res.StatusCode != http.StatusOK || body["path"] != "docs/ARCH.md" || body["size"].(float64) == 0 ||
		body["mtime"].(float64) == 0 || body["kind"] != "markdown" {
		t.Fatalf("stat: %d %v", res.StatusCode, body)
	}
	res, body = e.do(t, e.client, "GET", q("docs/guide"), "")
	if res.StatusCode != http.StatusOK || body["path"] != "docs/guide/index.md" {
		t.Fatalf("stat of a directory: %d %v", res.StatusCode, body)
	}
	for _, p := range []string{"gone.md", ".env", "../x.md", ""} {
		if res, _ := e.do(t, e.client, "GET", q(p), ""); res.StatusCode != http.StatusNotFound {
			t.Errorf("stat %q: %d", p, res.StatusCode)
		}
	}
}

// The key the tokens are signed with is made once and kept, so a token
// stays good across restarts.
func TestDocsKeyIsKept(t *testing.T) {
	e, project, _ := docsEnv(t)
	a, err := e.srv.docsToken(project)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.docsKeep.key = nil
	b, _ := e.srv.docsToken(project)
	if a != b {
		t.Fatalf("token changed: %s %s", a, b)
	}
	var stored string
	if err := e.store.GetJSON(docsKeyName, &stored); err != nil || len(stored) != 64 {
		t.Fatalf("stored key: %q %v", stored, err)
	}
}
