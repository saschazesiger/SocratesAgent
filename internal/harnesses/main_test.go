package harnesses

import (
	"os"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// TestMain runs every test of this package in a home of its own, so that no
// version probe or path this package reads can reach a real agent's folder.
func TestMain(m *testing.M) {
	os.Exit(testhome.Main(m))
}
