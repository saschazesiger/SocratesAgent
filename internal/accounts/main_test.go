package accounts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// fakeCodex is e2e/fakebin/fakecodex, built once, standing in for codex.
var fakeCodex string

// TestMain builds the fakes the sign-in tests run instead of the agents, and
// runs the tests with a home of their own, so that none of them can reach the
// real agent folders or data directory (internal/testhome).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "socrates-accounts-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeCodex = filepath.Join(dir, "codex")
	build := exec.Command("go", "build", "-o", fakeCodex, "./e2e/fakebin/fakecodex")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build fakecodex: %v\n%s", err, out)
		os.Exit(1)
	}
	code := testhome.Main(m)
	os.RemoveAll(dir)
	os.Exit(code)
}
