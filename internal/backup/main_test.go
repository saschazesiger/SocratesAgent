package backup

import (
	"os"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// lockProbeEnv makes the test binary the child of lockedElsewhere
// (lock_unix_test.go) instead of running the tests.
const lockProbeEnv = "SOCRATES_TEST_LOCK_PROBE"

var lockProbe func(path string)

// TestMain runs the tests with a home of their own, so that none of them can
// reach the real agent folders or data directory (internal/testhome).
func TestMain(m *testing.M) {
	if path := os.Getenv(lockProbeEnv); path != "" && lockProbe != nil {
		lockProbe(path)
		os.Exit(0)
	}
	os.Exit(testhome.Main(m))
}
