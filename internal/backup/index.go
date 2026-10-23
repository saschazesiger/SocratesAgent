package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// The backup's bookkeeping on this machine, under cache/backup (which is not
// itself backed up). All of it can be lost: the index is made again from a
// listing of the bucket and by reading the files once more, the manifests
// are downloaded again, and the state is read off the newest snapshot.
//
//	index.json                 which file was which chunks, which chunks exist
//	state.json                 the last run, the last success, the last error
//	manifests/<host>/<t>.json  manifests already read, for retention and GC
//	tmp/                       consistent copies of databases, while a run reads them
const (
	indexFile    = "index.json"
	stateFile    = "state.json"
	manifestsDir = "manifests"
	tmpDir       = "tmp"
	indexFormat  = 1
)

// index remembers, per file, the chunks it was made of when it had a given
// size and modification time, and which chunks the bucket holds. A file that
// has not changed is not read again, and a chunk the bucket holds is not
// uploaded again.
type index struct {
	Format int `json:"format"`
	// Place and Salt are the backup the index belongs to: a different place,
	// or a new meta.json at the same place, is a different backup.
	Place  string               `json:"place"`
	Salt   string               `json:"salt"`
	Files  map[string]fileState `json:"files"`
	Remote []string             `json:"remote"`
	// LastGC is when the last garbage collection finished.
	LastGC int64 `json:"last_gc,omitempty"`

	// remote is Remote as a set. It is made again from a listing of the
	// bucket once per start of Socrates and at every garbage collection.
	remote map[string]bool
}

// fileState is one file as the last snapshot read it.
type fileState struct {
	Size  int64 `json:"size"`
	MTime int64 `json:"mtime"`
	// SQLite is a database file; WALSize and WALMTime are its write-ahead
	// log's, which changes while the file itself does not.
	SQLite   bool  `json:"sqlite,omitempty"`
	WALSize  int64 `json:"wal_size,omitempty"`
	WALMTime int64 `json:"wal_mtime,omitempty"`
	// Stored is the length of what was backed up: the file's, or the
	// consistent copy's for a database.
	Stored int64    `json:"stored"`
	Chunks []string `json:"chunks"`
}

func cacheDir(dataDir string) string { return paths.New(dataDir).Path(paths.BackupCache) }

// loadIndex reads the index for a backup, or starts an empty one when there
// is none or it is another backup's.
func loadIndex(dataDir, place, salt string) *index {
	idx := &index{}
	raw, err := os.ReadFile(filepath.Join(cacheDir(dataDir), indexFile))
	if err != nil || json.Unmarshal(raw, idx) != nil || idx.Format != indexFormat || idx.Place != place || idx.Salt != salt {
		idx = &index{}
	}
	idx.Format, idx.Place, idx.Salt = indexFormat, place, salt
	if idx.Files == nil {
		idx.Files = map[string]fileState{}
	}
	idx.remote = make(map[string]bool, len(idx.Remote))
	for _, c := range idx.Remote {
		idx.remote[c] = true
	}
	return idx
}

// save writes the index.
func (idx *index) save(dataDir string) error {
	idx.Remote = idx.Remote[:0]
	for c := range idx.remote {
		idx.Remote = append(idx.Remote, c)
	}
	sort.Strings(idx.Remote)
	raw, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(cacheDir(dataDir), indexFile), raw, 0o600)
}

// allRemote reports whether the bucket holds every one of chunks.
func (idx *index) allRemote(chunks []string) bool {
	for _, c := range chunks {
		if !idx.remote[c] {
			return false
		}
	}
	return true
}

// manifestPath is where a manifest already read is kept.
func manifestPath(dataDir, id string) string {
	return filepath.Join(cacheDir(dataDir), manifestsDir, filepath.FromSlash(id)+".json")
}

// cachedManifest is a manifest kept from an earlier read, or nil. Manifests
// never change once written, so a copy is as good as the object.
func cachedManifest(dataDir, id string) *Manifest {
	raw, err := os.ReadFile(manifestPath(dataDir, id))
	if err != nil {
		return nil
	}
	var m Manifest
	if json.Unmarshal(raw, &m) != nil || m.ID != id {
		return nil
	}
	return &m
}

// keepManifest stores a manifest for later reads.
func keepManifest(dataDir string, m *Manifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(manifestPath(dataDir, m.ID), raw, 0o600)
}

// forgetManifests removes the kept copies of every manifest not in keep.
func forgetManifests(dataDir string, keep map[string]bool) {
	root := filepath.Join(cacheDir(dataDir), manifestsDir)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		id, ok := strings.CutSuffix(filepath.ToSlash(rel), ".json")
		if ok && !keep[id] {
			_ = os.Remove(p)
		}
		return nil
	})
}

// state is what the last runs did, kept so that a restart of Socrates still
// knows when the last backup was.
type state struct {
	LastSuccess *RunInfo `json:"last_success,omitempty"`
	LastAttempt int64    `json:"last_attempt,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
	LastErrorAt int64    `json:"last_error_at,omitempty"`
	// CheckedAt is the last run that found nothing changed since the last
	// snapshot, which is as good as a snapshot for being up to date.
	CheckedAt int64 `json:"checked_at,omitempty"`
	// Skipped, Withheld and Kept are what the last good run - one that
	// wrote a snapshot or found nothing new - could not take as it is: an
	// unchanged run writes no snapshot that would say so. Kept holds, for
	// each database whose earlier copy stands in for it, since when.
	Skipped  []string         `json:"skipped,omitempty"`
	Withheld []string         `json:"withheld,omitempty"`
	Kept     map[string]int64 `json:"kept,omitempty"`
}

// noteGaps records what a good run could not take, and since when each
// database has been standing in with an older copy.
func (st *state) noteGaps(info *RunInfo, now int64) {
	st.Skipped, st.Withheld = info.Skipped, info.Withheld
	kept := make(map[string]int64, len(info.Kept))
	for _, rel := range info.Kept {
		since, ok := st.Kept[rel]
		if !ok {
			since = now
		}
		kept[rel] = since
	}
	st.Kept = kept
	if len(kept) == 0 {
		st.Kept = nil
	}
}

func loadState(dataDir string) state {
	var st state
	raw, err := os.ReadFile(filepath.Join(cacheDir(dataDir), stateFile))
	if err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func (st state) save(dataDir string) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(cacheDir(dataDir), stateFile), raw, 0o600)
}

// resetTmp empties the folder databases are copied into, which a run that
// was killed may have left full.
func resetTmp(dataDir string) (string, error) {
	dir := filepath.Join(cacheDir(dataDir), tmpDir)
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return dir, os.MkdirAll(dir, 0o700)
}
