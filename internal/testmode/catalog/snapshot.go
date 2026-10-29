package catalog

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Snapshot copies the catalog of the repository at repoRoot into dest,
// everything but tools/ (the tools are copied into the run's runtime folder
// by CopyTool): a result always shows the text it was run against, even
// after the test changed. Only regular files and folders are copied, never
// what a symbolic link points at, and hidden ones are left out as Load
// leaves them out.
func Snapshot(repoRoot, dest string) error {
	src := filepath.Join(repoRoot, filepath.FromSlash(DirName))
	return copyTree(src, dest, func(rel string, e fs.DirEntry) bool {
		return strings.HasPrefix(e.Name(), ".") || rel == ToolsDir
	})
}

// CopyTool copies a tool's folder into dest, to run from there: a tool
// changed or reinstalled in the checkout in the middle of a run never
// reaches its cells. node_modules (its setup installs it) and .git are left
// out, and so are symbolic links; the files keep their executable bits.
func CopyTool(t *Tool, dest string) error {
	return copyTree(t.Dir, dest, func(_ string, e fs.DirEntry) bool {
		return e.IsDir() && (e.Name() == "node_modules" || e.Name() == ".git")
	})
}

// copyTree copies the regular files and folders below src into dest, but
// those skip names (rel is the path below src, with forward slashes).
func copyTree(src, dest string, skip func(rel string, e fs.DirEntry) bool) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", src)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == src {
			return nil
		}
		relOS, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if skip(filepath.ToSlash(relOS), e) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dest, relOS)
		switch {
		case e.IsDir():
			return os.MkdirAll(target, 0o755)
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				return err
			}
			return copyFile(p, target, info.Mode().Perm()&0o755)
		}
		return nil // a symbolic link, a socket, a device: not part of a copy
	})
}

func copyFile(src, dest string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// The mode of a new file is masked by the umask; the copy keeps the
	// source's executable bits whatever the umask.
	return os.Chmod(dest, mode|0o600)
}
