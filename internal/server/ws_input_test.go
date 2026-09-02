//go:build !windows

package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// TestTerminalDetachedClientIsReattached is the bug this file exists for.
//
// A tmux client can end without the browser hearing a thing: another client
// detaches it, the tmux server is restarted, the window is killed. What was
// left behind was a viewer entry whose ring still held a perfectly good screen
// and whose pseudo terminal was a closed file - and because the entry looked
// alive, every reconnect chose it again. The tab kept its picture, the socket
// kept saying "live", and not one keystroke ever reached the pane again.
//
// The reconnect now attaches a new terminal under the same entry: the screen
// is redrawn, the input counter survives - so nothing the tab is holding is
// handed back or written twice - and typing works.
func TestTerminalDetachedClientIsReattached(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)

	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	first.hello()
	first.send(1, "echo mark-one\r")
	first.waitFor("mark-one")
	rendered := first.settle()

	// Somebody else detaches every client of this session, which is what a
	// tmux server restart and a stray `tmux detach-client -a` both look like
	// from here.
	if _, err := e.tmux("detach-client", "-s", termux.TmuxName(id)); err != nil {
		t.Fatalf("detach-client: %v", err)
	}
	waitForClients(e, id, 0)

	// The socket is still open and still shows the screen. What it can no
	// longer do is deliver a keystroke, and it says so rather than pretending:
	// the frame is not acknowledged, so the tab keeps holding it.
	first.send(2, "echo mark-two\r")
	if code := first.closeStatus(15 * time.Second); code == 0 {
		t.Fatalf("the socket over a dead terminal never ended")
	}
	ack := first.waitCtrl("input_ack")
	if got := floatOf(t, ack, "seq"); got != 1 {
		t.Fatalf("the refusal acknowledged %v, want the last number that reached the pane, 1", got)
	}

	second := e.dialWS(id, fmt.Sprintf("viewer=tab-a&cols=100&rows=30&since=%d", rendered))
	hello := second.hello()
	if hello["viewer_fresh"] != false {
		t.Fatalf("the tab was forgotten, so what it held would be handed back: %#v", hello)
	}
	if got := floatOf(t, hello, "replay_from"); got != 0 {
		t.Fatalf("replay_from = %v; a terminal that was replaced is a redraw", got)
	}
	if got := floatOf(t, hello, "input_ack"); got != 1 {
		t.Fatalf("input_ack = %v, want 1: the input counter has to survive", got)
	}

	// And the whole point: the tab can type again, without a reload.
	second.send(2, "echo mark-two\r")
	e.waitForPane(id, "mark-two")
	if got := floatOf(t, second.waitCtrl("input_ack"), "seq"); got != 2 {
		t.Fatalf("the keystroke after the re-attach was not acknowledged: %v", got)
	}
}

// TestTerminalPasteStateIsNotCarriedOverASocket: a bracketed paste that was cut
// in half by a lost connection left the viewer treating everything as pasted
// text for as long as the tab lived - which means every terminal report the
// browser sends is typed into the shell instead of being dropped.
func TestTerminalPasteStateIsNotCarriedOverASocket(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)

	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	first.hello()
	// A paste that begins and never ends, because the socket goes.
	first.send(1, "\x1b[200~")
	first.waitCtrl("input_ack")
	first.drop()

	second := e.dialWS(id, "viewer=tab-a&cols=100&rows=30&since=0")
	second.hello()
	// A device-attributes reply, the ordinary thing a browser sends after a
	// reconnect. It is a report, not text, and it must not reach the shell.
	second.send(2, "\x1b[?1;2c")
	second.waitCtrl("input_ack")
	second.send(3, "echo after-paste\r")
	screen := e.waitForPane(id, "after-paste")
	if strings.Contains(screen, "1;2c") {
		t.Fatalf("a terminal report was typed into the pane:\n%s", screen)
	}
}

// waitForClients waits until a session has the number of tmux clients the test
// is waiting for.
func waitForClients(e *sessionEnv, id string, want int) {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	got := -1
	for time.Now().Before(deadline) {
		got = e.clientCount(id)
		if got == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("session %s has %d tmux clients, want %d", id, got, want)
}
