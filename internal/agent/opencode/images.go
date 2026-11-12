//go:build !windows

package opencode

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// toolImage is an image a tool call returned, still base64.
type toolImage struct {
	mime string
	data string
}

// toolImages are the images among a completed tool call's attachments:
// OpenCode makes each image block of an MCP tool's result a file part with
// a data: URL (data:<mime>;base64,<data>). Other files are left out.
func toolImages(attachments []ocPart) []toolImage {
	var out []toolImage
	for _, f := range attachments {
		rest, ok := strings.CutPrefix(f.URL, "data:")
		if !ok {
			continue
		}
		header, data, ok := strings.Cut(rest, ",")
		params := strings.Split(header, ";")
		mime := f.Mime
		if mime == "" {
			mime = params[0]
		}
		if !ok || data == "" || params[len(params)-1] != "base64" || !strings.HasPrefix(mime, "image/") {
			continue
		}
		out = append(out, toolImage{mime: mime, data: data})
	}
	return out
}

// imageEvents are the image parts of a tool call's images, inside the call's
// part, each kept as a file under the adapter's state dir: <parent>_img<i>,
// the same ids whenever the same call is mapped again (a catch-up, a
// rebuild). An image that does not decode or cannot be written is left out.
func imageEvents(stateDir, msg, parent string, images []toolImage) []agent.Event {
	var events []agent.Event
	for i, img := range images {
		path, size, ok := agent.SaveImage(stateDir, img.mime, img.data)
		if !ok {
			continue
		}
		content, _ := json.Marshal(map[string]any{"mime": img.mime, "path": path, "size": size})
		events = append(events, agent.Event{Type: agent.TypePart, MessageID: msg, PartID: fmt.Sprintf("%s_img%d", parent, i),
			ParentPartID: parent, Kind: agent.PartImage, State: agent.PartDone, Content: content})
	}
	return events
}
