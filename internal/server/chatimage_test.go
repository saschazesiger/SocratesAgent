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
	_ = os.Remove(pic)
	if res, _ := c.do(t, c.client, "GET", url+parts[0]+"/image", ""); res.StatusCode != http.StatusNotFound ||
		strings.HasPrefix(res.Header.Get("Content-Type"), "image/") {
		t.Fatalf("a file that is gone: %d", res.StatusCode)
	}
}
