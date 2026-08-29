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
		if strings.HasSuffix(r.URL.Path, ".css") || strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
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

const favicon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
<rect width="32" height="32" rx="8" fill="#111318"/>
<circle cx="16" cy="16" r="7" fill="none" stroke="#fff" stroke-width="2"/>
<circle cx="16" cy="16" r="2.4" fill="#fff"/>
</svg>`

// Favicon serves the app icon.
func Favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(favicon))
}
