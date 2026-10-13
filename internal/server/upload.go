package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Uploads: a file from the browser, put where the session's program can read
// it.
//
// A phone has no way to hand a screenshot to a CLI except by path, so the file
// is written into the session's working directory and the page types its
// absolute path onto the prompt. Claude Code turns a pasted image path into an
// attached image; Codex and OpenCode read it like any other file.

const (
	// uploadDir is where uploads land, under the working directory. It keeps
	// a .gitignore of its own, so nothing put there turns up in `git status`.
	uploadDir = ".socrates/uploads"
	// uploadMax is the whole request, every file in it together.
	uploadMax = 64 << 20
	// uploadFilesMax is how many files one request may carry.
	uploadFilesMax = 20
)

// uploadName makes a browser's file name safe to put on a command line: the
// base name only, letters, digits, dot, dash and underscore, nothing hidden.
// A name that comes out empty - "image.png" from a paste usually does not,
// but an emoji does - is named after the moment instead.
func uploadName(raw string, now time.Time) string {
	base := filepath.Base(strings.ReplaceAll(raw, `\`, "/"))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)), r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	name := b.String()
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	ext := filepath.Ext(name)
	if len(ext) > 12 {
		ext = ""
	}
	if stem := strings.TrimLeft(strings.TrimSuffix(name, ext), ".-"); stem == "" {
		return "upload-" + now.Format("20060102-150405") + ext
	}
	name = strings.TrimLeft(name, ".-")
	if len(name) > 100 {
		name = name[:100-len(ext)] + ext
	}
	return name
}

// freeName is name, or name with -2, -3, ... before its extension when that
// is taken. A file that was uploaded earlier is never overwritten: the agent
// may still be about to read it.
func freeName(dir, name string) (string, *os.File, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		candidate := name
		if i > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, candidate), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return candidate, f, err
	}
	return "", nil, errors.New("too many files with that name")
}

// prepareUploadDir makes the directory and its .gitignore.
func prepareUploadDir(workdir string) (string, error) {
	dir := filepath.Join(workdir, uploadDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// handleUpload takes a multipart form of one or more `file` parts and answers
// where each one was put.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	if info, err := os.Stat(row.Workdir); err != nil || !info.IsDir() {
		writeError(w, http.StatusConflict, "the working directory of this session does not exist")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, uploadMax)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart form")
		return
	}
	dir, err := prepareUploadDir(row.Workdir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type saved struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	var files []saved
	// A request that fails half way leaves nothing behind: the files written
	// so far were for a paste the person never got.
	fail := func(code int, msg string) {
		for _, f := range files {
			os.Remove(f.Path)
		}
		writeError(w, code, msg)
	}
	now := time.Now()
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				fail(http.StatusRequestEntityTooLarge, "that is more than 64 MB")
				return
			}
			fail(http.StatusBadRequest, "the upload was cut off")
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		if len(files) == uploadFilesMax {
			part.Close()
			fail(http.StatusBadRequest, fmt.Sprintf("at most %d files at once", uploadFilesMax))
			return
		}
		name, f, err := freeName(dir, uploadName(part.FileName(), now))
		if err != nil {
			part.Close()
			fail(http.StatusInternalServerError, err.Error())
			return
		}
		path := filepath.Join(dir, name)
		n, err := io.Copy(f, part)
		part.Close()
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		files = append(files, saved{Name: name, Path: path, Size: n})
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				fail(http.StatusRequestEntityTooLarge, "that is more than 64 MB")
				return
			}
			fail(http.StatusInternalServerError, err.Error())
			return
		}
	}
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "there was no file in the upload")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
