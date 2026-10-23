package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveBinary is the program an adapter starts: the first of choices that
// is set (the dashboard's StartSpec.Binary, then an override an adapter
// reads from the chat's environment), else name. A choice with a slash is a
// path, which must exist; a bare name, and name itself, are looked up on the
// PATH of env, and on this process's PATH only when env has none, so that a
// chat runs what its own environment would find.
func ResolveBinary(env []string, name string, choices ...string) (string, error) {
	for _, c := range choices {
		if c = strings.TrimSpace(c); c != "" {
			name = c
			break
		}
	}
	if strings.ContainsRune(name, '/') {
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("the configured binary: %w", err)
		}
		return name, nil
	}
	path, ok := lookupEnv(env, "PATH")
	if !ok {
		path = os.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s on the chat's PATH", name)
}

func lookupEnv(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}
