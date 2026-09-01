// This file decides what a tool card says. OpenCode names its tools in
// lowercase - bash, read, edit, webfetch - and reports their arguments as raw
// JSON; a card wants a heading a person recognises and one line of input.
package opencode

import (
	"encoding/json"
	"strings"
)

// ------------------------------------------------------------- tool wording

// toolTitle is the heading of a tool card, written for a human.
func toolTitle(name string, input json.RawMessage) string {
	switch name {
	case "bash":
		return "Ran a command"
	case "read":
		return titleWith("Read", input, "filePath", "path")
	case "write":
		return titleWith("Wrote", input, "filePath", "path")
	case "edit", "apply_patch":
		return titleWith("Edited", input, "filePath", "path")
	case "glob", "grep":
		return "Searched the code"
	case "list":
		return titleWith("Listed", input, "path")
	case "webfetch":
		return titleWith("Fetched", input, "url")
	case "websearch":
		return "Searched the web"
	case "todowrite", "todoread":
		return "Updated the plan"
	case "task":
		return "Ran a subagent"
	case "question":
		return "Asked a question"
	case "skill":
		return "Used a skill"
	case "":
		return "Ran a tool"
	default:
		return "Ran " + name
	}
}

func titleWith(verb string, input json.RawMessage, keys ...string) string {
	if v := field(input, keys...); v != "" {
		return verb + " " + v
	}
	return verb + " a file"
}

// toolSummary is the one-line input shown on the card. It prefers the field a
// person would recognise and falls back to the compact arguments.
func toolSummary(name string, input json.RawMessage) string {
	if v := field(input, "command", "filePath", "path", "pattern", "url", "query", "description", "prompt"); v != "" {
		return oneLine(v)
	}
	return oneLine(compact(input))
}

// field returns the first of keys that is a non-empty string in input.
func field(input json.RawMessage, keys ...string) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
