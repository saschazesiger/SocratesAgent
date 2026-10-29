package testmode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var extPattern = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)

// The evidence of an attempt (TEST-MODE.md, "The tool contract"): the
// folder $SOCRATES_TEST_EVIDENCE, where the tools write one JSON line per
// action into actions.jsonl with the files they name beside it, links.json
// and logs/. The agent controls that folder, so nothing in it is taken on
// trust: only regular files directly in it (no symlinks, no paths), each of a
// bounded size.
const (
	ActionsFile = "actions.jsonl"
	LinksFile   = "links.json"
	// maxActions is how many actions one attempt keeps.
	maxActions = 3000
	// maxLine is the longest action line read.
	maxLine = 256 << 10
	// MaxEvidenceFile is the largest evidence file served or attached.
	MaxEvidenceFile = 32 << 20
)

// errNotEvidence is a name that is no evidence file.
var errNotEvidence = errors.New("not an evidence file")

// EvidenceFile is the path of a file of an evidence folder by its name, which
// may sit one level deep (logs/x.txt); refused unless it is a regular file of
// at most MaxEvidenceFile.
func EvidenceFile(dir, name string) (string, error) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", errNotEvidence
	}
	parts := strings.Split(name, "/")
	if len(parts) > 2 {
		return "", errNotEvidence
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".") {
			return "", errNotEvidence
		}
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	if len(parts) == 2 {
		// The folder between must be a folder, not a link out.
		if fi, err := os.Lstat(filepath.Join(dir, parts[0])); err != nil || !fi.IsDir() {
			return "", errNotEvidence
		}
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxEvidenceFile {
		return "", errNotEvidence
	}
	return path, nil
}

// toolLine is a line of actions.jsonl as a tool writes it.
type toolLine struct {
	At          string          `json:"at"`
	Tool        string          `json:"tool"`
	Action      string          `json:"action"`
	Summary     string          `json:"summary"`
	URL         string          `json:"url"`
	Screenshot  string          `json:"screenshot"`
	MS          int64           `json:"ms"`
	Error       *string         `json:"error"`
	Interaction bool            `json:"interaction"`
	Details     json.RawMessage `json:"details"`
}

// ingest reads the actions the tools wrote since the last time into the
// attempt, and returns how many it took. Only whole lines are read: one being
// written is read the next time.
func ingest(a *Attempt, dir string, red *Redactor) (int, error) {
	got, err := readActions(dir, a.Offset, len(a.Actions), red)
	if err != nil {
		return 0, err
	}
	got.apply(a)
	return len(got.actions), nil
}

// actionsRead is what readActions read of an evidence folder's
// actions.jsonl: the offset it read from (0 when the file was replaced), the
// offset it got to, and the actions, numbered after the have the attempt
// had.
type actionsRead struct {
	from, to int64
	have     int
	actions  []Action
}

// apply takes what was read into the attempt.
func (got actionsRead) apply(a *Attempt) {
	a.Offset = got.to
	a.Actions = append(a.Actions, got.actions...)
}

// readActions reads the whole lines of the evidence's actions.jsonl after
// offset into actions numbered after the have an attempt has, at most
// maxActions in all; it reads files only, so that it may run without the
// runner's lock (the live view).
func readActions(dir string, offset int64, have int, red *Redactor) (actionsRead, error) {
	got := actionsRead{from: offset, to: offset, have: have}
	path := filepath.Join(dir, ActionsFile)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return got, nil
	}
	if err != nil {
		return got, err
	}
	if !fi.Mode().IsRegular() {
		return got, fmt.Errorf("%s is not a regular file", ActionsFile)
	}
	if fi.Size() < offset {
		// The file was replaced: start again from its beginning.
		got.from, got.to = 0, 0
	}
	f, err := os.Open(path)
	if err != nil {
		return actionsRead{from: offset, to: offset, have: have}, err
	}
	defer f.Close()
	if _, err := f.Seek(got.to, io.SeekStart); err != nil {
		return actionsRead{from: offset, to: offset, have: have}, err
	}
	rd := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			// A line without its newline is still being written.
			break
		}
		got.to += int64(len(line))
		line = bytes.TrimSpace(line)
		if len(line) == 0 || len(line) > maxLine || have+len(got.actions) >= maxActions {
			continue
		}
		var tl toolLine
		if json.Unmarshal(line, &tl) != nil || strings.TrimSpace(tl.Action) == "" {
			continue
		}
		act := Action{
			N: have + len(got.actions) + 1, At: tl.At, Tool: tl.Tool, Action: tl.Action,
			Summary: red.String(tl.Summary), URL: red.String(tl.URL), MS: tl.MS, Interaction: tl.Interaction,
			Details: red.JSON(tl.Details),
		}
		if tl.Error != nil {
			act.Error = red.String(*tl.Error)
		}
		if tl.Screenshot != "" {
			if _, err := EvidenceFile(dir, tl.Screenshot); err == nil && isImage(tl.Screenshot) {
				act.Screenshot = filepath.ToSlash(tl.Screenshot)
			} else {
				act.Error = strings.TrimSpace(act.Error + " (screenshot " + tl.Screenshot + " is not a file of the evidence)")
			}
		}
		got.actions = append(got.actions, act)
	}
	return got, nil
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	}
	return false
}

// readLinks is the evidence's links.json, redacted; nil when there is none.
func readLinks(dir string, red *Redactor) map[string]string {
	path, err := EvidenceFile(dir, LinksFile)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 256<<10 {
		return nil
	}
	var links map[string]string
	if json.Unmarshal(raw, &links) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range links {
		v = strings.TrimSpace(v)
		if k = strings.TrimSpace(k); k == "" || v == "" {
			continue
		}
		if !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
			continue
		}
		out[red.String(k)] = red.String(v)
	}
	return out
}

// attach copies a file the agent names into the evidence folder and returns
// its name there. src is absolute or relative to the folder; it must be a
// regular file (no symlink) of at most MaxEvidenceFile, in the folder or
// under one of roots (the repository, the temporary folder): never another
// file of the data directory, which would go into the backup and the page.
func attach(dir, src string, n int, roots ...string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", errors.New("no file named")
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(dir, src)
	}
	src = filepath.Clean(src)
	inside := func(root string) bool {
		rel, err := filepath.Rel(filepath.Clean(root), src)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	allowed := inside(dir)
	for _, root := range roots {
		if root != "" && inside(root) {
			allowed = true
		}
	}
	if !allowed {
		return "", fmt.Errorf("%s is neither in the evidence folder nor in the repository or the temporary folder", src)
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", src)
	}
	if fi.Size() > MaxEvidenceFile {
		return "", fmt.Errorf("%s is larger than %d MB", src, MaxEvidenceFile>>20)
	}
	// A file already directly in the folder is kept where it is.
	if filepath.Dir(src) == filepath.Clean(dir) && !strings.HasPrefix(filepath.Base(src), ".") {
		return filepath.Base(src), nil
	}
	ext := strings.ToLower(filepath.Ext(src))
	if !extPattern.MatchString(ext) {
		ext = ""
	}
	stem := strings.TrimSuffix(strings.ToLower(filepath.Base(src)), ext)
	stem = strings.Trim(keyUnsafe.ReplaceAllString(stem, "-"), "-")
	if len(stem) > 40 {
		stem = stem[:40]
	}
	name := fmt.Sprintf("attach-%02d-%s%s", n, stem, ext)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	out, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, io.LimitReader(in, MaxEvidenceFile)); err != nil {
		out.Close()
		return "", err
	}
	return name, out.Close()
}

// redactable is whether an evidence file is text a secret could be in.
func redactable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jsonl", ".json", ".txt", ".log", ".har", ".md", ".csv", ".xml", ".html":
		return true
	}
	return false
}

// redactEvidence redacts the text files of an evidence folder in place (its
// own and those of logs/): what the tools wrote goes into the backup and to
// the page with the known secrets cut, as the record does.
func redactEvidence(dir string, red *Redactor) {
	if red == nil || red.r == nil {
		return
	}
	for _, sub := range []string{"", "logs"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !redactable(e.Name()) {
				continue
			}
			path := filepath.Join(dir, sub, e.Name())
			if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxEvidenceFile {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if out := red.String(string(raw)); out != string(raw) {
				_ = os.WriteFile(path, []byte(out), 0o600)
			}
		}
	}
}
