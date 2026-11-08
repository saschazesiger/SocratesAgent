package artifacts

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
)

// KitPath is where the page kit's files are served (kit.css, kit.js): a
// page's origin is opaque, so they go with Access-Control-Allow-Origin: *.
const KitPath = "/artifact-kit/"

// Page is a version's content as the document a frame shows: a whole HTML
// document as it is, an HTML fragment inside the page kit's document, and
// Markdown as data the kit renders. stamp is the build's, added to the
// kit's addresses so that a page never pairs with an old kit ("" for none).
func Page(a Artifact, v Version, content []byte, stamp string) []byte {
	if v.Format != Markdown && IsDocument(content) {
		return content
	}
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>")
	b.WriteString(html.EscapeString(a.Title))
	q := ""
	if stamp != "" {
		q = "?v=" + html.EscapeString(stamp)
	}
	b.WriteString("</title>\n<link rel=\"stylesheet\" href=\"" + KitPath + "kit.css" + q + "\">\n" +
		"<script type=\"module\" src=\"" + KitPath + "kit.js" + q + "\"></script>\n</head>\n<body>\n")
	if v.Format == Markdown {
		// JSON as Go writes it escapes <, > and &, so nothing in the text
		// can end the script element.
		data, _ := json.Marshal(string(content))
		b.WriteString("<main class=\"page\" data-format=\"markdown\"></main>\n<script type=\"application/json\" id=\"artifact-markdown\">")
		b.Write(data)
		b.WriteString("</script>\n")
	} else {
		b.WriteString("<main class=\"page\">\n")
		b.Write(content)
		b.WriteString("\n</main>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return b.Bytes()
}

// IsDocument is whether HTML content is a whole document (it starts with a
// doctype or <html>, after white space and comments) rather than a
// fragment for the kit.
func IsDocument(content []byte) bool {
	s := string(content)
	if len(s) > 4096 {
		s = s[:4096]
	}
	s = strings.TrimPrefix(s, "\uFEFF")
	for {
		s = strings.TrimLeft(s, " \t\r\n\f")
		if !strings.HasPrefix(s, "<!--") {
			break
		}
		end := strings.Index(s, "-->")
		if end < 0 {
			return false
		}
		s = s[end+3:]
	}
	low := strings.ToLower(s)
	return strings.HasPrefix(low, "<!doctype") || strings.HasPrefix(low, "<html")
}
