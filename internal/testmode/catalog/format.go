package catalog

import _ "embed"

// Format is the catalog's format reference (FORMAT.md): for the people and
// agents who write a catalog, given to chat agents by the tool tests_format
// and pointed at from docs/TESTS-FORMAT.md.
//
//go:embed FORMAT.md
var Format string
