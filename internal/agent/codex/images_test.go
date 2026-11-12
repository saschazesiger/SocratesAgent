//go:build !windows

package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// pngBytes is a small PNG, as a screenshot tool would return one.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// mcpScreenshot is a completed mcpToolCall item whose result holds a text
// block and an image block.
func mcpScreenshot(t *testing.T, id string, data []byte) json.RawMessage {
	t.Helper()
	return jsonOf(map[string]any{"type": "mcpToolCall", "id": id, "server": "browser", "tool": "browser_screenshot",
		"arguments": map[string]any{}, "status": "completed", "result": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "Took a screenshot"},
			map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": "image/png"},
		}}})
}

// checkImage says what is wrong with an image part of a call: inside the
// call, done, its file under the state dir with the image's bytes.
func checkImage(t *testing.T, ev agent.Event, id, parent, stateDir string, data []byte) {
	t.Helper()
	var c struct {
		Mime string `json:"mime"`
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	_ = json.Unmarshal(ev.Content, &c)
	if ev.Type != agent.TypePart || ev.Kind != agent.PartImage || ev.State != agent.PartDone || ev.PartID != id ||
		ev.ParentPartID != parent || c.Mime != "image/png" || c.Size != len(data) || !filepath.IsAbs(c.Path) ||
		filepath.Dir(c.Path) != filepath.Join(stateDir, "images") || !strings.HasSuffix(c.Path, ".png") {
		t.Fatalf("image part %s", dump([]agent.Event{ev}))
	}
	got, err := os.ReadFile(c.Path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the image's file: %v", err)
	}
	if info, err := os.Stat(c.Path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the image's file mode: %v %v", info.Mode(), err)
	}
}

// An image an MCP tool returned is an image part inside the call's part,
// its file under the state dir; the call keeps its text and the image's
// type, not its base64.
func TestAnMCPToolsImageIsAnImagePart(t *testing.T) {
	data := pngBytes(t)
	stateDir := t.TempDir()
	a := newAdapter()
	r := newRecorder()
	a.out, a.thread, a.spec.StateDir = r, "th", stateDir
	frame := func(method string, params map[string]any) *frame {
		return &frame{Method: method, Params: jsonOf(params)}
	}
	a.handle(frame("turn/started", map[string]any{"threadId": "th", "turn": map[string]any{"id": "tu", "status": "inProgress"}}))
	item := mcpScreenshot(t, "call1", data)
	var started map[string]any
	_ = json.Unmarshal(item, &started)
	started["status"], started["result"] = "inProgress", nil
	a.handle(frame("item/started", map[string]any{"threadId": "th", "turnId": "tu", "item": started}))
	n := len(r.all())
	a.handle(frame("item/completed", map[string]any{"threadId": "th", "turnId": "tu", "item": item}))

	events := r.since(n)
	if len(events) != 2 {
		t.Fatalf("want the call and its image:\n%s", dump(events))
	}
	call := events[0]
	if call.Kind != agent.PartTool || call.State != agent.PartDone {
		t.Fatalf("the call %s", dump(events))
	}
	if bytes.Contains(call.Content, []byte(base64.StdEncoding.EncodeToString(data))) ||
		!bytes.Contains(call.Content, []byte(`"Took a screenshot"`)) || !bytes.Contains(call.Content, []byte(`"mimeType":"image/png"`)) {
		t.Fatalf("the call's content %s", call.Content)
	}
	checkImage(t, events[1], call.PartID+"_img0", call.PartID, stateDir, data)
	if img := findPart(r.projection(), func(pt *agent.Part) bool { return pt.Kind == agent.PartImage }); img == nil {
		t.Fatalf("the projection has no image part:\n%s", dump(r.all()))
	}
}

// A rebuild maps a call's images as the live mapping does, with the same
// ids each time, and the parts after them keep their numbers.
func TestARebuiltMCPToolsImageIsAnImagePart(t *testing.T) {
	data := pngBytes(t)
	stateDir := t.TempDir()
	items := []json.RawMessage{
		json.RawMessage(`{"type":"userMessage","id":"u","content":[{"type":"text","text":"look"}]}`),
		mcpScreenshot(t, "call1", data),
		json.RawMessage(`{"type":"agentMessage","id":"m","text":"done"}`),
	}
	first := historyEvents(historyTurn{ID: "x", Status: "completed"}, items, stateDir)
	again := historyEvents(historyTurn{ID: "x", Status: "completed"}, items, stateDir)
	var parts []agent.Event
	for _, ev := range first {
		if ev.Type == agent.TypePart {
			parts = append(parts, ev)
		}
	}
	if len(parts) != 3 || parts[0].PartID != "t_x_a_p1" || parts[0].Kind != agent.PartTool ||
		parts[2].PartID != "t_x_a_p2" || parts[2].Text != "done" {
		t.Fatalf("parts:\n%s", dump(first))
	}
	if bytes.Contains(parts[0].Content, []byte(base64.StdEncoding.EncodeToString(data))) {
		t.Fatalf("the call keeps the image's base64: %s", parts[0].Content)
	}
	checkImage(t, parts[1], "t_x_a_p1_img0", "t_x_a_p1", stateDir, data)
	if dump(first) != dump(again) {
		t.Fatalf("a second rebuild differs:\n%s\n%s", dump(first), dump(again))
	}
}

// A result without media is left as it was; an image that does not decode
// is no part; a resource's blob and an audio block's data go too.
func TestMCPImagesLeavesTheRestAlone(t *testing.T) {
	plain := json.RawMessage(`{"content":[{"type":"text","text":"hi"}],"structuredContent":{"a":1}}`)
	if images, out := mcpImages(plain); len(images) != 0 || string(out) != string(plain) {
		t.Fatalf("a text result: %v %s", images, out)
	}
	if images, out := mcpImages(nil); len(images) != 0 || out != nil {
		t.Fatalf("no result: %v %s", images, out)
	}
	mixed := json.RawMessage(`{"content":[{"type":"audio","data":"AAAA","mimeType":"audio/wav"},` +
		`{"type":"resource","resource":{"uri":"file:///x.bin","blob":"AAAA"}},{"type":"image","data":"not base64!","mimeType":"image/png"}]}`)
	images, out := mcpImages(mixed)
	if len(images) != 1 || bytes.Contains(out, []byte("AAAA")) || bytes.Contains(out, []byte("not base64")) ||
		!bytes.Contains(out, []byte(`"uri":"file:///x.bin"`)) {
		t.Fatalf("media: %v %s", images, out)
	}
	if events := imageEvents(t.TempDir(), "m", "p", images); len(events) != 0 {
		t.Fatalf("an image that does not decode: %s", dump(events))
	}
}
