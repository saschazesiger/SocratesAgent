package agent

import (
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// PagesDir is a chat's pages folder, <data>/chats/<id>/pages
// (paths.ChatPages): the Markdown files its agent writes there and links in
// the chat, the Docs view shows beside the chat (docs/design/DOCS.md). It is
// absolute, and "" when the data folder or the chat is not known.
func PagesDir(dataDir, chatID string) string {
	if dataDir == "" || chatID == "" {
		return ""
	}
	dir := filepath.Join(dataDir, paths.Chats, chatID, paths.ChatPages)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir
}

// PagesDir is the chat's pages folder, "" outside a chat.
func (s StartSpec) PagesDir() string { return PagesDir(s.DataDir, s.ChatID) }

// PagesGuidance is the instruction every chat gets about its pages folder
// dir: what to write there instead of a long chat answer, and how to point
// to it. It is "" without a folder.
func PagesGuidance(dir string) string {
	if dir == "" {
		return ""
	}
	return strings.ReplaceAll(pagesGuidance, "<dir>", dir)
}

const pagesGuidance = "Pages: when something is easier to take in as a document than as chat text - an " +
	"explanation, a plan, a comparison, a report, a diagram - write it as a Markdown file in this chat's pages " +
	"folder <dir> (it exists and is not part of any repository) and link it in your reply with its absolute " +
	"path, e.g. [The plan](<dir>/plan.md). Socrates lists the chat's pages beside the chat and shows them " +
	"rendered, with Mermaid diagrams (flowcharts, sequence, pie and xy charts ...), tables and GitHub alerts " +
	"(> [!NOTE]), and draws a page again when you change it: edit the same file rather than writing a new one " +
	"for the same subject. Start a page with a # title. Not for a few lines of text. The pages are deleted " +
	"with the chat."

// WithPagesGuidance is prompt with PagesGuidance(dir) after it, a blank
// line between, or prompt as it is without a folder.
func WithPagesGuidance(prompt, dir string) string {
	if g := PagesGuidance(dir); g != "" {
		return prompt + "\n\n" + g
	}
	return prompt
}
