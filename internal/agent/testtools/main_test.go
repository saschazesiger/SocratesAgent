package testtools

import (
	"os"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// TestMain runs the tests with a home of their own (internal/testhome).
func TestMain(m *testing.M) { os.Exit(testhome.Main(m)) }
