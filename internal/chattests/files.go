package chattests

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// keep copies files (and output, as a text file) into the run's folder,
// files/<test>/r<n>/, as evidence, after checking every one of them against
// the limits, so that a call keeps all its files or none. have is how many
// files the step (or the run) already has. It answers the evidence and the
// absolute paths it wrote, which the caller removes if the save fails.
func (s *Store) keep(chat string, d *doc, t *Test, r *Run, have int, files []Attach, output string, now time.Time) ([]Evidence, []string, error) {
	n := len(files)
	if output != "" {
		n++
	}
	if n == 0 {
		return nil, nil, nil
	}
	if have+n > MaxStepFiles {
		return nil, nil, fmt.Errorf("%d more files would make %d; a step (or a run) keeps at most %d", n, have+n, MaxStepFiles)
	}
	type source struct {
		Attach
		info fs.FileInfo
	}
	var (
		srcs  []source
		total int64
	)
	for _, a := range files {
		p := strings.TrimSpace(a.Path)
		if p == "" {
			return nil, nil, errors.New("a file has no path")
		}
		if !filepath.IsAbs(p) {
			return nil, nil, fmt.Errorf("%s is not an absolute path", p)
		}
		info, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, nil, fmt.Errorf("%s does not exist", p)
		case err != nil:
			return nil, nil, fmt.Errorf("%s cannot be read: %v", p, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return nil, nil, fmt.Errorf("%s is a symbolic link: attach the file it points to", p)
		case info.IsDir():
			return nil, nil, fmt.Errorf("%s is a folder: attach the files in it one by one", p)
		case !info.Mode().IsRegular():
			return nil, nil, fmt.Errorf("%s is not a regular file", p)
		case info.Size() > MaxFileSize:
			return nil, nil, fmt.Errorf("%s is %s; a file over %s is not kept: attach a smaller one (a cut log, a lower resolution)", p, size(info.Size()), size(MaxFileSize))
		}
		if len([]rune(strings.TrimSpace(a.Caption))) > MaxCaption {
			return nil, nil, fmt.Errorf("the caption of %s is longer than %d characters", p, MaxCaption)
		}
		a.Path = p
		srcs = append(srcs, source{a, info})
		total += info.Size()
	}
	if int64(len(output)) > MaxFileSize {
		return nil, nil, fmt.Errorf("the output is %s; output over %s is not kept: cut it to what matters", size(int64(len(output))), size(MaxFileSize))
	}
	total += int64(len(output))
	if used := usage(d); used+total > MaxChatSize {
		return nil, nil, fmt.Errorf("the chat's tests already keep %s of evidence and these files would pass the %s a chat may keep: delete tests that are no longer needed", size(used), size(MaxChatSize))
	}

	dir := filepath.Join(s.FilesDir(chat, t.ID), "r"+strconv.Itoa(r.N))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("the run's folder cannot be made: %v", err)
	}
	next := nextNumber(r)
	var (
		ev   []Evidence
		made []string
	)
	fail := func(err error) ([]Evidence, []string, error) {
		removeAll(made)
		return nil, nil, err
	}
	for _, src := range srcs {
		name, f, err := create(dir, &next, safeName(filepath.Base(src.Path)))
		if err != nil {
			return fail(err)
		}
		made = append(made, filepath.Join(dir, name))
		written, err := copyFrom(f, src.Path, src.info)
		if err != nil {
			return fail(err)
		}
		source := strings.TrimSpace(src.Source)
		if source == "" {
			source = string(ByAgent)
		}
		ev = append(ev, Evidence{File: path.Join("r"+strconv.Itoa(r.N), name), Caption: oneLine(src.Caption),
			Kind: kindOf(name), Source: source, At: now, Size: written})
	}
	if output != "" {
		name, f, err := create(dir, &next, "output.txt")
		if err != nil {
			return fail(err)
		}
		made = append(made, filepath.Join(dir, name))
		_, err = f.WriteString(output)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fail(fmt.Errorf("the output cannot be kept: %v", err))
		}
		ev = append(ev, Evidence{File: path.Join("r"+strconv.Itoa(r.N), name), Caption: "Output", Kind: KindText,
			Source: string(ByAgent), At: now, Size: int64(len(output))})
	}
	return ev, made, nil
}

// keepRecorded copies what a recording server took as evidence, as far as
// it fits: the newest files the step's room (have of MaxStepFiles) and the
// chat's take, each a regular file within MaxFileSize. A file that cannot
// be kept is left out; it never fails the call that judged the step.
func (s *Store) keepRecorded(chat string, d *doc, t *Test, r *Run, have int, files []Attach, now time.Time) ([]Evidence, []string) {
	room := MaxStepFiles - have
	budget := MaxChatSize - usage(d)
	type source struct {
		Attach
		info fs.FileInfo
	}
	var picked []source
	for i := len(files) - 1; i >= 0 && len(picked) < room; i-- {
		a := files[i]
		info, err := os.Lstat(a.Path)
		if err != nil || !filepath.IsAbs(a.Path) || !info.Mode().IsRegular() || info.Size() > MaxFileSize || info.Size() > budget {
			continue
		}
		budget -= info.Size()
		picked = append(picked, source{a, info})
	}
	if len(picked) == 0 {
		return nil, nil
	}
	dir := filepath.Join(s.FilesDir(chat, t.ID), "r"+strconv.Itoa(r.N))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil
	}
	next := nextNumber(r)
	var (
		ev   []Evidence
		made []string
	)
	// Oldest first, as they were taken.
	for i := len(picked) - 1; i >= 0; i-- {
		src := picked[i]
		name, f, err := create(dir, &next, safeName(filepath.Base(src.Path)))
		if err != nil {
			continue
		}
		full := filepath.Join(dir, name)
		written, err := copyFrom(f, src.Path, src.info)
		if err != nil {
			_ = os.Remove(full)
			continue
		}
		made = append(made, full)
		ev = append(ev, Evidence{File: path.Join("r"+strconv.Itoa(r.N), name), Caption: truncate(oneLine(src.Caption), MaxCaption),
			Kind: kindOf(name), Source: src.Source, At: now, Size: written})
	}
	return ev, made
}

// truncate cuts s to at most n characters.
func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// create makes the next free file "NN-name" in dir. The number counts the
// run's files; a file already there (left by a call whose save failed) is
// stepped over rather than overwritten.
func create(dir string, next *int, name string) (string, *os.File, error) {
	for {
		full := fmt.Sprintf("%02d-%s", *next, name)
		*next++
		f, err := os.OpenFile(filepath.Join(dir, full), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("the evidence cannot be kept: %v", err)
		}
		return full, f, nil
	}
}

// copyFrom copies the file Lstat described as info into f and closes f.
// The file opened must still be the one checked: a file swapped for a link
// in between is refused.
func copyFrom(f *os.File, src string, info fs.FileInfo) (int64, error) {
	defer f.Close()
	in, err := openChecked(src)
	if err != nil {
		return 0, fmt.Errorf("%s cannot be read: %v", src, err)
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return 0, fmt.Errorf("%s changed while it was being attached: attach it again", src)
	}
	n, err := io.Copy(f, io.LimitReader(in, MaxFileSize+1))
	if err != nil {
		return 0, fmt.Errorf("%s cannot be copied: %v", src, err)
	}
	if n > MaxFileSize {
		return 0, fmt.Errorf("%s grew past %s while it was being attached", src, size(MaxFileSize))
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("%s cannot be copied: %v", src, err)
	}
	return n, nil
}

// nextNumber is the number the run's next file takes: one more than the
// highest it has.
func nextNumber(r *Run) int {
	most := 0
	see := func(list []Evidence) {
		for _, e := range list {
			base := path.Base(e.File)
			if i := strings.IndexByte(base, '-'); i > 0 {
				if n, err := strconv.Atoi(base[:i]); err == nil && n > most {
					most = n
				}
			}
		}
	}
	see(r.Evidence)
	for _, st := range r.Steps {
		see(st.Evidence)
	}
	return most + 1
}

// usage is how much evidence the chat's tests keep, by what tests.json says.
func usage(d *doc) int64 {
	var n int64
	add := func(list []Evidence) {
		for _, e := range list {
			n += e.Size
		}
	}
	for _, t := range d.Tests {
		for _, r := range t.Runs {
			add(r.Evidence)
			for _, st := range r.Steps {
				add(st.Evidence)
			}
		}
	}
	return n
}

// safeName is a file's base name made safe for a path and a URL: letters,
// digits, dots, dashes and underscores, its extension kept.
func safeName(name string) string {
	var b strings.Builder
	dash := false
	for _, c := range name {
		ok := c == '.' || c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
			continue
		}
		b.WriteRune(c)
		dash = c == '-'
	}
	name = strings.Trim(b.String(), ".-")
	if len(name) > 80 {
		ext := path.Ext(name)
		if len(ext) > 10 {
			ext = ""
		}
		name = strings.Trim(name[:80-len(ext)], ".-") + ext
	}
	if name == "" || name[0] == '.' {
		name = "file" + name
	}
	return name
}

// kindOf tells a file's kind by its extension.
func kindOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return KindImage
	case ".txt", ".log", ".json", ".md", ".html", ".csv", ".xml", ".yaml", ".yml":
		return KindText
	case ".mp4", ".webm":
		return KindVideo
	}
	return KindFile
}

func removeAll(files []string) {
	for _, f := range files {
		os.Remove(f)
	}
}

// FilePath is the absolute path of a test's evidence file, for serving it:
// file is its name under the test's folder ("r2/03-home.png"). It must be a
// regular file inside that folder, reached without a link: no "..", no
// absolute path, no symbolic link on the way.
func (s *Store) FilePath(chat, test, file string) (string, error) {
	if err := checkChat(chat); err != nil {
		return "", err
	}
	if idNumber(test) == 0 || strings.ToUpper(test) != test {
		return "", fmt.Errorf("%q is not a test id", test)
	}
	if file == "" || strings.ContainsAny(file, "\\\x00") || strings.HasPrefix(file, "/") || path.Clean(file) != file {
		return "", fmt.Errorf("%q is not a file of a test", file)
	}
	parts := strings.Split(file, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".") {
			return "", fmt.Errorf("%q is not a file of a test", file)
		}
	}
	at := s.FilesDir(chat, test)
	for i, p := range parts {
		at = filepath.Join(at, p)
		info, err := os.Lstat(at)
		if err != nil {
			return "", fmt.Errorf("%s has no file %s", test, file)
		}
		last := i == len(parts)-1
		if info.Mode()&fs.ModeSymlink != 0 || (last && !info.Mode().IsRegular()) || (!last && !info.IsDir()) {
			return "", fmt.Errorf("%s has no file %s", test, file)
		}
	}
	return at, nil
}
