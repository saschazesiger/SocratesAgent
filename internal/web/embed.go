// Package web serves the embedded single page front end. Everything is plain
// HTML, CSS and JavaScript, so the whole application ships as one binary with
// no build step.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var files embed.FS

func sub() fs.FS {
	f, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return f
}

// Static serves the css/js assets.
func Static() http.Handler {
	fileServer := http.FileServer(http.FS(sub()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".css"), strings.HasSuffix(r.URL.Path, ".js"):
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasSuffix(r.URL.Path, ".png"):
			// Nothing in an embedded image carries a modification time, so
			// without a stated lifetime the browser refetches it every visit.
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ServiceWorker serves the offline shell worker. It has to come from the root
// rather than /static/, because a worker can only take charge of the paths
// below where it was served from, and it needs all of them.
func ServiceWorker(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(sub(), "sw.js")
	if err != nil {
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
	data, err := fs.ReadFile(sub(), name)
	if err != nil {
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
	data, err := fs.ReadFile(sub(), "img/favicon.png")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}
