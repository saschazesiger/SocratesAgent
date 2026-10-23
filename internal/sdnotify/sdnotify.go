// Package sdnotify speaks the little of systemd's sd_notify protocol Socrates
// needs: READY=1, STOPPING=1 and the watchdog's WATCHDOG=1. It is a datagram
// to the socket systemd names in NOTIFY_SOCKET, which is small enough not to
// be worth a dependency, and everything here is a silent no-op when Socrates
// is not running under systemd at all.
package sdnotify

import (
	"errors"
	"math"
	"net"
	"os"
	"strconv"
	"time"
)

// Notify sends one state message, such as "READY=1", to the service manager.
// Without NOTIFY_SOCKET there is nobody to tell, and that is not an error.
func Notify(state string) error {
	return notifyTo(os.Getenv("NOTIFY_SOCKET"), state)
}

func notifyTo(socket, state string) error {
	if socket == "" {
		return nil
	}
	// An abstract socket is named with a leading '@', and Go's net package
	// already turns that into the leading NUL the kernel wants. Anything
	// else that is not a path - systemd also knows vsock: addresses, for
	// virtual machines - is not something Socrates is ever run under.
	if socket[0] != '@' && socket[0] != '/' {
		return errors.New("sdnotify: unsupported NOTIFY_SOCKET " + strconv.Quote(socket))
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}

// WatchdogInterval is the watchdog timeout systemd set for this process, and
// whether there is one. WATCHDOG_PID is checked when it is there because the
// environment is inherited: a child that happens to be Socrates too - a
// developer's copy started in a session - must not ping for the service.
func WatchdogInterval() (time.Duration, bool) {
	return watchdogInterval(os.Getenv("WATCHDOG_USEC"), os.Getenv("WATCHDOG_PID"), os.Getpid())
}

func watchdogInterval(usec, pid string, self int) (time.Duration, bool) {
	if usec == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(usec, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/int64(time.Microsecond) {
		return 0, false
	}
	if pid != "" {
		p, err := strconv.Atoi(pid)
		if err != nil || p != self {
			return 0, false
		}
	}
	return time.Duration(n) * time.Microsecond, true
}
