package agenthost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxSocketPath is the shortest of the limits Socrates runs against: sun_path
// is 104 bytes on macOS and BSD, 108 on Linux, and the four bytes of headroom
// are for the ".tmp" a future rename might want.
const maxSocketPath = 100

// SocketPath returns a short, stable path for a host's socket. It deliberately
// does not live in the host directory: a data directory the user chose can be
// long enough on its own to blow the ~104 byte sun_path limit, and that
// failure shows up only as an opaque listen error.
//
//	$XDG_RUNTIME_DIR/socrates/<id>.sock     when XDG_RUNTIME_DIR is set
//	$TMPDIR/socrates-<uid>/<id>.sock        otherwise
//
// Both are created 0700. The chosen path is written into spec.json, and the
// host places a symlink named "sock" in its directory so a human looking at
// the directory can still find it.
func SocketPath(id string) (string, error) {
	dir, err := socketDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the socket directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, id+".sock")
	if len(path) > maxSocketPath {
		return "", fmt.Errorf("the socket path %s is %d bytes, and a unix socket may not be longer than %d - "+
			"set TMPDIR to somewhere shorter and start Socrates again", path, len(path), maxSocketPath)
	}
	return path, nil
}

func socketDir() (string, error) {
	if run := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); run != "" {
		return filepath.Join(run, "socrates"), nil
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("socrates-%d", os.Getuid())), nil
}
