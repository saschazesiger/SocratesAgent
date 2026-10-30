package accounts

import (
	"context"
	"errors"
)

// OpenCodeTerminal: the OpenCode adapter is not built for Windows.
func OpenCodeTerminal(context.Context, string, string, []string, string) (string, map[string]string, error) {
	return "", nil, errors.New("OpenCode does not run on Windows")
}

// OpenCodeModelsEnv: the OpenCode adapter is not built for Windows.
func OpenCodeModelsEnv(string, string, []string) ([]string, error) {
	return nil, errors.New("OpenCode does not run on Windows")
}

// OpenCodeModelsWatched: nothing, on Windows.
func OpenCodeModelsWatched(string, string) []string { return nil }
