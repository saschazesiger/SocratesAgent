package docs

import (
	"bytes"
	"io"
	"strings"
)

// titleHead is how much of a file Title reads: a title is at the top.
const titleHead = 8 << 10

// Title is the title of the Markdown file rel under root: the text of its
// first level-1 ATX heading (`# Title`) in the first titleHead bytes, trimmed,
// or "" when there is none or the file cannot be served (Resolve). Headings
// inside fenced code blocks are not titles.
func Title(root, rel string) string {
	t, err := Resolve(root, rel)
	if err != nil || t.Redirect || t.Kind != Markdown {
		return ""
	}
	f, _, err := t.Open()
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, titleHead+1)
	n, _ := io.ReadFull(f, head)
	return headingTitle(head[:n], n > titleHead)
}

// headingTitle is the text of the first level-1 ATX heading of Markdown
// text that has any, trimmed, or "". cut says the text goes on after it,
// so that its last line, which may be cut short, is not taken.
func headingTitle(text []byte, cut bool) string {
	if len(text) > titleHead {
		text = text[:titleHead]
	}
	text = bytes.TrimPrefix(text, []byte("\uFEFF"))
	lines := strings.Split(string(text), "\n")
	if cut && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	fence := ""
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		// Up to three spaces of indent; four are a code block.
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent > 3 {
			continue
		}
		line = line[indent:]
		if fence != "" {
			if strings.HasPrefix(line, fence) && strings.Trim(line, fence[:1]+" \t") == "" {
				fence = ""
			}
			continue
		}
		if f := fenceOf(line); f != "" {
			fence = f
			continue
		}
		if title, ok := h1(line); ok && title != "" {
			return title
		}
	}
	return ""
}

// fenceOf is the fence a line opens a code block with (``` or ~~~, three or
// more), or "".
func fenceOf(line string) string {
	for _, c := range []string{"`", "~"} {
		n := len(line) - len(strings.TrimLeft(line, c))
		if n >= 3 {
			// A backtick fence's info string has no backtick.
			if c == "`" && strings.Contains(line[n:], "`") {
				return ""
			}
			return line[:n]
		}
	}
	return ""
}

// h1 is the text of a level-1 ATX heading line (no indent left): "#" then a
// space, a tab or nothing, its closing sequence of #s dropped.
func h1(line string) (string, bool) {
	if !strings.HasPrefix(line, "#") {
		return "", false
	}
	rest := line[1:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	// The closing sequence: #s after a space, or the whole rest.
	if trimmed := strings.TrimRight(rest, "#"); trimmed != rest {
		if trimmed == "" {
			rest = ""
		} else if strings.HasSuffix(trimmed, " ") || strings.HasSuffix(trimmed, "\t") {
			rest = strings.TrimSpace(trimmed)
		}
	}
	return rest, true
}
