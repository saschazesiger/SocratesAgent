//go:build linux

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOpenCode is an opencode that lists the provider lab's model where the
// folder it runs in has an opencode.json, and prints that file as its
// resolved config.
func fakeOpenCode(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "opencode")
	script := `#!/bin/sh
case "$1" in
--version) echo 1.19.5-test ;;
models)
  printf 'a/one\n{"id": "one", "providerID": "a", "name": "One", "capabilities": {"toolcall": true}}\n'
  if [ -f opencode.json ]; then
    printf 'lab/qwen\n{"id": "qwen", "providerID": "lab", "name": "Qwen", "capabilities": {"toolcall": true}}\n'
  fi ;;
debug) if [ -f opencode.json ]; then /bin/cat opencode.json; else echo '{}'; fi ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// The new-chat sheet's look at a repository (REPOS.md §4.1): the folder probe
// and the MCP probe answer for the base branch's configuration as they do
// for a folder, with its files named as the repository has them; a
// repository or branch they cannot look at changes nothing and is no error.
// A chat made in the repository without a model runs on the model its
// configuration names.
func TestTheSheetPreviewsARepositorysConfiguration(t *testing.T) {
	f := newFakeGitHub(t)
	work := t.TempDir()
	testGit(t, work, "clone", "--quiet", filepath.Join(f.dir, "octo", "app.git"), ".")
	files := map[string]string{
		"opencode.json":             `{"model": "lab/qwen", "provider": {"lab": {"name": "Lab"}}, "agent": {"general": {"model": "a/one"}}}`,
		".mcp.json":                 `{"mcpServers": {"docs": {"command": "echo"}, "bad name": {"command": "x"}}}`,
		".opencode/plugin/hook.js":  "throw 1",
		"src/main.go":               "package main",
		".opencode/agent/review.md": "---\nmodel: a/one\n---\n",
	}
	for path, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(work, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, work, "add", "-A")
	testGit(t, work, "commit", "--quiet", "-m", "config")
	testGit(t, work, "push", "--quiet", "origin", "main")
	sha := testGit(t, work, "rev-parse", "HEAD")

	c := newRepoEnv(t, f, "")
	next := c.srv.Settings()
	next.Harnesses.OpenCode.Binary = fakeOpenCode(t)
	next.Harnesses.OpenCode.DefaultModel = "a/one"
	if err := c.srv.saveSettings(next); err != nil {
		t.Fatal(err)
	}
	c.srv.catalog.Refresh(t.Context())
	root := c.srv.Settings().Workspace.Root

	// The default branch, the repository named in another case.
	res, got := c.do(t, c.client, "GET", "/api/harnesses/folder?workdir_mode=dynamic&workdir=&repo=Octo/App", "")
	preview := filepath.Join(root, ".repos", "octo", "app.preview", sha)
	list, _ := got["harnesses"].([]any)
	if res.StatusCode != http.StatusOK || got["workdir"] != preview || len(list) != 1 {
		t.Fatalf("the repository: %d %v", res.StatusCode, got)
	}
	oc := list[0].(map[string]any)
	folder, _ := oc["folder"].(map[string]any)
	if oc["id"] != "opencode" || oc["default_model"] != "lab/qwen" || len(oc["models"].([]any)) != 2 || folder == nil {
		t.Fatalf("opencode in the repository: %v", oc)
	}
	names := []string{}
	for _, f := range folder["files"].([]any) {
		names = append(names, f.(string))
	}
	if strings.Join(names, " ") != "octo/app/opencode.json octo/app/.opencode/agent/review.md" {
		t.Fatalf("the config files are named %v", names)
	}
	var written []string
	_ = filepath.WalkDir(preview, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(preview, path)
			written = append(written, rel)
		}
		return nil
	})
	if strings.Join(written, " ") != ".mcp.json .opencode/agent/review.md opencode.json" {
		t.Fatalf("the preview holds %v", written)
	}

	// The MCP servers of the repository's .mcp.json, its problem named as
	// the repository has the file.
	res, got = c.do(t, c.client, "GET", "/api/mcp?workdir_mode=dynamic&workdir=&repo=octo/app&base=main", "")
	servers, _ := got["servers"].([]any)
	problems, _ := got["problems"].([]any)
	found := false
	for _, s := range servers {
		if m := s.(map[string]any); m["name"] == "docs" && m["from"] == "folder" {
			found = true
		}
	}
	if res.StatusCode != http.StatusOK || !found || len(problems) != 1 || !strings.Contains(problems[0].(string), "octo/app/.mcp.json") ||
		strings.Contains(problems[0].(string), root) {
		t.Fatalf("the repository's MCP servers: %d %v", res.StatusCode, got)
	}

	// What it cannot look at is a folder like any other.
	for _, query := range []string{"repo=octo/app&base=nope", "repo=octo/app&base=-x", "repo=octo/unknown", "repo=../etc"} {
		res, got = c.do(t, c.client, "GET", "/api/harnesses/folder?"+query, "")
		if list, _ := got["harnesses"].([]any); res.StatusCode != http.StatusOK || got["workdir"] != "" || len(list) != 0 {
			t.Fatalf("%s: %d %v", query, res.StatusCode, got)
		}
	}

	// A chat made in the repository without a model runs on the model its
	// worktree's configuration names; one asked for is kept.
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "oc-1", "harness": "opencode", "repo": "octo/app"})
	if s := sessionOf(t, body); code != http.StatusCreated || s["model"] != "lab/qwen" || s["repo"] != "octo/app" {
		t.Fatalf("a chat in the repository without a model: %d %v", code, body)
	}
	code, body = c.post(t, "/api/chats", map[string]any{"client_id": "oc-2", "harness": "opencode", "repo": "octo/app", "model": "a/one"})
	if s := sessionOf(t, body); code != http.StatusCreated || s["model"] != "a/one" {
		t.Fatalf("a chat in the repository with a model: %d %v", code, body)
	}

	// Without GitHub there is no repository to look at.
	c.srv.UseGitHub(nil, nil)
	res, got = c.do(t, c.client, "GET", "/api/harnesses/folder?repo=octo/app", "")
	if list, _ := got["harnesses"].([]any); res.StatusCode != http.StatusOK || got["workdir"] != "" || len(list) != 0 {
		t.Fatalf("without GitHub: %d %v", res.StatusCode, got)
	}
	res, got = c.do(t, c.client, "GET", "/api/mcp?repo=octo/app", "")
	if res.StatusCode != http.StatusOK || got["problems"] == nil {
		t.Fatalf("the MCP servers without GitHub: %d %v", res.StatusCode, got)
	}
}
