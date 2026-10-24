//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBinaryTakesTheFirstChoiceThenTheName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"tool", "other"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + dir}
	cases := []struct {
		name    string
		choices []string
		want    string
	}{
		{"nothing set is the name on the PATH", []string{"", " "}, filepath.Join(dir, "tool")},
		{"a bare name is looked up", []string{"other", "tool"}, filepath.Join(dir, "other")},
		{"an empty first choice gives way", []string{"", filepath.Join(dir, "other")}, filepath.Join(dir, "other")},
		{"a path is taken as it is", []string{filepath.Join(dir, "tool")}, filepath.Join(dir, "tool")},
	}
	for _, c := range cases {
		got, err := ResolveBinary(env, "tool", c.choices...)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, err := ResolveBinary(env, "tool", filepath.Join(dir, "missing")); err == nil {
		t.Error("a configured path that does not exist was taken")
	}
	// The chat's PATH decides, not this process's.
	if _, err := ResolveBinary([]string{"PATH=" + t.TempDir()}, "tool"); err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Errorf("a name missing from the chat's PATH: %v", err)
	}
}
