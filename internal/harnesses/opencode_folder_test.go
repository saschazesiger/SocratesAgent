package harnesses

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A folder's own OpenCode configuration is every config and agent file in it
// and above it, the nearest last; a folder without any has none, and so is
// like any other.
func TestOpenCodeFolderFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "opencode.json"), `{}`)
	writeFile(t, filepath.Join(root, "repo", ".opencode", "opencode.jsonc"), `{}`)
	writeFile(t, filepath.Join(root, "repo", ".opencode", "agents", "review.md"), "---\nmodel: a/b\n---\n")
	writeFile(t, filepath.Join(root, "repo", ".opencode", "agents", "notes.txt"), "not an agent")
	if err := os.MkdirAll(filepath.Join(root, "repo", "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "repo", "opencode.json"), 0o700); err != nil {
		t.Fatal(err) // a folder by that name is no config
	}

	got := openCodeFolderFiles(filepath.Join(root, "repo", "src"))
	want := []string{
		filepath.Join(root, "opencode.json"),
		filepath.Join(root, "repo", ".opencode", "opencode.jsonc"),
		filepath.Join(root, "repo", ".opencode", "agents", "review.md"),
	}
	// What lies above the temp folder is the machine's, not the test's.
	var mine []string
	for _, f := range got {
		if strings.HasPrefix(f, root) {
			mine = append(mine, f)
		}
	}
	if !reflect.DeepEqual(mine, want) {
		t.Fatalf("files = %v, want %v", mine, want)
	}
	if got := openCodeFolderFiles("relative/dir"); got != nil {
		t.Fatalf("a relative folder has files %v", got)
	}
}

// In a folder with its own opencode.json, OpenCode is asked there: the
// folder's provider adds its models, named as the folder names it, the
// folder's model is the default, and the agents it puts on a model of their
// own come along. Only those - no key of the resolved config is kept.
func TestOpenCodeFolderModels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for opencode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	// The stand-in lists the folder's model only where its config is, as
	// OpenCode does, and resolves the config of the folder it runs in.
	writeFile(t, bin, `#!/bin/sh
case "$1 $2" in
"models --verbose")
  cat "$LISTING"
  if [ -f opencode.json ]; then cat folder-listing; fi ;;
"debug config")
  if [ -f opencode.json ]; then cat opencode.json; else echo '{}'; fi ;;
*) exit 2 ;;
esac
`)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "listing"), `a/one
{"id": "one", "providerID": "a", "name": "One", "capabilities": {"toolcall": true}}
`)
	project := filepath.Join(dir, "project")
	writeFile(t, filepath.Join(project, "folder-listing"), `ollama/qwen:27b
{"id": "qwen:27b", "providerID": "ollama", "name": "Qwen 27B", "capabilities": {"toolcall": true}}
`)
	writeFile(t, filepath.Join(project, "opencode.json"), `{
  "model": "ollama/qwen:27b",
  "provider": {"ollama": {"name": "Ollama (lab)", "options": {"apiKey": "secret"}}},
  "agent": {"general": {"model": "a/one"}, "plan": {"temperature": 0.1}, "old": {"model": "a/one", "disable": true}}
}`)
	env := []string{"PATH=/usr/bin:/bin", "LISTING=" + filepath.Join(dir, "listing"),
		"XDG_STATE_HOME=" + filepath.Join(dir, "state"), "XDG_CACHE_HOME=" + filepath.Join(dir, "cache")}

	cat, err := OpenCode{}.DiscoverModelsInFolder(context.Background(), bin, project, [][]string{env})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range cat.Models {
		got = append(got, m.ID+"="+m.Group)
		if m.Default != (m.ID == "ollama/qwen:27b") {
			t.Fatalf("the folder's model is the default: %+v", cat.Models)
		}
	}
	if want := "a/one=a,ollama/qwen:27b=Ollama (lab)"; strings.Join(got, ",") != want {
		t.Fatalf("models = %v, want %s", got, want)
	}
	f := cat.Folder
	if f == nil || f.Model != "ollama/qwen:27b" {
		t.Fatalf("folder = %+v", f)
	}
	if !reflect.DeepEqual(f.Agents, []AgentModel{{Name: "general", Model: "a/one"}}) {
		t.Fatalf("agents = %+v", f.Agents)
	}
	if len(f.Files) == 0 || f.Files[len(f.Files)-1] != filepath.Join(project, "opencode.json") {
		t.Fatalf("files = %v", f.Files)
	}

	// Elsewhere the same binary lists what it lists everywhere.
	plain, err := OpenCode{}.DiscoverModelsIn(context.Background(), bin, [][]string{env})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Models) != 1 || plain.Folder != nil {
		t.Fatalf("the plain catalogue = %+v", plain)
	}
}

// A folder whose model OpenCode does not list - its provider not signed in -
// starts where any other does: OpenCode could not start on that model.
func TestOpenCodeFolderModelNotListed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for opencode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	writeFile(t, bin, `#!/bin/sh
case "$1 $2" in
"models --verbose") printf 'a/one\n{"id": "one", "providerID": "a", "capabilities": {"toolcall": true}}\n' ;;
"debug config") echo '{"model": "gone/model"}' ;;
*) exit 2 ;;
esac
`)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	cat, err := openCodeFolderModels(context.Background(), bin, dir, [][]string{{"PATH=/usr/bin:/bin", "HOME=" + dir}})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Folder == nil || cat.Folder.Model != "" || cat.Models[0].Default {
		t.Fatalf("catalogue = %+v, folder %+v", cat.Models, cat.Folder)
	}
}

// What the global configuration says is not the folder's: only a model or an
// agent's model the folder changes is. A folder not made yet is asked in the
// nearest one that is, whose configuration it will have.
func TestOpenCodeFolderIsWhatTheFolderChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for opencode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	writeFile(t, bin, `#!/bin/sh
case "$1 $2" in
"models --verbose") printf 'a/one\n{"id": "one", "providerID": "a", "capabilities": {"toolcall": true}}\n' ;;
"debug config")
  if [ "$(pwd)" = / ]; then echo '{"model": "a/one", "agent": {"explore": {"model": "a/one"}, "general": {"model": "a/one"}}}'
  else echo '{"model": "a/one", "agent": {"explore": {"model": "a/one"}, "general": {"model": "b/two"}}}'; fi ;;
*) exit 2 ;;
esac
`)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "project", ".opencode", "agents", "nested", "deep.md"), "---\nmodel: b/two\n---\n")
	notYet := filepath.Join(dir, "project", "new", "chat")
	cat, err := openCodeFolderModels(context.Background(), bin, notYet, [][]string{{"PATH=/usr/bin:/bin", "HOME=" + dir}})
	if err != nil {
		t.Fatal(err)
	}
	f := cat.Folder
	if f == nil || f.Model != "" || !reflect.DeepEqual(f.Agents, []AgentModel{{Name: "general", Model: "b/two"}}) {
		t.Fatalf("folder = %+v", f)
	}
	if want := filepath.Join(dir, "project", ".opencode", "agents", "nested", "deep.md"); len(f.Files) == 0 || f.Files[len(f.Files)-1] != want {
		t.Fatalf("files = %v", f.Files)
	}
}
