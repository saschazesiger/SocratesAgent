package accounts

import (
	"context"
	"errors"
)

// startOpenCodeSignIn: the OpenCode adapter is not built for Windows.
func startOpenCodeSignIn(context.Context, string, string, []string, string) (openCodeSignIn, error) {
	return nil, errors.New("OpenCode does not run on Windows")
}
