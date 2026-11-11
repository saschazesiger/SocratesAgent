package docs

import (
	"strings"
	"testing"
)

// A file's title is its first level-1 ATX heading, trimmed, past text,
// deeper headings, setext headings, indented and fenced code; "" without.
func TestHeadingTitle(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"# The plan\n\ntext", "The plan"},
		{"\uFEFF#   Spaced out   \r\nmore", "Spaced out"},
		{"Intro\n\n## Section\n\n# Later", "Later"},
		{"#Not a heading\n# Real", "Real"},
		{"   # Three spaces", "Three spaces"},
		{"    # Code block\n", ""},
		{"```bash\n# a comment\n```\n# After the fence", "After the fence"},
		{"~~~~\n# inside\n~~~\n# still inside\n~~~~\n# Out", "Out"},
		{"# Closed ##", "Closed"},
		{"# C# notes", "C# notes"},
		{"#\n# Second", "Second"},
		{"Title\n=====\n", ""},
		{"no heading at all", ""},
		{"", ""},
	} {
		if got := headingTitle([]byte(c.text), false); got != c.want {
			t.Errorf("%q: %q, want %q", c.text, got, c.want)
		}
	}
	// A cut text's last line may be cut short, and is no title.
	if got := headingTitle([]byte("text\n# Cut sh"), true); got != "" {
		t.Errorf("a cut line: %q", got)
	}
}

// Title reads the first 8 KB of a file the docs serve, and nothing else.
func TestTitle(t *testing.T) {
	root := tree(t, map[string]string{
		"plan.md":      "# Plan\n",
		"late.md":      strings.Repeat("filler line\n", 1000) + "# Too late\n",
		"none.md":      "text",
		".hidden/x.md": "# Hidden",
		"img.png":      "# Not Markdown",
	})
	for rel, want := range map[string]string{"plan.md": "Plan", "late.md": "", "none.md": "", ".hidden/x.md": "",
		"img.png": "", "missing.md": "", "../plan.md": ""} {
		if got := Title(root, rel); got != want {
			t.Errorf("%s: %q, want %q", rel, got, want)
		}
	}
}
