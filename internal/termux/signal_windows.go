package termux

// signalByName knows no names on Windows, where there is no tmux to report one.
func signalByName(name string) int { return 0 }

// sendSIGCHLD has nothing to signal on Windows, where there is no tmux.
func sendSIGCHLD(pid int) error { return nil }
