package artifacts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// fileVersion is the version of artifacts.json this build writes and reads.
const fileVersion = 1

// Store keeps every chat's artifacts under dir(chat)/artifacts/.
type Store struct {
	dir func(chat string) string

	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	onChange func(chat, artifact, what string)

	// now is the clock, replaced by the tests.
	now func() time.Time
}

// New makes a store whose chats keep their artifacts in
// dir(chat)/artifacts/.
func New(dir func(chat string) string) *Store {
	return &Store{dir: dir, locks: map[string]*sync.Mutex{}, now: time.Now}
}

// SetOnChange sets what is called after every change that was saved: the
// chat, the artifact and what happened to it (Created, Updated, Deleted).
// It is called with no lock held, so it may read the store.
func (s *Store) SetOnChange(f func(chat, artifact, what string)) {
	s.mu.Lock()
	s.onChange = f
	s.mu.Unlock()
}

// doc is artifacts.json.
type doc struct {
	Version   int        `json:"version"`
	Next      int        `json:"next"`
	Rev       int64      `json:"rev"`
	Artifacts []Artifact `json:"artifacts"`
}

// root is the chat's artifacts folder.
func (s *Store) root(chat string) string {
	dir := filepath.Join(s.dir(chat), "artifacts")
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func (s *Store) file(chat string) string { return filepath.Join(s.root(chat), "artifacts.json") }

// pagesDir is the folder of an artifact's versions.
func (s *Store) pagesDir(chat, id string) string { return filepath.Join(s.root(chat), "pages", id) }

// FilesDir is the folder of an artifact's files; the answers name them by
// their absolute path in it, so that an agent can open them.
func (s *Store) FilesDir(chat, id string) string { return filepath.Join(s.root(chat), "files", id) }

// contentPath is where a version's content is kept.
func (s *Store) contentPath(chat, id string, v Version) string {
	ext := ".html"
	if v.Format == Markdown {
		ext = ".md"
	}
	return filepath.Join(s.pagesDir(chat, id), strconv.Itoa(v.N)+ext)
}

func (s *Store) lock(chat string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[chat]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[chat] = l
	}
	return l
}

func (s *Store) changed(chat, artifact, what string) {
	s.mu.Lock()
	f := s.onChange
	s.mu.Unlock()
	if f != nil {
		f(chat, artifact, what)
	}
}

// load reads a chat's artifacts.json; a chat without one has no artifacts.
func (s *Store) load(chat string) (*doc, error) {
	if err := checkChat(chat); err != nil {
		return nil, err
	}
	file := s.file(chat)
	blob, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return &doc{Version: fileVersion, Next: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the chat's artifacts cannot be read: %v", err)
	}
	d := &doc{}
	if err := json.Unmarshal(blob, d); err != nil {
		return nil, fmt.Errorf("the chat's artifacts are unreadable: %s is not valid JSON (%v); move it away to start over", file, err)
	}
	if d.Version != fileVersion {
		return nil, fmt.Errorf("the chat's artifacts are unreadable: %s has version %d, and this Socrates reads version %d", file, d.Version, fileVersion)
	}
	for _, a := range d.Artifacts {
		if n := idNumber(a.ID); n >= d.Next {
			d.Next = n + 1
		}
	}
	if d.Next < 1 {
		d.Next = 1
	}
	return d, nil
}

// save writes artifacts.json whole: a temporary file beside it, a sync and
// a rename, so that a crash leaves the old file or the new one.
func (s *Store) save(chat string, d *doc) error {
	d.Version = fileVersion
	d.Rev++
	blob, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return err
	}
	if err := writeAtomic(s.root(chat), s.file(chat), append(blob, '\n')); err != nil {
		return fmt.Errorf("the chat's artifacts cannot be saved: %v", err)
	}
	return nil
}

// writeAtomic writes file in dir through a temporary file and a rename.
func writeAtomic(dir, file string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		return err
	}
	// The rename is only durable once the folder is synced; where a folder
	// cannot be opened for that (Windows), the rename has to do.
	if f, err := os.Open(dir); err == nil {
		f.Sync()
		f.Close()
	}
	return nil
}

// change runs f on the chat's artifacts under the chat's lock and saves
// them if f asks to; once they are saved and the lock is released, it calls
// OnChange with the artifact and what happened, which f answers.
func (s *Store) change(chat string, f func(d *doc) (id, what string, err error)) error {
	l := s.lock(chat)
	l.Lock()
	d, err := s.load(chat)
	if err != nil {
		l.Unlock()
		return err
	}
	id, what, err := f(d)
	if err == nil && what != "" {
		err = s.save(chat, d)
	}
	l.Unlock()
	if err == nil && what != "" {
		s.changed(chat, id, what)
	}
	return err
}

// read loads the chat's artifacts under its lock, for a consistent view.
func (s *Store) read(chat string) (*doc, error) {
	l := s.lock(chat)
	l.Lock()
	defer l.Unlock()
	return s.load(chat)
}

// List is every artifact of the chat, in the order they were made.
func (s *Store) List(chat string) ([]Artifact, error) {
	d, err := s.read(chat)
	if err != nil {
		return nil, err
	}
	return d.Artifacts, nil
}

// Get is one artifact, named by its id ("A3", in any case) or its exact
// title.
func (s *Store) Get(chat, ref string) (Artifact, error) {
	d, err := s.read(chat)
	if err != nil {
		return Artifact{}, err
	}
	i, err := find(d, ref)
	if err != nil {
		return Artifact{}, err
	}
	return d.Artifacts[i], nil
}

// Rev is the chat's revision: it grows with every change, and is 0 for a
// chat that never had an artifact.
func (s *Store) Rev(chat string) (int64, error) {
	d, err := s.read(chat)
	if err != nil {
		return 0, err
	}
	return d.Rev, nil
}

// Content is the content of a version of an artifact: the latest for 0.
func (s *Store) Content(chat, ref string, version int) (Artifact, Version, []byte, error) {
	l := s.lock(chat)
	l.Lock()
	defer l.Unlock()
	d, err := s.load(chat)
	if err != nil {
		return Artifact{}, Version{}, nil, err
	}
	i, err := find(d, ref)
	if err != nil {
		return Artifact{}, Version{}, nil, err
	}
	a := d.Artifacts[i]
	v := a.Latest()
	if version != 0 {
		var ok bool
		if v, ok = a.Version(version); !ok {
			return a, Version{}, nil, fmt.Errorf("%s has no version %d; it keeps %s", a.ID, version, versionWords(a))
		}
	}
	blob, err := os.ReadFile(s.contentPath(chat, a.ID, v))
	if err != nil {
		return a, v, nil, fmt.Errorf("version %d of %s cannot be read: %v", v.N, a.ID, err)
	}
	return a, v, blob, nil
}

// Create makes an artifact, its content version 1.
func (s *Store) Create(chat string, in Fields) (Artifact, error) {
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Artifact{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return Artifact{}, err
	}
	format, err := cleanFormat(in.Format, HTML)
	if err != nil {
		return Artifact{}, err
	}
	if err := checkContent(in.Content); err != nil {
		return Artifact{}, err
	}
	var out Artifact
	err = s.change(chat, func(d *doc) (string, string, error) {
		if len(d.Artifacts) >= MaxArtifacts {
			return "", "", fmt.Errorf("the chat has %d artifacts, as many as it may keep: delete or reuse one", len(d.Artifacts))
		}
		now := s.now().UTC()
		a := Artifact{ID: "A" + strconv.Itoa(d.Next), Title: title, Description: desc, Format: format,
			Key: newKey(), CreatedAt: now, UpdatedAt: now}
		staged, err := s.stage(chat, d, &a, in.Files, int64(len(in.Content)))
		if err != nil {
			return "", "", err
		}
		v := Version{N: 1, At: now, Size: int64(len(in.Content)), Format: format}
		path := s.contentPath(chat, a.ID, v)
		if err := writeAtomic(filepath.Dir(path), path, []byte(in.Content)); err != nil {
			staged.drop()
			return "", "", fmt.Errorf("the page cannot be kept: %v", err)
		}
		a.Versions = []Version{v}
		d.Artifacts = append(d.Artifacts, a)
		d.Next++
		if err := staged.commit(); err != nil {
			os.RemoveAll(s.pagesDir(chat, a.ID))
			return "", "", err
		}
		if err := s.save(chat, d); err != nil {
			staged.rollback()
			os.RemoveAll(s.pagesDir(chat, a.ID))
			os.RemoveAll(s.FilesDir(chat, a.ID))
			return "", "", err
		}
		staged.finish()
		out = a
		// Saved already: change saves nothing more, and tells.
		return a.ID, "", nil
	})
	if err != nil {
		return Artifact{}, err
	}
	s.changed(chat, out.ID, Created)
	return out, nil
}

// Update changes an artifact as p says, and answers it with what changed.
func (s *Store) Update(chat, ref string, p Patch) (Artifact, Change, error) {
	if p.Content != nil && len(p.Edits) > 0 {
		return Artifact{}, Change{}, errors.New("give either content (the whole page) or edits (changes to it), not both")
	}
	if len(p.Edits) > MaxEdits {
		return Artifact{}, Change{}, fmt.Errorf("%d edits are more than %d: give the whole content instead", len(p.Edits), MaxEdits)
	}
	note := oneLine(p.Note)
	if n := utf8.RuneCountInString(note); n > MaxNote {
		return Artifact{}, Change{}, fmt.Errorf("the note is %d characters long; keep it to %d", n, MaxNote)
	}
	var (
		out Artifact
		ch  Change
	)
	err := s.change(chat, func(d *doc) (string, string, error) {
		i, err := find(d, ref)
		if err != nil {
			return "", "", err
		}
		a := d.Artifacts[i]
		a.Versions = slices.Clone(a.Versions)
		a.Files = slices.Clone(a.Files)
		latest := a.Latest()
		now := s.now().UTC()

		if p.Title != nil {
			t, err := cleanTitle(*p.Title)
			if err != nil {
				return "", "", err
			}
			if t != a.Title {
				a.Title = t
				ch.Fields = append(ch.Fields, "title")
			}
		}
		if p.Description != nil {
			desc, err := cleanDescription(*p.Description)
			if err != nil {
				return "", "", err
			}
			if desc != a.Description {
				a.Description = desc
				ch.Fields = append(ch.Fields, "description")
			}
		}

		// The new content, if any: the whole of it, or the latest edited.
		format := latest.Format
		if p.Format != nil {
			if format, err = cleanFormat(*p.Format, latest.Format); err != nil {
				return "", "", err
			}
		}
		var content *string
		switch {
		case p.Content != nil:
			if err := checkContent(*p.Content); err != nil {
				return "", "", err
			}
			content = p.Content
		case len(p.Edits) > 0 || format != latest.Format:
			old, err := os.ReadFile(s.contentPath(chat, a.ID, latest))
			if err != nil {
				return "", "", fmt.Errorf("the latest version of %s cannot be read: %v", a.ID, err)
			}
			edited, err := applyEdits(string(old), p.Edits)
			if err != nil {
				return "", "", err
			}
			if err := checkContent(edited); err != nil {
				return "", "", err
			}
			content = &edited
		}
		var newVersion *Version
		if content != nil {
			same := format == latest.Format
			if same {
				old, err := os.ReadFile(s.contentPath(chat, a.ID, latest))
				same = err == nil && string(old) == *content
			}
			if !same {
				newVersion = &Version{N: latest.N + 1, At: now, Size: int64(len(*content)), Format: format, Note: note}
			}
		}

		// The files taken away, then those attached.
		for _, name := range p.RemoveFiles {
			name = strings.TrimSpace(name)
			j := slices.IndexFunc(a.Files, func(f File) bool { return f.Name == name })
			if j < 0 {
				return "", "", fmt.Errorf("%s has no file %q; it has %s", a.ID, name, fileWords(a))
			}
			a.Files = slices.Delete(a.Files, j, j+1)
			ch.Removed = append(ch.Removed, name)
		}
		// What the change adds: the new version, less the oldest ones it
		// pushes out.
		var grow int64
		if newVersion != nil {
			grow = newVersion.Size
			for _, v := range a.Versions[:max(0, len(a.Versions)+1-MaxVersions)] {
				grow -= v.Size
			}
		}
		staged, err := s.stage(chat, d, &a, p.Files, grow)
		if err != nil {
			return "", "", err
		}
		ch.Attached = staged.names()
		// A file taken away and attached again in one change is replaced.
		ch.Removed = slices.DeleteFunc(ch.Removed, func(name string) bool { return slices.Contains(ch.Attached, name) })

		var pruned []Version
		if newVersion != nil {
			path := s.contentPath(chat, a.ID, *newVersion)
			if err := writeAtomic(filepath.Dir(path), path, []byte(*content)); err != nil {
				staged.drop()
				return "", "", fmt.Errorf("the page cannot be kept: %v", err)
			}
			a.Versions = append(a.Versions, *newVersion)
			a.Format = format
			if len(a.Versions) > MaxVersions {
				pruned = slices.Clone(a.Versions[:len(a.Versions)-MaxVersions])
				a.Versions = slices.Clone(a.Versions[len(a.Versions)-MaxVersions:])
			}
			ch.Version = newVersion.N
		}
		if ch.Version == 0 && len(ch.Fields) == 0 && len(ch.Attached) == 0 && len(ch.Removed) == 0 {
			staged.drop()
			out = a
			return "", "", nil
		}
		a.UpdatedAt = now
		d.Artifacts[i] = a
		dropVersion := func() {
			if newVersion != nil {
				os.Remove(s.contentPath(chat, a.ID, *newVersion))
			}
		}
		if err := staged.commit(); err != nil {
			dropVersion()
			return "", "", err
		}
		if err := s.save(chat, d); err != nil {
			staged.rollback()
			dropVersion()
			return "", "", err
		}
		staged.finish()
		for _, v := range pruned {
			os.Remove(s.contentPath(chat, a.ID, v))
		}
		for _, name := range ch.Removed {
			os.Remove(filepath.Join(s.FilesDir(chat, a.ID), name))
		}
		out = a
		return a.ID, "", nil
	})
	if err != nil {
		return Artifact{}, Change{}, err
	}
	if ch.Version != 0 || len(ch.Fields) > 0 || len(ch.Attached) > 0 || len(ch.Removed) > 0 {
		s.changed(chat, out.ID, Updated)
	}
	return out, ch, nil
}

// Delete deletes an artifact with every version and file of it.
func (s *Store) Delete(chat, ref string) (Artifact, error) {
	var gone Artifact
	err := s.change(chat, func(d *doc) (string, string, error) {
		i, err := find(d, ref)
		if err != nil {
			return "", "", err
		}
		gone = d.Artifacts[i]
		d.Artifacts = slices.Delete(slices.Clone(d.Artifacts), i, i+1)
		return gone.ID, Deleted, nil
	})
	if err != nil {
		return Artifact{}, err
	}
	os.RemoveAll(s.pagesDir(chat, gone.ID))
	os.RemoveAll(s.FilesDir(chat, gone.ID))
	return gone, nil
}

// FilePath is the path of a file of an artifact, for one who has its key;
// an error for anything else (a wrong key, a name it has not).
func (s *Store) FilePath(chat, id, key, name string) (string, error) {
	if !fileNameRE.MatchString(name) {
		return "", errors.New("the artifact has no such file")
	}
	a, err := s.ByKey(chat, id, key)
	if err != nil {
		return "", err
	}
	dir := s.FilesDir(chat, a.ID)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return "", errors.New("the artifact has no such file")
	}
	for _, f := range a.Files {
		if f.Name == name {
			return filepath.Join(dir, f.Name), nil
		}
	}
	return "", errors.New("the artifact has no such file")
}

// addressChatRE is a chat id as an address may name one, checked before
// anything is looked up for an address anyone may ask for.
var addressChatRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ByKey is the artifact of an id whose key is key: what an address names.
func (s *Store) ByKey(chat, id, key string) (Artifact, error) {
	if idNumber(id) == 0 || id != strings.ToUpper(id) || len(key) != 32 || !addressChatRE.MatchString(chat) {
		return Artifact{}, errors.New("there is no such artifact")
	}
	// A chat without artifacts takes no lock (nor a place in the locks)
	// for an address made up.
	if info, err := os.Stat(s.file(chat)); err != nil || !info.Mode().IsRegular() {
		return Artifact{}, errors.New("there is no such artifact")
	}
	a, err := s.Get(chat, id)
	if err != nil || !keyEqual(a.Key, key) {
		return Artifact{}, errors.New("there is no such artifact")
	}
	return a, nil
}

// keyEqual compares two keys in a time that does not depend on where they
// differ.
func keyEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// ------------------------------------------------------------- the files

// staged is files copied beside their place, waiting for the save:
// commit moves them in (the files they replace kept aside), and then
// finish forgets what was replaced once the save worked, or rollback puts
// it back when it did not; drop throws away what was never moved in.
type staged struct {
	dir   string
	items []stagedFile
}

type stagedFile struct {
	name, tmp string
	// old is where the file it replaces was put aside, "" for none.
	old   string
	moved bool
}

func (st *staged) names() []string {
	out := make([]string, len(st.items))
	for i, it := range st.items {
		out[i] = it.name
	}
	return out
}

func (st *staged) drop() {
	for _, it := range st.items {
		os.Remove(it.tmp)
	}
	st.items = nil
}

func (st *staged) commit() error {
	for i := range st.items {
		it := &st.items[i]
		dst := filepath.Join(st.dir, it.name)
		if _, err := os.Lstat(dst); err == nil {
			it.old = it.tmp + ".old"
			if err := os.Rename(dst, it.old); err != nil {
				it.old = ""
				st.rollback()
				return fmt.Errorf("the file %s cannot be replaced: %v", it.name, err)
			}
		}
		if err := os.Rename(it.tmp, dst); err != nil {
			st.rollback()
			return fmt.Errorf("the file %s cannot be kept: %v", it.name, err)
		}
		it.moved = true
	}
	return nil
}

func (st *staged) rollback() {
	for _, it := range st.items {
		dst := filepath.Join(st.dir, it.name)
		if it.moved {
			os.Remove(dst)
		} else {
			os.Remove(it.tmp)
		}
		if it.old != "" {
			os.Rename(it.old, dst)
		}
	}
	st.items = nil
}

func (st *staged) finish() {
	for _, it := range st.items {
		if it.old != "" {
			os.Remove(it.old)
		}
	}
	st.items = nil
}

// fileNameRE is a name a file of a page may have: plain, so that a page
// names it as it is.
var fileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// stage checks the files to attach to a against the limits and copies them
// beside their place, and writes them into a.Files; grow is the content
// the change adds besides them. Files taken away in the same change have
// left a.Files already, so their bytes no longer count.
func (s *Store) stage(chat string, d *doc, a *Artifact, paths []string, grow int64) (*staged, error) {
	st := &staged{dir: s.FilesDir(chat, a.ID)}
	type src struct {
		path, name string
		info       fs.FileInfo
	}
	var (
		srcs  []src
		total int64
	)
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("%s is not an absolute path", p)
		}
		name := filepath.Base(p)
		if !fileNameRE.MatchString(name) || len(name) > MaxFileName {
			return nil, fmt.Errorf("%s: a page's file needs a plain name of letters, digits, '.', '-' and '_' (at most %d), not starting with '.' or '_': copy it to such a name first", p, MaxFileName)
		}
		if seen[name] {
			return nil, fmt.Errorf("two files are named %s", name)
		}
		seen[name] = true
		info, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("%s does not exist", p)
		case err != nil:
			return nil, fmt.Errorf("%s cannot be read: %v", p, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return nil, fmt.Errorf("%s is a symbolic link: attach the file it points to", p)
		case info.IsDir():
			return nil, fmt.Errorf("%s is a folder: attach the files in it one by one", p)
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf("%s is not a regular file", p)
		case info.Size() > MaxFileSize:
			return nil, fmt.Errorf("%s is %s; a file over %s is not kept", p, size(info.Size()), size(MaxFileSize))
		}
		srcs = append(srcs, src{p, name, info})
		total += info.Size()
	}
	// What the files replace no longer counts.
	var freed int64
	kept := 0
	for _, f := range a.Files {
		if seen[f.Name] {
			freed += f.Size
		} else {
			kept++
		}
	}
	if kept+len(srcs) > MaxFiles {
		return nil, fmt.Errorf("%s would have %d files; an artifact keeps at most %d", a.ID, kept+len(srcs), MaxFiles)
	}
	if used := usage(d, a) + total + grow - freed; used > MaxChatSize {
		return nil, fmt.Errorf("the chat's artifacts would keep %s, more than the %s a chat may keep: delete artifacts or versions that are no longer needed", size(used), size(MaxChatSize))
	}
	if len(srcs) == 0 {
		return st, nil
	}
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return nil, fmt.Errorf("the artifact's folder cannot be made: %v", err)
	}
	now := s.now().UTC()
	for _, sr := range srcs {
		n, tmp, err := copyIn(st.dir, sr.path, sr.info)
		if err != nil {
			st.drop()
			return nil, err
		}
		st.items = append(st.items, stagedFile{name: sr.name, tmp: tmp})
		f := File{Name: sr.name, Size: n, At: now}
		if j := slices.IndexFunc(a.Files, func(x File) bool { return x.Name == sr.name }); j >= 0 {
			a.Files[j] = f
		} else {
			a.Files = append(a.Files, f)
		}
	}
	// A file that grew while it was copied counts as it was copied.
	if used := usage(d, a) + grow; used > MaxChatSize {
		st.drop()
		return nil, fmt.Errorf("the chat's artifacts would keep %s, more than the %s a chat may keep: delete artifacts or versions that are no longer needed", size(used), size(MaxChatSize))
	}
	return st, nil
}

// copyIn copies the file at path into a temporary file in dir, and answers
// how much it wrote and the temporary file. The file opened is checked to
// be the one that was looked at (before, from Lstat: no link), so a file
// swapped for a link in between is refused.
func copyIn(dir, path string, before fs.FileInfo) (int64, string, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("%s cannot be read: %v", path, err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return 0, "", fmt.Errorf("%s changed while it was attached", path)
	}
	out, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return 0, "", fmt.Errorf("%s cannot be kept: %v", path, err)
	}
	n, err := io.Copy(out, io.LimitReader(in, MaxFileSize+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxFileSize {
		err = fmt.Errorf("it grew past %s", size(MaxFileSize))
	}
	if err != nil {
		os.Remove(out.Name())
		return 0, "", fmt.Errorf("%s cannot be kept: %v", path, err)
	}
	return n, out.Name(), nil
}

// usage is what the chat's artifacts keep, in bytes, counting the artifact
// being changed as it stands in a (not in d).
func usage(d *doc, a *Artifact) int64 {
	var n int64
	count := func(x Artifact) {
		for _, v := range x.Versions {
			n += v.Size
		}
		for _, f := range x.Files {
			n += f.Size
		}
	}
	for _, x := range d.Artifacts {
		if x.ID != a.ID {
			count(x)
		}
	}
	count(*a)
	return n
}

// ------------------------------------------------------------ the checks

// applyEdits makes the edits on content, in order.
func applyEdits(content string, edits []Edit) (string, error) {
	for i, e := range edits {
		which := fmt.Sprintf("edit %d", i+1)
		if len(edits) == 1 {
			which = "the edit"
		}
		if e.Old == "" {
			return "", fmt.Errorf("%s has no old text: name the text to replace", which)
		}
		n := strings.Count(content, e.Old)
		switch {
		case n == 0:
			return "", fmt.Errorf("%s: the old text is not in the page (after the edits before it); read it with artifact_read and copy the text exactly", which)
		case n > 1 && !e.All:
			return "", fmt.Errorf("%s: the old text is in the page %d times; give more of the text around it, or all: true to replace every one", which, n)
		}
		times := 1
		if e.All {
			times = n
		}
		if grown := len(content) + times*(len(e.New)-len(e.Old)); grown > MaxContent {
			return "", fmt.Errorf("%s would make the page %s; a version may be %s at most", which, size(int64(grown)), size(MaxContent))
		}
		if e.All {
			content = strings.ReplaceAll(content, e.Old, e.New)
		} else {
			content = strings.Replace(content, e.Old, e.New, 1)
		}
	}
	return content, nil
}

func cleanTitle(in string) (string, error) {
	t := oneLine(in)
	switch n := utf8.RuneCountInString(t); {
	case n == 0:
		return "", errors.New("the artifact needs a title: what it shows, in a few words")
	case n > MaxTitle:
		return "", fmt.Errorf("the title is %d characters long; keep it to %d and put the rest in the description", n, MaxTitle)
	}
	return t, nil
}

func cleanDescription(in string) (string, error) {
	d := oneLine(in)
	if n := utf8.RuneCountInString(d); n > MaxDescription {
		return "", fmt.Errorf("the description is %d characters long; keep it to %d", n, MaxDescription)
	}
	return d, nil
}

func cleanFormat(in, def string) (string, error) {
	switch f := strings.ToLower(strings.TrimSpace(in)); f {
	case "":
		return def, nil
	case HTML, Markdown:
		return f, nil
	case "md":
		return Markdown, nil
	default:
		return "", fmt.Errorf("the format is html or markdown, not %q", in)
	}
}

func checkContent(c string) error {
	switch {
	case strings.TrimSpace(c) == "":
		return errors.New("the page has no content")
	case len(c) > MaxContent:
		return fmt.Errorf("the page is %s; a version may be %s at most: move pictures and data into files", size(int64(len(c))), size(MaxContent))
	case !utf8.ValidString(c):
		return errors.New("the page is not valid UTF-8 text")
	}
	return nil
}

// find is the index of the artifact ref names: its id in any case, or its
// exact title.
func find(d *doc, ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1, errors.New("no artifact is named: give its id (A3) or its exact title")
	}
	if idNumber(ref) > 0 {
		id := strings.ToUpper(ref)
		for i, a := range d.Artifacts {
			if a.ID == id {
				return i, nil
			}
		}
		return -1, fmt.Errorf("artifact %s does not exist; %s", id, have(d))
	}
	var hits []int
	for i, a := range d.Artifacts {
		if a.Title == ref {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return -1, fmt.Errorf("no artifact is titled %q; %s", ref, have(d))
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = d.Artifacts[h].ID
	}
	return -1, fmt.Errorf("%d artifacts are titled %q (%s): name one by its id", len(hits), ref, strings.Join(ids, ", "))
}

// have says which artifacts the chat has, for an error about one it has
// not.
func have(d *doc) string {
	if len(d.Artifacts) == 0 {
		return "the chat has no artifacts yet (artifact_create makes one)"
	}
	const most = 30
	ids := make([]string, 0, most)
	for i, a := range d.Artifacts {
		if i == most {
			break
		}
		ids = append(ids, a.ID)
	}
	more := ""
	if len(d.Artifacts) > most {
		more = fmt.Sprintf(" and %d more (artifacts_list names them all)", len(d.Artifacts)-most)
	}
	return "the chat has " + strings.Join(ids, ", ") + more
}

func versionWords(a Artifact) string {
	if len(a.Versions) == 1 {
		return fmt.Sprintf("version %d only", a.Versions[0].N)
	}
	return fmt.Sprintf("versions %d to %d", a.Versions[0].N, a.Latest().N)
}

func fileWords(a Artifact) string {
	if len(a.Files) == 0 {
		return "none"
	}
	names := make([]string, len(a.Files))
	for i, f := range a.Files {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}

// checkChat refuses a chat id that is not one folder's name, before it can
// make a path that leads elsewhere.
func checkChat(chat string) error {
	if strings.TrimSpace(chat) == "" {
		return errors.New("no chat is named")
	}
	if chat == "." || chat == ".." || strings.ContainsAny(chat, "/\\\x00") {
		return fmt.Errorf("%q is not a chat id", chat)
	}
	return nil
}

// idNumber is the number of an artifact id ("A12", "a12": 12), or 0 for
// anything else.
func idNumber(id string) int {
	if len(id) < 2 || (id[0] != 'A' && id[0] != 'a') || id[1] == '0' {
		return 0
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n <= 0 || strconv.Itoa(n) != id[1:] {
		return 0
	}
	return n
}

func newKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// size is a size in words: "812 B", "14 KB", "2.1 MB".
func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%d KB", (n+1<<9)>>10)
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/(1<<20)), ".0") + " MB"
	}
}
