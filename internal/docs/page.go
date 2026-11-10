package docs

import (
	"bytes"
	"html"

	"github.com/saschazesiger/SocratesAgent/internal/artifacts"
)

// Page is a Markdown file as the document a frame shows: the artifacts'
// page kit renders it (marked, Mermaid), exactly as it renders a Markdown
// artifact, from the escaped text in the same <pre>. title is the file's
// name, rel its path in the folder, on the body as data-docs so the kit
// knows it is a doc (its links lead to other docs, not out of the frame).
// stamp is the build's, added to the kit's addresses ("" for none).
func Page(title, rel string, content []byte, stamp string) []byte {
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>")
	b.WriteString(html.EscapeString(title))
	q := ""
	if stamp != "" {
		q = "?v=" + html.EscapeString(stamp)
	}
	b.WriteString("</title>\n<link rel=\"stylesheet\" href=\"" + artifacts.KitPath + "kit.css" + q + "\">\n" +
		"<script type=\"module\" src=\"" + artifacts.KitPath + "kit.js" + q + "\"></script>\n</head>\n" +
		"<body data-docs=\"" + html.EscapeString(rel) + "\">\n")
	// As in artifacts.Page: the text shows as it is when the kit cannot
	// load, and the newline after <pre> is the one the parser drops.
	b.WriteString("<main class=\"page\" data-format=\"markdown\">" +
		"<pre id=\"artifact-markdown\" style=\"white-space:pre-wrap;overflow-wrap:anywhere\">\n")
	b.WriteString(html.EscapeString(string(content)))
	b.WriteString("</pre></main>\n</body>\n</html>\n")
	return b.Bytes()
}
