package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// A restore happens in two steps, so that no step can leave a data directory
// half old and half new:
//
//  1. Restore downloads a snapshot into a staging directory inside the data
//     directory (<data>/.restore-<random>), every file written to a temporary
//     name, synced and renamed, every chunk proved against its name. Only
//     when the whole tree is there does it write restore.pending, which names
//     the staging directory.
//  2. ApplyPending, before the database is opened - at the next start of
//     Socrates, or straight away by `socrates restore` - moves the staged
//     tree into place and removes the marker. It is safe to run again after
//     a crash half way: what has been moved is no longer in the staging
//     directory, and the rest still is.
//
// A staging directory without a marker is a restore that never finished,
// and the next ApplyPending removes it.

// ErrNotEmpty is a data directory that already holds an installation. A
// restore never merges into one.
var ErrNotEmpty = errors.New("this data directory already holds a Socrates installation")

// Progress is a restore as it goes.
type Progress struct {
	Snapshot  string `json:"snapshot"`
	Files     int    `json:"files"`
	FilesDone int    `json:"files_done"`
	Bytes     int64  `json:"bytes"`
	BytesDone int64  `json:"bytes_done"`
}

// RestoreOptions say what to restore where.
type RestoreOptions struct {
	Config Config
	// Snapshot is a snapshot id, or "latest" (or empty) for the newest of
	// every machine.
	Snapshot string
	DataDir  string
	// Progress, when set, is told after every file.
	Progress func(Progress)
}

// pendingRestore is restore.pending.
type pendingRestore struct {
	Staging  string `json:"staging"`
	Snapshot string `json:"snapshot"`
	At       int64  `json:"at"`
}

// List opens the backup cfg names and lists its snapshots, newest first, for
// a machine that has none of the backup's bookkeeping yet: a new one about to
// be restored. Manifests read for the list are kept in dataDir's cache.
func List(ctx context.Context, cfg Config, dataDir string) ([]Summary, error) {
	r, err := openRepo(ctx, cfg, false, 0)
	if err != nil {
		return nil, err
	}
	e := &Engine{opts: Options{DataDir: dataDir}}
	e.opts.defaults()
	return e.summaries(ctx, r, "")
}

// Restore stages a snapshot in the data directory and marks it for
// ApplyPending. It refuses a data directory that already holds an
// installation, and proves the passphrase before anything is written.
func Restore(ctx context.Context, opts RestoreOptions) (Summary, error) {
	if holds, why, err := HoldsStore(opts.DataDir); err != nil {
		return Summary{}, err
	} else if holds {
		return Summary{}, fmt.Errorf("%w: %s", ErrNotEmpty, why)
	}
	r, err := openRepo(ctx, opts.Config, false, 0)
	if err != nil {
		return Summary{}, err
	}
	refs, err := r.listSnapshots(ctx)
	if err != nil {
		return Summary{}, err
	}
	ref, err := resolve(refs, opts.Snapshot)
	if err != nil {
		return Summary{}, err
	}
	m, err := r.getManifest(ctx, ref.ID)
	if err != nil {
		return Summary{}, err
	}
	if err := m.validate(); err != nil {
		return Summary{}, err
	}
	summary := m.Summary()

	stage, err := os.MkdirTemp(opts.DataDir, paths.RestoreStagingPrefix)
	if err != nil {
		return Summary{}, err
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(stage)
		}
	}()
	prog := Progress{Snapshot: m.ID, Files: summary.Files, Bytes: summary.Size}
	report := func() {
		if opts.Progress != nil {
			opts.Progress(prog)
		}
	}
	report()
	var dirs []Entry
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return Summary{}, err
		}
		dst := filepath.Join(stage, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return Summary{}, err
		}
		switch e.Type {
		case TypeDir:
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return Summary{}, err
			}
			dirs = append(dirs, e)
		case TypeSymlink:
			if err := os.Symlink(e.Target, dst); err != nil {
				return Summary{}, err
			}
		case TypeFile:
			if err := restoreFile(ctx, r, dst, e); err != nil {
				return Summary{}, fmt.Errorf("%s: %w", e.Path, err)
			}
			prog.FilesDone++
			prog.BytesDone += e.Size
			report()
		}
	}
	// The directories last, deepest first: writing into a directory moves
	// its time, and a mode without write permission would stop the writes.
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Path > dirs[j].Path })
	for _, e := range dirs {
		dst := filepath.Join(stage, filepath.FromSlash(e.Path))
		if err := os.Chmod(dst, fileMode(e.Mode)); err != nil {
			return Summary{}, err
		}
		mtime := time.Unix(0, e.MTime)
		if err := os.Chtimes(dst, mtime, mtime); err != nil {
			return Summary{}, err
		}
	}
	marker, err := json.Marshal(pendingRestore{Staging: filepath.Base(stage), Snapshot: m.ID, At: time.Now().UnixMilli()})
	if err != nil {
		return Summary{}, err
	}
	if err := writeFileAtomic(paths.New(opts.DataDir).Path(paths.RestorePending), marker, 0o600); err != nil {
		return Summary{}, err
	}
	done = true
	return summary, nil
}

// restoreFile writes one file of a snapshot: every chunk downloaded and
// proved, the whole written to a temporary name, synced, and renamed.
func restoreFile(ctx context.Context, r *repo, dst string, e Entry) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".part-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	var written int64
	for _, name := range e.Chunks {
		plain, err := r.getChunk(ctx, name)
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := tmp.Write(plain); err != nil {
			tmp.Close()
			return err
		}
		written += int64(len(plain))
	}
	if written != e.Size {
		tmp.Close()
		return fmt.Errorf("%w: %d bytes where the snapshot says %d", ErrCorrupt, written, e.Size)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), fileMode(e.Mode)); err != nil {
		return err
	}
	mtime := time.Unix(0, e.MTime)
	if err := os.Chtimes(tmp.Name(), mtime, mtime); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// RestorePending reports whether a staged restore waits for ApplyPending.
func RestorePending(dataDir string) bool {
	_, err := os.Stat(paths.New(dataDir).Path(paths.RestorePending))
	return err == nil
}

// ApplyPending moves a staged restore into place, before anything has opened
// the data directory's database. It reports the snapshot it applied, or ""
// when there was none; a staging directory no marker names is removed.
func ApplyPending(dataDir string) (string, error) {
	layout := paths.New(dataDir)
	marker := layout.Path(paths.RestorePending)
	raw, err := os.ReadFile(marker)
	if errors.Is(err, fs.ErrNotExist) {
		return "", removeStaging(dataDir, "")
	}
	if err != nil {
		return "", err
	}
	var p pendingRestore
	if err := json.Unmarshal(raw, &p); err != nil || !strings.HasPrefix(p.Staging, paths.RestoreStagingPrefix) ||
		strings.ContainsAny(p.Staging, `/\`) {
		return "", fmt.Errorf("%s is damaged; remove it to start without the restore", marker)
	}
	stage := filepath.Join(dataDir, p.Staging)
	if _, err := os.Stat(stage); err == nil {
		if err := moveInto(stage, dataDir); err != nil {
			return "", fmt.Errorf("move the restored files into place: %w", err)
		}
		if err := os.RemoveAll(stage); err != nil {
			return "", err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.Remove(marker); err != nil {
		return "", err
	}
	return p.Snapshot, removeStaging(dataDir, "")
}

// removeStaging removes every staging directory but keep.
func removeStaging(dataDir, keep string) error {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), paths.RestoreStagingPrefix) && e.Name() != keep {
			if err := os.RemoveAll(filepath.Join(dataDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// moveInto moves everything in src into dst: what dst does not have is
// renamed into it, a directory both have is merged, and anything else in
// dst is replaced. A database that is replaced loses its write-ahead log and
// shared memory too - left beside the restored file they would be replayed
// into it.
func moveInto(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		sInfo, err := os.Lstat(s)
		if err != nil {
			return err
		}
		dInfo, err := os.Lstat(d)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case sInfo.IsDir() && dInfo.IsDir():
			if err := moveInto(s, d); err != nil {
				return err
			}
			if err := os.Chmod(d, sInfo.Mode().Perm()); err != nil {
				return err
			}
			if err := os.Chtimes(d, sInfo.ModTime(), sInfo.ModTime()); err != nil {
				return err
			}
			if err := os.Remove(s); err != nil {
				return err
			}
			continue
		default:
			if err := os.RemoveAll(d); err != nil {
				return err
			}
		}
		if sInfo.Mode().IsRegular() && isSQLite(s) {
			for _, suffix := range sqliteCompanions {
				if err := os.Remove(d + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
		}
		if err := os.Rename(s, d); err != nil {
			return err
		}
	}
	return nil
}

// HoldsStore reports whether a data directory already holds an installation
// a restore would overwrite, and in a few words why: its database has a
// password or sessions, or a folder only an installation in use fills is not
// empty. A database that a new server has just created is not one.
func HoldsStore(dataDir string) (bool, string, error) {
	layout := paths.New(dataDir)
	if RestorePending(dataDir) {
		return true, "a restore is already waiting to be applied", nil
	}
	if _, err := os.Stat(layout.DB()); err == nil {
		password, sessions, err := storeContents(layout.DB())
		if err != nil {
			return false, "", fmt.Errorf("read %s: %w", layout.DB(), err)
		}
		if password {
			return true, "it has a password", nil
		}
		if sessions > 0 {
			return true, fmt.Sprintf("it has %d sessions", sessions), nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, "", err
	}
	for _, dir := range []string{paths.Chats, paths.Agents, paths.Accounts, paths.Terminals, paths.Sessions, "profiles"} {
		entries, err := os.ReadDir(layout.Path(dir))
		if err == nil && len(entries) > 0 {
			return true, dir + "/ is not empty", nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, "", err
		}
	}
	return false, "", nil
}

// storeContents reads, without writing anything, whether a database has a
// password and how many sessions. The names are internal/store's (and
// internal/server's PasswordKey); a database without those tables has
// neither.
func storeContents(db string) (bool, int, error) {
	dsn := (&url.URL{Scheme: "file", Path: db, RawQuery: "mode=ro&_pragma=busy_timeout(10000)"}).String()
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return false, 0, err
	}
	defer conn.Close()
	var tables int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('kv', 'sessions')`).Scan(&tables); err != nil {
		return false, 0, err
	}
	if tables < 2 {
		return false, 0, nil
	}
	var hash string
	err = conn.QueryRow(`SELECT value FROM kv WHERE key = 'password_hash'`).Scan(&hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, 0, err
	}
	var sessions int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		return false, 0, err
	}
	return strings.TrimSpace(hash) != "", sessions, nil
}
