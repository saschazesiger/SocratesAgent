package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// folderOpenCode puts an opencode on PATH that lists one model everywhere
// and, in a folder with an opencode.json, the model that file names too, as
// OpenCode does with a project's provider. Every listing is counted in
// calls.
func folderOpenCode(t *testing.T) (calls string) {
	t.Helper()
	onlyClaude(t)
	bin := os.Getenv("PATH")
	calls = filepath.Join(t.TempDir(), "calls")
	// The project's folder is found as OpenCode finds it: upwards.
	script := `#!/bin/sh
d=$(pwd)
while [ -n "$d" ] && [ ! -f "$d/opencode.json" ]; do d=${d%/*}; done
cd "${d:-/}"
case "$1" in
--version) echo 1.19.5-test ;;
models)
  echo x >> ` + calls + `
  printf 'a/one\n{"id": "one", "providerID": "a", "name": "One", "capabilities": {"toolcall": true}}\n'
  if [ -f opencode.json ]; then
    printf 'lab/%s\n{"id": "%s", "providerID": "lab", "name": "Lab %s", "capabilities": {"toolcall": true}}\n' "$(/bin/cat model-id)" "$(/bin/cat model-id)" "$(/bin/cat model-id)"
  fi ;;
debug)
  if [ -f opencode.json ]; then /bin/cat opencode.json; else echo '{}'; fi ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return calls
}

func labFolder(t *testing.T, model string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model-id"), []byte(model), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `{"model": "lab/` + model + `", "agent": {"general": {"model": "a/one"}}}`
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func countCalls(t *testing.T, path string) int {
	raw, _ := os.ReadFile(path)
	return strings.Count(string(raw), "x")
}

// In a folder with an opencode.json OpenCode's catalogue is the one OpenCode
// lists there, started on the folder's model; anywhere else it is the
// general one, and the folder answer has nothing in it.
func TestAFolderWithItsOwnConfigHasItsOwnModels(t *testing.T) {
	calls := folderOpenCode(t)
	c := New(newStore(), settingsFn(config.Default()))
	general := mustAgent(t, c.Refresh(context.Background()), "opencode")
	if len(general.Models) != 1 || general.Folder != nil {
		t.Fatalf("general = %+v", general)
	}

	if got := c.Folder(context.Background(), t.TempDir()); len(got) != 0 {
		t.Fatalf("a folder without a config changes %+v", got)
	}
	dir := labFolder(t, "qwen")
	got := c.Folder(context.Background(), dir)
	if len(got) != 1 || got[0].ID != "opencode" {
		t.Fatalf("folder = %+v", got)
	}
	oc := got[0]
	var ids []string
	for _, m := range oc.Models {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "a/one,lab/qwen" || oc.DefaultModel != "lab/qwen" {
		t.Fatalf("models %v, default %q", ids, oc.DefaultModel)
	}
	if oc.Folder == nil || oc.Folder.Model != "lab/qwen" || len(oc.Folder.Agents) != 1 || oc.Folder.Agents[0].Model != "a/one" {
		t.Fatalf("folder config = %+v", oc.Folder)
	}
	if cached, ok := c.FolderCached("opencode", dir); !ok || cached.DefaultModel != "lab/qwen" {
		t.Fatalf("cached = %+v, %v", cached, ok)
	}

	// Asked again, it is the answer there is: OpenCode is not asked again.
	before := countCalls(t, calls)
	c.Folder(context.Background(), dir)
	if after := countCalls(t, calls); after != before {
		t.Fatalf("the folder was listed again: %d calls, then %d", before, after)
	}

	// A changed config is a new answer, waited for.
	if err := os.WriteFile(filepath.Join(dir, "model-id"), []byte("llama"), 0o600); err != nil {
		t.Fatal(err)
	}
	newer := `{"model": "lab/llama"}`
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "opencode.json"), later, later); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.FolderCached("opencode", dir); ok {
		t.Fatal("the answer for the old config is still taken as current")
	}
	got = c.Folder(context.Background(), dir)
	if len(got) != 1 || got[0].DefaultModel != "lab/llama" || got[0].Folder.Agents != nil {
		t.Fatalf("after the change = %+v", got)
	}
}

// A request that gives up does not stop the folder's discovery: the next one
// has the answer.
func TestAFolderRequestThatGivesUpLeavesTheAnswerBehind(t *testing.T) {
	folderOpenCode(t)
	c := New(newStore(), settingsFn(config.Default()))
	c.Refresh(context.Background())
	dir := labFolder(t, "qwen")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Folder(ctx, dir)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if a, ok := c.FolderCached("opencode", dir); ok {
			if a.DefaultModel != "lab/qwen" {
				t.Fatalf("answer = %+v", a)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the discovery did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Folders with the same configuration share one answer - a project's
// subfolders, a chat folder not made yet - and a discovery that failed is
// not an answer: the next request asks again.
func TestFolderAnswersAreSharedAndFailuresAreNotKept(t *testing.T) {
	calls := folderOpenCode(t)
	c := New(newStore(), settingsFn(config.Default()))
	c.Refresh(context.Background())
	dir := labFolder(t, "qwen")
	sub := filepath.Join(dir, "src")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if a, ok := c.FolderAgent(context.Background(), "opencode", sub); !ok || a.DefaultModel != "lab/qwen" {
		t.Fatalf("subfolder = %+v %v", a, ok)
	}
	before := countCalls(t, calls)
	if a, ok := c.FolderAgent(context.Background(), "opencode", filepath.Join(dir, "not", "made", "yet")); !ok || a.DefaultModel != "lab/qwen" {
		t.Fatalf("a folder not made yet = %+v %v", a, ok)
	}
	if after := countCalls(t, calls); after != before {
		t.Fatalf("the same configuration was listed again: %d, then %d", before, after)
	}

	// A folder where the listing fails, then works.
	broken := labFolder(t, "llama")
	if err := os.WriteFile(filepath.Join(broken, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(os.Getenv("PATH"), "opencode")
	raw, _ := os.ReadFile(bin)
	script := strings.Replace(string(raw), "models)\n", "models)\n  if [ -f fail ]; then exit 1; fi\n", 1)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.FolderAgent(context.Background(), "opencode", broken); ok {
		t.Fatal("a failed listing gave an answer")
	}
	if err := os.Remove(filepath.Join(broken, "fail")); err != nil {
		t.Fatal(err)
	}
	if a, ok := c.FolderAgent(context.Background(), "opencode", broken); !ok || a.DefaultModel != "lab/llama" {
		t.Fatalf("the failure was kept: %+v %v", a, ok)
	}
}
