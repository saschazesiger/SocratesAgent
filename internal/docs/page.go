package docs

import (
	"bytes"
	"html"
)

// KitPath is where the page kit's files are served (kit.css, kit.js;
// web.ArtifactKit): a page's origin is opaque, so they go with
// Access-Control-Allow-Origin: *. The kit keeps the name of the chat
// artifacts it was first made for, as does the id of the <pre> it reads.
const KitPath = "/artifact-kit/"

// Page is a Markdown file as the document a frame shows: the page kit
// renders it (marked, Mermaid) from the escaped text in a <pre> it replaces
// (the id is the one the kit looks for). title is the file's name, rel its
// path in the folder, on the body as data-docs so the kit knows it is a doc
// (its links lead to other docs, not out of the frame). stamp is the
// build's, added to the kit's addresses so that a page never pairs with an
// old kit ("" for none).
func Page(title, rel string, content []byte, stamp string) []byte {
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>")
	b.WriteString(html.EscapeString(title))
	q := ""
	if stamp != "" {
		q = "?v=" + html.EscapeString(stamp)
	}
	b.WriteString("</title>\n<link rel=\"stylesheet\" href=\"" + KitPath + "kit.css" + q + "\">\n" +
		"<script type=\"module\" src=\"" + KitPath + "kit.js" + q + "\"></script>\n</head>\n" +
		"<body data-docs=\"" + html.EscapeString(rel) + "\">\n")
	// The text shows as it is when the kit cannot load (a login proxy in
	// front of Socrates that wants a cookie the sandboxed page does not
	// send), and the newline after <pre> is the one the parser drops.
	b.WriteString("<main class=\"page\" data-format=\"markdown\">" +
		"<pre id=\"artifact-markdown\" style=\"white-space:pre-wrap;overflow-wrap:anywhere\">\n")
	b.WriteString(html.EscapeString(string(content)))
	b.WriteString("</pre></main>\n</body>\n</html>\n")
	return b.Bytes()
}
