//go:build !windows

package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The byte range of SQLite's SHARED lock on a database file (os_unix.c,
// SHARED_FIRST and SHARED_SIZE): every connection to a database in WAL
// mode holds it for as long as it is open.
const (
	sharedFirst = 0x40000000 + 2
	sharedSize  = 510
)

func init() { lockProbe = probeLock }

// probeLock is the child process of lockedElsewhere: it says whether
// another process holds a lock on path's SHARED range.
func probeLock(path string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer f.Close()
	lk := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: sharedFirst, Len: sharedSize}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		fmt.Println("error:", err)
		return
	}
	if lk.Type == syscall.F_UNLCK {
		fmt.Println("free")
	} else {
		fmt.Println("locked")
	}
}

// lockedElsewhere asks another process whether this one holds its lock on
// path: POSIX locks belong to a process, so only another one sees them.
func lockedElsewhere(t *testing.T, path string) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lockProbeEnv+"="+path)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the probe: %v", err)
	}
	answer := strings.TrimSpace(string(out))
	if answer != "locked" && answer != "free" {
		t.Fatalf("the probe says %q", answer)
	}
	return answer == "locked"
}

// Looking at whether a file is a database must not cost a database this
// process has open its locks. Closing any file descriptor drops every POSIX
// lock the process holds on the file, so a raw open and close of the
// server's own database left it unlocked after every backup run, and
// another program's connection to it then took itself for the last one and
// deleted the write-ahead log the server was still writing into.
func TestLookingAtADatabaseKeepsItsLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	if !lockedElsewhere(t, path) {
		t.Fatal("the open database holds no lock to begin with")
	}
	if !isSQLite(path) {
		t.Fatal("the database is not taken for one")
	}
	if !lockedElsewhere(t, path) {
		t.Fatal("looking at the database dropped its locks")
	}
	for _, s := range []struct{ name, text string }{{"empty", ""}, {"text", strings.Repeat("not a database\n", 20)}} {
		p := filepath.Join(t.TempDir(), s.name)
		if err := os.WriteFile(p, []byte(s.text), 0o600); err != nil {
			t.Fatal(err)
		}
		if isSQLite(p) {
			t.Errorf("the %s file is taken for a database", s.name)
		}
	}
}
