package main

import (
	"os"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

func TestMain(m *testing.M) {
	// The start a rehearsal of the migration kills half way
	// (rehearsal_test.go) is this binary too.
	if data := os.Getenv(rehearsalChild); data != "" {
		os.Exit(rehearsalStart(data))
	}
	os.Exit(testhome.Main(m))
}
