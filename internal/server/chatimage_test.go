//go:build linux

package server

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// An image part's file is served as the image it is, to who may read the
// chat; a file that is not an image, a part that is not an image, and one
// whose file is gone are not.
func TestAnImagePartsFileIsServed(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	dir := t.TempDir()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	pic := filepath.Join(dir, "drawn.png")
	text := filepath.Join(dir, "notes.png")
	if os.WriteFile(pic, b.Bytes(), 0o600) != nil || os.WriteFile(text, []byte("<svg onload=alert(1)>"), 0o600) != nil {
		t.Fatal("write")
	}
	c.send(t, id, "m1", "/image "+pic)
	c.send(t, id, "m2", "/image "+text)
	journal := c.waitJournal(t, id, 15*time.Second, "the images", turnsEnded(2))
	var parts []string
	text1 := ""
	for _, ev := range journal {
		if ev.Type == agent.TypePart && ev.Kind == agent.PartImage {
			parts = append(parts, ev.PartID)
		}
		if ev.Type == agent.TypePart && ev.Kind == agent.PartText && text1 == "" {
			text1 = ev.PartID
		}
	}
	if len(parts) != 2 {
		t.Fatalf("image parts %v", parts)
	}
	url := "/api/chats/" + id + "/parts/"

	res, _ := c.do(t, c.client, "GET", url+parts[0]+"/image", "")
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, b.Bytes()) ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the image: %d %q %d bytes", res.StatusCode, res.Header.Get("Content-Type"), len(got))
	}
	if res, _ := c.do(t, c.anon, "GET", url+parts[0]+"/image", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
	if res, _ := c.do(t, c.client, "GET", url+parts[1]+"/image", ""); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("a file that is not an image: %d", res.StatusCode)
	}
	if res, _ := c.do(t, c.client, "GET", url+text1+"/image", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a text part: %d", res.StatusCode)
	}
	// A FIFO at the path is no image, and opening it would wait for a
	// writer: it is refused at once.
	fifo := filepath.Join(dir, "pipe.png")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	c.send(t, id, "m3", "/image "+fifo)
	journal = c.waitJournal(t, id, 15*time.Second, "the FIFO's image", turnsEnded(3))
	pipe := ""
	for _, ev := range journal {
		if ev.Type == agent.TypePart && ev.Kind == agent.PartImage {
			pipe = ev.PartID
		}
	}
	quick := &http.Client{Jar: c.client.Jar, Timeout: 5 * time.Second}
	if res, _ := c.do(t, quick, "GET", url+pipe+"/image", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a FIFO: %d", res.StatusCode)
	}

	_ = os.Remove(pic)
	if res, _ := c.do(t, c.client, "GET", url+parts[0]+"/image", ""); res.StatusCode != http.StatusNotFound ||
		strings.HasPrefix(res.Header.Get("Content-Type"), "image/") {
		t.Fatalf("a file that is gone: %d", res.StatusCode)
	}
}

// An image a message carries is served by its name from the chat's
// attachments, to who may read the chat; a file there that is not an image,
// a name that leaves the folder and a hidden one are not.
func TestAnAttachedImageIsServedByItsName(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 3))); err != nil {
		t.Fatal(err)
	}
	if code, body := c.upload(t, id, map[string]string{"shot.png": b.String(), "fake.png": "<html><script>x</script>"}); code != http.StatusOK {
		t.Fatalf("upload: %d %v", code, body)
	}
	url := "/api/chats/" + id + "/attachments/"
	res, _ := c.do(t, c.client, "GET", url+"shot.png", "")
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, b.Bytes()) {
		t.Fatalf("the attached image: %d %q %d bytes", res.StatusCode, res.Header.Get("Content-Type"), len(got))
	}
	if res, _ := c.do(t, c.anon, "GET", url+"shot.png", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
	if res, _ := c.do(t, c.client, "GET", url+"fake.png", ""); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("a file that is not an image: %d", res.StatusCode)
	}
	if err := os.WriteFile(filepath.Join(c.srv.chatAttachments(id), ".hidden.png"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"..%2Fevents.jsonl", ".hidden.png", "missing.png"} {
		if res, _ := c.do(t, c.client, "GET", url+bad, ""); res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", bad, res.StatusCode)
		}
	}
}

// An image the agent showed in what it wrote (a Markdown image of a file on
// this machine) is served, by its absolute path or one relative to the
// chat's folder; a path no text of the agent's names is not, image or not.
func TestAnImageTheAgentShowedIsServed(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	dir := t.TempDir()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 5, 5))); err != nil {
		t.Fatal(err)
	}
	shown := filepath.Join(dir, "chart.png")
	other := filepath.Join(dir, "other.png")
	inFolder := filepath.Join(c.data, "out", "plot.png")
	_ = os.MkdirAll(filepath.Dir(inFolder), 0o755)
	for _, p := range []string{shown, other, inFolder} {
		if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The scripted agent says back what it is told: its answer names both.
	c.send(t, id, "m1", "Here: ![the chart]("+shown+") and ![](out/plot.png)")
	c.waitJournal(t, id, 15*time.Second, "the answer", turnsEnded(1))
	url := "/api/chats/" + id + "/image?path="
	for _, p := range []string{shown, "out/plot.png"} {
		res, _ := c.do(t, c.client, "GET", url+p, "")
		got, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, b.Bytes()) {
			t.Fatalf("%s: %d %q", p, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
	for _, p := range []string{other, "/etc/passwd", inFolder, ""} {
		if res, _ := c.do(t, c.client, "GET", url+p, ""); res.StatusCode != http.StatusNotFound {
			t.Fatalf("%q: %d", p, res.StatusCode)
		}
	}
	if res, _ := c.do(t, c.anon, "GET", url+shown, ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
}
