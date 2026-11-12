package repos

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The docs of a repository (DOCS.md §7): the Markdown files and pictures of a
// branch as they are at its newest commit on GitHub, written into a folder of
// their own, so that the Docs view reads them as it reads any folder:
//
//	<root>/.repos/<owner>/<name>.docs/<sha>/   one per commit, the newest few kept
//
// Every folder has a manifest (docsManifest, a hidden file the Docs view
// never lists nor serves) of the blob each file was written from: a file the
// next commit did not change is linked from the folder before, so that it
// keeps its time and a page on screen is not drawn again for nothing. A file
// that changed gets the time of the commit.

const (
	// docsKeep is how many commits' docs one repository keeps.
	docsKeep = 3
	// docsMaxFiles and docsMaxBytes bound what one commit's docs write.
	docsMaxFiles = 5000
	docsMaxBytes = 256 << 20
	// docsBatch is about how much one `git cat-file --batch` reads at once.
	docsBatch = 32 << 20
	// docsManifest is the folder's list of "<blob> <path>" lines.
	docsManifest = ".socrates-blobs"
)

// DocsDir is where the docs of a repository go.
func (m *Manager) DocsDir(fullName string) string {
	owner, name, _ := strings.Cut(fullName, "/")
	return filepath.Join(m.root(), ".repos", owner, name+".docs")
}

// DocsShown is the newest docs folder Docs made of the repository in this
// process, "" for none or one that is gone. It takes no lock: a page on
// screen asks every few seconds, and must not wait for a chat's push.
func (m *Manager) DocsShown(fullName string) string {
	m.mu.Lock()
	dir := m.docs[strings.ToLower(fullName)]
	m.mu.Unlock()
	if dir == "" {
		return ""
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// ForgetDocs forgets the docs folder DocsShown answers for the repository:
// they are no longer to be shown as they were.
func (m *Manager) ForgetDocs(fullName string) {
	m.mu.Lock()
	delete(m.docs, strings.ToLower(fullName))
	m.mu.Unlock()
}

// Docs is a folder with the files of the repository's branch that want
// takes (by their slash-separated path in the repository and their size), as
// they are at the branch's newest commit on GitHub. The cache is fetched
// first when it does not know the branch yet, or when it last fetched longer
// than maxAge ago (a negative maxAge: only when it does not know the
// branch). A fetch that fails while the cache knows the branch leaves the
// docs as they were last fetched; it is logged, not returned. Nothing is
// written that the repository has as a symlink or submodule, and no path
// that would leave the folder.
func (m *Manager) Docs(ctx context.Context, fullName, branch string, maxAge time.Duration, want func(rel string, size int64) bool) (dir, sha string, err error) {
	if !ValidName(fullName) {
		return "", "", fmt.Errorf("%q is not a repository", fullName)
	}
	if err := CheckBranch(ctx, branch); err != nil {
		return "", "", err
	}
	defer m.lock(fullName)()
	cache := m.CacheDir(fullName)
	ref := "refs/remotes/origin/" + branch
	ok := false
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err == nil {
		sha, ok = m.resolve(ctx, cache, ref)
	}
	if ago := m.fetchedAgo(fullName); !ok || maxAge >= 0 && (ago < 0 || ago > maxAge) {
		if err := m.fetchLocked(ctx, fullName, cache); err != nil {
			if !ok {
				return "", "", err
			}
			log.Printf("repos: the docs of %s are shown as last fetched, GitHub could not be reached: %v", fullName, err)
			// Tried: the next listing waits maxAge as after a fetch, rather
			// than holding the repository's lock for another try at once.
			m.noteFetched(fullName)
		} else if sha, ok = m.resolve(ctx, cache, ref); !ok {
			return "", "", fmt.Errorf("%w: %s", ErrNoBranch, branch)
		}
	}
	all := m.DocsDir(fullName)
	dir = filepath.Join(all, sha)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		if err := m.writeDocs(ctx, cache, sha, all, want); err != nil {
			return "", "", err
		}
	}
	// The newest used is the last to go.
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	prunePreviews(all, docsKeep)
	m.mu.Lock()
	if m.docs == nil {
		m.docs = map[string]string{}
	}
	m.docs[strings.ToLower(fullName)] = dir
	m.mu.Unlock()
	return dir, sha, nil
}

// docsEntry is a file of a commit the docs take.
type docsEntry struct {
	rel, blob string
	size      int64
}

// writeDocs writes the wanted files of commit sha into all/<sha>, by way of
// a hidden folder beside it that is renamed when it is complete.
func (m *Manager) writeDocs(ctx context.Context, cache, sha, all string, want func(string, int64) bool) error {
	out, err := m.local(ctx, cache, "ls-tree", "-r", "-l", "-z", "--full-tree", sha)
	if err != nil {
		return err
	}
	var files []docsEntry
	var total int64
	for _, entry := range strings.Split(out, "\x00") {
		// <mode> SP <type> SP <object> SP+ <size> TAB <path>
		meta, rel, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || !safeRel(rel) || !want(rel, size) {
			continue
		}
		if len(files) >= docsMaxFiles || total+size > docsMaxBytes {
			continue
		}
		total += size
		files = append(files, docsEntry{rel: rel, blob: fields[2], size: size})
	}
	if err := os.MkdirAll(all, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(all, "."+sha+".")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	prevDir, prev := newestDocs(all)
	when := time.Now()
	if out, err := m.local(ctx, cache, "show", "-s", "--format=%ct", sha); err == nil {
		if secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			when = time.Unix(secs, 0)
		}
	}
	var manifest strings.Builder
	var read []docsEntry
	for _, f := range files {
		fmt.Fprintf(&manifest, "%s %s\n", f.blob, f.rel)
		target := filepath.Join(tmp, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if prev[f.rel] == f.blob {
			if os.Link(filepath.Join(prevDir, filepath.FromSlash(f.rel)), target) == nil {
				continue
			}
		}
		read = append(read, f)
	}
	for len(read) > 0 {
		n, size := 0, int64(0)
		for n < len(read) && (n == 0 || size+read[n].size <= docsBatch) {
			size += read[n].size
			n++
		}
		if err := m.writeBlobs(ctx, cache, tmp, read[:n], when); err != nil {
			return err
		}
		read = read[n:]
	}
	if err := os.WriteFile(filepath.Join(tmp, docsManifest), []byte(manifest.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(all, sha))
}

// writeBlobs writes files into dir, read with one `git cat-file --batch`, and
// gives them the time when.
func (m *Manager) writeBlobs(ctx context.Context, cache, dir string, files []docsEntry, when time.Time) error {
	var in strings.Builder
	for _, f := range files {
		in.WriteString(f.blob + "\n")
	}
	out, err := run(ctx, gitEnv("", ""), cache, in.String(), "cat-file", "--batch")
	if err != nil {
		return err
	}
	for _, f := range files {
		// <object> SP blob SP <size> LF <contents> LF
		head, rest, ok := strings.Cut(out, "\n")
		fields := strings.Fields(head)
		if !ok || len(fields) != 3 || fields[0] != f.blob || fields[1] != "blob" {
			return fmt.Errorf("git cat-file answered %q for %s", head, f.rel)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size > len(rest) {
			return fmt.Errorf("git cat-file answered %q for %s", head, f.rel)
		}
		target := filepath.Join(dir, filepath.FromSlash(f.rel))
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = file.WriteString(rest[:size])
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		_ = os.Chtimes(target, when, when)
		out = strings.TrimPrefix(rest[size:], "\n")
	}
	return nil
}

// newestDocs is the docs folder of a repository used last, with its
// manifest (path -> blob); "" and nil for none.
func newestDocs(all string) (string, map[string]string) {
	entries, err := os.ReadDir(all)
	if err != nil {
		return "", nil
	}
	best, bestAt := "", time.Time{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(bestAt) {
			best, bestAt = filepath.Join(all, e.Name()), info.ModTime()
		}
	}
	if best == "" {
		return "", nil
	}
	f, err := os.Open(filepath.Join(best, docsManifest))
	if err != nil {
		return "", nil
	}
	defer f.Close()
	blobs := map[string]string{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64<<10), 1<<20)
	for scan.Scan() {
		if blob, rel, ok := strings.Cut(scan.Text(), " "); ok {
			blobs[rel] = blob
		}
	}
	return best, blobs
}
