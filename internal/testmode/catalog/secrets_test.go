package catalog

import (
	"reflect"
	"testing"
)

// The secrets command's lines are taken as secretctl prints them: split at
// the first =, the value raw, an export dropped, everything else skipped.
func TestParseSecrets(t *testing.T) {
	out := "# device-qa\n" +
		"BROWSERSTACK_ACCOUNTS=alice:k1,bob:k2\n" +
		"export QA_GOOGLE_PASSWORD='p=a\"ss' \r\n" +
		"\n" +
		"  QA_EMPTY=\n" +
		"not a line\n" +
		"1BAD=x\n" +
		"BAD-NAME=x\n" +
		"=x\n" +
		"QA_X=first\n" +
		"QA_X=second"
	want := map[string]string{
		"BROWSERSTACK_ACCOUNTS": "alice:k1,bob:k2",
		"QA_GOOGLE_PASSWORD":    "'p=a\"ss' ",
		"QA_EMPTY":              "",
		"QA_X":                  "second",
	}
	if got := ParseSecrets([]byte(out)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	if got := ParseSecrets(nil); len(got) != 0 {
		t.Fatalf("nothing: %v", got)
	}
}

// A pool member of the form name:secret is labelled by its name; one
// without a colon goes by its number.
func TestMemberLabel(t *testing.T) {
	var tool Tool
	for value, want := range map[string]string{"alice:k1": "alice", "a:b:c": "a", "k1": "", ":k": ""} {
		if got := tool.MemberLabel(value); got != want {
			t.Errorf("MemberLabel(%q) = %q, want %q", value, got, want)
		}
	}
}
