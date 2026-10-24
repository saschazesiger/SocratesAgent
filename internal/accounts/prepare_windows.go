package accounts

import "errors"

// prepareOpenCode: the OpenCode adapter is not built for Windows.
func prepareOpenCode(string, string) error {
	return errors.New("OpenCode chats do not run on Windows")
}
