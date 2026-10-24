//go:build !windows

package termux

import (
	"strings"

	"golang.org/x/sys/unix"
)

// signalByName is the number of a signal tmux reported by name.
//
// tmux renders #{pane_dead_signal} through sig2name, which answers with the
// signal's name from sys_signame where the C library has one - macOS and the
// BSDs, lower case and without the SIG ("kill") - and with its number only
// everywhere else. The tmux server is on this machine, so this machine's own
// numbering is the one it meant.
func signalByName(name string) int {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return 0
	}
	if !strings.HasPrefix(name, "SIG") {
		name = "SIG" + name
	}
	return int(unix.SignalNum(name))
}
