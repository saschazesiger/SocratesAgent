package artifacts

import (
	"os"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// TestMain runs the tests with a home of their own (internal/testhome), in
// UTC so that the answers' times do not depend on the machine.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(testhome.Main(m))
}
