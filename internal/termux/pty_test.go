//go:build !windows

package termux

import (
	"os/exec"
	"testing"
	"time"
)

// Closing the master ends a read on it while the program on the terminal is
// still there. With the blocking descriptor pty leaves behind, the read went
// on, the runtime put the actual close off until it returned, and so the
// terminal was never hung up and the read never returned: Viewer.Close
// waited for its pump for ever (TestConcurrentViewers hung for 40 minutes).
func TestClosingTheMasterEndsAReadOnIt(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	master, _, err := startPTY(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	read := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := master.Read(buf); err != nil {
				read <- err
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-read:
		t.Fatalf("the read ended before Close, so this proves nothing: %v", err)
	default:
	}
	// A resize must not put the descriptor back in blocking mode.
	if err := setPTYSize(master, 100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if err := master.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-read:
	case <-time.After(3 * time.Second):
		t.Fatal("the read on the master went on after Close")
	}
}
