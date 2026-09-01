package agenthost

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

// SweepLegacyTerminals ends the terminal hosts of the previous version.
//
// Someone who upgrades in place has `socrates term-host` processes running
// interactive agent TUIs under <dataDir>/terminals/ - a directory the new
// Manager.Restore never visits. They would keep running forever, detached and
// invisible, so they are closed once here, in the one place that still knows
// the old wire format, and then the directory is gone. This function is
// deleted in the release after next.
func SweepLegacyTerminals(dataDir string) {
	root := filepath.Join(dataDir, "terminals")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	closed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if closeLegacyTerminal(filepath.Join(root, entry.Name(), "sock")) {
			closed++
		}
	}
	_ = os.RemoveAll(root)
	if closed > 0 {
		log.Printf("agents: closed %d terminal session(s) left over from the previous version", closed)
	}
}

// closeLegacyTerminal speaks the old protocol's close op - one line, id 1 -
// and waits briefly for any answer or for the socket to close. Failures are
// ignored: the directory goes either way, and a host whose socket is already
// dead has nothing left to end.
func closeLegacyTerminal(sock string) bool {
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(map[string]any{"id": 1, "op": "close"}); err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	_, _ = conn.Read(buf)
	return true
}
