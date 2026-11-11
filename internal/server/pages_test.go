package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// pagesChat is a chat row of a signed-in server; no agent runs, so its
// pages folder is not there until something makes it.
func pagesChat(t *testing.T, e *testEnv, id string) string {
	t.Helper()
	if err := e.store.CreateSession(&store.Session{ID: id, Title: "A chat", Harness: "claude", Kind: store.KindChat,
		State: store.StateRunning, Workdir: t.TempDir(), WorkdirMode: "custom"}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A chat's view names its pages folder, which the Docs view lists with the
// files' titles and serves; a session that is no chat has none.
func TestAChatsPagesFolderIsItsDocs(t *testing.T) {
	e := signedIn(t)
	chat := pagesChat(t, e, "c1")
	row, err := e.store.GetSession(chat)
	if err != nil {
		t.Fatal(err)
	}
	dir := e.srv.view(row).PagesDir
	if want := filepath.Join(e.srv.dataDir, paths.Chats, chat, paths.ChatPages); dir != want || !filepath.IsAbs(dir) {
		t.Fatalf("pages_dir: %q, want %q", dir, want)
	}
	if err := e.store.CreateSession(&store.Session{ID: "s1", Title: "A shell", Harness: "shell"}); err != nil {
		t.Fatal(err)
	}
	if shell, _ := e.store.GetSession("s1"); e.srv.view(shell).PagesDir != "" {
		t.Fatal("a shell has a pages folder")
	}

	// A chat from before pages: listing makes its folder.
	res, body := e.do(t, e.client, "GET", "/api/docs?folder="+url.QueryEscape(dir)+"&titles=1", "")
	if res.StatusCode != http.StatusOK || len(body["files"].([]any)) != 0 {
		t.Fatalf("an empty pages folder: %d %v", res.StatusCode, body)
	}
	writeTree(t, dir, map[string]string{
		"plan.md":       "# The plan\n\nFirst this.\n",
		"notes.md":      "Some notes, no title.\n",
		"deep/later.md": "Intro\n\n```bash\n# not a title\n```\n\n## Section\n\n#  Later title  ##\n",
	})
	res, body = e.do(t, e.client, "GET", "/api/docs?folder="+url.QueryEscape(dir)+"&titles=1", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %v", res.StatusCode, body)
	}
	titles := map[string]any{}
	for _, f := range body["files"].([]any) {
		m := f.(map[string]any)
		titles[m["path"].(string)] = m["title"]
	}
	if titles["plan.md"] != "The plan" || titles["notes.md"] != "" || titles["deep/later.md"] != "Later title" || len(titles) != 3 {
		t.Fatalf("titles: %v", titles)
	}
	// Without titles=1 there are none.
	_, body = docsBase(t, e, dir)
	if _, has := body["files"].([]any)[0].(map[string]any)["title"]; has {
		t.Fatalf("a title without asking: %v", body)
	}
	base, _ := docsBase(t, e, dir)
	if res, page := get(t, e, base+"plan.md"); res.StatusCode != http.StatusOK || !strings.Contains(page, "First this.") {
		t.Fatalf("a page: %d %s", res.StatusCode, page)
	}
	res, body = e.do(t, e.client, "GET", "/api/docs/stat?folder="+url.QueryEscape(dir)+"&path=plan.md", "")
	if res.StatusCode != http.StatusOK || body["path"] != "plan.md" {
		t.Fatalf("stat: %d %v", res.StatusCode, body)
	}

	// Another folder of the chat's, or the pages folder of no chat, is not
	// shown.
	other := filepath.Join(e.srv.dataDir, paths.Chats, chat, "attachments")
	ghost := filepath.Join(e.srv.dataDir, paths.Chats, "c-gone", paths.ChatPages)
	for _, folder := range []string{other, ghost, dir + "/deep"} {
		writeTree(t, folder, map[string]string{"x.md": "# X"})
		if res, _ := e.do(t, e.client, "GET", "/api/docs?folder="+url.QueryEscape(folder), ""); res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", folder, res.StatusCode)
		}
	}

	// A link put in the folder's place serves nothing.
	elsewhere := t.TempDir()
	writeTree(t, elsewhere, map[string]string{"secret.md": "# Secret"})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.do(t, e.client, "GET", "/api/docs?folder="+url.QueryEscape(dir), ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a link as the pages folder: %d", res.StatusCode)
	}
	if res, _ := get(t, e, base+"plan.md"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a page through a link: %d", res.StatusCode)
	}
}

// A listing of more than maxDocsTitles files is listed without titles.
func TestManyPagesAreListedWithoutTitles(t *testing.T) {
	e := signedIn(t)
	chat := pagesChat(t, e, "c1")
	dir, err := agenthost.MakePages(e.srv.dataDir, chat)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for i := 0; i <= maxDocsTitles; i++ {
		files[fmt.Sprintf("p%03d.md", i)] = "# Page\n"
	}
	writeTree(t, dir, files)
	res, body := e.do(t, e.client, "GET", "/api/docs?folder="+url.QueryEscape(dir)+"&titles=1", "")
	list, _ := body["files"].([]any)
	if res.StatusCode != http.StatusOK || len(list) != maxDocsTitles+1 {
		t.Fatalf("list: %d %d", res.StatusCode, len(list))
	}
	if _, has := list[0].(map[string]any)["title"]; has {
		t.Fatal("titles for a long listing")
	}
}

// The start deletes every chat's artifacts folder, from the built-in server
// pages replaced, and nothing else of the chat.
func TestTheChatsArtifactsAreDeletedOnce(t *testing.T) {
	e := newEnv(t)
	chats := filepath.Join(e.srv.dataDir, paths.Chats)
	writeTree(t, chats, map[string]string{
		"c1/artifacts/artifacts.json":   "{}",
		"c1/artifacts/A1/v1.md":         "# Old",
		"c1/events.jsonl":               "",
		"c1/pages/plan.md":              "# Plan",
		"c2/artifacts/artifacts.json":   "{}",
		"c3/events.jsonl":               "",
		"c3/attachments/artifacts.json": "{}",
	})
	e.srv.forgetArtifacts()
	for _, gone := range []string{"c1/artifacts", "c2/artifacts"} {
		if _, err := os.Lstat(filepath.Join(chats, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s is still there: %v", gone, err)
		}
	}
	for _, kept := range []string{"c1/events.jsonl", "c1/pages/plan.md", "c3/events.jsonl", "c3/attachments/artifacts.json"} {
		if _, err := os.Lstat(filepath.Join(chats, kept)); err != nil {
			t.Fatalf("%s: %v", kept, err)
		}
	}
	// A second start finds nothing.
	e.srv.forgetArtifacts()
}

// A chat that had Socrates Artifacts switched on keeps working: the choice
// finds no server and is passed over.
func TestAChatsChoiceOfTheGoneArtifactsServerIsPassedOver(t *testing.T) {
	e := signedIn(t)
	chat := pagesChat(t, e, "c1")
	if _, err := e.store.UpdateChatOptions(chat, func(raw json.RawMessage) json.RawMessage {
		return withOption(raw, "mcp", map[string]bool{"socrates-artifacts": true})
	}); err != nil {
		t.Fatal(err)
	}
	res, body := e.do(t, e.client, "GET", "/api/sessions/"+chat+"/mcp", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mcp: %d %v", res.StatusCode, body)
	}
	for _, s := range body["servers"].([]any) {
		if s.(map[string]any)["name"] == "socrates-artifacts" {
			t.Fatalf("the gone server: %v", body)
		}
	}
	if probs := body["problems"].([]any); len(probs) != 0 {
		t.Fatalf("problems: %v", probs)
	}
	if e.srv.mcp.Get("socrates-artifacts") != nil {
		t.Fatal("Socrates Artifacts is still built in")
	}
}
