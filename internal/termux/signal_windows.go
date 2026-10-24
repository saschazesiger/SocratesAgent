package termux

// signalByName knows no names on Windows, where there is no tmux to report one.
func signalByName(name string) int { return 0 }
