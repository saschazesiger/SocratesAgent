//go:build !windows

package termux

import (
	"testing"
	"time"
)

// TestViewerEndedAfterAForeignDetach: a tmux client that somebody else
// detached is finished, and it has to say so.
//
// Nothing about the Viewer changes shape when that happens - the ring still
// holds the last screen, the struct is still there - so a caller that keeps a
// viewer between connections cannot tell a working terminal from a closed file
// without asking. The transport asks this on every reconnect; without it, a
// tab kept picking a dead terminal for ever and nothing it typed arrived.
func TestViewerEndedAfterAForeignDetach(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	v := l.attach(row.ID, 100, 30)
	if _, err := v.Write([]byte("echo still-alive\n")); err != nil {
		t.Fatal(err)
	}
	waitForRing(t, v, 5*time.Second, "still-alive")
	if v.Ended() {
		t.Fatalf("an attached viewer says it has ended")
	}

	l.tmuxOut("detach-client", "-s", TmuxName(row.ID))
	waitFor(t, 10*time.Second, "the viewer to notice its terminal ended", v.Ended)

	if _, err := v.Write([]byte("echo never\n")); err == nil {
		t.Fatalf("a viewer whose terminal ended took a keystroke")
	}
	select {
	case <-v.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("Done is not closed for a viewer that has ended")
	}
}
