package server

import (
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/docs"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// A chat's files (docs/design/PAGES.md, "Any file"): a file its agent links
// or names in the chat - a video it cut, a recording, a PDF, a report, a
// log, wherever on this machine it lies - opens beside the chat, in a player
// or a viewer for its kind. The chat's pages folder is served by the Docs
// routes (docs.go); every other file goes through these routes, which serve
// a file only when a text of the agent's in this chat names it: what the
// agent showed, never any file a browser asks for.
//
//	GET /api/chats/{id}/file?path=<as named>[&download=1]   the file
//	GET /api/chats/{id}/file/info?path=<as named>           {path, name, size, mtime, kind}
//	GET /api/chats/{id}/files                               {files: [...]}: what the agent linked

const (
	// maxLinkedRefs is the most distinct paths a chat's texts are looked
	// into for its linked files, the newest first.
	maxLinkedRefs = 400
	// maxLinkedFiles is the most linked files listed.
	maxLinkedFiles = 100
	// linkedTTL is how long a chat's linked files are kept when its
	// conversation did not change: a file named before may be written since.
	linkedTTL = 10 * time.Second
)

// chatFileTarget is the file a chat names by raw (a link's path or a `code`
// path as the agent wrote it): absolute, a file:// address, or relative to
// the chat's working directory. ok is false for a path that is no file
// path at all.
func chatFileTarget(row *store.Session, raw string) (string, bool) {
	ref := strings.TrimPrefix(strings.TrimPrefix(raw, "file://localhost"), "file://")
	if ref == "" || strings.ContainsRune(ref, 0) || len(ref) > 4096 || strings.Contains(ref, "://") {
		return "", false
	}
	if !filepath.IsAbs(ref) {
		if row.Workdir == "" {
			return "", false
		}
		ref = filepath.Join(row.Workdir, ref)
	}
	return filepath.Clean(ref), true
}

// chatNamedFile answers the file a request's ?path= names, open, when the
// agent named that path in the chat as a whole (chatRefs: a link's target, a
// `code` path, a bare absolute path - never any part of its text) and it is
// a regular file; else it has said why not.
func (s *Server) chatNamedFile(w http.ResponseWriter, r *http.Request) (*os.File, os.FileInfo, string, bool) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return nil, nil, "", false
	}
	raw := r.URL.Query().Get("path")
	abs, ok := chatFileTarget(row, raw)
	if !ok || !hasExt(raw) {
		writeError(w, http.StatusNotFound, "the chat names no such file")
		return nil, nil, "", false
	}
	refs, err := s.chatRefs(row.ID, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the chat could not be read")
		return nil, nil, "", false
	}
	if !refs.names[raw] {
		writeError(w, http.StatusNotFound, "the chat names no such file")
		return nil, nil, "", false
	}
	f, info, err := docs.OpenFile(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, "That file is not there.")
		return nil, nil, "", false
	}
	return f, info, abs, true
}

// hasExt is whether a path ends in a file name with an extension: what a
// file the chat shows is named by.
func hasExt(ref string) bool {
	base := path.Base(strings.TrimRight(ref, "/"))
	return len(ref) >= 4 && strings.Contains(strings.TrimPrefix(base, "."), ".")
}

// handleChatFile serves a file the agent named, as its kind is shown
// (serveChatFile).
func (s *Server) handleChatFile(w http.ResponseWriter, r *http.Request) {
	f, info, abs, ok := s.chatNamedFile(w, r)
	if !ok {
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, no-cache")
	serveChatFile(w, r, f, info, abs, docs.FileKind(abs), false)
}

// handleChatFileInfo says what a file the agent named is, for the viewer
// beside the chat: {path (absolute), name, size, mtime, kind}.
func (s *Server) handleChatFileInfo(w http.ResponseWriter, r *http.Request) {
	f, info, abs, ok := s.chatNamedFile(w, r)
	if !ok {
		return
	}
	defer f.Close()
	writeJSON(w, http.StatusOK, map[string]any{
		"path": abs, "name": filepath.Base(abs), "size": info.Size(), "mtime": info.ModTime().UnixMilli(),
		"kind": docs.SniffKind(f, docs.FileKind(abs)),
	})
}

// serveChatFile answers a file of a chat as its kind is shown: a video, an
// audio or a picture as its type (with ranges, for seeking), a PDF for the
// browser's viewer, an HTML page in a sandbox of an opaque origin, where its
// scripts may run but act as nobody, text as text/plain (never as a script
// or a page), and any other file as a download. Nothing but the HTML page
// and the PDF is a document of its own (CSP sandbox), and nothing is sniffed
// into another type. anyOrigin lets a page of an opaque origin (an HTML page
// of the same pages folder) read it.
func serveChatFile(w http.ResponseWriter, r *http.Request, f *os.File, info os.FileInfo, name string, kind docs.Kind, anyOrigin bool) {
	ctype := docs.FileContentType(name, kind)
	if kind == docs.Other {
		if sniffed := docs.SniffKind(f, kind); sniffed != kind {
			kind, ctype = sniffed, docs.SniffedContentType(f, sniffed)
		}
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	switch kind {
	case docs.HTML:
		h.Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox "+
			"allow-modals allow-downloads allow-forms; frame-ancestors 'self'")
	case docs.PDF:
		// The browser's PDF viewer does not run in a sandbox.
		h.Set("Content-Security-Policy", "frame-ancestors 'self'")
	default:
		h.Set("Content-Security-Policy", "sandbox")
	}
	if anyOrigin {
		h.Set("Access-Control-Allow-Origin", "*")
	}
	disposition := "inline"
	if kind == docs.Other || r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	if d := mime.FormatMediaType(disposition, map[string]string{"filename": filepath.Base(name)}); d != "" {
		h.Set("Content-Disposition", d)
	} else {
		h.Set("Content-Disposition", disposition)
	}
	http.ServeContent(w, r, "", info.ModTime(), f)
}

/* ------------------------------------------------------- linked files */

// linkedFile is a file the agent linked or named in the chat.
type linkedFile struct {
	// Ref is the path as the agent named it, which the file route is asked
	// for; Path is the file's absolute path.
	Ref   string    `json:"ref"`
	Path  string    `json:"path"`
	Name  string    `json:"name"`
	Size  int64     `json:"size"`
	Mtime int64     `json:"mtime"`
	Kind  docs.Kind `json:"kind"`
}

type linkedSeen struct {
	seq     int64
	workdir string
	at      time.Time
	files   []linkedFile
}

// chatRef is a path the agent's texts name as a whole: when it was named
// last, and whether by a link.
type chatRef struct {
	at     int64
	linked bool
}

// refsSeen is every path a chat's agent named, as of a seq: refs as
// written, names each of them also without file:// and percent-decoded,
// as the viewer asks for them.
type refsSeen struct {
	seq   int64
	refs  map[string]chatRef
	names map[string]bool
}

// linkedState keeps each chat's named paths while its conversation does
// not change, and its linked files for linkedTTL.
type linkedState struct {
	mu    sync.Mutex
	chats map[string]linkedSeen
	refs  map[string]refsSeen
}

var (
	// A Markdown link's target (an image's too), with or without <>.
	linkedLinkRe = regexp.MustCompile(`\]\(\s*(?:<([^>\n]+)>|([^)\s]+))`)
	// A `code` span that is nothing but a path.
	linkedCodeRe = regexp.MustCompile("`([^`\\s]+)`")
	// A bare absolute path in the text.
	linkedBareRe = regexp.MustCompile(`(?:^|[\s(:"'])((?:file://)?/[^\s` + "`" + `'"()<>\[\]]+)`)
)

// shownKind is whether a file named by a `code` path or bare in the text,
// rather than linked, counts as linked: something to look at or listen to,
// not the source files and notes a coding chat names all the time.
func shownKind(k docs.Kind) bool {
	switch k {
	case docs.Video, docs.Audio, docs.Image, docs.PDF, docs.HTML:
		return true
	}
	return false
}

// plainRef is a ref without its file:// scheme.
func plainRef(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "file://localhost"), "file://")
}

// textRefs calls fn with every path a text names as a whole, as written: a
// link's target (linked), a `code` span that is a path, a bare absolute
// path; without a heading or a query, trailing punctuation or what is no
// file's path on this machine (another scheme, a placeholder, a pattern).
func textRefs(text string, fn func(ref string, linked bool)) {
	add := func(ref string, linked bool) {
		ref = strings.TrimRight(ref, ".,;:!?")
		if i := strings.IndexAny(ref, "#?"); i >= 0 {
			ref = ref[:i]
		}
		if ref == "" || len(ref) > 4096 || !hasExt(ref) {
			return
		}
		plain := plainRef(ref)
		if strings.Contains(plain, "://") || strings.HasPrefix(plain, "//") || strings.HasPrefix(plain, "~") ||
			strings.Contains(plain, "${") || strings.ContainsAny(plain, "*<>|\x00") {
			return
		}
		if i := strings.IndexByte(plain, ':'); i >= 0 && !strings.HasPrefix(plain, "/") {
			return // mailto:, data:, C:...
		}
		fn(ref, linked)
	}
	for _, m := range linkedLinkRe.FindAllStringSubmatch(text, -1) {
		add(m[1]+m[2], true)
	}
	for _, m := range linkedCodeRe.FindAllStringSubmatch(text, -1) {
		add(m[1], false)
	}
	for _, m := range linkedBareRe.FindAllStringSubmatch(text, -1) {
		add(m[1], false)
	}
}

// listedRef is whether a named path counts among the chat's linked files: a
// link's target of any kind but Markdown (the Docs view's), a `code` path or
// a bare path of a kind to look at (shownKind).
func listedRef(ref string, linked bool) bool {
	k := docs.FileKind(path.Base(plainRef(ref)))
	return k != docs.Markdown && (linked || shownKind(k))
}

// linkedRefs adds the paths a text links or names that count among the
// chat's linked files to refs (ref -> when), each as written.
func linkedRefs(text string, at int64, refs map[string]int64) {
	textRefs(text, func(ref string, linked bool) {
		if listedRef(ref, linked) && at > refs[ref] {
			refs[ref] = at
		}
	})
}

// chatRefs is every path the chat's agent named (refsSeen), read again
// only when the conversation changed.
func (s *Server) chatRefs(id string, p *chatPeer) (refsSeen, error) {
	var seq int64
	if err := p.view(func(proj *agent.Projection) { seq = proj.LastSeq }); err != nil {
		return refsSeen{}, err
	}
	k := &s.linked
	k.mu.Lock()
	seen, ok := k.refs[id]
	k.mu.Unlock()
	if ok && seen.seq == seq {
		return seen, nil
	}
	seen = refsSeen{refs: map[string]chatRef{}, names: map[string]bool{}}
	if err := p.view(func(proj *agent.Projection) {
		seen.seq = proj.LastSeq
		proj.AgentTexts(func(text string, at int64) {
			textRefs(text, func(ref string, linked bool) {
				was := seen.refs[ref]
				seen.refs[ref] = chatRef{at: max(was.at, at), linked: was.linked || linked}
			})
		})
	}); err != nil {
		return refsSeen{}, err
	}
	for ref := range seen.refs {
		plain := plainRef(ref)
		seen.names[ref], seen.names[plain] = true, true
		if dec, err := url.PathUnescape(plain); err == nil {
			seen.names[dec] = true
		}
	}
	k.mu.Lock()
	if k.refs == nil {
		k.refs = map[string]refsSeen{}
	}
	if len(k.refs) > 200 {
		clear(k.refs)
	}
	k.refs[id] = seen
	k.mu.Unlock()
	return seen, nil
}

// handleChatLinkedFiles lists the files the agent linked or named in the
// chat that are there, the one changed last first, for the chat's Pages
// menu beside its pages folder:
//
//	GET /api/chats/{id}/files  {"files": [{ref, path, name, size, mtime, kind}]}
//
// A file of the chat's pages folder is not among them (the folder lists
// it), nor a Markdown file (the Docs view's).
func (s *Server) handleChatLinkedFiles(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	named, err := s.chatRefs(row.ID, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the chat could not be read")
		return
	}
	k := &s.linked
	k.mu.Lock()
	seen, ok := k.chats[row.ID]
	k.mu.Unlock()
	if ok && seen.seq == named.seq && seen.workdir == row.Workdir && time.Since(seen.at) <= linkedTTL {
		writeJSON(w, http.StatusOK, map[string]any{"files": seen.files})
		return
	}
	order := make([]string, 0, len(named.refs))
	for ref, one := range named.refs {
		if listedRef(ref, one.linked) {
			order = append(order, ref)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := named.refs[order[i]].at, named.refs[order[j]].at
		if a != b {
			return a > b
		}
		return order[i] < order[j]
	})
	if len(order) > maxLinkedRefs {
		order = order[:maxLinkedRefs]
	}
	pages := s.chatPagesDir(row.ID)
	files := []linkedFile{}
	have := map[string]bool{}
	for _, ref := range order {
		// As written, else percent-decoded as a link's path has it: the
		// ref the viewer asks for is the one that is there.
		var info os.FileInfo
		abs, ok := chatFileTarget(row, ref)
		if ok {
			info, err = os.Stat(abs)
		}
		if (!ok || err != nil) && strings.Contains(ref, "%") {
			if dec, derr := url.PathUnescape(plainRef(ref)); derr == nil {
				if abs, ok = chatFileTarget(row, dec); ok {
					ref = dec
					info, err = os.Stat(abs)
				}
			}
		}
		if !ok || err != nil || !info.Mode().IsRegular() || have[abs] {
			continue
		}
		if pages != "" && strings.HasPrefix(abs, pages+string(filepath.Separator)) {
			continue
		}
		have[abs] = true
		files = append(files, linkedFile{Ref: ref, Path: abs, Name: filepath.Base(abs), Size: info.Size(),
			Mtime: info.ModTime().UnixMilli(), Kind: docs.FileKind(abs)})
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Mtime > files[j].Mtime })
	if len(files) > maxLinkedFiles {
		files = files[:maxLinkedFiles]
	}
	k.mu.Lock()
	if k.chats == nil {
		k.chats = map[string]linkedSeen{}
	}
	if len(k.chats) > 200 {
		for id, one := range k.chats {
			if time.Since(one.at) > linkedTTL {
				delete(k.chats, id)
			}
		}
	}
	k.chats[row.ID] = linkedSeen{seq: named.seq, workdir: row.Workdir, at: time.Now(), files: files}
	k.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
