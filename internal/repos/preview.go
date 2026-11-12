package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The preview of a repository's agent configuration (REPOS.md §4.1): before a
// chat has a worktree, the new-chat sheet asks what the repository's
// configuration does to the agents - an opencode.json's models and model, the
// MCP servers of its .mcp.json - and the catalogue reads it from a folder
// that holds those files and nothing else, as they are at the base branch's
// commit:
//
//	<root>/.repos/<owner>/<name>.preview/<sha>/   one per commit, the newest few kept

// previewKeep is how many commits' previews one repository keeps.
const previewKeep = 4

// previewMaxFile and previewMaxFiles bound what one preview writes: a
// configuration file is a few kilobytes, and a repository is not unpacked
// here.
const (
	previewMaxFile  = 1 << 20
	previewMaxFiles = 256
)

// PreviewDir is where the previews of a repository's configuration go.
func (m *Manager) PreviewDir(fullName string) string {
	owner, name, _ := strings.Cut(fullName, "/")
	return filepath.Join(m.root(), ".repos", owner, name+".preview")
}

// ErrNoBranch is a base branch GitHub does not have.
var ErrNoBranch = errors.New("the repository has no such branch")

// Preview is a folder with the configuration files of the repository's
// branch base as they are at its newest commit on GitHub, for a look at what
// a chat made from it would find: roots are the paths in the repository to
// look under (files, or folders whose files are looked at too), want says
// which of the files found are wanted, by their slash-separated path in the
// repository. Nothing else is written, nothing the repository has as a
// symlink or submodule, and no path that would leave the folder.
//
// The cache is fetched only when it is not there yet or does not know the
// branch: the branch list the sheet asks for at the same time fetches it.
// A commit's preview is made once and kept, with the newest few others.
func (m *Manager) Preview(ctx context.Context, fullName, base string, roots []string, want func(rel string) bool) (dir, sha string, err error) {
	if !ValidName(fullName) {
		return "", "", fmt.Errorf("%q is not a repository", fullName)
	}
	if err := CheckBranch(ctx, base); err != nil {
		return "", "", err
	}
	defer m.lock(fullName)()
	cache := m.CacheDir(fullName)
	ref := "refs/remotes/origin/" + base
	sha, ok := "", false
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err == nil {
		sha, ok = m.resolve(ctx, cache, ref)
	}
	if !ok {
		if err := m.fetchLocked(ctx, fullName, cache); err != nil {
			return "", "", err
		}
		if sha, ok = m.resolve(ctx, cache, ref); !ok {
			return "", "", fmt.Errorf("%w: %s", ErrNoBranch, base)
		}
	}
	previews := m.PreviewDir(fullName)
	dir = filepath.Join(previews, sha)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		// The newest used is the last to go.
		now := time.Now()
		_ = os.Chtimes(dir, now, now)
		return dir, sha, nil
	}
	if err := os.MkdirAll(previews, 0o755); err != nil {
		return "", "", err
	}
	tmp, err := os.MkdirTemp(previews, "."+sha+".")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)
	if err := m.writePreview(ctx, cache, sha, tmp, roots, want); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return "", "", err
	}
	prunePreviews(previews, previewKeep)
	return dir, sha, nil
}

// writePreview writes the wanted files of commit sha under roots into dir.
func (m *Manager) writePreview(ctx context.Context, cache, sha, dir string, roots []string, want func(string) bool) error {
	var specs []string
	for _, root := range roots {
		// A path, not a pattern: `:(literal)` keeps git from reading one.
		if root = strings.Trim(path.Clean(filepath.ToSlash(root)), "/"); safeRel(root) {
			specs = append(specs, ":(literal)"+root)
		}
	}
	if len(specs) == 0 {
		return nil
	}
	out, err := m.local(ctx, cache, append([]string{"ls-tree", "-r", "-l", "-z", "--full-tree", sha, "--"}, specs...)...)
	if err != nil {
		return err
	}
	written := 0
	for _, entry := range strings.Split(out, "\x00") {
		// <mode> SP <type> SP <object> SP+ <size> TAB <path>
		meta, rel, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size > previewMaxFile || !safeRel(rel) || !want(rel) {
			continue
		}
		if written++; written > previewMaxFiles {
			break
		}
		data, err := m.local(ctx, cache, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// safeRel says whether rel, a path as git names it in a tree, is a file
// inside the folder it is written into: relative, no "." or ".." part, no
// .git part, nothing git or the file system would read another way.
func safeRel(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "\\\x00\n\r") || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// prunePreviews removes all but the keep most recently used previews of a
// repository, and what a preview cut off half way left behind. Called
// with the repository's lock held.
func prunePreviews(previews string, keep int) {
	entries, err := os.ReadDir(previews)
	if err != nil {
		return
	}
	type seen struct {
		name string
		at   time.Time
	}
	var list []seen
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			// Previews are written under the repository's lock, which the
			// caller holds: this one is left over from a server that stopped.
			_ = os.RemoveAll(filepath.Join(previews, e.Name()))
			continue
		}
		list = append(list, seen{e.Name(), info.ModTime()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.After(list[j].at) })
	for _, old := range list[min(keep, len(list)):] {
		_ = os.RemoveAll(filepath.Join(previews, old.name))
	}
}
