package harnesses

import (
	"os"
	"testing"
)

// That is the promise the fallback chain makes: a shell is never "not
// installed".
func TestShellDefaultBinaryIsSomethingToRun(t *testing.T) {
	bin := Shell{}.DefaultBinary()
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("the default shell %q is not there: %v", bin, err)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the default shell %q is not something to run: %v", bin, info.Mode())
	}
}
