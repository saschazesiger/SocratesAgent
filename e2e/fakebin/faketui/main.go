// faketui is the fake CLI the browser suite runs instead of Claude Code,
// Codex and OpenCode. One program, installed under three names, and which name
// it was started as decides how it pretends to keep a session id.
//
// It is not a protocol speaker, because nothing in Socrates speaks a protocol
// any more: it is a terminal program in a tmux pane, and what the suite needs
// from it is that it behaves like one. In particular it tells the truth about
// the two things the launchers are most likely to get wrong -
//
//   - where each CLI writes the state that a resume depends on, and
//   - how each CLI refuses: a reused Claude session id, an unknown OpenCode
//     session, an unauthenticated request to OpenCode's server -
//
// because those paths are unreachable behind a fake that only echoes.
//
// It also answers the white-background question end to end: on start it writes
// the OSC 10 and OSC 11 queries its real counterpart writes, and its banner
// says which background it was told about. Only OSC 11 decides that. It must
// never wait for a `CSI ? 996 n` answer: tmux 3.3a, which is what the Docker
// base image ships, does not send one.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// probeWindow is how long the terminal gets to answer the colour queries. The
// answer comes from tmux itself, out of the window style, with nobody
// attached, so it is there in microseconds; the window is generous only so
// that a loaded machine does not report a false "unknown".
const probeWindow = 300 * time.Millisecond

func main() {
	name := filepath.Base(os.Args[0])
	args := os.Args[1:]

	restore := rawMode()
	defer restore()

	input := readStdin()
	theme := probeTheme(input)

	cwd, _ := os.Getwd()
	// Codex takes its working root as -C, OpenCode as the positional project
	// path that comes after every flag. Both are read here, so that the fake's
	// idea of where it is running is the launcher's and not the pane's.
	if dir := valueOf(args, "-C"); dir != "" {
		cwd = dir
	}
	if len(args) > 0 && name == "opencode" {
		if last := args[len(args)-1]; filepath.IsAbs(last) {
			cwd = last
		}
	}
	model := valueOf(args, "--model")
	if model == "" {
		model = valueOf(args, "-m")
	}

	logLaunch(name, args, cwd)

	session, err := startSession(name, args, cwd)
	if err != nil {
		out("%s\r\n", err)
		restore()
		os.Exit(1)
	}

	out("FAKE %s theme=%s cwd=%s model=%s argv=%d\r\n", name, theme, cwd, model, len(args))
	out("> ")

	line := make([]byte, 0, 256)
	for chunk := range input {
		for _, b := range chunk {
			switch b {
			case '\r', '\n':
				out("\r\n")
				if done, code := handle(string(line), session); done {
					restore()
					os.Exit(code)
				}
				line = line[:0]
				out("> ")
			case 0x7f, 0x08: // backspace
				if len(line) > 0 {
					line = line[:len(line)-1]
					out("\b \b")
				}
			case 0x03: // Ctrl-C
				out("^C\r\n")
				restore()
				os.Exit(130)
			case 0x04: // Ctrl-D
				restore()
				os.Exit(0)
			default:
				if b >= 0x20 {
					line = append(line, b)
					out("%c", b)
				}
			}
		}
	}
	restore()
}

// handle runs one line of input and reports whether the program should exit.
func handle(line string, session *sessionState) (bool, int) {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "/exit"):
		code := 0
		if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "/exit"))); err == nil {
			code = n
		}
		return true, code
	case line == "/spin":
		// Two hundred lines as fast as they can be written, which is what the
		// replay ring and the backpressure path are measured against.
		for i := 1; i <= 200; i++ {
			out("spin %d\r\n", i)
		}
		return false, 0
	case line == "/alt":
		out("\x1b[?1049h\x1b[H+----------------+\r\n|  ALTERNATE     |\r\n+----------------+\r\n")
		return false, 0
	case line == "/id":
		out("session %s\r\n", session.id)
		return false, 0
	}
	// A real turn is what makes Codex and OpenCode write their state down, so
	// the fake does the same rather than writing it at start-up.
	session.turn()
	out("you said: %s\r\n", line)
	return false, 0
}

// ------------------------------------------------------------ session state

// sessionState is the per-name pretence about a session id.
type sessionState struct {
	name string
	id   string
	cwd  string
	turn func()
}

func startSession(name string, args []string, cwd string) (*sessionState, error) {
	s := &sessionState{name: name, cwd: cwd, turn: func() {}}
	switch name {
	case "claude":
		return s, s.startClaude(args)
	case "codex":
		return s, s.startCodex(args)
	case "opencode":
		return s, s.startOpenCode(args)
	}
	return s, nil
}

// startClaude mirrors the one refusal that matters: a session id may be used
// once. The real binary answers `Error: Session ID <id> is already in use.`
// and stops, which is what a restart that reused --session-id would hit.
func (s *sessionState) startClaude(args []string) error {
	dir := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "fake")
	if resume := valueOf(args, "--resume"); resume != "" {
		s.id = resume
		if _, err := os.Stat(filepath.Join(dir, resume+".jsonl")); err != nil {
			return fmt.Errorf("Error: No conversation found with session ID: %s", resume)
		}
		return nil
	}
	id := valueOf(args, "--session-id")
	if id == "" {
		id = uuid.NewString()
	}
	s.id = id
	path := filepath.Join(dir, id+".jsonl")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("Error: Session ID %s is already in use.", id)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	record, err := json.Marshal(map[string]any{"type": "mode", "sessionId": id, "cwd": s.cwd})
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(record, '\n'), 0o644)
}

// startCodex writes nothing yet. Verified against the real thing: neither a
// rollout file nor an index row exists until a user turn has happened, and a
// watcher that was allowed to assume otherwise would be a watcher the suite
// never exercised.
func (s *sessionState) startCodex(args []string) error {
	s.id = uuid.NewString()
	if len(args) > 0 && args[0] == "resume" && len(args) > 1 {
		s.id = args[1]
	}
	s.turn = func() {
		s.turn = func() {}
		if err := writeRollout(s.id, s.cwd); err != nil {
			out("rollout failed: %v\r\n", err)
		}
	}
	return nil
}

func writeRollout(id, cwd string) error {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".codex")
	}
	now := time.Now()
	dir := filepath.Join(home, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("rollout-%s-%s.jsonl", now.Format("2006-01-02T15-04-05"), id))
	meta, err := json.Marshal(map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"session_id":  id,
			"id":          id,
			"timestamp":   now.Format(time.RFC3339),
			"cwd":         cwd,
			"originator":  os.Getenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE"),
			"cli_version": "0.152.0-fake",
		},
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(meta, '\n'), 0o644); err != nil {
		return err
	}
	return writeThreadsRow(filepath.Join(home, "state_5.sqlite"), id, path, cwd)
}

func writeThreadsRow(dbPath, id, rollout, cwd string) error {
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS threads (
		id TEXT PRIMARY KEY, rollout_path TEXT, created_at INTEGER,
		updated_at INTEGER, cwd TEXT, has_user_event INTEGER)`); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	_, err = db.Exec(`INSERT OR REPLACE INTO threads
		(id, rollout_path, created_at, updated_at, cwd, has_user_event) VALUES (?,?,?,?,?,1)`,
		id, rollout, now, now, cwd)
	return err
}

// startOpenCode serves the same HTTP surface the real TUI serves on its
// --port, with the same Basic-auth layer: without OPENCODE_SERVER_PASSWORD
// there is no authentication at all, and with one an unauthenticated request
// is a 401 carrying a WWW-Authenticate header.
func (s *sessionState) startOpenCode(args []string) error {
	if want := valueOf(args, "--session"); want != "" {
		known, err := openCodeHasSession(want)
		if err != nil || !known {
			return fmt.Errorf("Error: Session not found: %s", want)
		}
		s.id = want
	}
	port, err := strconv.Atoi(valueOf(args, "--port"))
	if err != nil || port <= 0 {
		return fmt.Errorf("Error: --port is required by this fake, got %q", valueOf(args, "--port"))
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	password := os.Getenv("OPENCODE_SERVER_PASSWORD")
	username := os.Getenv("OPENCODE_SERVER_USERNAME")
	if username == "" {
		username = "opencode"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		if password != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != username || pass != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="Secure Area"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		if r.Method == http.MethodPost {
			id := newOpenCodeID()
			dir := r.URL.Query().Get("directory")
			if dir == "" {
				dir = s.cwd
			}
			if err := recordOpenCodeSession(id, dir); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"id": id, "directory": dir})
			return
		}
		sessions, err := openCodeSessions()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, sessions)
	})
	go func() { _ = http.Serve(ln, mux) }()

	// As with Codex, a session exists once there has been a turn.
	s.turn = func() {
		s.turn = func() {}
		if s.id == "" {
			s.id = newOpenCodeID()
		}
		if err := recordOpenCodeSession(s.id, s.cwd); err != nil {
			out("session record failed: %v\r\n", err)
		}
	}
	return nil
}

// newOpenCodeID mints a descending id, the way OpenCode's own generator does:
// a newer session sorts before an older one, which is the trap the discoverer
// has to avoid.
func newOpenCodeID() string {
	descending := int64(1) << 42
	descending -= time.Now().UnixMilli()
	return fmt.Sprintf("ses_%012x%s", descending, strings.ReplaceAll(uuid.NewString(), "-", "")[:12])
}

func openCodeDB() (*sql.DB, error) {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	dir := filepath.Join(data, "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "opencode.db"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS session (
		id TEXT PRIMARY KEY, directory TEXT, time_created INTEGER, time_updated INTEGER)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func recordOpenCodeSession(id, dir string) error {
	db, err := openCodeDB()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UnixMilli()
	_, err = db.Exec(`INSERT OR REPLACE INTO session (id, directory, time_created, time_updated)
		VALUES (?,?,?,?)`, id, dir, now, now)
	return err
}

func openCodeHasSession(id string) (bool, error) {
	db, err := openCodeDB()
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	var one int
	err = db.QueryRow("SELECT 1 FROM session WHERE id = ?", id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func openCodeSessions() ([]map[string]any, error) {
	db, err := openCodeDB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT id, directory, time_created, time_updated FROM session")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []map[string]any{}
	for rows.Next() {
		var id, dir string
		var created, updated int64
		if err := rows.Scan(&id, &dir, &created, &updated); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "directory": dir,
			"time": map[string]any{"created": created, "updated": updated},
		})
	}
	return out, rows.Err()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ------------------------------------------------------------ the terminal

// rawMode turns the line discipline off through stty, so that an OSC answer -
// which carries no newline - is delivered instead of sitting in the kernel's
// line buffer until the user presses Enter. stty rather than an ioctl because
// this program is a test fixture and one subprocess costs nothing.
func rawMode() func() {
	read := exec.Command("stty", "-g")
	read.Stdin = os.Stdin
	saved, err := read.Output()
	if err != nil {
		return func() {}
	}
	set := exec.Command("stty", "raw", "-echo")
	set.Stdin = os.Stdin
	if err := set.Run(); err != nil {
		return func() {}
	}
	return func() {
		restore := exec.Command("stty", strings.TrimSpace(string(saved)))
		restore.Stdin = os.Stdin
		_ = restore.Run()
	}
}

// readStdin pumps standard input into a channel. It is one goroutine for the
// life of the program: the colour probe reads the first few hundred
// milliseconds of it, and the line loop reads the rest.
func readStdin() <-chan []byte {
	ch := make(chan []byte, 16)
	go func() {
		defer close(ch)
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				ch <- chunk
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// probeTheme asks the terminal what its colours are and decides light from
// dark out of the OSC 11 answer alone.
func probeTheme(input <-chan []byte) string {
	out("\x1b]10;?\x1b\\\x1b]11;?\x1b\\")
	deadline := time.After(probeWindow)
	seen := ""
	for {
		select {
		case chunk, ok := <-input:
			if !ok {
				return themeOf(seen)
			}
			seen += string(chunk)
			if theme := themeOf(seen); theme != "unknown" {
				return theme
			}
		case <-deadline:
			return themeOf(seen)
		}
	}
}

// themeOf reads the background out of an OSC 11 reply. The reply is
// `ESC ] 11 ; rgb:RRRR/GGGG/BBBB` followed by ST or BEL, and anything at least
// half bright is a light background.
func themeOf(seen string) string {
	at := strings.Index(seen, "]11;rgb:")
	if at < 0 {
		return "unknown"
	}
	rest := seen[at+len("]11;rgb:"):]
	end := strings.IndexAny(rest, "\x1b\x07")
	if end < 0 {
		return "unknown"
	}
	parts := strings.Split(rest[:end], "/")
	if len(parts) != 3 {
		return "unknown"
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseUint(p, 16, 64)
		if err != nil {
			return "unknown"
		}
		// The components come back with as many hex digits as the terminal
		// felt like using, so each is scaled by its own width.
		total += float64(v) / float64(uint64(1)<<(4*len(p))-1)
	}
	if total/3 >= 0.5 {
		return "light"
	}
	return "dark"
}

// ------------------------------------------------------------ small helpers

// logLaunch appends this launch to $FAKE_LOG as one JSON object, so a scenario
// can assert the exact flags the launcher built without reading a screen.
func logLaunch(name string, args []string, cwd string) {
	path := os.Getenv("FAKE_LOG")
	if path == "" {
		return
	}
	env := map[string]string{}
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		env[key] = value
	}
	record, err := json.Marshal(map[string]any{
		"name": name, "argv": args, "cwd": cwd, "pid": os.Getpid(),
		"at": time.Now().UnixMilli(), "env": env,
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(record, '\n'))
}

// valueOf returns the argument after a flag, and "" when the flag is absent.
// It also understands --flag=value, which is how a person types it by hand.
func valueOf(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if value, ok := strings.CutPrefix(a, flag+"="); ok {
			return value
		}
	}
	return ""
}

func out(format string, args ...any) { fmt.Fprintf(os.Stdout, format, args...) }
