package agent

import "testing"

// The pages guidance names the chat's folder, and is nothing without one.
func TestPagesGuidance(t *testing.T) {
	if PagesGuidance("") != "" || WithPagesGuidance("x", "") != "x" || PagesDir("", "c1") != "" || PagesDir("/d", "") != "" {
		t.Fatal("guidance without a folder")
	}
	dir := PagesDir("/d", "c1")
	if dir != "/d/chats/c1/pages" || (StartSpec{DataDir: "/d", ChatID: "c1"}).PagesDir() != dir {
		t.Fatalf("pages dir: %q", dir)
	}
	want := "Pages: when something is easier to take in as a document than as chat text - an explanation, a plan, a " +
		"comparison, a report, a diagram - write it as a Markdown file in this chat's pages folder /d/chats/c1/pages " +
		"(it exists and is not part of any repository) and link it in your reply with its absolute path, e.g. " +
		"[The plan](/d/chats/c1/pages/plan.md). Socrates lists the chat's pages beside the chat and shows them " +
		"rendered, with Mermaid diagrams (flowcharts, sequence, pie and xy charts ...), tables and GitHub alerts " +
		"(> [!NOTE]), and draws a page again when you change it: edit the same file rather than writing a new one " +
		"for the same subject. Start a page with a # title. Not for a few lines of text. The pages are deleted with " +
		"the chat."
	if got := PagesGuidance(dir); got != want {
		t.Fatalf("guidance:\n%s", got)
	}
	if got := WithPagesGuidance("x", dir); got != "x\n\n"+want {
		t.Fatalf("appended:\n%s", got)
	}
}
