//go:build !windows

package server

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Any file the agent names in its text - by a link, a `code` path, a bare
// absolute path - is served to the signed-in person as its kind is shown,
// in ranges, by its absolute path or one relative to the chat's folder; a
// path no text of the agent's names is not, nor anything too vague to be a
// file's path. The chat lists what it linked.
func TestFilesTheAgentNamesAreServed(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "demo.mp4")
	files := map[string]string{
		video:                             "\x00\x00\x00\x18ftypmp42" + strings.Repeat("v", 100),
		filepath.Join(dir, "other.mp4"):   "never named",
		filepath.Join(dir, "my take.mp3"): "ID3" + strings.Repeat("a", 50),
		filepath.Join(c.data, "out", "report.html"):    "<h1>Report</h1><script>1</script>",
		filepath.Join(c.data, "out", "notes.txt"):      "plain notes\n",
		filepath.Join(c.data, "out", "data.bin"):       "\x00\x01\x02binary",
		filepath.Join(c.data, "out", "image.svg"):      "<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>",
		filepath.Join(c.data, "docs", "NOTES.md"):      "# Notes\n",
		filepath.Join(c.data, "out", "nameless.thing"): "just some words\n",
	}
	for p, content := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fifo := filepath.Join(dir, "pipe.mp4")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// The scripted agent says back what it is told.
	c.send(t, id, "m1", "The [demo]("+video+"), [the take](<"+filepath.Join(dir, "my take.mp3")+">) and `out/report.html`, "+
		"[notes](out/notes.txt), [data](out/data.bin), `out/image.svg`, [the notes](docs/NOTES.md), "+
		"[nameless](out/nameless.thing), the pipe "+fifo+" and `a` and `/`.")
	c.waitJournal(t, id, 15*time.Second, "the answer", turnsEnded(1))
	base := "/api/chats/" + id + "/file?path="

	type want struct{ ctype, csp, disposition string }
	for ref, w := range map[string]want{
		video:                             {"video/mp4", "sandbox", "inline"},
		filepath.Join(dir, "my take.mp3"): {"audio/mpeg", "sandbox", "inline"},
		"out/report.html":                 {"text/html; charset=utf-8", "sandbox allow-scripts", "inline"},
		"out/notes.txt":                   {"text/plain; charset=utf-8", "sandbox", "inline"},
		"out/data.bin":                    {"application/octet-stream", "sandbox", "attachment"},
		"out/image.svg":                   {"image/svg+xml", "sandbox", "inline"},
		"out/nameless.thing":              {"text/plain; charset=utf-8", "sandbox", "inline"},
	} {
		res, _ := c.do(t, c.client, "GET", base+url.QueryEscape(ref), "")
		got, _ := io.ReadAll(res.Body)
		h := res.Header
		if res.StatusCode != http.StatusOK || h.Get("Content-Type") != w.ctype || !strings.HasPrefix(h.Get("Content-Security-Policy"), w.csp) ||
			!strings.HasPrefix(h.Get("Content-Disposition"), w.disposition) || h.Get("X-Content-Type-Options") != "nosniff" || len(got) == 0 {
			t.Fatalf("%s: %d %q %q %q", ref, res.StatusCode, h.Get("Content-Type"), h.Get("Content-Security-Policy"), h.Get("Content-Disposition"))
		}
	}
	// A video is served in ranges, for seeking; download=1 saves it.
	req, _ := http.NewRequest("GET", c.server.URL+base+url.QueryEscape(video)+"&download=1", nil)
	req.Header.Set("Range", "bytes=0-3")
	res, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusPartialContent || len(got) != 4 || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("a range: %d %d %q", res.StatusCode, len(got), res.Header.Get("Content-Disposition"))
	}
	// What it is, for the viewer.
	res, info := c.do(t, c.client, "GET", "/api/chats/"+id+"/file/info?path="+url.QueryEscape("out/nameless.thing"), "")
	if res.StatusCode != http.StatusOK || info["kind"] != "text" || info["path"] != filepath.Join(c.data, "out", "nameless.thing") || info["name"] != "nameless.thing" {
		t.Fatalf("info: %d %v", res.StatusCode, info)
	}
	// Never what it did not name, nor anything that is no file's path, nor
	// a pipe (which would hang the request), nor to a stranger.
	// Nor a part of a path it named: a named path is a whole one.
	tail := video[len(filepath.Dir(dir)):]
	for _, bad := range []string{filepath.Join(dir, "other.mp4"), "/etc/passwd", "a", "/", "", fifo, "out", "http://example.com/x.mp4",
		tail, "demo.mp4", "ut/notes.txt"} {
		if res, _ := c.do(t, c.client, "GET", base+url.QueryEscape(bad), ""); res.StatusCode != http.StatusNotFound {
			t.Fatalf("%q: %d", bad, res.StatusCode)
		}
	}
	if res, _ := c.do(t, c.anon, "GET", base+url.QueryEscape(video), ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}

	// The linked files: a link's target of any kind but Markdown, a `code`
	// path to look at; that are there and regular.
	res, body := c.do(t, c.client, "GET", "/api/chats/"+id+"/files", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("linked: %d %v", res.StatusCode, body)
	}
	listed := map[string]string{}
	for _, f := range body["files"].([]any) {
		m := f.(map[string]any)
		listed[m["path"].(string)] = m["kind"].(string)
		if res, _ := c.do(t, c.client, "GET", base+url.QueryEscape(m["ref"].(string)), ""); res.StatusCode != http.StatusOK {
			t.Fatalf("a listed file is not served by its ref %q: %d", m["ref"], res.StatusCode)
		}
	}
	wantListed := map[string]string{
		video:                             "video",
		filepath.Join(dir, "my take.mp3"): "audio",
		filepath.Join(c.data, "out", "report.html"):    "html",
		filepath.Join(c.data, "out", "notes.txt"):      "text",
		filepath.Join(c.data, "out", "data.bin"):       "file",
		filepath.Join(c.data, "out", "image.svg"):      "image",
		filepath.Join(c.data, "out", "nameless.thing"): "file",
	}
	if len(listed) != len(wantListed) {
		t.Fatalf("linked: %v", listed)
	}
	for p, k := range wantListed {
		if listed[p] != k {
			t.Fatalf("linked %s: %q in %v", p, listed[p], listed)
		}
	}
}

// linkedRefs finds what a text links or names, as written, and leaves out
// what is no file to show.
func TestLinkedRefs(t *testing.T) {
	refs := map[string]int64{}
	linkedRefs("See [the cut](out/draft-v2.mp4), ![shot](/tmp/a.png) and `review/voice-a.mp3`. "+
		"Also `internal/server/docs.go`, [code](main.go), [web](https://example.com/x.mp4), [doc](docs/A.md), "+
		"the final file is /data/out/final.webm. Not `out/${v}.mp3` nor `*.mp4` nor [mail](mailto:a@b.c). "+
		"[spaced](<out/my take.wav>) and file:///srv/x.pdf#page=2", 7, refs)
	want := []string{"out/draft-v2.mp4", "/tmp/a.png", "review/voice-a.mp3", "main.go", "/data/out/final.webm",
		"out/my take.wav", "file:///srv/x.pdf"}
	if len(refs) != len(want) {
		t.Fatalf("refs: %v", refs)
	}
	for _, w := range want {
		if refs[w] != 7 {
			t.Fatalf("%q missing: %v", w, refs)
		}
	}
}
