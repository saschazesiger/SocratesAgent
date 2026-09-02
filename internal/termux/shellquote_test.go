//go:build !windows

package termux

import (
	"os/exec"
	"strings"
	"testing"
)

// TestShellQuoteRoundTrip runs the awkward characters through a real /bin/sh,
// which is what tmux does with the two strings that go through one.
func TestShellQuoteRoundTrip(t *testing.T) {
	for _, value := range []string{
		"plain",
		"it's",
		"a b",
		"$HOME",
		"`id`",
		"line\nbreak",
		`back\slash`,
		`"double"`,
		"semi;colon & pipe|",
		"/a b/it's $weird#dir/socrates",
	} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+ShellQuote(value)).Output()
		if err != nil {
			t.Fatalf("sh refused %q: %v", value, err)
		}
		if string(out) != value {
			t.Fatalf("%q came back as %q", value, string(out))
		}
	}
}

func TestShellQuoteWrapsEverything(t *testing.T) {
	if got := ShellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("ShellQuote(\"a'b\") = %s", got)
	}
	if !strings.HasPrefix(ShellQuote(""), "'") {
		t.Fatal("an empty string must still be quoted, or it disappears from the command")
	}
}
