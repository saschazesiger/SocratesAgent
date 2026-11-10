// Package docs is a project folder's Markdown, as the Docs view shows it:
// every Markdown file of the folder's own tree,
// listed, and one of them, or an image it shows, found for serving.
//
// The folder is the person's repository, not a place Socrates keeps: what is
// listed and what is served are checked against the same rules, so that an
// address can only ever name a file the listing could have named. A link
// never leaves the folder (no "..", no symbolic link pointing out of it),
// never reaches into a hidden directory (.git, .env) or a dependency tree
// (node_modules, vendor), and only finds Markdown and images.
package docs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The bounds of a scan. A repository with more Markdown than MaxFiles is
// listed in part, as is one whose tree is deeper than MaxDepth or larger
// than MaxEntries entries: Listing.Truncated says so.
const (
	MaxFiles   = 2000
	MaxDepth   = 16
	MaxEntries = 100000
	// hasEntries is how far Has looks before it gives up: it answers for
	// every folder of the sidebar, so it must stay cheap.
	hasEntries = 5000
)

// The largest file served: a Markdown page is read whole into the page, an
// image is streamed.
const (
	MaxMarkdownSize = 5 << 20
	MaxImageSize    = 20 << 20
)

// ErrNotServed is a path that names nothing the Docs view shows: outside
// the folder, hidden, in a dependency tree, or neither Markdown nor an image.
var ErrNotServed = errors.New("that file is not shown in the docs")

// ErrTooLarge is a file over MaxMarkdownSize or MaxImageSize.
var ErrTooLarge = errors.New("that file is too large to show")

// skipped are the directories never looked into, besides hidden ones: other
// people's code and build output, whose READMEs are not the project's.
var skipped = map[string]bool{
	"node_modules": true, "vendor": true, "third_party": true, "bower_components": true,
	"dist": true, "build": true, "target": true, "out": true, "__pycache__": true,
	"venv": true, "site-packages": true, "coverage": true,
}

// Kind is what a served file is.
type Kind string

const (
	Markdown Kind = "markdown"
	Image    Kind = "image"
)

// imageTypes are the images a page may show, by extension, with the type
// they are served as.
var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".svg": "image/svg+xml", ".webp": "image/webp", ".avif": "image/avif",
	".ico": "image/x-icon", ".bmp": "image/bmp",
}

// KindOf is the kind of a file by its name, "" for one not served.
func KindOf(name string) Kind {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case ext == ".md" || ext == ".markdown":
		return Markdown
	case imageTypes[ext] != "":
		return Image
	}
	return ""
}

// ContentType is the type an image is served as, by its name.
func ContentType(name string) string {
	return imageTypes[strings.ToLower(path.Ext(name))]
}

// File is a Markdown file of a listing.
type File struct {
	// Path is relative to the folder, with slashes.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Mtime is when it last changed, in unix milliseconds.
	Mtime int64 `json:"mtime"`
}

// Listing is a folder's Markdown files, sorted by path.
type Listing struct {
	Files     []File `json:"files"`
	Truncated bool   `json:"truncated"`
}

// hidden is whether a path component is never listed nor served: a dot
// file or directory, or a name of the skip list.
func hidden(name string) bool {
	return strings.HasPrefix(name, ".") || skipped[name]
}

// openRoot is a folder resolved to its real path, refused with a readable
// error when it is not a directory.
func openRoot(root string) (string, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("the folder %s is not there: %w", root, fs.ErrNotExist)
		}
		return "", fmt.Errorf("the folder %s cannot be read: %w", root, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("the folder %s cannot be read: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder", root)
	}
	return real, nil
}

// walk visits the tree under root breadth first, so that when a bound cuts
// it short the files nearest the top are the ones seen. It never follows a
// symbolic link and never enters a hidden directory. visit gets every
// Markdown file with its path relative to root, and stops the walk by
// returning false. walk reports whether a bound (max entries, MaxDepth) cut
// the walk short; a directory that cannot be read is passed over.
func walk(root string, max int, visit func(rel string, d fs.DirEntry) bool) bool {
	type dir struct {
		rel   string
		depth int
	}
	queue := []dir{{"", 0}}
	seen := 0
	truncated := false
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(d.rel)))
		if err != nil {
			continue
		}
		for {
			// In batches, so that a directory of a million entries is not
			// read whole before the bound is checked.
			entries, err := f.ReadDir(256)
			for _, e := range entries {
				if seen++; seen > max {
					f.Close()
					return true
				}
				name := e.Name()
				if strings.HasPrefix(name, ".") || skipped[name] && e.IsDir() {
					continue
				}
				rel := name
				if d.rel != "" {
					rel = d.rel + "/" + name
				}
				switch t := e.Type(); {
				case t.IsDir():
					if d.depth+1 > MaxDepth {
						truncated = true
						continue
					}
					queue = append(queue, dir{rel, d.depth + 1})
				case t.IsRegular():
					if KindOf(name) == Markdown && !visit(rel, e) {
						f.Close()
						return truncated
					}
				}
			}
			if err != nil {
				break
			}
		}
		f.Close()
	}
	return truncated
}

// Scan lists every Markdown file under root (.md and .markdown, any case),
// sorted by path. Symbolic links are left out, and hidden and skipped
// directories are not entered. A root that is not there is an error
// wrapping fs.ErrNotExist.
func Scan(root string) (Listing, error) {
	real, err := openRoot(root)
	if err != nil {
		return Listing{}, err
	}
	out := Listing{Files: []File{}}
	full := false
	cut := walk(real, MaxEntries, func(rel string, d fs.DirEntry) bool {
		if len(out.Files) >= MaxFiles {
			full = true
			return false
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return true
		}
		out.Files = append(out.Files, File{Path: rel, Size: info.Size(), Mtime: info.ModTime().UnixMilli()})
		return true
	})
	// cut: the walk met a bound of its own; full: the files' bound, which
	// only the next file over it can tell.
	out.Truncated = cut || full
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out, nil
}

// Has is whether root holds any Markdown file, looked for cheaply: the
// folder's own entries first, then its tree, as far as a few thousand
// entries.
func Has(root string) bool {
	real, err := openRoot(root)
	if err != nil {
		return false
	}
	found := false
	walk(real, hasEntries, func(string, fs.DirEntry) bool {
		found = true
		return false
	})
	return found
}

// RootReadme is the README of root itself (README.md or README.markdown,
// any case) as its name is spelt on disk, or "" when there is none.
func RootReadme(root string) string {
	real, err := openRoot(root)
	if err != nil {
		return ""
	}
	return readmeIn(real)
}

// readmeIn is the page a directory opens on: its README (README.md before
// README.markdown), else its index.md, any case; "" for none.
func readmeIn(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, rank := "", 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		r := 0
		switch strings.ToLower(e.Name()) {
		case "readme.md":
			r = 3
		case "readme.markdown":
			r = 2
		case "index.md":
			r = 1
		}
		if r > rank {
			best, rank = e.Name(), r
		}
	}
	return best
}

// limitOf is the largest file of a kind that is served.
func limitOf(k Kind) int64 {
	if k == Markdown {
		return MaxMarkdownSize
	}
	return MaxImageSize
}

// Target is a file found for serving.
type Target struct {
	// Abs is the file's real path, symbolic links resolved.
	Abs string
	// Rel is the path asked for, cleaned. With Redirect, it is the page a
	// directory opens on, and the caller sends the browser there.
	Rel      string
	Redirect bool
	Kind     Kind
	Size     int64
	// Mtime is when it last changed, in unix milliseconds.
	Mtime int64

	info fs.FileInfo
	// root and inside are the folder's real path and the file's, relative
	// to it, as checked: Open opens it inside the folder only.
	root, inside string
}

// cleanRel is a slash-separated path asked for, cleaned into its
// components, or ErrNotServed when it could name anything the listing
// would not.
func cleanRel(rel string) ([]string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\\x00") || filepath.IsAbs(rel) ||
		filepath.VolumeName(rel) != "" {
		return nil, ErrNotServed
	}
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		switch {
		case p == "" || p == ".":
			continue
		case p == ".." || hidden(p):
			return nil, ErrNotServed
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return nil, ErrNotServed
	}
	return parts, nil
}

// Resolve finds the file rel names under root for serving. rel is
// slash-separated and relative; anything that would leave root (an
// absolute path, "..", a symbolic link pointing out), reach into a hidden
// or skipped directory, or name something neither Markdown nor an image is
// ErrNotServed. A directory resolves to its README (or index.md) with
// Redirect set, and is not found without one; a file that is not there is
// an error wrapping fs.ErrNotExist.
func Resolve(root, rel string) (Target, error) {
	parts, err := cleanRel(rel)
	if err != nil {
		return Target{}, err
	}
	realRoot, err := openRoot(root)
	if err != nil {
		return Target{}, err
	}
	clean := strings.Join(parts, "/")
	real, err := filepath.EvalSymlinks(filepath.Join(realRoot, filepath.FromSlash(clean)))
	if err != nil {
		return Target{}, fmt.Errorf("%s is not there: %w", clean, fs.ErrNotExist)
	}
	// The file itself, its links followed, must be one the listing could
	// name: inside the folder, and in no hidden or skipped directory.
	inside, err := filepath.Rel(realRoot, real)
	if err != nil || !filepath.IsLocal(inside) {
		return Target{}, ErrNotServed
	}
	if inside != "." {
		if _, err := cleanRel(filepath.ToSlash(inside)); err != nil {
			return Target{}, ErrNotServed
		}
	}
	// What is looked at is opened inside the folder (os.OpenInRoot: no link
	// out of it is followed), and where the opened file really is is checked
	// again: a directory swapped for a link after the check above does not
	// lead anywhere the listing could not name.
	f, err := os.OpenInRoot(realRoot, inside)
	if err != nil {
		return Target{}, fmt.Errorf("%s is not there: %w", clean, fs.ErrNotExist)
	}
	info, err := f.Stat()
	where := openedPath(f)
	f.Close()
	if err != nil {
		return Target{}, fmt.Errorf("%s is not there: %w", clean, fs.ErrNotExist)
	}
	if where != "" && where != real {
		return Target{}, ErrNotServed
	}
	if info.IsDir() {
		name := readmeIn(real)
		if name == "" {
			return Target{}, fmt.Errorf("%s has no README: %w", clean, fs.ErrNotExist)
		}
		return Target{Rel: clean + "/" + name, Redirect: true}, nil
	}
	kind := KindOf(clean)
	if !info.Mode().IsRegular() || kind == "" || KindOf(real) != kind {
		return Target{}, ErrNotServed
	}
	if info.Size() > limitOf(kind) {
		return Target{}, ErrTooLarge
	}
	return Target{Abs: real, Rel: clean, Kind: kind, Size: info.Size(), Mtime: info.ModTime().UnixMilli(), info: info,
		root: realRoot, inside: inside}, nil
}

// Open opens the file Resolve found, and refuses it when what is at its
// path now is another file than the one it checked (replaced in between
// by a link, say) or has grown past its bound.
func (t Target) Open() (*os.File, fs.FileInfo, error) {
	if t.info == nil {
		return nil, nil, ErrNotServed
	}
	f, err := os.OpenInRoot(t.root, t.inside)
	if err != nil {
		return nil, nil, fmt.Errorf("%s is not there: %w", t.Rel, fs.ErrNotExist)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, t.info) {
		f.Close()
		return nil, nil, fmt.Errorf("%s is not there: %w", t.Rel, fs.ErrNotExist)
	}
	if info.Size() > limitOf(t.Kind) {
		f.Close()
		return nil, nil, ErrTooLarge
	}
	return f, info, nil
}

// openedPath is where an open file really is, on a system that says so
// (Linux's /proc/self/fd), or "" where it cannot be told.
func openedPath(f *os.File) string {
	where, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	if err != nil || !filepath.IsAbs(where) {
		return ""
	}
	return where
}
