package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func TestUploadName(t *testing.T) {
	now := time.Date(2026, 10, 7, 15, 30, 12, 0, time.UTC)
	for raw, want := range map[string]string{
		"screenshot.png":            "screenshot.png",
		"My Photo (1).JPG":          "My-Photo-1.JPG",
		"../../etc/passwd":          "passwd",
		`C:\Users\me\diagram.svg`:   "diagram.svg",
		".env":                      "upload-20261007-153012.env",
		"😀.png":                     "upload-20261007-153012.png",
		"rm -rf $(whoami);echo.txt": "rm-rf-whoamiecho.txt",
	} {
		if got := uploadName(raw, now); got != want {
			t.Errorf("uploadName(%q) = %q, want %q", raw, got, want)
		}
	}
}

func upload(t *testing.T, env *testEnv, client *http.Client, id string, files map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	for name, content := range files {
		part, err := form.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		part.Write([]byte(content))
	}
	form.Close()
	req, _ := http.NewRequest("POST", env.server.URL+"/api/sessions/"+id+"/upload", &buf)
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	json.NewDecoder(res.Body).Decode(&body)
	return res, body
}

func TestUploadLandsInTheWorkingDirectory(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	workdir := t.TempDir()
	if err := env.store.CreateSession(&store.Session{
		ID: "s-up", Title: "t", Harness: "shell", Workdir: workdir, TmuxName: "t-up",
	}); err != nil {
		t.Fatal(err)
	}

	if res, _ := upload(t, env, env.anon, "s-up", map[string]string{"a.txt": "x"}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous upload = %d", res.StatusCode)
	}
	if res, _ := upload(t, env, env.client, "nope", map[string]string{"a.txt": "x"}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("upload to no session = %d", res.StatusCode)
	}

	var paths []string
	for _, content := range []string{"first", "second"} {
		res, body := upload(t, env, env.client, "s-up", map[string]string{"shot.png": content})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("upload = %d %v", res.StatusCode, body)
		}
		files, _ := body["files"].([]any)
		if len(files) != 1 {
			t.Fatalf("files = %v", body)
		}
		path, _ := files[0].(map[string]any)["path"].(string)
		paths = append(paths, path)
		if got, _ := os.ReadFile(path); string(got) != content {
			t.Fatalf("%s holds %q, want %q", path, got, content)
		}
	}
	dir := filepath.Join(workdir, uploadDir)
	// A second upload with the same name never overwrites the first.
	if paths[0] != filepath.Join(dir, "shot.png") || paths[1] != filepath.Join(dir, "shot-2.png") {
		t.Fatalf("paths = %v", paths)
	}
	if ignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(ignore) != "*\n" {
		t.Fatalf(".gitignore = %q", ignore)
	}

	// A request with no file in it is an error, not an empty success.
	if res, _ := upload(t, env, env.client, "s-up", nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty upload = %d", res.StatusCode)
	}
}
