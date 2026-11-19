package docs

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"syscall"
	"unicode/utf8"
)

// A chat's files (docs/design/PAGES.md, "Any file"): besides Markdown and
// pictures, a chat's pages folder holds whatever its agent wants to show -
// a video, a recording, a PDF, an HTML report, a CSV, a log - and a chat
// shows a file its agent links wherever it lies. FileKind says how the app
// shows one: in a player, in the browser's PDF viewer, in a sandboxed frame,
// as text, or as a card to download it.
const (
	Video Kind = "video"
	Audio Kind = "audio"
	PDF   Kind = "pdf"
	HTML  Kind = "html"
	Text  Kind = "text"
	// Other is any other file: offered for download.
	Other Kind = "file"
)

// fileTypes are the files a browser shows itself, by extension, with the
// type they are served as.
var fileTypes = map[string]struct {
	kind  Kind
	ctype string
}{
	".mp4": {Video, "video/mp4"}, ".m4v": {Video, "video/mp4"}, ".webm": {Video, "video/webm"},
	".mov": {Video, "video/quicktime"}, ".ogv": {Video, "video/ogg"}, ".mkv": {Video, "video/x-matroska"},
	".avi": {Video, "video/x-msvideo"}, ".mpeg": {Video, "video/mpeg"}, ".mpg": {Video, "video/mpeg"},
	".3gp": {Video, "video/3gpp"},

	".mp3": {Audio, "audio/mpeg"}, ".wav": {Audio, "audio/wav"}, ".ogg": {Audio, "audio/ogg"},
	".oga": {Audio, "audio/ogg"}, ".opus": {Audio, "audio/ogg"}, ".m4a": {Audio, "audio/mp4"},
	".aac": {Audio, "audio/aac"}, ".flac": {Audio, "audio/flac"}, ".weba": {Audio, "audio/webm"},
	".aif": {Audio, "audio/aiff"}, ".aiff": {Audio, "audio/aiff"}, ".mid": {Audio, "audio/midi"},
	".midi": {Audio, "audio/midi"},

	".pdf": {PDF, "application/pdf"},

	".html": {HTML, "text/html; charset=utf-8"}, ".htm": {HTML, "text/html; charset=utf-8"},
	".xhtml": {HTML, "text/html; charset=utf-8"},
}

// textExts are files that are text by their name: shown as text, served as
// text/plain, never as what a browser would run (a script, a page).
var textExts = map[string]bool{
	".txt": true, ".text": true, ".log": true, ".out": true, ".err": true, ".csv": true, ".tsv": true,
	".json": true, ".jsonl": true, ".ndjson": true, ".geojson": true, ".yaml": true, ".yml": true,
	".toml": true, ".xml": true, ".ini": true, ".cfg": true, ".conf": true, ".properties": true,
	".srt": true, ".vtt": true, ".sql": true, ".diff": true, ".patch": true, ".rst": true, ".adoc": true,
	".tex": true, ".bib": true, ".org": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".ps1": true, ".bat": true, ".py": true, ".rb": true, ".pl": true, ".php": true, ".lua": true,
	".r": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".go": true, ".mod": true, ".sum": true, ".rs": true, ".java": true, ".kt": true, ".kts": true,
	".scala": true, ".swift": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true,
	".cs": true, ".m": true, ".dart": true, ".ex": true, ".exs": true, ".erl": true, ".hs": true,
	".clj": true, ".css": true, ".scss": true, ".sass": true, ".less": true, ".vue": true,
	".svelte": true, ".astro": true, ".graphql": true, ".gql": true, ".proto": true, ".tf": true,
	".hcl": true, ".nix": true, ".gradle": true, ".cmake": true, ".mk": true, ".dockerfile": true,
	".env.example": true, ".lock": true, ".gitignore": true, ".editorconfig": true, ".mmd": true,
	".dot": true, ".gv": true, ".ics": true, ".vcf": true, ".http": true,
}

// textNames are files without an extension that are text by their name.
var textNames = map[string]bool{
	"makefile": true, "dockerfile": true, "containerfile": true, "procfile": true, "gemfile": true,
	"rakefile": true, "vagrantfile": true, "jenkinsfile": true, "license": true, "licence": true,
	"copying": true, "notice": true, "authors": true, "changelog": true, "readme": true, "todo": true,
}

// FileKind is how a file is shown, by its name: never "" (Other for a file
// whose name says nothing). A chat's files are any file; the Docs view of a
// project folder keeps to KindOf.
func FileKind(name string) Kind {
	if k := KindOf(name); k != "" {
		return k
	}
	base := strings.ToLower(path.Base(name))
	ext := path.Ext(base)
	if t, ok := fileTypes[ext]; ok {
		return t.kind
	}
	if textExts[ext] || (ext == "" && textNames[base]) {
		return Text
	}
	return Other
}

// FileContentType is the type a file of a kind is served as: what a browser
// shows inline for the kinds it shows, text/plain for text (never a script
// or a page), and for any other file its registered type or
// application/octet-stream.
func FileContentType(name string, kind Kind) string {
	ext := strings.ToLower(path.Ext(name))
	switch kind {
	case Image:
		return ContentType(name)
	case Markdown, Text:
		return "text/plain; charset=utf-8"
	case Video, Audio, PDF, HTML:
		if t, ok := fileTypes[ext]; ok {
			return t.ctype
		}
	}
	if t := mime.TypeByExtension(ext); t != "" && !risky(t) {
		return t
	}
	return "application/octet-stream"
}

// risky is a type a browser would run or render as a document of its own:
// such a file of an unknown kind is only ever a download.
func risky(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, "html") || strings.Contains(t, "javascript") || strings.Contains(t, "xml") ||
		strings.Contains(t, "svg") || strings.HasPrefix(t, "text/") || strings.Contains(t, "ecmascript")
}

// SniffKind refines Other by the file's first bytes: a file whose name says
// nothing (a "notes", an "output.1") is Text when it reads as UTF-8 text,
// and a video, an audio or a PDF when its bytes say so. Any other kind is
// kept as it is.
func SniffKind(f io.ReaderAt, kind Kind) Kind {
	if kind != Other {
		return kind
	}
	head := make([]byte, 512)
	n, _ := f.ReadAt(head, 0)
	head = head[:n]
	if n == 0 {
		return Text
	}
	switch t := http.DetectContentType(head); {
	case strings.HasPrefix(t, "video/"):
		return Video
	case strings.HasPrefix(t, "audio/"):
		return Audio
	case t == "application/pdf":
		return PDF
	case strings.HasPrefix(t, "text/plain"):
		if bytes.IndexByte(head, 0) < 0 && (utf8.Valid(head) || utf8.Valid(trimRune(head))) {
			return Text
		}
	}
	return kind
}

// SniffedContentType is the type a sniffed kind is served as: the bytes'
// own for a video, an audio or a PDF, text/plain for text.
func SniffedContentType(f io.ReaderAt, kind Kind) string {
	switch kind {
	case Text:
		return "text/plain; charset=utf-8"
	case Video, Audio, PDF:
		head := make([]byte, 512)
		n, _ := f.ReadAt(head, 0)
		return http.DetectContentType(head[:n])
	}
	return "application/octet-stream"
}

// trimRune is b without the bytes of a rune cut at its end, which a head of
// 512 bytes may have.
func trimRune(b []byte) []byte {
	for i := 1; i <= 3 && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			return b[:len(b)-i]
		}
	}
	return b
}

// OpenFile opens a regular file for serving: looked at before it is opened
// (opening a FIFO would wait for a writer) and again once open, non
// blocking, so that it is the same regular file both times.
func OpenFile(name string) (*os.File, os.FileInfo, error) {
	before, err := os.Stat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, ErrNotServed
	}
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		f.Close()
		return nil, nil, ErrNotServed
	}
	return f, info, nil
}
