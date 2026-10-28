package chattools

import (
	"os"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// TestMain runs the tests with a home of their own, so that none of them can
// reach the real agent folders or data directory (internal/testhome).
func TestMain(m *testing.M) { os.Exit(testhome.Main(m)) }
