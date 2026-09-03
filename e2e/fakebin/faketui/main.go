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
// It also carries the busy/idle signals the activity detector reads, because a
// fake that is only ever idle would leave every layer of that ladder untested:
// Claude's ~/.claude/sessions/<pid>.json, Codex's spinner title, OpenCode's
// event stream and status map, and the screen furniture all three paint. The
// commands that drive them are /busy, /hang, /ask, /nofile and /title.
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
	"sync"
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

	// Socrates asks a CLI two questions before it ever opens a pane: what
	// version it is, and what it can run. Both are plain subprocesses with a
	// pipe for a stdout, so they are answered here and the program exits
	// without ever touching a terminal.
	if code, handled := answerQuery(name, args); handled {
		os.Exit(code)
	}

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
	case strings.HasPrefix(line, "/busy"):
		session.busy(millis(line, "/busy", 3000), true)
		return false, 0
	case strings.HasPrefix(line, "/hang"):
		// The runaway guard's fixture: the furniture of a turn in flight, and
		// then nothing at all - no output, no signal update.
		session.busy(millis(line, "/hang", 20000), false)
		return false, 0
	case line == "/ask":
		session.ask()
		return false, 0
	case line == "/nofile":
		session.dropStatusFile()
		return false, 0
	case strings.HasPrefix(line, "/title"):
		out("\x1b]2;%s\x07", strings.TrimSpace(strings.TrimPrefix(line, "/title")))
		return false, 0
	}
	// A permission prompt takes its answer before anything else does.
	if session.answerAsked(line) {
		return false, 0
	}
	// A real turn is what makes Codex and OpenCode write their state down, so
	// the fake does the same rather than writing it at start-up.
	session.turn()
	out("you said: %s\r\n", line)
	return false, 0
}

// ------------------------------------------------------------ session state

// sessionState is the per-name pretence about a session id, and about what the
// program is doing: the busy/idle detector reads a different signal from each
// of the three, and the fake has to tell all three of them the truth.
type sessionState struct {
	name string
	id   string
	cwd  string
	turn func()

	mu sync.Mutex
	// nofile is /nofile having been given: the Claude status file is gone and
	// is not written again, which is how the suite reaches the layers below it.
	nofile bool
	// asked is a permission prompt on screen, and askID the request id the
	// OpenCode stream named it with.
	asked  bool
	askID  string
	paint  chan struct{}
	events *sseHub
}

func startSession(name string, args []string, cwd string) (*sessionState, error) {
	s := &sessionState{name: name, cwd: cwd, turn: func() {}, events: newSSEHub()}
	var err error
	switch name {
	case "claude":
		err = s.startClaude(args)
	case "codex":
		err = s.startCodex(args)
	case "opencode":
		err = s.startOpenCode(args)
	}
	if err != nil {
		return s, err
	}
	// A harness that has started is idle, and says so before anything is
	// typed: a detector that had to wait for the first turn would have nothing
	// to read while a session is starting up.
	s.setStatus("idle", "")
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
	// authed is the Basic-auth layer the real server puts in front of
	// everything: without OPENCODE_SERVER_PASSWORD there is none at all, and
	// with one an unauthenticated request is a 401 carrying WWW-Authenticate.
	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if password != "" {
				user, pass, ok := r.BasicAuth()
				if !ok || user != username || pass != password {
					w.Header().Set("WWW-Authenticate", `Basic realm="Secure Area"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
			}
			next(w, r)
		}
	}

	mux := http.NewServeMux()
	// The busy/idle detector's two endpoints: the event stream it subscribes
	// to, and the status map it seeds the stream from on every connect.
	mux.HandleFunc("/event", authed(s.serveEvents))
	mux.HandleFunc("/session/status", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.events.snapshot())
	}))
	// The prompts that are open right now, which is what a detector seeds its
	// waiting set from when it (re)connects and re-reads while the stream is
	// up. An empty array when there is none. The `directory` query the real
	// server takes is ignored on purpose: one fake serves exactly one session
	// in one working directory, so there is nothing to filter, and answering
	// whatever is asked keeps the fake from having a second opinion about
	// which directory a prompt belongs to.
	mux.HandleFunc("/permission", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.pendingPermissions())
	}))
	mux.HandleFunc("/session", authed(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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

// outMu serialises writes to the terminal: the busy painter is a goroutine of
// its own, and two Fprintf calls interleaved would tear an escape sequence in
// half.
var outMu sync.Mutex

func out(format string, args ...any) {
	outMu.Lock()
	defer outMu.Unlock()
	fmt.Fprintf(os.Stdout, format, args...)
}

// ------------------------------------------------------------- the queries

// fakeVersion is what every name reports for `--version`. It carries the word
// "fake" on purpose: a version string in a screenshot or a dashboard should
// never be mistaken for a real installation's.
const fakeVersion = "0.0.0-fake"

// answerQuery handles the non-interactive commands: `--version`, which all
// three names answer, and the model listing, whose command and whose output
// differ per CLI. Claude Code has no listing command at all - Socrates ships a
// static list for it - so as claude nothing but the version is answered.
//
// It reports whether it handled the arguments; when it did, the caller exits
// with the status it returns and no pane is ever opened.
func answerQuery(name string, args []string) (int, bool) {
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-V") {
		fmt.Printf("%s %s\n", name, fakeVersion)
		return 0, true
	}
	switch {
	case name == "codex" && len(args) >= 2 && args[0] == "debug" && args[1] == "models":
		return codexModels(), true
	case name == "opencode" && len(args) >= 1 && args[0] == "models":
		return openCodeModels(hasFlag(args, "--json")), true
	}
	return 0, false
}

// codexModels prints the shape `codex debug models` prints: one document with
// a models array, each entry carrying the slug, the display name, the levels
// of reasoning it supports and the one it starts on.
func codexModels() int {
	type level struct {
		Effort string `json:"effort"`
	}
	type entry struct {
		Slug        string  `json:"slug"`
		DisplayName string  `json:"display_name"`
		Description string  `json:"description"`
		Default     string  `json:"default_reasoning_level"`
		Visibility  string  `json:"visibility"`
		Priority    int     `json:"priority"`
		Levels      []level `json:"supported_reasoning_levels"`
	}
	doc := struct {
		Models []entry `json:"models"`
	}{Models: []entry{
		{Slug: "gpt-5.1-codex", DisplayName: "GPT-5.1 Codex", Description: "the fake's default",
			Default: "medium", Visibility: "show", Priority: 1,
			Levels: []level{{"low"}, {"medium"}, {"high"}, {"xhigh"}}},
		{Slug: "gpt-5.1-codex-mini", DisplayName: "GPT-5.1 Codex mini", Description: "the small one",
			Default: "low", Visibility: "show", Priority: 2,
			Levels: []level{{"low"}, {"medium"}, {"high"}}},
		// A hidden entry, because the parser is meant to drop it and nothing
		// else in the suite would notice if it stopped.
		{Slug: "gpt-5-internal", DisplayName: "internal", Default: "medium",
			Visibility: "hide", Priority: 3, Levels: []level{{"medium"}}},
	}}
	raw, err := json.Marshal(doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(string(raw))
	return 0
}

// openCodeModels prints what `opencode models` prints: one provider/model id
// per line. `--json` is answered too, with the array of objects a newer build
// prints, so both halves of the discoverer are reachable from the suite.
func openCodeModels(asJSON bool) int {
	ids := []string{"opencode/big-pickle", "openai/gpt-5-mini", "anthropic/claude-sonnet-4-5"}
	if !asJSON {
		for _, id := range ids {
			fmt.Println(id)
		}
		return 0
	}
	entries := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		provider, model, _ := strings.Cut(id, "/")
		entries = append(entries, map[string]string{"id": model, "providerID": provider, "name": model})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(string(raw))
	return 0
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// ------------------------------------------------------- the activity signals

// Everything below exists so that the busy/idle detector can be tested end to
// end against all three exact layers: Claude's status file, Codex's terminal
// title and OpenCode's event stream, plus the screen furniture each of the
// three paints and the output quiescence all of them have.

// paintEvery is how often a busy harness repaints its spinner line. All three
// real ones repaint several times a second, which is what makes "no bytes for
// thirty seconds" a fact about the program rather than about the terminal.
const paintEvery = 100 * time.Millisecond

// spinners is the Braille frame set Codex animates its title with, and the
// glyph Claude and OpenCode rotate in front of their busy line.
var spinners = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// millis reads the argument of /busy and /hang.
func millis(line, prefix string, fallback int) time.Duration {
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if n, err := strconv.Atoi(rest); err == nil && n > 0 {
		return time.Duration(n) * time.Millisecond
	}
	return time.Duration(fallback) * time.Millisecond
}

// busy runs a turn. With paint it looks like one for its whole length; without
// it - /hang - the furniture is drawn once and then nothing at all is written
// and no signal is updated, which is the only way to reach the runaway guard.
func (s *sessionState) busy(d time.Duration, paint bool) {
	s.stopPaint()
	s.setStatus("busy", "")
	s.paintBusy(0)
	stop := make(chan struct{})
	s.mu.Lock()
	s.paint = stop
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			if s.paint == stop {
				s.paint = nil
			}
			s.mu.Unlock()
		}()
		deadline := time.After(d)
		frame := 0
		for {
			var tick <-chan time.Time
			if paint {
				tick = time.After(paintEvery)
			}
			select {
			case <-stop:
				return
			case <-deadline:
				s.setStatus("idle", "")
				s.paintIdle()
				return
			case <-tick:
				frame++
				s.paintBusy(frame)
			}
		}
	}()
}

func (s *sessionState) stopPaint() {
	s.mu.Lock()
	stop := s.paint
	s.paint = nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// paintBusy writes the bottom line each real TUI writes while it works, with
// the literals the detector's screen layer matches on - including OpenCode's
// `esc interrupt`, which is not the other two's `esc to interrupt`.
func (s *sessionState) paintBusy(frame int) {
	glyph := spinners[frame%len(spinners)]
	switch s.name {
	case "claude":
		out("\r%s Cooking… (%ds · esc to interrupt)  ⏵⏵ auto mode on", glyph, frame/10)
	case "codex":
		out("\r Working (%ds • esc to interrupt)", frame/10)
		// Codex is the one whose title carries the state, spinner and all.
		s.setTitle(glyph + " " + filepath.Base(s.cwd))
	case "opencode":
		out("\r%s Thinking   ⬝⬝⬝⬝⬝⬝■■  esc interrupt      tab agents  ctrl+p commands", glyph)
	default:
		out("\rworking %d", frame)
	}
}

// paintIdle writes the furniture each TUI leaves behind when a turn ends.
func (s *sessionState) paintIdle() {
	switch s.name {
	case "claude":
		out("\r\n✻ Worked for 3s · done\r\n❯ \r\n  ⏵⏵ auto mode on (shift+tab to cycle)\r\n")
	case "codex":
		out("\r\n› Ask Codex to do anything\r\n")
		s.setTitle(filepath.Base(s.cwd))
	case "opencode":
		out("\r\n▣  Build · fake · 3.0s\r\n  ⬝⬝⬝⬝⬝⬝  10.5K (1%%) · $0.02   ctrl+p commands\r\n")
	default:
		out("\r\ndone\r\n")
	}
	out("> ")
}

// ask puts the verbatim permission prompt of this harness on the screen and
// says so through its own signal.
func (s *sessionState) ask() {
	s.stopPaint()
	s.mu.Lock()
	s.asked = true
	s.askID = fmt.Sprintf("per_%d", time.Now().UnixNano())
	askID := s.askID
	s.mu.Unlock()

	s.setStatus("waiting", "permission prompt")
	switch s.name {
	case "claude":
		out("\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. Yes, and don't ask again\r\n   4. No\r\n" +
			" Esc to cancel · Tab to amend\r\n")
	case "codex":
		out("\r\n  Would you like to make the following edits?\r\n\r\n  › 1. Yes, proceed (y)\r\n" +
			"    2. No, and tell Codex what to do differently (esc)\r\n\r\n" +
			"  Press enter to confirm or esc to cancel\r\n")
		s.setTitle("[ . ] Action Required | " + filepath.Base(s.cwd))
	case "opencode":
		// The card OpenCode paints, bottom bar and all: the bar still carries
		// `ctrl+p commands`, which is the idle literal, so a scraper has to
		// read the permission rows first.
		out("\r\n  ┃  △ Permission required\r\n  ┃    # Shell command\r\n" +
			"  ┃  $ echo hello-from-bash\r\n" +
			"  ┃   Allow once   Allow always   Reject" +
			"          ctrl+f fullscreen  ⇆ select  enter confirm\r\n" +
			"  ⬝⬝⬝⬝⬝⬝  10.5K (1%%) · $0.02   ctrl+p commands\r\n")
		s.events.publish(map[string]any{
			"type": "permission.asked",
			"properties": map[string]any{
				"id": askID, "sessionID": s.statusID(),
				"permission": "bash", "patterns": []string{"echo hello"},
			},
		})
	}
}

// pendingPermissions is the prompt this fake is holding, if it is holding one,
// in the shape GET /permission answers with.
func (s *sessionState) pendingPermissions() []map[string]any {
	s.mu.Lock()
	asked, askID := s.asked, s.askID
	s.mu.Unlock()
	if !asked || s.name != "opencode" || askID == "" {
		return []map[string]any{}
	}
	return []map[string]any{{
		"id": askID, "sessionID": s.statusID(), "permission": "bash",
	}}
}

// answerAsked takes the answer to a prompt that is on screen, and reports
// whether the line was one.
func (s *sessionState) answerAsked(line string) bool {
	s.mu.Lock()
	asked, askID := s.asked, s.askID
	s.mu.Unlock()
	if !asked {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "1", "y", "yes":
	default:
		return false
	}
	s.mu.Lock()
	s.asked, s.askID = false, ""
	s.mu.Unlock()
	if s.name == "opencode" {
		s.events.publish(map[string]any{
			"type": "permission.replied",
			"properties": map[string]any{
				"requestID": askID, "sessionID": s.statusID(), "reply": "once",
			},
		})
	}
	s.setStatus("idle", "")
	s.paintIdle()
	return true
}

// setStatus drives whichever exact signal this name is pretending to be.
func (s *sessionState) setStatus(status, waitingFor string) {
	switch s.name {
	case "claude":
		s.writeStatusFile(status, waitingFor)
	case "codex":
		switch status {
		case "busy":
			s.setTitle(spinners[0] + " " + filepath.Base(s.cwd))
		case "waiting":
			s.setTitle("[ . ] Action Required | " + filepath.Base(s.cwd))
		default:
			s.setTitle(filepath.Base(s.cwd))
		}
	case "opencode":
		// A real OpenCode session holding a permission prompt keeps reporting
		// busy until the prompt is answered — the stream and GET
		// /session/status both — and it is that busy that the watcher's
		// session-idle rule reads as "the prompt is still open". Anything
		// else here would have the fake retire its own prompt.
		kind := "idle"
		if status == "busy" || status == "waiting" {
			kind = "busy"
		}
		s.events.setStatus(s.statusID(), kind)
		s.events.publish(map[string]any{
			"type": "session.status",
			"properties": map[string]any{
				"sessionID": s.statusID(),
				"status":    map[string]any{"type": kind},
			},
		})
	}
}

// setTitle is the OSC 2 the real Codex writes, and what /title writes by hand.
func (s *sessionState) setTitle(text string) { out("\x1b]2;%s\x07", text) }

// statusFilePath is where Claude Code keeps the file the detector reads:
// ~/.claude/sessions/<pid>.json, keyed by the process's own pid.
func (s *sessionState) statusFilePath() string {
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions", strconv.Itoa(os.Getpid())+".json")
}

func (s *sessionState) writeStatusFile(status, waitingFor string) {
	s.mu.Lock()
	skip := s.nofile
	s.mu.Unlock()
	if skip {
		return
	}
	path := s.statusFilePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	now := time.Now().UnixMilli()
	record := map[string]any{
		"pid": os.Getpid(), "status": status,
		"updatedAt": now, "statusUpdatedAt": now,
	}
	if waitingFor != "" {
		record["waitingFor"] = waitingFor
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}

// dropStatusFile is /nofile: the exact layer goes away and stays away, which
// is what sends the detector down to the screen and then to quiescence.
func (s *sessionState) dropStatusFile() {
	s.mu.Lock()
	s.nofile = true
	s.mu.Unlock()
	if path := s.statusFilePath(); path != "" {
		_ = os.Remove(path)
	}
	out("status file dropped\r\n")
}

// statusID is the session id the OpenCode stream reports activity under.
func (s *sessionState) statusID() string {
	if s.id != "" {
		return s.id
	}
	return "ses_fakestatus"
}

// ------------------------------------------------------------ the SSE server

// sseHub is the fake OpenCode event stream: the subscribers, and the busy set
// a reconnect is seeded from through GET /session/status.
type sseHub struct {
	mu     sync.Mutex
	subs   map[chan string]bool
	status map[string]string
}

func newSSEHub() *sseHub {
	return &sseHub{subs: map[chan string]bool{}, status: map[string]string{}}
}

func (h *sseHub) setStatus(id, kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if kind == "busy" || kind == "retry" {
		h.status[id] = kind
		return
	}
	delete(h.status, id)
}

// snapshot is what GET /session/status answers with: the sessions that are
// working, an idle one simply being absent from the map.
func (h *sseHub) snapshot() map[string]map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]map[string]string{}
	for id, kind := range h.status {
		out[id] = map[string]string{"type": kind}
	}
	return out
}

func (h *sseHub) publish(event map[string]any) {
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub <- string(raw):
		default:
			// A reader that has stopped reading loses events rather than
			// stopping the pane.
		}
	}
}

func (h *sseHub) subscribe() chan string {
	ch := make(chan string, 32)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch
}

func (h *sseHub) unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// serveEvents is GET /event: the stream the real TUI's server serves, with the
// same heartbeat and the same `data: ` framing.
func (s *sessionState) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming here", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
	flusher.Flush()

	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, "data: {\"type\":\"server.heartbeat\",\"properties\":{}}\n\n")
			flusher.Flush()
		}
	}
}
