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
		"for the same subject. Start a page with a # title. In a page, link a Markdown file of the working " +
		"directory by its absolute path, never by a path relative to the working directory (in a page that means " +
		"the pages folder) and never only as a `code` path; another page by its name, e.g. [The plan](plan.md). " +
		"Not for a few lines of text. The pages are deleted with the chat.\n\n" +
		"Files: Socrates shows any other file beside the chat too - a video, a recording, a picture, a PDF, an " +
		"HTML page, a CSV, a log - in a player or a viewer for its kind, and lists every file you link with the " +
		"pages. To show the person a file, link it in your reply as a Markdown link with its absolute path (or one " +
		"relative to the working directory), e.g. [The demo](/srv/app/out/demo.mp4): never only as a `code` path, " +
		"and never through a web server, a tunnel or a share of your own. What is worth keeping with the chat " +
		"(a final video, a report) can go into the pages folder: copy it there, an HTML page with the files it " +
		"loads, and a page can play it, <video src=\"demo.mp4\" controls></video> or <audio src=\"take.mp3\" " +
		"controls></audio>. Make what the person should watch or hear in a format every browser plays: MP4 " +
		"(H.264 and AAC) or WebM video, MP3, M4A or WAV audio."
	if got := PagesGuidance(dir); got != want {
		t.Fatalf("guidance:\n%s", got)
	}
	if got := WithPagesGuidance("x", dir); got != "x\n\n"+want {
		t.Fatalf("appended:\n%s", got)
	}
}
