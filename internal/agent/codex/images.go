package codex

import (
	"encoding/json"
	"fmt"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// toolImage is an image block of an MCP tool call's result, still base64.
type toolImage struct {
	mime string
	data string
}

// mcpImages takes the media out of an MCP tool call's result (McpToolCallResult,
// MCP content blocks): the image blocks, as images the chat shows as parts of
// their own, and the result without the base64 of any block, which would only
// bloat the journal. A block keeps its type and mime type, a resource its uri.
func mcpImages(result json.RawMessage) ([]toolImage, json.RawMessage) {
	var r map[string]json.RawMessage
	if len(result) == 0 || json.Unmarshal(result, &r) != nil {
		return nil, result
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(r["content"], &blocks) != nil {
		return nil, result
	}
	var images []toolImage
	changed := false
	for _, b := range blocks {
		var typ, data, mime string
		_ = json.Unmarshal(b["type"], &typ)
		_ = json.Unmarshal(b["data"], &data)
		_ = json.Unmarshal(b["mimeType"], &mime)
		if typ == "image" && data != "" {
			images = append(images, toolImage{mime: mime, data: data})
		}
		if _, ok := b["data"]; ok && typ != "text" {
			delete(b, "data")
			changed = true
		}
		var res map[string]json.RawMessage
		if typ == "resource" && json.Unmarshal(b["resource"], &res) == nil {
			if _, ok := res["blob"]; ok {
				delete(res, "blob")
				b["resource"] = jsonOf(res)
				changed = true
			}
		}
	}
	if !changed {
		return images, result
	}
	r["content"] = jsonOf(blocks)
	return images, jsonOf(r)
}

// imageEvents are the image parts of a tool call's images, inside the call's
// part, each kept as a file under the adapter's state dir: <parent>_img<i>,
// the same ids whenever the same call is mapped again (a rebuild). An image
// that does not decode or cannot be written is left out.
func imageEvents(stateDir, msgID, parent string, images []toolImage) []agent.Event {
	var events []agent.Event
	for i, img := range images {
		path, size, ok := agent.SaveImage(stateDir, img.mime, img.data)
		if !ok {
			continue
		}
		events = append(events, agent.Event{Type: agent.TypePart, MessageID: msgID, PartID: fmt.Sprintf("%s_img%d", parent, i),
			ParentPartID: parent, Kind: agent.PartImage, State: agent.PartDone,
			Content: jsonOf(map[string]any{"mime": img.mime, "path": path, "size": size})})
	}
	return events
}
