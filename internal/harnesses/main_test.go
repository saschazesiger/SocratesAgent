package harnesses

import (
	"os"
	"testing"
)

// TestMain exists for one reason: the fake CLI this package builds is shared
// by every test through a sync.Once, so no single test can own the directory
// it lives in and t.TempDir cannot clean it up. Removing it here is the only
// point at which the last test that needs it has finished.
func TestMain(m *testing.M) {
	code := m.Run()
	if fakeTUIDir != "" {
		_ = os.RemoveAll(fakeTUIDir)
	}
	os.Exit(code)
}
