package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// In a folder with an opencode.json of its own, the sheet is told OpenCode's
// models there - the folder's provider among them - and the model the folder
// starts on, ahead of the dashboard's default; a chat made there without a
// model runs on it. Anywhere else nothing changes.
func TestAFolderConfiguresOpenCodeForItself(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for opencode")
	}
	c := newChatEnv(t, time.Hour)
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
	next := c.srv.Settings()
	next.Harnesses.OpenCode.Binary = bin
	next.Harnesses.OpenCode.DefaultModel = "a/one"
	next.Workspace.AllowCustom = true
	if err := c.srv.saveSettings(next); err != nil {
		t.Fatal(err)
	}
	c.srv.catalog.Refresh(t.Context())

	dir := t.TempDir()
	config := `{"model": "lab/qwen", "provider": {"lab": {"name": "Lab", "options": {"apiKey": "secret"}}},
 "agent": {"general": {"model": "a/one"}}}`
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	res, got := c.do(t, c.client, "GET", "/api/harnesses/folder?workdir_mode=custom&workdir="+url.QueryEscape(dir), "")
	list, _ := got["harnesses"].([]any)
	if res.StatusCode != http.StatusOK || got["workdir"] != dir || len(list) != 1 {
		t.Fatalf("folder: %d %v", res.StatusCode, got)
	}
	oc := list[0].(map[string]any)
	folder, _ := oc["folder"].(map[string]any)
	if oc["id"] != "opencode" || oc["default_model"] != "lab/qwen" || len(oc["models"].([]any)) != 2 || folder == nil {
		t.Fatalf("opencode in the folder: %v", oc)
	}
	agents, _ := folder["agents"].([]any)
	if len(agents) != 1 || agents[0].(map[string]any)["model"] != "a/one" {
		t.Fatalf("the folder's agents: %v", folder)
	}
	for _, m := range oc["models"].([]any) {
		if m.(map[string]any)["id"] == "lab/qwen" && m.(map[string]any)["group"] != "Lab" {
			t.Fatalf("the folder's provider is not named as the folder names it: %v", m)
		}
	}

	model, _ := c.srv.chatModelIn(t.Context(), c.srv.Settings(), "opencode", dir, "", "")
	if model != "lab/qwen" {
		t.Fatalf("a chat in the folder without a model runs on %q", model)
	}
	if model, _ := c.srv.chatModelIn(t.Context(), c.srv.Settings(), "opencode", dir, "a/one", ""); model != "a/one" {
		t.Fatalf("a model asked for is not kept: %q", model)
	}
	plain := t.TempDir()
	if model, _ := c.srv.chatModelIn(t.Context(), c.srv.Settings(), "opencode", plain, "", ""); model != "a/one" {
		t.Fatalf("elsewhere the dashboard's default is not taken: %q", model)
	}
	res, got = c.do(t, c.client, "GET", "/api/harnesses/folder?workdir_mode=custom&workdir="+url.QueryEscape(plain), "")
	if list, _ := got["harnesses"].([]any); res.StatusCode != http.StatusOK || len(list) != 0 {
		t.Fatalf("a folder without a config: %d %v", res.StatusCode, got)
	}
	// A path half typed changes nothing, and is no error.
	res, got = c.do(t, c.client, "GET", "/api/harnesses/folder?workdir_mode=custom&workdir=rel", "")
	if list, _ := got["harnesses"].([]any); res.StatusCode != http.StatusOK || len(list) != 0 {
		t.Fatalf("half typed: %d %v", res.StatusCode, got)
	}
	res, _ = c.do(t, c.client, "GET", "/api/harnesses/folder?session=nope", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown chat: %d", res.StatusCode)
	}
}
