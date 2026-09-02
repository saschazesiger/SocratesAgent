package termux

import "strings"

// ShellQuote wraps s so that /bin/sh reads it back as exactly s.
//
// Almost nothing here goes through a shell: a program's argv is handed to tmux
// as separate arguments. Two strings are the exception, because tmux itself
// runs them that way - the `pipe-pane` command and the body of a `run-shell`
// hook - and both embed paths the user chose. Single quotes with '\” for an
// embedded quote is the one form that means the same thing in every POSIX
// shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
