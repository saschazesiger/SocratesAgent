package termux

// signalChildEnded has nothing to send on Windows, where there is no tmux
// server to wake.
func signalChildEnded(int) error { return nil }
