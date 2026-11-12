//go:build linux

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

// pushDocs commits files (path -> content) onto main of the fake GitHub's
// repository.
func (f *fakeGitHub) pushDocs(t *testing.T, name string, files map[string]string) {
	t.Helper()
	work := t.TempDir()
	testGit(t, work, "clone", "--quiet", "--branch", "main", f.base+"/"+name+".git", ".")
	writeTree(t, work, files)
	testGit(t, work, "add", "-A")
	testGit(t, work, "commit", "--quiet", "-m", "docs")
	testGit(t, work, "push", "--quiet", "origin", "main")
}

// A repository's group has Docs: the Markdown files and pictures of its
// default branch as GitHub has them, for a repository some chat works in,
// served under a token that names the repository and outlives a commit.
func TestTheDocsOfARepositoryGroup(t *testing.T) {
	f := newFakeGitHub(t)
	c := newRepoEnv(t, f, "")
	f.pushDocs(t, "octo/app", map[string]string{
		"README.md":       "# App\n\nSee [the design](docs/DESIGN.md).\n",
		"docs/DESIGN.md":  "# Design\n",
		"docs/flow.png":   "\x89PNG\r\n\x1a\nfake",
		"main.go":         "package main\n",
		".github/NOTE.md": "# hidden\n",
	})
	list := func(key string) (*http.Response, map[string]any) {
		return c.do(t, c.client, "GET", "/api/docs?folder="+url.QueryEscape(key), "")
	}
	if res, _ := list("github:octo/app"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a repository no chat works in: %d", res.StatusCode)
	}
	if err := c.store.CreateSession(&store.Session{ID: "r1", Title: "In the repo", Harness: "fake",
		Workdir: filepath.Join(t.TempDir(), "app-r1"), WorkdirMode: store.WorkdirRepo, Repo: "octo/app", Base: "main",
		Branch: "socrates/x"}); err != nil {
		t.Fatal(err)
	}
	res, body := list("github:OCTO/app")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("listing: %d %v", res.StatusCode, body)
	}
	var paths []string
	for _, one := range body["files"].([]any) {
		paths = append(paths, one.(map[string]any)["path"].(string))
	}
	if strings.Join(paths, ",") != "README.md,docs/DESIGN.md" || body["readme"] != "README.md" ||
		body["folder"] != "github:OCTO/app" {
		t.Fatalf("listing: %v", body)
	}
	repo, _ := body["repo"].(map[string]any)
	if repo["name"] != "octo/app" || repo["branch"] != "main" || repo["web"] != nil {
		t.Fatalf("the repository: %v", body["repo"])
	}
	base := body["base"].(string)

	// The pages are the token's, without the login; nothing but docs.
	anon := c.server.Client()
	get := func(path string) (int, string) {
		res, err := anon.Get(c.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		text, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(text)
	}
	if code, text := get(base + "docs/DESIGN.md?raw=1"); code != http.StatusOK || text != "# Design\n" {
		t.Fatalf("a page: %d %q", code, text)
	}
	if code, _ := get(base + "docs/flow.png"); code != http.StatusOK {
		t.Fatalf("a picture: %d", code)
	}
	for _, path := range []string{"main.go", ".github/NOTE.md", ".socrates-blobs", "../app.git/config"} {
		if code, _ := get(base + path); code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, code)
		}
	}
	stat := func() map[string]any {
		res, body := c.do(t, c.client, "GET", "/api/docs/stat?folder=github:octo/app&path=docs/DESIGN.md", "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("stat: %d %v", res.StatusCode, body)
		}
		return body
	}
	before := stat()

	// A new commit on GitHub is shown once fetched, under the same token.
	f.pushDocs(t, "octo/app", map[string]string{"docs/DESIGN.md": "# Design, again\n"})
	if res, _ := list("github:octo/app"); res.StatusCode != http.StatusOK {
		t.Fatalf("listing again: %d", res.StatusCode)
	}
	if after := stat(); after["size"] != before["size"] {
		t.Fatalf("GitHub was asked again within the minute: %v, was %v", after, before)
	}
	if res, _ := c.do(t, c.client, "GET", "/api/docs?folder=github:octo/app&fetch=1", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("listing with fetch=1: %d", res.StatusCode)
	}
	if after := stat(); after["size"] == before["size"] {
		t.Fatalf("the stat did not follow the commit: %v, was %v", after, before)
	}
	if code, text := get(base + "docs/DESIGN.md?raw=1"); code != http.StatusOK || text != "# Design, again\n" {
		t.Fatalf("the page after a commit: %d %q", code, text)
	}

	// The probe: the repository of a chat has docs, another does not.
	code, probed := c.post(t, "/api/docs/probe", map[string]any{"folders": []string{"github:octo/app", "github:octo/other"}})
	answers, _ := probed["docs"].(map[string]any)
	if code != http.StatusOK || answers["github:octo/app"] != true || answers["github:octo/other"] != false {
		t.Fatalf("probe: %d %v", code, probed)
	}

	// A repository the App no longer reaches: its docs as fetched are not
	// shown either.
	if err := os.Rename(filepath.Join(f.dir, "octo", "app.git"), filepath.Join(f.dir, "octo", "gone.git")); err != nil {
		t.Fatal(err)
	}
	if res, _ := list("github:octo/app"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a repository out of reach: %d", res.StatusCode)
	}
	if code, _ := get(base + "README.md"); code != http.StatusNotFound {
		t.Fatalf("a page of a repository out of reach: %d", code)
	}

	// Without GitHub, the docs are not shown.
	c.srv.UseGitHub(nil, nil)
	if code, _ := get(base + "README.md"); code != http.StatusNotFound {
		t.Fatalf("without GitHub: %d", code)
	}
	if res, _ := c.do(t, c.server.Client(), "GET", "/api/docs?folder=github:octo/app", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
}
