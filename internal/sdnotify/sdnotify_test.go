package sdnotify

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// listenNotify stands in for systemd: a unixgram socket that receives what
// Notify sends.
func listenNotify(t *testing.T, name string) *net.UnixConn {
	t.Helper()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen %s: %v", name, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func receive(t *testing.T, conn *net.UnixConn) string {
	t.Helper()
	buf := make([]byte, 256)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf[:n])
}

func TestNotifySendsToPathSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn := listenNotify(t, path)
	t.Setenv("NOTIFY_SOCKET", path)

	if err := Notify("READY=1"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := receive(t, conn); got != "READY=1" {
		t.Fatalf("received %q, want READY=1", got)
	}
}

func TestNotifySendsToAbstractSocket(t *testing.T) {
	name := fmt.Sprintf("@socrates-sdnotify-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	conn := listenNotify(t, name)
	t.Setenv("NOTIFY_SOCKET", name)

	if err := Notify("WATCHDOG=1"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := receive(t, conn); got != "WATCHDOG=1" {
		t.Fatalf("received %q, want WATCHDOG=1", got)
	}
}

func TestNotifyWithoutSocketIsSilent(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := Notify("READY=1"); err != nil {
		t.Fatalf("Notify without NOTIFY_SOCKET: %v", err)
	}
}

func TestNotifyRejectsWhatItCannotSpeak(t *testing.T) {
	if err := notifyTo("vsock:2:1234", "READY=1"); err == nil {
		t.Fatal("a vsock address was accepted")
	}
	if err := notifyTo(filepath.Join(t.TempDir(), "absent.sock"), "READY=1"); err == nil {
		t.Fatal("a socket nobody listens on was accepted")
	}
}

func TestWatchdogInterval(t *testing.T) {
	const self = 4242
	cases := []struct {
		usec, pid string
		want      time.Duration
		ok        bool
	}{
		{"", "", 0, false},
		{"120000000", "", 2 * time.Minute, true},
		{"120000000", "4242", 2 * time.Minute, true},
		// Inherited by a child: the watchdog is somebody else's.
		{"120000000", "1", 0, false},
		{"120000000", "x", 0, false},
		{"0", "", 0, false},
		{"-5", "", 0, false},
		{"soon", "", 0, false},
		{"99999999999999999", "", 0, false},
	}
	for _, c := range cases {
		got, ok := watchdogInterval(c.usec, c.pid, self)
		if got != c.want || ok != c.ok {
			t.Errorf("watchdogInterval(%q, %q) = %v, %v; want %v, %v", c.usec, c.pid, got, ok, c.want, c.ok)
		}
	}
}

func TestWatchdogIntervalFromEnvironment(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "")
	t.Setenv("WATCHDOG_PID", "")
	if _, ok := WatchdogInterval(); ok {
		t.Fatal("a watchdog without WATCHDOG_USEC")
	}
	t.Setenv("WATCHDOG_USEC", "3000000")
	t.Setenv("WATCHDOG_PID", fmt.Sprint(os.Getpid()))
	if got, ok := WatchdogInterval(); !ok || got != 3*time.Second {
		t.Fatalf("WatchdogInterval() = %v, %v; want 3s, true", got, ok)
	}
}
