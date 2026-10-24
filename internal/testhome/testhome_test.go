package testhome

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

// Every directory with tests has a TestMain that goes through Main or
// Isolate, so a package added later cannot run against the real home by
// forgetting to.
func TestEveryTestPackageRunsThroughMain(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	dirs := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "testdata", "out":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) < 2 {
		t.Fatalf("found only %d test packages under %s", len(dirs), root)
	}
	for dir, files := range dirs {
		rel, _ := filepath.Rel(root, dir)
		if !runsThroughMain(t, files) {
			t.Errorf("%s: no TestMain that calls testhome.Main or testhome.Isolate", rel)
		}
	}
}

// runsThroughMain reports whether one of files declares a TestMain whose body
// calls Main or Isolate of this package (testhome.X from outside, X inside).
func runsThroughMain(t *testing.T, files []string) bool {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return !found
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "testhome" && (fun.Sel.Name == "Main" || fun.Sel.Name == "Isolate") {
						found = true
					}
				case *ast.Ident:
					if f.Name.Name == "testhome" && (fun.Name == "Main" || fun.Name == "Isolate") {
						found = true
					}
				}
				return !found
			})
			if found {
				return true
			}
		}
	}
	return false
}

// After Isolate nothing points at the real home: HOME, the agents' folders
// derived from it, the data folder and the workspace root are all inside the
// temporary one, and Go still finds the caches it had.
func TestIsolatePointsEverythingAtATemporaryHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if !strings.Contains(home, "socrates-testhome-") {
		t.Fatalf("HOME is %q", home)
	}
	if data := os.Getenv("SOCRATES_DATA_DIR"); !strings.HasPrefix(data, filepath.Dir(home)) {
		t.Fatalf("SOCRATES_DATA_DIR is %q, outside %q", data, filepath.Dir(home))
	}
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "SOCRATES_WORKSPACE_ROOT", "ANTHROPIC_API_KEY"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s is still set: %q", name, v)
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "OPENCODE_") || strings.HasPrefix(kv, "SOCRATES_S3_") {
			t.Errorf("%s is still set", kv)
		}
	}
	if short, err := filepath.EvalSymlinks(shortTemp); err == nil && runtime.GOOS != "windows" && len(os.TempDir()) > len(short) {
		t.Errorf("the temporary directory %q is longer than %q", os.TempDir(), short)
	}
	if cache := os.Getenv("GOCACHE"); cache != "" && strings.HasPrefix(cache, home) {
		t.Errorf("GOCACHE moved into the temporary home: %q", cache)
	}
}
