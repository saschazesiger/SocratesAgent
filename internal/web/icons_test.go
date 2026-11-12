package web

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every icon name the pages use is a Z-Dash glyph or one of ui.js's GLYPH
// names: the bundle's scan lists a name it does not know under "unknown",
// and ui.js icon() throws on it when it is drawn, which stops the page that
// draws it (an "Open on GitHub" with a glyph Z-Dash does not have kept the
// Docs page on its spinner).
func TestEveryIconIsAGlyph(t *testing.T) {
	raw, err := os.ReadFile("static/ds/ds.bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Unknown []string `json:"unknown"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	ui, err := os.ReadFile("static/js/ui.js")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const GLYPH = \{(.*?)\};`).FindSubmatch(ui)
	if block == nil {
		t.Fatal("ui.js has no GLYPH map")
	}
	aliases := map[string]bool{}
	for _, m := range regexp.MustCompile(`(\w+):\s*'`).FindAllSubmatch(block[1], -1) {
		aliases[string(m[1])] = true
	}
	for _, entry := range bundle.Unknown {
		// "<file>:<line> icon <name>"
		fields := strings.Fields(entry)
		if len(fields) == 3 && fields[1] == "icon" && !aliases[fields[2]] {
			t.Errorf("%s: Z-Dash has no glyph %q and ui.js's GLYPH does not name it", fields[0], fields[2])
		}
	}
}
