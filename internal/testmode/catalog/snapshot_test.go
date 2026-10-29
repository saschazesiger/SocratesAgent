package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// listTree lists the files below dir with their permission bits.
func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case e.IsDir():
			out = append(out, filepath.ToSlash(rel)+"/")
		case info.Mode()&os.ModeSymlink != 0:
			out = append(out, filepath.ToSlash(rel)+" link")
		default:
			out = append(out, filepath.ToSlash(rel)+" "+info.Mode().Perm().String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// A run's snapshot is the catalog without tools/, hidden files and links;
// a tool's copy has its hidden files and executable bits but no
// node_modules, .git or links.
func TestSnapshotAndCopyTool(t *testing.T) {
	root := writeRepo(t, baseRepo())
	dir := filepath.Join(root, ".socrates", "tests")
	outside := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outside, []byte("never copied"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"web/leak.md", "tools/driver/leak"} {
		if err := os.Symlink(outside, filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	drv := filepath.Join(dir, "tools", "driver")
	for name, mode := range map[string]os.FileMode{"bin/drv": 0o755, ".npmrc": 0o644, "node_modules/x/i.js": 0o644, ".git/HEAD": 0o644} {
		p := filepath.Join(drv, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(name), mode); err != nil || os.Chmod(p, mode) != nil {
			t.Fatal(err)
		}
	}

	snap := filepath.Join(t.TempDir(), "catalog")
	if err := Snapshot(root, snap); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"README.md -rw-r--r--", "TESTS.md -rw-r--r--",
		"api/", "api/AREA.md -rw-r--r--", "api/ping.md -rw-r--r--",
		"web/", "web/AREA.md -rw-r--r--", "web/home.md -rw-r--r--", "web/notes.txt -rw-r--r--",
		"web/sign-in/", "web/sign-in/README.md -rw-r--r--", "web/sign-in/form.md -rw-r--r--",
	}
	if got := listTree(t, snap); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "driver")
	if err := CopyTool(c.Tools["driver"], dest); err != nil {
		t.Fatal(err)
	}
	want = []string{".npmrc -rw-r--r--", "TOOL.md -rw-r--r--", "bin/", "bin/drv -rwxr-xr-x"}
	if got := listTree(t, dest); !reflect.DeepEqual(got, want) {
		t.Fatalf("tool copy:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if err := Snapshot(t.TempDir(), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("a snapshot of a repository without a catalog")
	}
}
