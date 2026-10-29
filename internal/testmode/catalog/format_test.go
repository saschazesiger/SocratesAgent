package catalog

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The format reference names every front-matter key the parser takes: the
// keys are read from the parser's own calls (d.keys("title", ...)), so a key
// added to the code and not to FORMAT.md fails here.
func TestTheFormatReferenceNamesEveryKey(t *testing.T) {
	call := regexp.MustCompile(`\b\w+\.keys\(([^)]*)\)`)
	quoted := regexp.MustCompile(`"([a-z_]+)"`)
	keys := append([]string{}, testKeys...)
	for _, file := range []string{"catalog.go", "area.go", "tool.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			for _, k := range quoted.FindAllStringSubmatch(m[1], -1) {
				keys = append(keys, k[1])
			}
		}
	}
	if len(keys) < 25 {
		t.Fatalf("only %d keys found in the parser: %v", len(keys), keys)
	}
	for _, k := range keys {
		if !strings.Contains(Format, "`"+k+"`") && !strings.Contains(Format, "`secrets."+k+"`") && !strings.Contains(Format, "`pool."+k+"`") {
			t.Errorf("FORMAT.md does not name the key %q", k)
		}
	}
	for _, word := range []string{SelectAll, SelectRotate, SelectExcept, EvidenceScreenshot, EvidenceAny, EvidenceNone, P0, P1, P2,
		"## Steps", "Expect:", "actions.jsonl", "links.json", "SOCRATES_TEST_VAULT", "tests_check", "socrates tests check"} {
		if !strings.Contains(Format, word) {
			t.Errorf("FORMAT.md does not mention %q", word)
		}
	}
}
