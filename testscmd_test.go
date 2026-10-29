package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// `socrates tests check` prints every problem with its path from the
// repository's root, then a summary, and fails on errors only.
func TestTestsCheck(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := testsMain([]string{"check", "internal/testmode/catalog/testdata/browser"}, &out, &errOut); code != 0 {
		t.Fatalf("browser catalog: exit %d\n%s%s", code, out.String(), errOut.String())
	}
	if out.String() != "1 area, 6 tests, 14 cells\n" {
		t.Fatalf("browser catalog: %q", out.String())
	}

	repo := t.TempDir()
	dir := filepath.Join(repo, ".socrates", "tests")
	for name, content := range map[string]string{
		"TESTS.md":   "Hello.",
		"a/AREA.md":  "---\ntitel: A\n---\n",
		"a/x.md":     "---\ntitle: X\n---\n## Steps\n1. Do.\n   Expect: done.\n",
		"b/notes.md": "no AREA.md",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	if code := testsMain([]string{"check", repo}, &out, &errOut); code != 1 {
		t.Fatalf("broken catalog: exit %d", code)
	}
	want := ".socrates/tests/a/AREA.md:2: unknown key \"titel\" (known: title, tools, columns, dimensions, params, evidence, mcp, timeout)\n" +
		".socrates/tests/b/: a folder of the catalog needs an AREA.md (or it is not an area and its tests would be lost)\n" +
		".socrates/tests/a/x.md: warning: no priority: the test runs as P1 (priority: P0, P1 or P2)\n" +
		"1 area, 1 test, 1 cell; 2 errors, 1 warning\n"
	if out.String() != want {
		t.Fatalf("broken catalog:\n%s\nwant\n%s", out.String(), want)
	}

	if code := testsMain([]string{"check", t.TempDir()}, &out, &errOut); code != 1 {
		t.Fatalf("no catalog: exit %d", code)
	}
	if code := testsMain(nil, &out, &errOut); code != 2 {
		t.Fatalf("no subcommand: exit %d", code)
	}
	if code := testsMain([]string{"chek"}, &out, &errOut); code != 2 {
		t.Fatalf("typo: exit %d", code)
	}
}
