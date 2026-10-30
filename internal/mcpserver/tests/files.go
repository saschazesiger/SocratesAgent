package tests

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// The catalog's files, read, written and deleted for an agent: only inside
// the repository's .socrates/tests/, by names the catalog takes (letters,
// digits, -, _ and ., nothing hidden), never through a symbolic link.

var segmentRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// dir is the repository's catalog folder.
func (c *call) dir() string { return filepath.Join(c.root, filepath.FromSlash(catalog.DirName)) }

// catalogPath is a path inside the catalog folder, cleaned, or why it is
// not one.
func catalogPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, catalog.DirName+"/")
	if p == "" {
		return "", errors.New("Name the file: its path inside " + catalog.DirName + "/.")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%s is not a path inside %s/.", p, catalog.DirName)
	}
	for _, seg := range strings.Split(p, "/") {
		if !segmentRE.MatchString(seg) || strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("%s: a folder or file name of the catalog is letters, digits, -, _ and ., and does not start with a dot.", p)
		}
	}
	return path.Clean(p), nil
}

// noLinks refuses a path that goes through a symbolic link below the
// repository: a write there could land outside the catalog.
func (c *call) noLinks(rel string) error {
	at := c.root
	for _, seg := range append(strings.Split(catalog.DirName, "/"), strings.Split(rel, "/")...) {
		at = filepath.Join(at, seg)
		fi, err := os.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; the catalog does not follow them.", at)
		}
	}
	return nil
}

func (c *call) readFile(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return c.listFiles()
	}
	rel, err := catalogPath(p)
	if err != nil {
		return "", err
	}
	if err := c.noLinks(rel); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(c.dir(), filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("The catalog has no file %s/%s.", catalog.DirName, rel)
	}
	if err != nil {
		return "", err
	}
	if len(data) > maxFile {
		return "", fmt.Errorf("%s/%s is larger than %d KB.", catalog.DirName, rel, maxFile>>10)
	}
	return string(data), nil
}

func (c *call) listFiles() (string, error) {
	var files []string
	err := filepath.WalkDir(c.dir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != c.dir() {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(c.dir(), p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("This repository (%s) has no %s/ yet.", c.root, catalog.DirName), nil
	}
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	return fmt.Sprintf("The files of %s/ (%s):\n- %s", catalog.DirName, c.root, strings.Join(files, "\n- ")), nil
}

func (c *call) writeFile(p, content string, executable bool) (string, error) {
	rel, err := catalogPath(p)
	if err != nil {
		return "", err
	}
	if len(content) > maxFile {
		return "", fmt.Errorf("The file is larger than %d KB.", maxFile>>10)
	}
	if err := c.noLinks(rel); err != nil {
		return "", err
	}
	full := filepath.Join(c.dir(), filepath.FromSlash(rel))
	if fi, err := os.Stat(full); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s/%s is not a file.", catalog.DirName, rel)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	// A file written again keeps its executable bit unless asked for it.
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(full); executable || err == nil && fi.Mode().Perm()&0o100 != 0 {
		mode = 0o755
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".write-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.WriteString(content)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), full)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return fmt.Sprintf("Wrote %s/%s (%d bytes).\n%s", catalog.DirName, rel, len(content), c.checkFile(rel)), nil
}

func (c *call) deleteFile(p string) (string, error) {
	rel, err := catalogPath(p)
	if err != nil {
		return "", err
	}
	if err := c.noLinks(rel); err != nil {
		return "", err
	}
	full := filepath.Join(c.dir(), filepath.FromSlash(rel))
	fi, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("The catalog has no file %s/%s.", catalog.DirName, rel)
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s/%s is not a file; delete the files in it one by one.", catalog.DirName, rel)
	}
	if err := os.Remove(full); err != nil {
		return "", err
	}
	// The folders it leaves empty go too, up to the catalog's.
	for d := filepath.Dir(full); d != c.dir() && strings.HasPrefix(d, c.dir()+string(filepath.Separator)); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return fmt.Sprintf("Deleted %s/%s.\n%s", catalog.DirName, rel, c.checkFile("")), nil
}

// checkFile is the catalog's check after a change: the problems of the
// file rel ("" for none in particular) and how many the catalog has.
func (c *call) checkFile(rel string) string {
	if _, err := os.Stat(filepath.Join(c.dir(), catalog.TestsFile)); err != nil {
		return fmt.Sprintf("The catalog has no %s yet: write it before a run can start.", catalog.TestsFile)
	}
	cat, err := catalog.Load(c.root)
	if err != nil {
		return "The catalog could not be read: " + err.Error()
	}
	var b strings.Builder
	var mine []string
	for _, list := range []struct {
		label    string
		problems []catalog.Problem
	}{{"error", cat.Errors}, {"warning", cat.Warnings}} {
		for _, pr := range list.problems {
			if rel != "" && pr.File == rel {
				pr.File = catalog.DirName + "/" + pr.File
				mine = append(mine, list.label+": "+pr.String())
			}
		}
	}
	if len(mine) > 0 {
		b.WriteString("Its problems:\n- " + strings.Join(mine, "\n- ") + "\n")
	} else if rel != "" {
		b.WriteString("It has no problems.\n")
	}
	fmt.Fprintf(&b, "The catalog: %d area(s), %d test(s), %d cell(s); %d error(s), %d warning(s)",
		len(cat.Areas), cat.TestCount(), len(cat.Cells()), len(cat.Errors), len(cat.Warnings))
	if len(cat.Errors)+len(cat.Warnings) > len(mine) {
		b.WriteString(" (" + chattools.TestsCheck + " lists them all)")
	}
	b.WriteString(".")
	return b.String()
}
