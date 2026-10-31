package chattests

import (
	"os"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// TestMain runs the tests with a home of their own, so that none of them can
// reach the real agent folders or data directory (internal/testhome). The
// answers show times in the server's zone; the tests pin it to UTC so that
// what they expect does not depend on the machine's.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(testhome.Main(m))
}
