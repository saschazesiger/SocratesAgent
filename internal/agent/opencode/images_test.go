//go:build linux

package opencode

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

// screenshotPart is an MCP tool part of the chat's session; a completed one
// has its image as an attachment with a data: URL, as OpenCode keeps an MCP
// result's image block, and a PDF attachment beside it.
func screenshotPart(data []byte, status string) string {
	attachments := ""
	if status == toolCompleted {
		attachments = `,"attachments":[{"id":"prt_att1","sessionID":"ses_root","messageID":"msg_a1","type":"file","mime":"image/png",` +
			`"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(data) + `"},` +
			`{"id":"prt_att2","type":"file","mime":"application/pdf","url":"data:application/pdf;base64,JVBERi0="}]`
	}
	return `{"id":"prt_shot","sessionID":"ses_root","messageID":"msg_a1","type":"tool","tool":"browser_browser_screenshot","callID":"c1",
		"state":{"status":"` + status + `","input":{},"output":"Took a screenshot","title":"","metadata":{},"time":{"start":100,"end":110}` +
		attachments + `}}`
}

// checkImage says what is wrong with an image part of a call: inside the
// call, done, its file under the state dir with the image's bytes.
func checkImage(t *testing.T, ev agent.Event, stateDir string, data []byte) {
	t.Helper()
	var c struct {
		Mime string `json:"mime"`
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	_ = json.Unmarshal(ev.Content, &c)
	if ev.Type != agent.TypePart || ev.Kind != agent.PartImage || ev.State != agent.PartDone || ev.PartID != "prt_shot_img0" ||
		ev.ParentPartID != "prt_shot" || ev.MessageID != "msg_a1" || c.Mime != "image/png" || c.Size != len(data) ||
		!filepath.IsAbs(c.Path) || filepath.Dir(c.Path) != filepath.Join(stateDir, "images") || !strings.HasSuffix(c.Path, ".png") {
		raw, _ := json.Marshal(ev)
		t.Fatalf("image part %s", raw)
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
// once, when the call ends; the call's text is the tool's text only.
func TestAnMCPToolsImageIsAnImagePart(t *testing.T) {
	data := pngBytes(t)
	stateDir := t.TempDir()
	rec := newRecorder()
	a := New()
	a.out, a.session, a.spec.StateDir = rec, "ses_root", stateDir
	a.handlePart(0, mustPart(t, screenshotPart(data, toolRunning)))
	a.handlePart(0, mustPart(t, screenshotPart(data, toolCompleted)))
	// A catch-up that reads the call again writes nothing new.
	a.handlePart(0, mustPart(t, screenshotPart(data, toolCompleted)))
	var parts []agent.Event
	rec.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.Type == agent.TypePart {
				parts = append(parts, ev)
			}
		}
	})
	if len(parts) != 3 || parts[1].PartID != "prt_shot" || parts[1].State != agent.PartDone || parts[1].Text != "Took a screenshot" ||
		bytes.Contains(parts[1].Content, []byte("base64")) {
		t.Fatalf("parts %+v", parts)
	}
	checkImage(t, parts[2], stateDir, data)
}

// A rebuild maps a call's images as the live mapping does, with the same
// ids.
func TestARebuiltMCPToolsImageIsAnImagePart(t *testing.T) {
	data := pngBytes(t)
	stateDir := t.TempDir()
	msgs := []ocMessageWithParts{
		{Info: ocMessage{ID: "msg_u1", SessionID: "ses_root", Role: "user"}},
		{Info: ocMessage{ID: "msg_a1", SessionID: "ses_root", Role: "assistant", ParentID: "msg_u1"},
			Parts: []ocPart{mustPart(t, screenshotPart(data, toolCompleted))}},
	}
	msgs[1].Info.Time.Completed = 120
	var parts []agent.Event
	for _, ev := range historyEvents(msgs, "", stateDir, nil) {
		if ev.Type == agent.TypePart {
			parts = append(parts, ev)
		}
	}
	if len(parts) != 2 || parts[0].PartID != "prt_shot" || parts[0].Kind != agent.PartTool {
		t.Fatalf("parts %+v", parts)
	}
	checkImage(t, parts[1], stateDir, data)
}

// Only images with a base64 data: URL are images of a call; a file:// one,
// another file and a running call's are not.
func TestToolImages(t *testing.T) {
	images := toolImages([]ocPart{
		{Mime: "image/png", URL: "file:///tmp/a.png"},
		{Mime: "text/plain", URL: "data:text/plain;base64,aGk="},
		{Mime: "image/png", URL: "data:image/png,raw"},
		{URL: "data:image/jpeg;base64,/9j/"},
	})
	if len(images) != 1 || images[0].mime != "image/jpeg" || images[0].data != "/9j/" {
		t.Fatalf("images %+v", images)
	}
	running := mustPart(t, screenshotPart(pngBytes(t), toolRunning))
	running.State.Attachments = []ocPart{{Mime: "image/png", URL: "data:image/png;base64,AAAA"}}
	if c := convertTool(running); len(c.images) != 0 {
		t.Fatalf("a running call's images %+v", c.images)
	}
}
