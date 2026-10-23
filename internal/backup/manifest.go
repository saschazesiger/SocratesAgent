package backup

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// manifestFormat is the version of the manifest. A manifest of a later
// format is refused by a restore rather than half understood.
const manifestFormat = 1

// The kinds of entry a manifest holds.
const (
	TypeDir     = "dir"
	TypeFile    = "file"
	TypeSymlink = "symlink"
)

// Manifest is one snapshot: every directory, file and symbolic link of the
// data directory the backup took, at the moment it took them.
type Manifest struct {
	Format int `json:"format"`
	// ID is "<host>/<time>", and the manifest's object name.
	ID       string `json:"id"`
	Host     string `json:"host"`
	Hostname string `json:"hostname"`
	// Version is the Socrates build that wrote it.
	Version   string  `json:"version"`
	CreatedAt int64   `json:"created_at"`
	Entries   []Entry `json:"entries"`
}

// Entry is one path of a manifest.
type Entry struct {
	// Path is slash separated and relative to the data directory.
	Path string `json:"path"`
	Type string `json:"type"`
	// Mode is the permission bits (with setuid, setgid and sticky).
	Mode uint32 `json:"mode,omitempty"`
	// MTime is the modification time in nanoseconds since the epoch.
	MTime int64 `json:"mtime,omitempty"`
	// Size and Chunks are a file's: its length, and the chunks it is made
	// of in order.
	Size   int64    `json:"size,omitempty"`
	Chunks []string `json:"chunks,omitempty"`
	// Target is a symbolic link's, exactly as it reads: a relative link
	// stays relative.
	Target string `json:"target,omitempty"`
}

// Summary is what a list of snapshots says about one.
type Summary struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Hostname string `json:"hostname,omitempty"`
	At       int64  `json:"at"`
	// Size and Files are the sum of the files' lengths and their number;
	// they are zero for a snapshot whose manifest was not read.
	Size  int64 `json:"size"`
	Files int   `json:"files"`
	// Own is a snapshot of this machine.
	Own bool `json:"own,omitempty"`
}

// Summary sums a manifest up.
func (m *Manifest) Summary() Summary {
	s := Summary{ID: m.ID, Host: m.Host, Hostname: m.Hostname, At: m.CreatedAt}
	for _, e := range m.Entries {
		if e.Type == TypeFile {
			s.Size += e.Size
			s.Files++
		}
	}
	return s
}

// chunkSet is every chunk a manifest refers to.
func (m *Manifest) chunkSet(into map[string]bool) {
	for _, e := range m.Entries {
		for _, c := range e.Chunks {
			into[c] = true
		}
	}
}

// sameEntries reports whether two manifests hold the same files, meaning a
// new snapshot would say nothing the previous one does not.
func sameEntries(a, b []Entry) bool {
	return slices.EqualFunc(a, b, func(x, y Entry) bool {
		return x.Path == y.Path && x.Type == y.Type && x.Mode == y.Mode && x.MTime == y.MTime &&
			x.Size == y.Size && x.Target == y.Target && slices.Equal(x.Chunks, y.Chunks)
	})
}

// validate refuses a manifest a restore must not act on: an unknown format,
// or a path that would leave the directory it is restored into.
func (m *Manifest) validate() error {
	if m.Format != manifestFormat {
		return fmt.Errorf("snapshot %s has format %d, and this Socrates only knows format %d", m.ID, m.Format, manifestFormat)
	}
	seen := map[string]bool{}
	for _, e := range m.Entries {
		clean := path.Clean(e.Path)
		if e.Path == "" || clean != e.Path || path.IsAbs(clean) || clean == "." || clean == ".." ||
			strings.HasPrefix(clean, "../") || seen[clean] {
			return fmt.Errorf("snapshot %s: %w (path %q)", m.ID, ErrCorrupt, e.Path)
		}
		seen[clean] = true
		switch e.Type {
		case TypeDir, TypeFile, TypeSymlink:
		default:
			return fmt.Errorf("snapshot %s: %w (%q is a %q)", m.ID, ErrCorrupt, e.Path, e.Type)
		}
		if e.Type == TypeSymlink && e.Target == "" {
			return fmt.Errorf("snapshot %s: %w (link %q has no target)", m.ID, ErrCorrupt, e.Path)
		}
	}
	return nil
}

func sortRefs(refs []snapRef) {
	sort.Slice(refs, func(i, j int) bool {
		if !refs[i].At.Equal(refs[j].At) {
			return refs[i].At.Before(refs[j].At)
		}
		return refs[i].ID < refs[j].ID
	})
}

// resolve finds the snapshot an id names among refs: an exact id, or
// "latest" for the newest of every host.
func resolve(refs []snapRef, id string) (snapRef, error) {
	if len(refs) == 0 {
		return snapRef{}, ErrNoBackup
	}
	if id == "" || id == "latest" {
		return refs[len(refs)-1], nil
	}
	for _, r := range refs {
		if r.ID == id {
			return r, nil
		}
	}
	return snapRef{}, fmt.Errorf("there is no snapshot %q", id)
}
