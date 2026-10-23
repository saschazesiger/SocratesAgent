package codex

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SharedDirs are the CODEX_HOME folders every account of a data folder
// shares, so that a thread started under one account resumes under another
// (REBUILD.md §3.2, §6.1). Everything else - auth.json, config.toml, the
// SQLite indexes - stays the account's own; codex finds a rollout it has not
// indexed by scanning sessions/.
var SharedDirs = []string{"sessions", "archived_sessions"}

// PrepareHome makes home a CODEX_HOME whose SharedDirs are relative symlinks
// into shared (e.g. <data>/agents/codex), creating what is missing. A real
// folder already in home is moved into the shared one first, file by file,
// without overwriting anything there; a link that points elsewhere is
// replaced. It is safe to call on every launch.
func PrepareHome(home, shared string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	for _, name := range SharedDirs {
		target := filepath.Join(shared, name)
		if err := os.MkdirAll(target, 0o700); err != nil {
			return err
		}
		link := filepath.Join(home, name)
		rel, err := filepath.Rel(home, target)
		if err != nil {
			return err
		}
		fi, err := os.Lstat(link)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case fi.Mode()&fs.ModeSymlink != 0:
			if dest, _ := os.Readlink(link); dest == rel {
				continue
			}
			if err := os.Remove(link); err != nil {
				return err
			}
		case fi.IsDir():
			if err := mergeInto(link, target); err != nil {
				return fmt.Errorf("move %s into %s: %w", link, target, err)
			}
		default:
			return fmt.Errorf("%s is a file, not a folder", link)
		}
		if err := os.Symlink(rel, link); err != nil {
			return err
		}
	}
	return nil
}

// mergeInto moves every file under src to the same place under dst, keeps
// what dst already has, and removes src once it is empty.
func mergeInto(src, dst string) error {
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if _, err := os.Lstat(to); err == nil {
			return fmt.Errorf("%s exists in both", rel)
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			return err
		}
		return os.Rename(path, to)
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(src)
}
