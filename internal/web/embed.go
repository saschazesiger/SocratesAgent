// Package web serves the embedded single page front end. Everything is plain
// HTML, CSS and JavaScript, so the whole application ships as one binary with
// no build step.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

//go:embed static
var files embed.FS

// version is a short hash of every embedded asset. It is stamped onto each
// URL the pages ask for, which is what keeps a build whole: the document of
// one build can only ever load the scripts of that same build, because the
// scripts of the next one live at different addresses. Without it a browser -
// or the offline worker, which keeps its own copy of the app - can pair a new
// page with a script it kept from the previous version, and the app dies on
// an element the old script still expects to find.
var version = ""

// assets is every embedded file exactly as it goes out over the wire, keyed by
// its path below static/. The stamping happens once here rather than per
// request: nothing in an embedded file changes while the process runs.
var assets = map[string][]byte{}

// stampedRef matches the address of an asset in a document: href="/static/..."
// or src="/favicon.png". The query is added inside the quotes.
var stampedRef = regexp.MustCompile(`((?:href|src)=")(/static/[^"?#]+|/favicon\.png)"`)

// stampedImport matches a module something pulls in - a script importing
// './x.js', or the inline module of a page importing '/static/js/x.js'. A
// specifier does not inherit the query of the file it is written in, so every
// hop has to be stamped or only the entry point is versioned.
var stampedImport = regexp.MustCompile(`(from\s+["'](?:\.{1,2}/|/static/)[^"']+\.js)(["'])`)

func init() {
	raw := readAll()
	version = hashOf(raw)
	for name, data := range raw {
		assets[name] = stamp(name, data)
	}
}

func readAll() map[string][]byte {
	out := map[string][]byte{}
	err := fs.WalkDir(files, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, "static/")] = data
		return nil
	})
	if err != nil {
		panic(err)
	}
	return out
}

// hashOf is taken over the raw files, so it is the same on every machine that
// builds the same source and does not depend on the stamping it feeds.
func hashOf(raw map[string][]byte) string {
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	slices.Sort(names)
	sum := sha256.New()
	for _, name := range names {
		fmt.Fprintf(sum, "%s %x\n", name, sha256.Sum256(raw[name]))
	}
	return hex.EncodeToString(sum.Sum(nil))[:12]
}

func stamp(name string, data []byte) []byte {
	switch {
	case name == "sw.js":
		// The worker names its cache after the build and asks for the same
		// stamped addresses the pages do, so one version of the app is kept
		// and served as a whole.
		return bytes.ReplaceAll(data, []byte("__VERSION__"), []byte(version))
	case strings.HasSuffix(name, ".html"):
		data = stampedRef.ReplaceAll(data, []byte("${1}${2}?v="+version+`"`))
		return stampedImport.ReplaceAll(data, []byte("${1}?v="+version+"${2}"))
	case strings.HasSuffix(name, ".js"):
		return stampedImport.ReplaceAll(data, []byte("${1}?v="+version+"${2}"))
	}
	return data
}

// asset returns a file as it is served, or nil when there is no such file.
func asset(name string) []byte { return assets[path.Clean(strings.TrimPrefix(name, "/"))] }

// Static serves the css/js assets.
func Static() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := asset(r.URL.Path)
		if data == nil {
			http.NotFound(w, r)
			return
		}
		serve(w, r, r.URL.Path, data)
	})
}

// serve writes one asset.
func serve(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	w.Header().Set("Cache-Control", cacheFor(r, "no-cache"))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// cacheFor says how long a browser may keep an asset. An address that carries
// the build stamp names one exact version of one file and can never mean
// anything else, so it is kept for good. Anything asked for without the stamp
// falls back to what its caller thinks is safe.
func cacheFor(r *http.Request, unstamped string) string {
	if r.URL.Query().Get("v") != "" {
		return "public, max-age=31536000, immutable"
	}
	return unstamped
}

// ServiceWorker serves the offline shell worker. It has to come from the root
// rather than /static/, because a worker can only take charge of the paths
// below where it was served from, and it needs all of them.
func ServiceWorker(w http.ResponseWriter, r *http.Request) {
	data := asset("sw.js")
	if data == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	// Always revalidated, so a new version of Socrates is never held back by
	// the very thing that is supposed to make it survive a bad connection.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(data)
}

// ServePage writes one of the embedded HTML documents.
func ServePage(w http.ResponseWriter, r *http.Request, name string) {
	data := asset(name)
	if data == nil {
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	_, _ = w.Write(data)
}

// Favicon serves the app icon from the root, where a browser looks for it
// on its own even when a page never says where it is.
func Favicon(w http.ResponseWriter, r *http.Request) {
	data := asset("img/favicon.png")
	if data == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	// Nothing in an embedded image carries a modification time, so without a
	// stated lifetime the browser refetches it every visit.
	w.Header().Set("Cache-Control", cacheFor(r, "public, max-age=86400"))
	_, _ = w.Write(data)
}
