package testtools

import (
	"strings"
	"testing"
)

func TestWorse(t *testing.T) {
	for _, c := range [][3]string{{"", Pass, Pass}, {Pass, "", Pass}, {Pass, Issue, Issue}, {Fail, Blocked, Fail},
		{NA, Pass, Pass}, {Blocked, Issue, Blocked}} {
		if got := Worse(c[0], c[1]); got != c[2] {
			t.Errorf("Worse(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// Every role's guidance names its tools as the agent knows them, and only
// tools the role has.
func TestGuidanceNamesTheRolesTools(t *testing.T) {
	for _, kind := range []string{KindCell, KindReviewer, KindAnalyst} {
		g := Guidance(kind, "mcp__socrates__", true)
		for _, d := range Tools(kind) {
			if !Has(kind, d.Name) {
				t.Errorf("%s: Has(%s)", kind, d.Name)
			}
			if d.Schema["type"] != "object" {
				t.Errorf("%s: %s has no object schema", kind, d.Name)
			}
		}
		if !strings.Contains(g, "ToolSearch (select:mcp__socrates__") {
			t.Errorf("%s: no ToolSearch line", kind)
		}
		for _, other := range []string{KindCell, KindReviewer, KindAnalyst} {
			if other == kind {
				continue
			}
			for _, d := range Tools(other) {
				if strings.Contains(g, "mcp__socrates__"+d.Name+" ") || strings.Contains(g, "mcp__socrates__"+d.Name+".") {
					t.Errorf("%s names %s's tool %s", kind, other, d.Name)
				}
			}
		}
	}
	if Tools("") != nil || Guidance("", "", false) != "" || IsTest("") {
		t.Error("an ordinary chat has test tools")
	}
}
