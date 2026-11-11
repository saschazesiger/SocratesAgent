package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/docs"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/web"
)

// Docs: every project folder of the sidebar has a Docs view, its Markdown
// files as a tree, each rendered by the page kit (/artifact-kit/) in a sandboxed
// frame (internal/docs finds and checks them). A chat's link to a Markdown
// file of its folder opens the same page beside the chat.
//
// A folder is shown only when it is a session's working directory, lies
// inside the workspace root, or is a chat's pages folder (paths.ChatPages,
// where its agent writes the pages it links in the chat): the folder a
// request names is the person's choice, but it has to be one Socrates
// already works in, not any path on the disk.
//
// The pages are served at /docs/{token}/{path}, outside the login: a page is
// a sandbox with an opaque origin, so the requests it makes itself (its
// images, a page it links to) carry no cookie. The token is the
// capability instead, an HMAC of the folder under a key of the server's,
// handed out only to a signed-in page by GET /api/docs. It names a folder
// and nothing else, and a path under it reaches only what internal/docs
// serves: Markdown and images, never a hidden file.

const (
	// docsKeyName is where the token key is kept in the key/value table.
	docsKeyName = "docs.key"
	// maxDocsProbe is the most folders one probe asks about: every folder of
	// a sidebar, with room to spare.
	maxDocsProbe = 300
	// docsProbeWorkers is how many folders a probe looks into at once.
	docsProbeWorkers = 8
	// docsProbeTTL is how long a probe's answer for a folder is kept: the
	// sidebar asks again on every render.
	docsProbeTTL = 20 * time.Second
	// docsSigLen is the length of a token's signature, in base64url
	// characters (192 bits).
	docsSigLen = 32
	// maxDocsTitles is the most files a listing reads the titles of
	// (?titles=1): a chat's pages, not a repository's docs.
	maxDocsTitles = 300
	// maxDocsToken bounds a token: a folder of maxBoardFolder bytes,
	// encoded, and its signature.
	maxDocsToken = (maxBoardFolder*4+2)/3 + 1 + docsSigLen
)

// docsPageCSP is a doc page's policy: a sandbox with an opaque origin
// wherever it is shown, a frame of the app or a tab of its own, so that
// nothing it asks of Socrates carries the person's cookie, with scripts held
// to the kit and Mermaid. A doc is a file of someone's repository or what a
// chat's agent wrote, and its HTML (an <img onerror>, a <script>) must run
// nothing: no inline script, no request of its own (connect-src) that could
// carry the page's address - its token - away.
// The kit is named by host as well as 'self', which a sandboxed page's
// opaque origin does not match everywhere.
func docsPageCSP(r *http.Request) string {
	kit := "'self'"
	if host := r.Host; host != "" && docsHostOK(host) {
		kit += " " + host + docs.KitPath
	}
	return "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads; " +
		"frame-ancestors 'self'; script-src " + kit + " https://cdn.jsdelivr.net/npm/mermaid@11/; " +
		"connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
}

// docsHostOK is whether a Host header can stand in a policy as it is.
func docsHostOK(host string) bool {
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]", c)) {
			return false
		}
	}
	return true
}

// docsState is the Docs view's share of the server: the token key, made
// on first use, and the probe's answers. Its zero value is ready.
type docsState struct {
	keyMu sync.Mutex
	key   []byte

	probeMu sync.Mutex
	probe   map[string]docsProbed
}

type docsProbed struct {
	has  bool
	repo docs.Repo
	at   time.Time
}

// docsKey is the key the tokens are signed with: 32 random bytes, kept in
// the key/value table so that a token stays good across restarts.
func (s *Server) docsKey() ([]byte, error) {
	s.docsKeep.keyMu.Lock()
	defer s.docsKeep.keyMu.Unlock()
	if s.docsKeep.key != nil {
		return s.docsKeep.key, nil
	}
	var stored string
	err := s.store.GetJSON(docsKeyName, &stored)
	if errors.Is(err, store.ErrNotFound) {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		stored = hex.EncodeToString(raw)
		if err := s.store.SetJSON(docsKeyName, stored); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(stored)
	if err != nil || len(key) < 16 {
		return nil, errors.New("the docs key is damaged")
	}
	s.docsKeep.key = key
	return key, nil
}

// docsSig is a folder's signature under the key.
func docsSig(key []byte, folder string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("docs\x00" + folder))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))[:docsSigLen]
}

// docsToken is the token of a folder's pages: the folder, base64url, and
// its signature.
func (s *Server) docsToken(folder string) (string, error) {
	key, err := s.docsKey()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString([]byte(folder)) + "." + docsSig(key, folder), nil
}

// docsTokenFolder is the folder a token names, or false for a token this
// server did not sign. Its form is checked before anything is computed.
func (s *Server) docsTokenFolder(token string) (string, bool) {
	if len(token) < 2+docsSigLen || len(token) > maxDocsToken {
		return "", false
	}
	dot := len(token) - docsSigLen - 1
	if token[dot] != '.' {
		return "", false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if i != dot && !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[:dot])
	if err != nil {
		return "", false
	}
	key, err := s.docsKey()
	if err != nil {
		return "", false
	}
	if !hmac.Equal([]byte(token[dot+1:]), []byte(docsSig(key, string(raw)))) {
		return "", false
	}
	return string(raw), true
}

// docsAllowed is whether a folder's docs may be shown: an absolute
// directory that is there, spelt as the sidebar spells it (boardFolder),
// which is a session's working directory, lies inside the workspace root,
// or is the pages folder of a chat that exists. workdirs is every session's
// working directory, cleaned, read here when nil.
func (s *Server) docsAllowed(folder string, workdirs map[string]bool) bool {
	if !filepath.IsAbs(folder) || filepath.Clean(folder) != folder {
		return false
	}
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		return false
	}
	if _, ok := s.chatOfPages(folder); ok {
		return true
	}
	if workdirs == nil {
		workdirs = s.sessionWorkdirs()
	}
	if workdirs[folder] {
		return true
	}
	root := strings.TrimSpace(s.Settings().Workspace.Root)
	if root == "" || !filepath.IsAbs(root) {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return false
	}
	// Not a hidden directory of a project (.git): its Markdown is no doc.
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// sessionWorkdirs is every session's working directory, cleaned as the
// sidebar spells it.
func (s *Server) sessionWorkdirs() map[string]bool {
	out := map[string]bool{}
	list, err := s.store.ListSessions()
	if err != nil {
		return out
	}
	for _, sess := range list {
		if sess.Workdir != "" {
			out[filepath.Clean(sess.Workdir)] = true
		}
	}
	return out
}

// handleDocsProbe says which folders have Markdown, for the sidebar:
//
//	POST /api/docs/probe  {"folders": [...]}  ->  {"docs": {"<folder>": true|false},
//	                                               "repos": {"<folder>": {remote, prefix}}}
//
// The answer is keyed by the folders as they were sent. A folder whose docs
// may not be shown is false. `repos` names, for a folder with docs in a git
// repository with a remote, where its files are on the forge, so that a chat
// can open a link to one of them by its forge address beside the chat.
func (s *Server) handleDocsProbe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body struct {
		Folders []string `json:"folders"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Folders) > maxDocsProbe {
		writeError(w, http.StatusBadRequest, "ask about at most "+strconv.Itoa(maxDocsProbe)+" folders at once")
		return
	}
	answers := make([]docsProbed, len(body.Folders))
	workdirs := s.sessionWorkdirs()
	sem := make(chan struct{}, docsProbeWorkers)
	var wg sync.WaitGroup
	// A folder named twice is looked at once.
	asked := make(map[string]bool, len(body.Folders))
	for i, raw := range body.Folders {
		folder, err := boardFolder(raw)
		if err != nil || asked[folder] {
			continue
		}
		asked[folder] = true
		if p, ok := s.docsProbeCached(folder); ok {
			answers[i] = p
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p := docsProbed{has: s.docsAllowed(folder, workdirs) && docs.Has(folder)}
			if p.has {
				p.repo = docs.RepoOf(folder)
			}
			answers[i] = s.docsProbeKeep(folder, p)
		}()
	}
	wg.Wait()
	// Every name of a folder gets its answer.
	byFolder := make(map[string]docsProbed, len(body.Folders))
	for i, raw := range body.Folders {
		if folder, err := boardFolder(raw); err == nil && answers[i].has {
			byFolder[folder] = answers[i]
		}
	}
	out := make(map[string]bool, len(body.Folders))
	repos := map[string]docs.Repo{}
	for _, raw := range body.Folders {
		folder, err := boardFolder(raw)
		p := byFolder[folder]
		out[raw] = err == nil && p.has
		if out[raw] && p.repo.Remote != "" {
			repos[raw] = p.repo
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": out, "repos": repos})
}

// docsProbeCached is a probe's answer for a folder from the last
// docsProbeTTL.
func (s *Server) docsProbeCached(folder string) (docsProbed, bool) {
	s.docsKeep.probeMu.Lock()
	defer s.docsKeep.probeMu.Unlock()
	p, ok := s.docsKeep.probe[folder]
	if !ok || time.Since(p.at) > docsProbeTTL {
		return docsProbed{}, false
	}
	return p, true
}

// docsProbeKeep keeps a probe's answer, and drops the old ones once there
// are many. It answers what it kept.
func (s *Server) docsProbeKeep(folder string, p docsProbed) docsProbed {
	s.docsKeep.probeMu.Lock()
	defer s.docsKeep.probeMu.Unlock()
	if s.docsKeep.probe == nil {
		s.docsKeep.probe = map[string]docsProbed{}
	}
	if len(s.docsKeep.probe) > 4*maxDocsProbe {
		for k, p := range s.docsKeep.probe {
			if time.Since(p.at) > docsProbeTTL {
				delete(s.docsKeep.probe, k)
			}
		}
	}
	p.at = time.Now()
	s.docsKeep.probe[folder] = p
	return p
}

// docsHome is the page a folder's docs open on: its README (as spelt on
// disk), else the first file of the listing, else "".
func docsHome(folder string, files []docs.File) string {
	if readme := docs.RootReadme(folder); readme != "" {
		return readme
	}
	if len(files) > 0 {
		return files[0].Path
	}
	return ""
}

// titledFile is a file of a listing with its title (?titles=1).
type titledFile struct {
	docs.File
	// Title is its first level-1 heading, "" for none (docs.Title).
	Title string `json:"title"`
}

// handleDocs lists a folder's Markdown:
//
//	GET /api/docs?folder=/root/project[&titles=1]
//	  {"folder", "files": [{"path", "size", "mtime"[, "title"]}], "truncated", "base", "readme"}
//
// base is where the pages are served, "/docs/<token>/": a file's page is
// base + its path, each segment escaped. titles=1 gives every file its
// title, the text of its first "# " heading or "", when the listing has at
// most maxDocsTitles files; a longer one is listed without. A chat's pages
// folder that is not there yet (a chat from before pages) is made.
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	folder, err := boardFolder(r.URL.Query().Get("folder"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if id, ok := s.chatOfPages(folder); ok {
		if _, err := agenthost.MakePages(s.dataDir, id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if !s.docsAllowed(folder, nil) {
		writeError(w, http.StatusNotFound, "there are no docs for that folder")
		return
	}
	list, err := docs.Scan(folder)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	token, err := s.docsToken(folder)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var files any = list.Files
	if r.URL.Query().Get("titles") == "1" && len(list.Files) <= maxDocsTitles {
		titled := make([]titledFile, len(list.Files))
		for i, f := range list.Files {
			titled[i] = titledFile{File: f, Title: docs.Title(folder, f.Path)}
		}
		files = titled
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"folder":    folder,
		"files":     files,
		"truncated": list.Truncated,
		"base":      "/docs/" + token + "/",
		"readme":    docsHome(folder, list.Files),
	})
}

// handleDocsStat answers one file's size and time, so that a page can tell
// a file changed on disk:
//
//	GET /api/docs/stat?folder=/root/project&path=docs/A.md  {"path", "size", "mtime", "kind"}
//
// A directory answers for the page it opens on, whose path is the one
// answered.
func (s *Server) handleDocsStat(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	folder, err := boardFolder(q.Get("folder"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.docsAllowed(folder, nil) {
		writeError(w, http.StatusNotFound, "there are no docs for that folder")
		return
	}
	t, err := docs.Resolve(folder, q.Get("path"))
	if err == nil && t.Redirect {
		t, err = docs.Resolve(folder, t.Rel)
	}
	if err != nil || t.Redirect {
		writeError(w, http.StatusNotFound, docsMissing(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": t.Rel, "size": t.Size, "mtime": t.Mtime, "kind": t.Kind})
}

// docsMissing is what a 404 says about a file Resolve did not find.
func docsMissing(err error) string {
	switch {
	case errors.Is(err, docs.ErrTooLarge):
		return "That file is too large to show."
	case errors.Is(err, fs.ErrNotExist):
		return "That file is not there."
	}
	return "That file is not shown in the docs."
}

// docsPageURL is the address of a file's page under a token, its path
// escaped segment by segment.
func docsPageURL(token, rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/docs/" + token + "/" + strings.Join(parts, "/")
}

// handleDocsPage serves a folder's doc, or an image a doc shows:
//
//	GET /docs/{token}/                 the folder's README (a redirect)
//	GET /docs/{token}/{path}           a Markdown file's page (?raw=1: its text)
//	GET /docs/{token}/{path}           an image
//	GET /docs/{token}/{dir}            the directory's README (a redirect)
//
// Outside the login (the page's own requests carry no cookie): the token is
// checked, then the folder is checked again, as it may have stopped being
// one whose docs are shown.
func (s *Server) handleDocsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	folder, ok := s.docsTokenFolder(token)
	if !ok || !utf8.ValidString(folder) || strings.ContainsRune(folder, 0) || !s.docsAllowed(folder, nil) {
		http.Error(w, "There are no such docs.", http.StatusNotFound)
		return
	}
	rel := r.PathValue("path")
	if strings.Trim(rel, "/") == "" {
		home := docs.RootReadme(folder)
		if home == "" {
			if list, err := docs.Scan(folder); err == nil && len(list.Files) > 0 {
				home = list.Files[0].Path
			}
		}
		if home == "" {
			http.Error(w, "There is no Markdown in this folder.", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, docsPageURL(token, home), http.StatusFound)
		return
	}
	t, err := docs.Resolve(folder, rel)
	if err != nil {
		http.Error(w, docsMissing(err), http.StatusNotFound)
		return
	}
	if t.Redirect {
		http.Redirect(w, r, docsPageURL(token, t.Rel), http.StatusFound)
		return
	}
	f, info, err := t.Open()
	if err != nil {
		http.Error(w, docsMissing(err), http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, no-cache")
	if t.Kind == docs.Image || r.URL.Query().Get("raw") == "1" {
		// A file by itself (an SVG opened in a tab, the text) runs
		// nothing. A picture may be read by any origin (the page's is
		// opaque); a page's text is no other page's to read.
		ctype := docs.ContentType(t.Rel)
		if t.Kind == docs.Markdown {
			ctype = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Security-Policy", "sandbox")
		if t.Kind == docs.Image {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		http.ServeContent(w, r, "", info.ModTime(), f)
		return
	}
	content, err := io.ReadAll(io.LimitReader(f, docs.MaxMarkdownSize+1))
	if err != nil || len(content) > docs.MaxMarkdownSize {
		http.Error(w, "That file could not be read.", http.StatusNotFound)
		return
	}
	page := docs.Page(path.Base(t.Rel), t.Rel, content, web.Version())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", docsPageCSP(r))
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(page)
	}
}
