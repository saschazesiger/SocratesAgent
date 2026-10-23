package main

import (
	"os"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

func TestMain(m *testing.M) { os.Exit(testhome.Main(m)) }
