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
// commands that drive them are /busy, /hang, /ask (and /asktwice), /nofile and
// /title.
//
// And it asks questions the way the real programs ask them, because the answer
// operator drives a menu with keystrokes and a fake that only took whole lines
// could never tell it whether a digit, an arrow or an Esc did what it meant:
// /question opens Claude Code's AskUserQuestion menu (its plan approval, or a
// permission prompt whose last row declines with words of one's own) and
// switches the input from lines to keys while it is open, and /textq ends a
// turn on a question in prose. Every answer is written to FAKE_LOG. /tasks
// works through a task list, for the outcome of a turn.
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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// probeWindow is how long the terminal gets to answer the colour queries. The
// answer comes from tmux itself, out of the window style, with nobody
// attached, so it is there in microseconds; the window is generous only so
// that a loaded machine does not report a false "unknown".
const probeWindow = 300 * time.Millisecond

// bootDelay is how long the fake takes to paint its banner, the way every
// real CLI takes a moment: they are Node programs and spend a few hundred
// milliseconds loading. Socrates attaches a pane's journal with its own tmux
// command just after new-session, so what a pane prints in its first
// milliseconds is on the screen but not in the journal. No real CLI is that
// fast, and a fake that was - tmux answers the colour queries itself, in
// microseconds - lost its banner from the journal, which is where half the
// suite reads it. The wait is after the colour probe, which must not hold
// keystrokes (it reads stdin for the answer), and after the launch is logged,
// so FAKE_LOG is as early as it always was; what is typed meanwhile waits in
// the input channel for the prompt.
const bootDelay = 250 * time.Millisecond

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

	// The first prompt, when the session was prepared with one, comes after
	// every option: everything from "--" on is operands, and the flag
	// lookups below must not mistake a prompt that mentions -m for a model.
	prompt, hasPrompt := initialPrompt(name, args)
	if i := indexOf(args, "--"); i >= 0 {
		args = args[:i]
	}

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

	logLaunch(name, os.Args[1:], cwd, prompt, hasPrompt)

	session, err := startSession(name, args, cwd)
	if err != nil {
		out("%s\r\n", err)
		restore()
		os.Exit(1)
	}

	time.Sleep(bootDelay)
	out("FAKE %s theme=%s cwd=%s model=%s argv=%d\r\n", name, theme, cwd, model, len(os.Args)-1)
	if hasPrompt {
		// One line, whatever the prompt holds: a suite asserts on it with a
		// screen capture, and a fifty kilobyte prompt painted in full would
		// scroll the banner away.
		first, _, _ := strings.Cut(prompt, "\n")
		out("prompt: %s (%d bytes)\r\n", first, len(prompt))
	}
	out("> ")

	// Outside a question the input is a line editor; while a question menu
	// is open every byte is a key, decoded the way a real TUI decodes it.
	// Which of the two a byte goes to is decided byte by byte, because
	// "/question\r1" can arrive as one chunk and its last byte already belongs
	// to the menu the line before it opened. An escape sequence - an arrow,
	// Home, End - is decoded either way, and goes to whichever is open once
	// it is whole.
	var ed lineEditor
	var keys keyDecoder
	var escapeTimeout <-chan time.Time
	press := func(pressed []key) {
		for _, k := range pressed {
			if !session.inQuestion() {
				ed.key(k)
				continue
			}
			if done, code := session.questionKey(k); done {
				restore()
				os.Exit(code)
			}
		}
	}
	for {
		var chunk []byte
		select {
		case c, ok := <-input:
			if !ok {
				restore()
				return
			}
			chunk = c
		case <-escapeTimeout:
			// Nothing followed the ESC, so it was the Esc key and not the
			// start of an arrow.
			escapeTimeout = nil
			press(keys.flush())
			continue
		}
		escapeTimeout = nil
		for _, b := range chunk {
			if session.inQuestion() || keys.pending() || b == 0x1b {
				press(keys.feed(b))
				continue
			}
			switch b {
			case '\r', '\n':
				out("\r\n")
				if done, code := handle(ed.submit(), session); done {
					restore()
					os.Exit(code)
				}
				// A question menu is the last thing on the screen, as it is
				// in the real programs, and has no line prompt under it.
				if !session.inQuestion() {
					out("> ")
				}
			case 0x7f, 0x08: // backspace
				ed.key(key{kind: keyBackspace})
			case 0x03: // Ctrl-C
				out("^C\r\n")
				restore()
				os.Exit(130)
			case 0x04: // Ctrl-D
				restore()
				os.Exit(0)
			default:
				if b >= 0x20 {
					ed.key(key{kind: keyText, b: b})
				}
			}
		}
		if keys.pending() {
			escapeTimeout = time.After(escapeWait)
		}
	}
}

// lineEditor is the prompt's line, edited the way the real programs' prompts
// edit it: Left, Right, Home and End move along it, BSpace and Delete take a
// character out where the cursor is, and Up brings back the line sent before
// it - in Claude Code a "!" shell command as well as any other - which Enter
// then sends again. Down goes forward again, to an empty line at the end.
type lineEditor struct {
	buf     []byte
	pos     int
	history []string
	back    int
	// part is the first bytes of a UTF-8 character still waiting for the
	// rest: a key is one byte, and the character goes into the line and onto
	// the screen only once it is whole, as a terminal draws it.
	part []byte
}

// submit is Enter: the line, which is kept for Up, and an empty prompt.
func (e *lineEditor) submit() string {
	e.part = nil
	line := string(e.buf)
	if line != "" {
		e.history = append(e.history, line)
	}
	e.buf, e.pos, e.back = e.buf[:0], 0, len(e.history)
	return line
}

// cursorBack moves the terminal's cursor back over n characters.
func cursorBack(n int) {
	if n > 0 {
		out("\x1b[%dD", n)
	}
}

// replace puts other text on the line, with the cursor at its end.
func (e *lineEditor) replace(text string) {
	cursorBack(utf8.RuneCount(e.buf[:e.pos]))
	e.buf = append(e.buf[:0], text...)
	e.pos = len(e.buf)
	out("%s\x1b[K", text)
}

func (e *lineEditor) key(k key) {
	switch k.kind {
	case keyText:
		e.part = append(e.part, k.b)
		if !utf8.FullRune(e.part) {
			return
		}
		char := e.part
		e.part = nil
		e.buf = slices.Insert(e.buf, e.pos, char...)
		e.pos += len(char)
		out("%s", char)
		if tail := e.buf[e.pos:]; len(tail) > 0 {
			out("%s", tail)
			cursorBack(utf8.RuneCount(tail))
		}
	case keyBackspace:
		if e.pos == 0 {
			return
		}
		_, size := utf8.DecodeLastRune(e.buf[:e.pos])
		e.buf = slices.Delete(e.buf, e.pos-size, e.pos)
		e.pos -= size
		tail := e.buf[e.pos:]
		out("\b%s \b", tail)
		cursorBack(utf8.RuneCount(tail))
	case keyDelete:
		if e.pos == len(e.buf) {
			return
		}
		_, size := utf8.DecodeRune(e.buf[e.pos:])
		e.buf = slices.Delete(e.buf, e.pos, e.pos+size)
		tail := e.buf[e.pos:]
		out("%s ", tail)
		cursorBack(utf8.RuneCount(tail) + 1)
	case keyLeft:
		if e.pos > 0 {
			_, size := utf8.DecodeLastRune(e.buf[:e.pos])
			e.pos -= size
			out("\b")
		}
	case keyRight:
		if e.pos < len(e.buf) {
			_, size := utf8.DecodeRune(e.buf[e.pos:])
			out("%s", e.buf[e.pos:e.pos+size])
			e.pos += size
		}
	case keyHome:
		cursorBack(utf8.RuneCount(e.buf[:e.pos]))
		e.pos = 0
	case keyEnd:
		out("%s", e.buf[e.pos:])
		e.pos = len(e.buf)
	case keyUp:
		if e.back > 0 {
			e.back--
			e.replace(e.history[e.back])
		}
	case keyDown:
		if e.back < len(e.history) {
			e.back++
			next := ""
			if e.back < len(e.history) {
				next = e.history[e.back]
			}
			e.replace(next)
		}
	}
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
	case strings.HasPrefix(line, "/bg"):
		// A turn that ends with something it started still running behind
		// it, for that long - and then what each real program does when it
		// stops.
		session.background(millis(line, "/bg", 6000))
		return false, 0
	case line == "/ask" || strings.HasPrefix(line, "/ask "):
		session.ask(millis(line, "/ask", 0))
		return false, 0
	case line == "/asktwice":
		// Parallel tool calls: the same permission prompt again the moment
		// the first one is answered, with no turn in between.
		session.mu.Lock()
		session.askAgain = 1
		session.mu.Unlock()
		session.ask(0)
		return false, 0
	case line == "/question" || strings.HasPrefix(line, "/question "):
		session.askQuestion(strings.TrimSpace(strings.TrimPrefix(line, "/question")))
		return false, 0
	case line == "/textq":
		session.askInText()
		return false, 0
	case line == "/tasks" || strings.HasPrefix(line, "/tasks "):
		session.workOnTasks(strings.TrimSpace(strings.TrimPrefix(line, "/tasks")))
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
	// A question asked in prose is answered by the next line typed, which is
	// otherwise an ordinary turn: the real programs read it as one too, and
	// go to work on it.
	answered := session.answerInText(line) || session.answerFeedback(line)
	// A real turn is what makes Codex and OpenCode write their state down, so
	// the fake does the same rather than writing it at start-up.
	session.turn()
	out("you said: %s\r\n", line)
	if answered {
		session.busy(proseWork, true)
	}
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
	// taskIDs is the task list /tasks made, and which of its tasks are
	// completed: the list is the session's, kept from turn to turn.
	taskIDs []bool
	// nofile is /nofile having been given: the Claude status file is gone and
	// is not written again, which is how the suite reaches the layers below it.
	nofile bool
	// asked is a permission prompt on screen, and askID the request id the
	// OpenCode stream named it with.
	asked bool
	askID string
	// askWork is how long the program works on the answer to /ask <ms>
	// before it is idle again: real Claude Code goes on with the same turn
	// for minutes after a permission is granted. Zero is idle at once.
	askWork time.Duration
	// askAgain is how many more times the prompt is asked again the moment
	// it is answered (/asktwice).
	askAgain int
	paint    chan struct{}
	events   *sseHub
	// children is the background sub-agents /bg started under OpenCode, by
	// id, with the session that started them.
	children map[string]string
	// question is the menu /question opened, while it is open, and askedInText
	// is /textq having ended a turn on a question in prose that the next line
	// answers.
	question    *question
	askedInText bool
	// feedback is the permission prompt declined through its last row,
	// whose words the next line typed is.
	feedback *question
	// transcript is Claude's conversation file, which a question is written
	// into the way the real program writes it; "" under the other two names.
	transcript string
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
		s.transcript = filepath.Join(dir, resume+".jsonl")
		if _, err := os.Stat(s.transcript); err != nil {
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
	s.transcript = path
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
	// The question tool's three routes: what is being asked, and the two
	// ways a client settles it without a keystroke. The directory query is
	// ignored for the reason it is ignored on /permission.
	mux.HandleFunc("/question", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.pendingQuestions())
	}))
	mux.HandleFunc("/question/", authed(s.serveQuestionReply))
	// Session details carry the running cost in current OpenCode, which is the
	// value Socrates mirrors into the header. A fixed non-zero number makes the
	// browser scenario prove both the authenticated endpoint and its rendering.
	mux.HandleFunc("/session/", authed(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/session/")
		// A background sub-agent says whose it is, which is how the watcher
		// tells one it was never introduced to from a session of its own.
		if parent, ok := s.childOf(id); ok {
			writeJSON(w, map[string]any{"id": id, "parentID": parent, "directory": s.cwd})
			return
		}
		exists, err := openCodeHasSession(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !exists {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"id": id, "directory": s.cwd, "cost": 1.25})
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
func logLaunch(name string, args []string, cwd, prompt string, hasPrompt bool) {
	path := os.Getenv("FAKE_LOG")
	if path == "" {
		return
	}
	env := map[string]string{}
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		env[key] = value
	}
	fields := map[string]any{
		"name": name, "argv": args, "cwd": cwd, "pid": os.Getpid(),
		"at": time.Now().UnixMilli(), "env": env,
	}
	if hasPrompt {
		// The whole prompt, so a test can compare it byte for byte with what
		// was typed into the draft.
		fields["prompt"] = prompt
		fields["prompt_bytes"] = len(prompt)
	}
	appendLog(fields)
}

// appendLog writes one JSON object as one line of $FAKE_LOG, and nothing at
// all without one.
func appendLog(fields map[string]any) {
	path := os.Getenv("FAKE_LOG")
	if path == "" {
		return
	}
	record, err := json.Marshal(fields)
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

// initialPrompt is the first prompt the launcher handed over, in the form each
// real CLI takes it: Claude Code and Codex as the positional argument after
// "--", OpenCode as --prompt=<text>.
func initialPrompt(name string, args []string) (string, bool) {
	if name == "opencode" {
		for _, a := range args {
			if value, ok := strings.CutPrefix(a, "--prompt="); ok {
				return value, true
			}
		}
		if value := valueOf(args, "--prompt"); value != "" {
			return value, true
		}
		return "", false
	}
	if i := indexOf(args, "--"); i >= 0 && i+1 < len(args) {
		return args[i+1], true
	}
	return "", false
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
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
	s.busyThen(d, paint, func() {
		s.setStatus("idle", "")
		s.paintIdle()
	})
}

// busyThen is a turn that ends some other way than idle: a plan that was
// sent back is followed by the same dialog again, not by the prompt.
func (s *sessionState) busyThen(d time.Duration, paint bool, then func()) {
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
				then()
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

// paintIdle writes the furniture each TUI leaves behind when a turn ends, and
// the fake's own line prompt under it.
func (s *sessionState) paintIdle() {
	s.idleFurniture()
	out("> ")
}

// idleFurniture is paintIdle without the line prompt, for a turn that ends
// inside handle, whose caller writes that prompt itself.
func (s *sessionState) idleFurniture() {
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
}

// background is /bg: the turn is over and something it started goes on for d,
// said the way each real program says it (Claude Code 2.1.281, Codex 0.158.0,
// OpenCode 1.18.32 with background sub-agents). When it ends, Claude and
// OpenCode take the turn up again to report on it; Codex only takes its line
// away.
func (s *sessionState) background(d time.Duration) {
	s.stopPaint()
	child := s.statusID() + "_bg"
	switch s.name {
	case "claude":
		s.writeStatusFile("shell", "")
		out("\r\n✻ Worked for 3s · done · 1 shell still running\r\n❯ \r\n" +
			"  ⏵⏵ auto mode on · 1 shell · ← for agents · ↓ to manage\r\n")
	case "codex":
		s.setTitle(filepath.Base(s.cwd))
		out("\r\n• OK\r\n  1 background terminal running · /ps to view · /stop to close\r\n" +
			"› Ask Codex to do anything\r\n")
	case "opencode":
		s.mu.Lock()
		if s.children == nil {
			s.children = map[string]string{}
		}
		s.children[child] = s.statusID()
		s.mu.Unlock()
		s.events.publish(map[string]any{
			"type": "session.created",
			"properties": map[string]any{
				"sessionID": child,
				"info":      map[string]any{"id": child, "parentID": s.statusID()},
			},
		})
		s.setStatus("idle", "")
		s.events.setStatus(child, "busy")
		s.events.publish(map[string]any{
			"type":       "session.status",
			"properties": map[string]any{"sessionID": child, "status": map[string]any{"type": "busy"}},
		})
		out("\r\n  ⠹ General Task (background) — Sleep\r\n▣  Build · fake · 3.0s\r\n" +
			"  ⬝⬝⬝⬝⬝⬝  10.5K (1%%) · $0.02   ctrl+p commands\r\n")
	default:
		return
	}
	go func() {
		time.Sleep(d)
		switch s.name {
		case "codex":
			// The line goes, by a repaint, and Codex stays idle.
			out("\x1b[2J\x1b[H• OK\r\n› Ask Codex to do anything\r\n")
		case "opencode":
			s.events.setStatus(child, "idle")
			s.events.publish(map[string]any{
				"type":       "session.status",
				"properties": map[string]any{"sessionID": child, "status": map[string]any{"type": "idle"}},
			})
			s.busy(800*time.Millisecond, true)
		default:
			out("\x1b[2J\x1b[H● Background command completed (exit code 0)\r\n")
			s.busy(800*time.Millisecond, true)
		}
	}()
}

// childOf is the parent of a background sub-agent this fake started, for GET
// /session/:id.
func (s *sessionState) childOf(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.children[id]
	return parent, ok
}

// ask puts the verbatim permission prompt of this harness on the screen and
// says so through its own signal.
func (s *sessionState) ask(work time.Duration) {
	s.stopPaint()
	s.mu.Lock()
	s.asked = true
	s.askWork = work
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
	if q := s.question; q != nil && q.kind == "permit" {
		asked, askID = true, q.id
	}
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
	work := s.askWork
	s.askWork = 0
	again := s.askAgain > 0
	if again {
		s.askAgain--
	}
	s.mu.Unlock()
	if s.name == "opencode" {
		s.events.publish(map[string]any{
			"type": "permission.replied",
			"properties": map[string]any{
				"requestID": askID, "sessionID": s.statusID(), "reply": "once",
			},
		})
	}
	if again {
		s.ask(0)
		return true
	}
	if work > 0 {
		// /ask <ms>: the turn the permission was asked in goes on.
		s.busy(work, true)
		return true
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
// $CLAUDE_CONFIG_DIR/sessions/<pid>.json, or ~/.claude/sessions/<pid>.json
// without one, keyed by the process's own pid.
func (s *sessionState) statusFilePath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "sessions", strconv.Itoa(os.Getpid())+".json")
	}
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

// ------------------------------------------------------- the question menus

// Everything below is /question and /textq: the menus a coding agent asks a
// person with, painted the way Claude Code 2.1.303 paints them and driven by
// the same keys, so that the answer operator can be tested against a pane
// that holds it to what the real menu would have done with each keystroke.
//
// Under Claude every kind is there: one single-select question, one
// multi-select question, two questions on tabs with a review tab, and the
// plan approval. Codex and OpenCode ask the single question only, under the
// signal each of them waits with: Codex's Action Required title, and
// OpenCode's question tool on its event stream and HTTP surface.

// escapeWait is how long an ESC waits for the rest of an escape sequence
// before it counts as the Esc key. tmux writes the three bytes of an arrow in
// one go, so they nearly always arrive in one read; the wait only has to cover
// a read that split them, and stays far below the gap between two keys that a
// person or the operator leaves.
const escapeWait = 50 * time.Millisecond

// answerWork is how long the fake works on an answer before it is idle again:
// long enough for the detector, which looks once a second, to commit the turn
// the answer started - as it commits the model call a real one starts - and
// short enough that no scenario waits on it. A turn shorter than a tick is
// seen or not by luck, and an answer submitted at the machine is told from a
// question declined there by that turn.
const answerWork = 1500 * time.Millisecond

// proseWork is the turn an answer to a question asked in prose starts: a turn
// like any other, long enough that the detector commits it, as it commits a
// real one. The line typed is echoed at once; only the turn after it says
// that it was taken.
const proseWork = 1500 * time.Millisecond

// planWork is Claude planning again after a plan was sent back with
// feedback: a model call, which takes seconds, so the detector commits the
// turn it is - as it does a real one - before the revised plan asks again.
// A revision that came back inside one detector tick would be a prompt
// changed in place with the state never leaving waiting, which the real
// program never does.
const planWork = 2 * time.Second

// keyKind is what one key press is, once its bytes are decoded.
type keyKind int

const (
	keyText keyKind = iota // one printable byte, possibly part of a UTF-8 rune
	keyEnter
	keyBackspace
	keyUp
	keyDown
	keyLeft
	keyRight
	keyTab
	keyBackTab
	keyEscape
	keyInterrupt
	keyEOF
	keyHome
	keyEnd
	keyDelete
)

type key struct {
	kind keyKind
	b    byte
}

// keyDecoder turns terminal bytes into keys: printable bytes, the control
// codes a menu reacts to, and the CSI and SS3 sequences of the arrows and of
// Shift+Tab. An ESC with nothing after it yet is held until either the next
// byte arrives or the main loop stops waiting and calls flush.
type keyDecoder struct{ seq []byte }

func (d *keyDecoder) pending() bool { return len(d.seq) > 0 }

// flush is the wait for a follow-up byte running out: what was held was the
// Esc key.
func (d *keyDecoder) flush() []key {
	if len(d.seq) == 0 {
		return nil
	}
	d.seq = nil
	return []key{{kind: keyEscape}}
}

func (d *keyDecoder) feed(b byte) []key {
	switch len(d.seq) {
	case 0:
		if b == 0x1b {
			d.seq = append(d.seq, b)
			return nil
		}
		return plainKey(b)
	case 1:
		if b == '[' || b == 'O' {
			d.seq = append(d.seq, b)
			return nil
		}
		// ESC and then something that starts no sequence: the Esc key, and
		// then that something as a key of its own.
		d.seq = nil
		return append([]key{{kind: keyEscape}}, d.feed(b)...)
	}
	// Parameters, which is how a modified arrow comes: ESC [ 1 ; 2 A - and,
	// first after the bracket, the private marker of a mouse report
	// (ESC [ < 0 ; 3 ; 4 M), which is swallowed whole like any other.
	private := len(d.seq) == 2 && d.seq[1] == '[' && (b == '<' || b == '>' || b == '?' || b == '=')
	if (b >= '0' && b <= '9') || b == ';' || private {
		d.seq = append(d.seq, b)
		return nil
	}
	params := string(d.seq[2:])
	d.seq = nil
	switch b {
	case 'H':
		return []key{{kind: keyHome}}
	case 'F':
		return []key{{kind: keyEnd}}
	case '~':
		// Home, End and Delete the way tmux's own terminal sends them.
		switch params {
		case "1", "7":
			return []key{{kind: keyHome}}
		case "4", "8":
			return []key{{kind: keyEnd}}
		case "3":
			return []key{{kind: keyDelete}}
		}
		return nil
	case 'A':
		return []key{{kind: keyUp}}
	case 'B':
		return []key{{kind: keyDown}}
	case 'C':
		return []key{{kind: keyRight}}
	case 'D':
		return []key{{kind: keyLeft}}
	case 'Z':
		return []key{{kind: keyBackTab}}
	}
	// Any other sequence - a function key, a focus report - is swallowed
	// whole, which is what a TUI does with a key it has no use for.
	return nil
}

func plainKey(b byte) []key {
	switch b {
	case '\r', '\n':
		return []key{{kind: keyEnter}}
	case 0x7f, 0x08:
		return []key{{kind: keyBackspace}}
	case '\t':
		return []key{{kind: keyTab}}
	case 0x03:
		return []key{{kind: keyInterrupt}}
	case 0x04:
		return []key{{kind: keyEOF}}
	}
	if b >= 0x20 {
		return []key{{kind: keyText, b: b}}
	}
	return nil
}

// digit is the option number a key names, 1 to 9.
func digit(b byte) (int, bool) {
	if b >= '1' && b <= '9' {
		return int(b - '0'), true
	}
	return 0, false
}

type questionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type questionItem struct {
	Header  string
	Text    string
	Multi   bool
	Options []questionOption
}

// The questions are the ones the real screens were captured with, so that a
// fake screen can be laid beside a real one and differ only where the real
// one is not text.
var (
	databaseQuestion = questionItem{Header: "DB", Text: "Which database should we use?", Options: []questionOption{
		{"Postgres", "Relational, robust"},
		{"SQLite", "Embedded file"},
	}}
	// cacheQuestion is databaseQuestion asked about the cache: /question cache.
	cacheQuestion = questionItem{Header: "Cache", Text: "Which database should the cache use?", Options: databaseQuestion.Options}
	colorQuestion = questionItem{Header: "Color", Text: "Which color should the button be?", Options: []questionOption{
		{"Red", "Red button"},
		{"Green", "Green button"},
		{"Blue", "Blue button"},
	}}
	featuresQuestion = questionItem{Header: "Features", Text: "Which features do you want?", Multi: true, Options: []questionOption{
		{"Dark mode", "Add a dark color theme"},
		{"Search", "Add search functionality"},
		{"Export", "Add export functionality"},
	}}
	// /question yesno: two tabs whose options are the same two labels, which
	// only the question above each menu tells apart.
	testsQuestion = questionItem{Header: "Tests", Text: "Run the tests?", Options: []questionOption{
		{"Yes", "Run the whole suite"},
		{"No", "Skip them"},
	}}
	pushQuestion = questionItem{Header: "Push", Text: "Push to main?", Options: []questionOption{
		{"Yes", "Push the branch"},
		{"No", "Keep it local"},
	}}
)

// The plan approval as Claude Code shows it in auto mode, which is the mode
// Socrates runs it in.
const (
	planText    = "Create hello.txt in the working directory containing the single line hi."
	planPrompt  = "Claude has written up a plan and is ready to execute. Would you like to proceed?"
	feedbackRow = 2
)

var planOptions = []string{"Yes, and use auto mode", "Yes, manually approve edits", "Tell Claude what to change"}

// textQuestion is what /textq ends its turn with.
const textQuestion = "Which color should the button be? Red, green or blue?"

// rejectedTool is how Claude Code's transcript begins every refusal of a tool
// call, the declined question and the plan sent back alike.
const rejectedTool = "The user doesn't want to proceed with this tool use. The tool use was rejected " +
	"(eg. if it was a file edit, the new_string was NOT written to the file)."

// outcome is what one key did to a menu.
type outcome int

const (
	outcomeNone    outcome = iota // nothing visible changed
	outcomeRepaint                // the menu changed and is drawn again
	outcomeAnswered
	outcomeDeclined
	outcomeRevise // a plan sent back with feedback, which Claude plans again
	// outcomeFeedback is a permission prompt declined through its last row,
	// "No, and tell Claude what to do differently (esc)": the prompt closes
	// and the words are typed at the program's own prompt after it.
	outcomeFeedback
)

// question is one open menu and everything the keys have done to it so far.
type question struct {
	kind  string // single, multi, multiq, plan or permit
	id    string // the tool_use id in Claude's transcript, the request id on OpenCode's stream
	items []questionItem
	// harness is the program that asks it, which a permission prompt is
	// drawn and keyed by.
	harness string
	// chat is whether the menu ends in a "Chat about this" row, which only
	// Claude Code's does.
	chat bool
	// again is how many more times the menu is asked again, the same, the
	// moment it is answered (/question permit2).
	again int
	// host is the host a Claude network permission prompt asks about, and
	// nextHost the one it asks about when it is asked again: /question
	// permit2b asks for example1.com after example.com, a prompt that differs
	// from the one answered by a digit alone.
	host, nextHost string
	// tab is the item on screen, and len(items) the review tab of a menu with
	// several. cursor is the highlighted row of every tab, the review tab's
	// last; the plan approval uses the first.
	tab    int
	cursor []int
	// picked is the option a single-select item was answered with: -1 for
	// none yet and len(Options) for its free text. checked is a multi-select
	// item's ticks, its free-text row last. free is what was typed into the
	// "Type something." row.
	picked   []int
	checked  [][]bool
	free     []string
	answered []bool
	// toChat is the menu having been left through "Chat about this".
	toChat bool
	// The plan approval: the plan as it stands, how often it was sent back,
	// the row it was settled with, and the feedback typed into the third row.
	plan      string
	revisions int
	planRow   int
	feedback  string
}

func newQuestion(kind string, items []questionItem, chat bool) *question {
	q := &question{kind: kind, items: items, chat: chat, cursor: make([]int, len(items)+1)}
	for _, item := range items {
		q.picked = append(q.picked, -1)
		q.checked = append(q.checked, make([]bool, len(item.Options)+1))
		q.free = append(q.free, "")
		q.answered = append(q.answered, false)
	}
	if kind == "plan" {
		q.plan = planText
	}
	return q
}

// The rows of item i, top to bottom: its options, "Type something.", for a
// multi-select item the unnumbered "Submit", and "Chat about this". The
// numbers on screen are the options' 1 to n, n+1 for the free text and n+2 for
// the chat row; Submit has none.
func (q *question) freeRow(i int) int { return len(q.items[i].Options) }

func (q *question) submitRow(i int) int {
	if q.items[i].Multi {
		return q.freeRow(i) + 1
	}
	return -1
}

func (q *question) chatRow(i int) int {
	if !q.chat {
		return -1
	}
	return q.rowCount(i) - 1
}

func (q *question) rowCount(i int) int {
	n := q.freeRow(i) + 1
	if q.items[i].Multi {
		n++
	}
	if q.chat {
		n++
	}
	return n
}

// press applies one key to the menu.
func (q *question) press(k key) outcome {
	switch {
	case q.kind == "permit" && q.harness == "opencode":
		return q.pressSides(k)
	case k.kind == keyEscape && q.kind == "permit":
		return outcomeFeedback
	case k.kind == keyEscape:
		return outcomeDeclined
	case q.kind == "permit":
		return q.pressPermit(k)
	case q.kind == "plan":
		return q.pressPlan(k)
	case q.tab == len(q.items):
		return q.pressReview(k)
	}
	return q.pressItem(k)
}

// pressItem is a key on a question tab, with the rules verified against the
// real menu: a digit chooses a single-select option at once and toggles a
// multi-select one; the free-text row's digit moves the cursor there in a
// single-select item and only ticks it in a multi-select one; on that row
// every printable byte, digits too, is text; anywhere else a letter is lost.
func (q *question) pressItem(k key) outcome {
	i := q.tab
	item := q.items[i]
	free := q.freeRow(i)
	cursor := &q.cursor[i]
	switch k.kind {
	case keyText:
		if *cursor == free {
			q.free[i] += string([]byte{k.b})
			if item.Multi {
				q.checked[i][free] = true
			}
			return outcomeRepaint
		}
		n, ok := digit(k.b)
		switch {
		case !ok:
			return outcomeNone
		case n <= len(item.Options):
			return q.choose(i, n-1)
		case n == free+1:
			if item.Multi {
				q.checked[i][free] = !q.checked[i][free]
			} else {
				*cursor = free
			}
			return outcomeRepaint
		case q.chat && n == free+2:
			q.toChat = true
			return outcomeDeclined
		}
		return outcomeNone
	case keyBackspace:
		if *cursor != free || q.free[i] == "" {
			return outcomeNone
		}
		_, size := utf8.DecodeLastRuneInString(q.free[i])
		q.free[i] = q.free[i][:len(q.free[i])-size]
		return outcomeRepaint
	case keyUp:
		if *cursor == 0 {
			return outcomeNone
		}
		*cursor--
		return outcomeRepaint
	case keyDown:
		if *cursor == q.rowCount(i)-1 {
			return outcomeNone
		}
		*cursor++
		return outcomeRepaint
	case keyTab, keyRight:
		return q.moveTab(1)
	case keyBackTab, keyLeft:
		return q.moveTab(-1)
	case keyEnter:
		switch *cursor {
		case free:
			if q.free[i] == "" {
				return outcomeNone
			}
			if item.Multi {
				q.checked[i][free] = true
				return outcomeRepaint
			}
			q.picked[i] = free
			return q.advance()
		case q.submitRow(i):
			return q.advance()
		case q.chatRow(i):
			q.toChat = true
			return outcomeDeclined
		}
		return q.choose(i, *cursor)
	}
	return outcomeNone
}

func (q *question) choose(i, option int) outcome {
	if q.items[i].Multi {
		q.checked[i][option] = !q.checked[i][option]
		return outcomeRepaint
	}
	q.picked[i] = option
	return q.advance()
}

// advance is the item on screen answered: the whole menu when it is the only
// one, and otherwise the next tab, which after the last item is the review.
func (q *question) advance() outcome {
	q.answered[q.tab] = true
	if len(q.items) == 1 {
		return outcomeAnswered
	}
	q.tab++
	return outcomeRepaint
}

func (q *question) moveTab(delta int) outcome {
	if len(q.items) < 2 {
		return outcomeNone
	}
	next := min(max(q.tab+delta, 0), len(q.items))
	if next == q.tab {
		return outcomeNone
	}
	q.tab = next
	return outcomeRepaint
}

// pressReview is a key on the review tab: 1 submits every answer, 2 cancels
// the whole menu.
func (q *question) pressReview(k key) outcome {
	cursor := &q.cursor[len(q.items)]
	switch k.kind {
	case keyText:
		switch k.b {
		case '1':
			return outcomeAnswered
		case '2':
			return outcomeDeclined
		}
	case keyUp, keyDown:
		*cursor = 1 - *cursor
		return outcomeRepaint
	case keyBackTab, keyLeft:
		return q.moveTab(-1)
	case keyEnter:
		if *cursor == 0 {
			return outcomeAnswered
		}
		return outcomeDeclined
	}
	return outcomeNone
}

// pressPlan is a key on the plan approval: 1 and 2 approve, 3 moves to "Tell
// Claude what to change", where the text typed replaces the label and Enter
// sends the plan back with it, or Shift+Tab approves with it.
func (q *question) pressPlan(k key) outcome {
	cursor := &q.cursor[0]
	switch k.kind {
	case keyText:
		if *cursor == feedbackRow {
			q.feedback += string([]byte{k.b})
			return outcomeRepaint
		}
		switch k.b {
		case '1', '2':
			q.planRow = int(k.b - '1')
			return outcomeAnswered
		case '3':
			*cursor = feedbackRow
			return outcomeRepaint
		}
	case keyBackspace:
		if *cursor != feedbackRow || q.feedback == "" {
			return outcomeNone
		}
		_, size := utf8.DecodeLastRuneInString(q.feedback)
		q.feedback = q.feedback[:len(q.feedback)-size]
		return outcomeRepaint
	case keyUp:
		if *cursor == 0 {
			return outcomeNone
		}
		*cursor--
		return outcomeRepaint
	case keyDown:
		if *cursor == feedbackRow {
			return outcomeNone
		}
		*cursor++
		return outcomeRepaint
	case keyBackTab:
		if *cursor == feedbackRow && q.feedback != "" {
			q.planRow = feedbackRow
			return outcomeAnswered
		}
	case keyEnter:
		if *cursor < feedbackRow {
			q.planRow = *cursor
			return outcomeAnswered
		}
		if q.feedback != "" {
			q.planRow = feedbackRow
			return outcomeRevise
		}
	}
	return outcomeNone
}

// pressPermit is a key on a permission prompt: an option's digit takes it at
// once - and Codex's "y" its first - the last row's digit, its Enter or Esc
// declines with words to follow, and the arrows move the cursor.
func (q *question) pressPermit(k key) outcome {
	cursor := &q.cursor[0]
	options := len(q.items[0].Options)
	switch k.kind {
	case keyText:
		if n, ok := digit(k.b); ok {
			switch {
			case n >= 1 && n <= options:
				q.picked[0] = n - 1
				return outcomeAnswered
			case n == options+1:
				return outcomeFeedback
			}
		}
		if q.harness == "codex" && k.b == 'y' {
			q.picked[0] = 0
			return outcomeAnswered
		}
	case keyUp:
		if *cursor == 0 {
			return outcomeNone
		}
		*cursor--
		return outcomeRepaint
	case keyDown:
		if *cursor == options {
			return outcomeNone
		}
		*cursor++
		return outcomeRepaint
	case keyEnter:
		if *cursor < options {
			q.picked[0] = *cursor
			return outcomeAnswered
		}
		return outcomeFeedback
	}
	return outcomeNone
}

// revise is Claude having written the plan again after it was sent back: the
// same dialog, with the change in the plan and the cursor back on top.
func (q *question) revise() {
	q.revisions++
	q.plan += "\nChanged as asked: " + q.feedback
	q.feedback = ""
	q.planRow = 0
	q.cursor[0] = 0
}

// answer is what one item was answered with: the options chosen, and the
// text of one's own, in the shape FAKE_LOG records it.
type answer struct {
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
	Text     string   `json:"text"`
}

// line is the answer as the real menu writes it back: the options, and the
// free text after them, joined with commas.
func (a answer) line() string {
	parts := append([]string{}, a.Choices...)
	if a.Text != "" {
		parts = append(parts, a.Text)
	}
	return strings.Join(parts, ", ")
}

func (q *question) answers() []answer {
	if q.kind == "plan" {
		a := answer{Question: planPrompt, Choices: []string{planOptions[q.planRow]}}
		if q.planRow == feedbackRow {
			a.Text = q.feedback
		}
		return []answer{a}
	}
	answers := make([]answer, 0, len(q.items))
	for i, item := range q.items {
		a := answer{Question: item.Text, Choices: []string{}}
		free := len(item.Options)
		if item.Multi {
			for j, option := range item.Options {
				if q.checked[i][j] {
					a.Choices = append(a.Choices, option.Label)
				}
			}
			if q.checked[i][free] {
				a.Text = q.free[i]
			}
		} else if p := q.picked[i]; p >= 0 && p < free {
			a.Choices = append(a.Choices, item.Options[p].Label)
		} else if p == free {
			a.Text = q.free[i]
		}
		answers = append(answers, a)
	}
	return answers
}

// applyReply is OpenCode's POST /question/:id/reply: one list of labels per
// item, where a label that is none of the options is the answer of one's own.
func (q *question) applyReply(labels [][]string) {
	for i := range q.items {
		if i >= len(labels) {
			break
		}
		free := len(q.items[i].Options)
		for _, label := range labels[i] {
			option := slices.IndexFunc(q.items[i].Options, func(o questionOption) bool { return o.Label == label })
			if option < 0 {
				option = free
				q.free[i] = label
			}
			if q.items[i].Multi {
				q.checked[i][option] = true
			} else {
				q.picked[i] = option
			}
		}
		q.answered[i] = true
	}
}

// --- the screens

// pointer is the margin of one row: the cursor's mark when the cursor is on it.
func pointer(mark string, row, cursor int) string {
	if row == cursor {
		return mark
	}
	return strings.Repeat(" ", utf8.RuneCountInString(mark))
}

func screenOf(lines []string) string { return strings.Join(lines, "\r\n") + "\r\n" }

// claudeScreen is the menu verbatim as Claude Code 2.1.303 draws it: one
// question with its header chip, several on tabs with a review tab, or the plan
// approval.
func (q *question) claudeScreen() string {
	if q.kind == "plan" {
		return q.claudePlanScreen()
	}
	tabbed := len(q.items) > 1
	var lines []string
	if tabbed {
		lines = append(lines, strings.Repeat("─", 80), q.tabStrip(), "")
	} else {
		lines = append(lines, " ☐ "+q.items[0].Header, "")
	}
	if q.tab == len(q.items) {
		return screenOf(append(lines, q.reviewLines()...))
	}
	i := q.tab
	item := q.items[i]
	cursor := q.cursor[i]
	free := q.freeRow(i)
	box := func(on bool) string {
		if on {
			return "[✔] "
		}
		return "[ ] "
	}
	lines = append(lines, item.Text, "")
	for j, option := range item.Options {
		if item.Multi {
			lines = append(lines, fmt.Sprintf("%s%d. %s%s", pointer("❯ ", j, cursor), j+1, box(q.checked[i][j]), option.Label),
				"         "+option.Description)
			continue
		}
		lines = append(lines, fmt.Sprintf("%s%d. %s", pointer("❯ ", j, cursor), j+1, option.Label),
			"     "+option.Description)
	}
	label := "Type something."
	if q.free[i] != "" {
		label = q.free[i]
	}
	if item.Multi {
		label = box(q.checked[i][free]) + label
	}
	lines = append(lines, fmt.Sprintf("%s%d. %s", pointer("❯ ", free, cursor), free+1, label))
	if item.Multi {
		lines = append(lines, pointer("❯ ", q.submitRow(i), cursor)+"   Submit")
	}
	lines = append(lines, strings.Repeat("─", 26),
		fmt.Sprintf("%s%d. Chat about this", pointer("❯ ", q.chatRow(i), cursor), free+2), "")
	if tabbed {
		lines = append(lines, "Enter to select · Tab/Arrow keys to navigate · Esc to cancel")
	} else {
		lines = append(lines, "Enter to select · ↑/↓ to navigate · Esc to cancel")
	}
	return screenOf(lines)
}

// tabStrip is the row of tabs over a menu of several questions. A tab with an
// answer is ticked, and the one on screen is in reverse video, which is the
// real menu's highlight as near as a plain terminal gets to it.
func (q *question) tabStrip() string {
	chip := func(tab int, text string) string {
		if tab == q.tab {
			return "\x1b[7m" + text + "\x1b[27m"
		}
		return text
	}
	chips := []string{"←"}
	for i, item := range q.items {
		mark := "☐"
		if q.answered[i] || slices.Contains(q.checked[i], true) {
			mark = "☒"
		}
		chips = append(chips, chip(i, mark+" "+item.Header))
	}
	chips = append(chips, chip(len(q.items), "✔ Submit"), "→")
	return strings.Join(chips, "  ")
}

func (q *question) reviewLines() []string {
	lines := []string{"Review your answers", ""}
	complete := true
	for i, a := range q.answers() {
		if !q.answered[i] && a.line() == "" {
			complete = false
			continue
		}
		lines = append(lines, " ● "+a.Question, "   → "+a.line())
	}
	if !complete {
		lines = append(lines, "", "You have not answered all questions")
	}
	cursor := q.cursor[len(q.items)]
	return append(lines, "", "Ready to submit your answers?", "",
		pointer("❯ ", 0, cursor)+"1. Submit answers",
		pointer("❯ ", 1, cursor)+"2. Cancel")
}

func (q *question) claudePlanScreen() string {
	rule := "  " + strings.Repeat("─", 44)
	dashes := "  " + strings.Repeat("╌", 42)
	lines := []string{rule, "   Ready to code?", "", "   Here is Claude's plan:", dashes, "   Plan", ""}
	for _, text := range strings.Split(q.plan, "\n") {
		lines = append(lines, "   "+text)
	}
	lines = append(lines, dashes, "", "", rule, "   "+planPrompt, "")
	for j, label := range planOptions {
		if j == feedbackRow && q.feedback != "" {
			label = q.feedback
		}
		lines = append(lines, fmt.Sprintf("   %s%d. %s", pointer("❯ ", j, q.cursor[0]), j+1, label))
	}
	return screenOf(append(lines, "        shift+tab to approve with this feedback"))
}

// codexScreen is a numbered menu under Codex's own footer, the one its
// approval prompts carry and the screen layer reads as waiting.
func (q *question) codexScreen() string {
	item, cursor, free := q.items[0], q.cursor[0], q.freeRow(0)
	lines := []string{"  " + item.Text, ""}
	for j, option := range item.Options {
		lines = append(lines, fmt.Sprintf("  %s%d. %s  %s", pointer("› ", j, cursor), j+1, option.Label, option.Description))
	}
	lines = append(lines, fmt.Sprintf("  %s%d. %s", pointer("› ", free, cursor), free+1, freeLabel(q.free[0])),
		"", "  Press enter to confirm or esc to cancel")
	return screenOf(lines)
}

// highlight is how OpenCode marks the row or the option the cursor is on: a
// background colour and nothing else, so that a screen read as text - which
// is how the server reads it - shows no cursor at all.
func highlight(text string, on bool) string {
	if on {
		return "\x1b[48;5;238m" + text + "\x1b[49m"
	}
	return text
}

// openCodeScreen is OpenCode's question card, with its bottom bar under it.
// Like the real one it numbers its rows and marks the highlighted one in
// colour only.
func (q *question) openCodeScreen() string {
	item, cursor, free := q.items[0], q.cursor[0], q.freeRow(0)
	lines := []string{"  ┃  △ Question", "  ┃    # " + item.Header, "  ┃  " + item.Text}
	for j, option := range item.Options {
		lines = append(lines, "  ┃  "+highlight(fmt.Sprintf("%d. %s  %s", j+1, option.Label, option.Description), j == cursor))
	}
	lines = append(lines, "  ┃  "+highlight(fmt.Sprintf("%d. %s", free+1, freeLabel(q.free[0])), free == cursor),
		"  ┃   ↑↓ select  enter submit  esc dismiss",
		"  ⬝⬝⬝⬝⬝⬝  10.5K (1%) · $0.02   ctrl+p commands")
	return screenOf(lines)
}

// openCodePermit is OpenCode's permission card for a shell command: its
// options side by side, the highlight on one of them in colour only.
var openCodePermit = questionItem{Text: "Permission required", Options: []questionOption{
	{Label: "Allow once"}, {Label: "Allow always"}, {Label: "Reject"},
}}

// openCodePermitScreen is that card as OpenCode paints it, bottom bar and all.
func (q *question) openCodePermitScreen() string {
	options := make([]string, 0, len(q.items[0].Options))
	for j, option := range q.items[0].Options {
		options = append(options, highlight(option.Label, j == q.cursor[0]))
	}
	return screenOf([]string{
		"  ┃  △ Permission required", "  ┃    # Shell command", "  ┃  $ echo hello-from-bash",
		"  ┃   " + strings.Join(options, "   ") + "          ctrl+f fullscreen  ⇆ select  enter confirm",
		"  ⬝⬝⬝⬝⬝⬝  10.5K (1%) · $0.02   ctrl+p commands",
	})
}

// pressSides is a key on OpenCode's permission card: Left and Right (and
// Tab) move the highlight, which stops at either end, Enter takes the option
// it is on, and Escape rejects.
func (q *question) pressSides(k key) outcome {
	cursor := &q.cursor[0]
	last := len(q.items[0].Options) - 1
	switch k.kind {
	case keyLeft, keyBackTab:
		if *cursor == 0 {
			return outcomeNone
		}
		*cursor--
		return outcomeRepaint
	case keyRight, keyTab:
		if *cursor == last {
			return outcomeNone
		}
		*cursor++
		return outcomeRepaint
	case keyEnter:
		q.picked[0] = *cursor
		return outcomeAnswered
	case keyEscape:
		q.picked[0] = last
		return outcomeAnswered
	}
	return outcomeNone
}

// permitReply is the reply OpenCode's stream says a permission card was
// settled with.
func (q *question) permitReply() string {
	switch q.picked[0] {
	case 0:
		return "once"
	case 1:
		return "always"
	}
	return "reject"
}

// The permission prompts whose last row declines with words of one's own: a
// network request out of Claude Code's sandbox, as 2.1.303 asks it, and a
// command Codex wants to run.
var (
	claudePermit = questionItem{Text: "Do you want to allow this connection?", Options: []questionOption{
		{Label: "Yes"}, {Label: "Yes, and don't ask again for example.com"},
	}}
	codexPermit = questionItem{Text: "Would you like to run the following command?", Options: []questionOption{
		{Label: "Yes, proceed"},
	}}
)

// feedbackLabel is the last row of a permission prompt.
func feedbackLabel(harness string) string {
	if harness == "codex" {
		return "No, and tell Codex what to do differently"
	}
	return "No, and tell Claude what to do differently"
}

// permitScreen is a permission prompt with its decline-with-words row last.
func (q *question) permitScreen() string {
	item, cursor := q.items[0], q.cursor[0]
	if q.harness == "codex" {
		lines := []string{"  " + item.Text, "", "  $ curl https://example.com", ""}
		for j, option := range item.Options {
			lines = append(lines, fmt.Sprintf("  %s%d. %s (y)", pointer("› ", j, cursor), j+1, option.Label))
		}
		n := len(item.Options)
		lines = append(lines, fmt.Sprintf("  %s%d. %s (esc)", pointer("› ", n, cursor), n+1, feedbackLabel(q.harness)),
			"", "  Press enter to confirm or esc to cancel")
		return screenOf(lines)
	}
	host := q.host
	if host == "" {
		host = "example.com"
	}
	lines := []string{" Network request outside of sandbox", " Host: " + host, "", " " + item.Text}
	for j, option := range item.Options {
		lines = append(lines, fmt.Sprintf(" %s%d. %s", pointer("❯ ", j, cursor), j+1, option.Label))
	}
	n := len(item.Options)
	return screenOf(append(lines, fmt.Sprintf(" %s%d. %s (esc)", pointer("❯ ", n, cursor), n+1, feedbackLabel(q.harness))))
}

func freeLabel(text string) string {
	if text == "" {
		return "Type something."
	}
	return text
}

// --- the session's side of a question

// askQuestion is /question [single|cache|multi|multiq|yesno|plan|permit|permit2|permit2b].
// cache is the single question asked about something else: the same options
// under other words, a question that differs from /question's on the screen
// and in the transcript's record only by what it asks.
// permit2 is the permission prompt asked once more the moment it is
// answered, the same on the screen: a menu that takes its digit at once,
// followed by the next of parallel tool calls.
// permit2b is permit2 with the second prompt for example1.com, which a digit
// "1" echoed into the first one's "example.com" would read as.
// FAKE_REPAINT_MS makes every repaint of an open menu that long late.
func (s *sessionState) askQuestion(kind string) {
	if kind == "" {
		kind = "single"
	}
	again, nextHost := 0, ""
	switch kind {
	case "permit2":
		kind, again = "permit", 1
	case "permit2b":
		kind, again, nextHost = "permit", 1, "example1.com"
	}
	var items []questionItem
	if kind == "cache" {
		kind = "single"
		items = []questionItem{cacheQuestion}
	}
	switch kind {
	case "single":
		if items == nil {
			items = []questionItem{databaseQuestion}
		}
	case "multi":
		items = []questionItem{featuresQuestion}
	case "multiq":
		items = []questionItem{colorQuestion, featuresQuestion}
	case "yesno":
		items = []questionItem{testsQuestion, pushQuestion}
	case "plan":
	case "permit":
		switch s.name {
		case "codex":
			items = []questionItem{codexPermit}
		case "opencode":
			items = []questionItem{openCodePermit}
		default:
			items = []questionItem{claudePermit}
		}
	default:
		out("unknown question kind %q: single, cache, multi, multiq, yesno, plan, permit, permit2 or permit2b\r\n", kind)
		return
	}
	switch {
	case s.name == "opencode" && kind != "single" && kind != "permit":
		out("%s asks a single question or a permission only\r\n", s.name)
		return
	case s.name == "codex" && kind != "single" && kind != "permit":
		out("%s asks a single question only\r\n", s.name)
		return
	}
	s.stopPaint()
	q := newQuestion(kind, items, s.name != "codex" && s.name != "opencode" && kind != "permit")
	q.harness, q.again, q.nextHost = s.name, again, nextHost
	s.showQuestion(q)
}

// showQuestion puts a menu on the screen and says so through the harness's
// own signal. The transcript record is written first and the status last, so
// that whatever reacts to the status finds the screen and the record there
// already.
func (s *sessionState) showQuestion(q *question) {
	s.mu.Lock()
	q.id = s.newQuestionID(q)
	s.question = q
	screen := s.questionScreen(q)
	s.mu.Unlock()

	s.recordQuestion(q)
	s.paintQuestion(screen)
	switch {
	case s.name == "opencode" && q.kind == "permit":
		s.events.publish(map[string]any{
			"type": "permission.asked",
			"properties": map[string]any{
				"id": q.id, "sessionID": s.statusID(),
				"permission": "bash", "patterns": []string{"echo hello-from-bash"},
			},
		})
	case s.name == "opencode":
		s.events.publish(map[string]any{"type": "question.asked", "properties": s.questionRequest(q)})
	}
	note := "input needed"
	if q.kind == "plan" || q.kind == "permit" {
		note = "permission prompt"
	}
	s.setStatus("waiting", note)
}

func (s *sessionState) newQuestionID(q *question) string {
	tail := strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if s.name == "opencode" && q.kind == "permit" {
		return "per_" + tail
	}
	if s.name == "opencode" {
		return "que_" + tail
	}
	return "toolu_fake" + tail
}

func (s *sessionState) questionScreen(q *question) string {
	if q.kind == "permit" && s.name == "opencode" {
		return q.openCodePermitScreen()
	}
	if q.kind == "permit" {
		return q.permitScreen()
	}
	switch s.name {
	case "codex":
		return q.codexScreen()
	case "opencode":
		return q.openCodeScreen()
	}
	return q.claudeScreen()
}

// paintQuestion draws a menu from the top of a cleared screen. Every repaint
// is a whole one: drawn below the last, a menu taller than a narrow pane
// would wrap and a redraw counted in lines would land in the wrong place.
func (s *sessionState) paintQuestion(screen string) {
	out("\x1b[2J\x1b[H%s", screen)
}

func (s *sessionState) inQuestion() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.question != nil
}

// questionKey is one key while a menu is open, and reports whether the program
// should exit: Ctrl-C and Ctrl-D still end it, as they do outside a menu.
func (s *sessionState) questionKey(k key) (bool, int) {
	switch k.kind {
	case keyInterrupt:
		out("^C\r\n")
		return true, 130
	case keyEOF:
		return true, 0
	}
	s.mu.Lock()
	q := s.question
	if q == nil {
		s.mu.Unlock()
		return false, 0
	}
	result := q.press(k)
	screen := ""
	if result == outcomeRepaint {
		screen = s.questionScreen(q)
	}
	s.mu.Unlock()
	switch result {
	case outcomeNone:
	case outcomeRepaint:
		if ms, _ := strconv.Atoi(os.Getenv("FAKE_REPAINT_MS")); ms > 0 {
			// A TUI that repaints late, as one under load does: the key was
			// taken, and the screen shows it a while later.
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		s.paintQuestion(screen)
	default:
		s.settle(q, result, "keys")
	}
	return false, 0
}

// settle closes a menu the way it was closed - answered, declined, or for a
// plan sent back with feedback - by keys or, under OpenCode, over HTTP. A menu
// that is no longer the open one was settled already and is left alone.
func (s *sessionState) settle(q *question, result outcome, via string) {
	s.mu.Lock()
	if s.question != q {
		s.mu.Unlock()
		return
	}
	s.question = nil
	s.mu.Unlock()

	answers := q.answers()
	if result == outcomeDeclined {
		answers = []answer{}
	}
	if result != outcomeFeedback {
		// The words of a decline with feedback are its answer, and are
		// logged when they come.
		s.logAnswer(q, answers, result, via)
	}
	s.recordQuestionResult(q, answers, result)
	if s.name == "opencode" && q.kind == "permit" {
		reply := q.permitReply()
		s.events.publish(map[string]any{
			"type":       "permission.replied",
			"properties": map[string]any{"requestID": q.id, "sessionID": s.statusID(), "reply": reply},
		})
		if reply == "reject" {
			s.paintQuestion("  ┃  ✗ Permission rejected\r\n")
			s.setStatus("idle", "")
			s.paintIdle()
			return
		}
		s.paintQuestion("  ┃  ✓ Permission allowed " + reply + "\r\n")
		s.busy(answerWork, true)
		return
	}
	switch result {
	case outcomeAnswered:
		if s.name == "opencode" {
			labels := make([][]string, 0, len(answers))
			for _, a := range answers {
				labels = append(labels, strings.Split(a.line(), ", "))
			}
			s.events.publish(map[string]any{"type": "question.replied", "properties": map[string]any{
				"sessionID": s.statusID(), "requestID": q.id, "answers": labels,
			}})
		}
		if q.again > 0 {
			// The next of parallel tool calls, asked the moment this one is
			// answered and looking the same: the state never leaves waiting.
			next := newQuestion(q.kind, q.items, q.chat)
			next.harness, next.again, next.host = q.harness, q.again-1, q.nextHost
			s.showQuestion(next)
			return
		}
		s.paintQuestion(s.answeredText(q, answers))
		s.busy(answerWork, true)
	case outcomeDeclined:
		if s.name == "opencode" {
			s.events.publish(map[string]any{"type": "question.rejected", "properties": map[string]any{
				"sessionID": s.statusID(), "requestID": q.id,
			}})
		}
		s.paintQuestion(s.declinedText(q))
		s.setStatus("idle", "")
		s.paintIdle()
	case outcomeFeedback:
		s.mu.Lock()
		s.feedback = q
		s.mu.Unlock()
		if s.name == "codex" {
			s.paintQuestion("• Request declined; tell Codex what to do differently\r\n")
		} else {
			s.paintQuestion("● Network request to example.com\r\n  ⎿  No (tell Claude what to do differently)\r\n")
		}
		s.setStatus("idle", "")
		s.paintIdle()
	case outcomeRevise:
		s.paintQuestion("● User rejected Claude's plan:\r\n  ⎿  " + q.feedback + "\r\n")
		s.busyThen(planWork, true, func() {
			q.revise()
			s.showQuestion(q)
		})
	}
}

// answeredText is what is left on the screen once a menu took its answer.
// Claude's is verbatim; the other two say the same in their own furniture.
func (s *sessionState) answeredText(q *question, answers []answer) string {
	if q.kind == "plan" {
		return "● User approved Claude's plan\r\n"
	}
	var b strings.Builder
	switch s.name {
	case "codex":
		b.WriteString("• Answered\r\n")
		for _, a := range answers {
			fmt.Fprintf(&b, "  └ %s → %s\r\n", a.Question, a.line())
		}
	case "opencode":
		b.WriteString("  → Asked 1 question\r\n")
		for _, a := range answers {
			fmt.Fprintf(&b, "  ┃  · %s → %s\r\n", a.Question, a.line())
		}
	default:
		b.WriteString("● User answered Claude's questions:\r\n")
		for i, a := range answers {
			lead := "     · "
			if i == 0 {
				lead = "  ⎿  · "
			}
			fmt.Fprintf(&b, "%s%s → %s\r\n", lead, a.Question, a.line())
		}
	}
	return b.String()
}

func (s *sessionState) declinedText(q *question) string {
	switch {
	case s.name == "codex":
		return "• Question dismissed\r\n"
	case s.name == "opencode":
		return "  ┃  Question dismissed\r\n"
	case q.kind == "plan":
		return "● User rejected Claude's plan\r\n"
	}
	return "● User declined to answer questions\r\n"
}

// logAnswer appends what a menu was settled with to $FAKE_LOG, beside the
// launch records. It carries no "name", so a reader picking launches out by
// name never takes it for one.
func (s *sessionState) logAnswer(q *question, answers []answer, result outcome, via string) {
	record := map[string]any{
		"event": "answer", "harness": s.name, "kind": q.kind, "id": q.id,
		"pid": os.Getpid(), "at": time.Now().UnixMilli(), "via": via,
		"declined": result == outcomeDeclined, "answers": answers,
	}
	if q.toChat {
		record["chat"] = true
	}
	if q.kind == "plan" {
		record["approved"] = result == outcomeAnswered
		record["revision"] = q.revisions
	}
	appendLog(record)
}

// recordQuestion writes the tool call that asks, in the envelope Claude Code
// writes it in, so that the server's reading of a pending question is tested
// against the shape it meets in production.
func (s *sessionState) recordQuestion(q *question) {
	if q.kind == "permit" {
		// A permission is no tool call of its own in the transcript.
		return
	}
	use := map[string]any{"type": "tool_use", "id": q.id}
	if q.kind == "plan" {
		use["name"] = "ExitPlanMode"
		use["input"] = map[string]any{"plan": "# Plan\n\n" + q.plan + "\n"}
	} else {
		use["name"] = "AskUserQuestion"
		use["input"] = map[string]any{"questions": q.toolQuestions()}
	}
	s.appendTranscript("assistant", map[string]any{
		"role": "assistant", "type": "message", "model": "fake",
		"content": []any{use}, "stop_reason": "tool_use",
	}, nil)
}

func (q *question) toolQuestions() []map[string]any {
	questions := make([]map[string]any, 0, len(q.items))
	for _, item := range q.items {
		questions = append(questions, map[string]any{
			"question": item.Text, "header": item.Header,
			"multiSelect": item.Multi, "options": item.Options,
		})
	}
	return questions
}

// recordQuestionResult writes the tool_result that settles the tool call,
// with the wording Claude Code uses for each way it can end.
func (s *sessionState) recordQuestionResult(q *question, answers []answer, result outcome) {
	if q.kind == "permit" {
		return
	}
	var content string
	var toolUseResult any
	extra := map[string]any{}
	isError := result != outcomeAnswered
	switch {
	case result == outcomeDeclined:
		content = rejectedTool + " STOP what you are doing and wait for the user to tell you how to proceed."
		toolUseResult = "User rejected tool use"
	case result == outcomeRevise:
		content = rejectedTool + " To tell you how to proceed, the user said:\n" + q.feedback
		toolUseResult = "Error: " + content
		extra["userFeedback"] = q.feedback
	case q.kind == "plan":
		plan := "# Plan\n\n" + q.plan + "\n"
		content = "User has approved your plan. You can now start coding.\n\n## Approved Plan:\n" + plan
		toolUseResult = map[string]any{"plan": plan}
	default:
		pairs := make([]string, 0, len(answers))
		byQuestion := map[string]string{}
		for _, a := range answers {
			pairs = append(pairs, `"`+a.Question+`"="`+a.line()+`"`)
			byQuestion[a.Question] = a.line()
		}
		content = "The user answered: " + strings.Join(pairs, ", ") + ". Read the answers carefully — they may " +
			"request clarification, changes, or that you not proceed — and follow what they actually say."
		toolUseResult = map[string]any{"questions": q.toolQuestions(), "answers": byQuestion}
	}
	block := map[string]any{"type": "tool_result", "tool_use_id": q.id, "content": content}
	if isError {
		block["is_error"] = true
	}
	extra["toolUseResult"] = toolUseResult
	s.appendTranscript("user", map[string]any{"role": "user", "content": []any{block}}, extra)
}

// appendTranscript adds one record to Claude's conversation file. Under the
// other two names there is none, and nothing is written.
func (s *sessionState) appendTranscript(kind string, message, extra map[string]any) {
	if s.transcript == "" {
		return
	}
	record := map[string]any{
		"type": kind, "sessionId": s.id, "uuid": uuid.NewString(), "isSidechain": false,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "cwd": s.cwd, "message": message,
	}
	for k, v := range extra {
		record[k] = v
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.transcript, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(raw, '\n'))
}

// questionRequest is the open menu as OpenCode's question tool describes it,
// on the stream and from GET /question.
func (s *sessionState) questionRequest(q *question) map[string]any {
	questions := make([]map[string]any, 0, len(q.items))
	for _, item := range q.items {
		questions = append(questions, map[string]any{
			"question": item.Text, "header": item.Header, "options": item.Options,
			"multiple": item.Multi, "custom": true,
		})
	}
	return map[string]any{"id": q.id, "sessionID": s.statusID(), "questions": questions}
}

func (s *sessionState) pendingQuestions() []map[string]any {
	s.mu.Lock()
	q := s.question
	s.mu.Unlock()
	if q == nil || s.name != "opencode" {
		return []map[string]any{}
	}
	return []map[string]any{s.questionRequest(q)}
}

// serveQuestionReply is POST /question/:id/reply, with a body of
// {"answers":[[label…]…]}, and POST /question/:id/reject: the two ways an
// OpenCode client settles a question without a keystroke.
func (s *sessionState) serveQuestionReply(w http.ResponseWriter, r *http.Request) {
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/question/"), "/")
	if r.Method != http.MethodPost || (action != "reply" && action != "reject") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Answers [][]string `json:"answers"`
	}
	if action == "reply" {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	q := s.question
	if q == nil || q.id != id {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	result := outcomeDeclined
	if action == "reply" {
		q.applyReply(body.Answers)
		result = outcomeAnswered
	}
	s.mu.Unlock()
	s.settle(q, result, "http")
	writeJSON(w, true)
}

// askInText is /textq: a turn that ends on a question in prose, with the
// program idle and the prompt open under it, as a real agent ends a turn it
// wants an answer to.
func (s *sessionState) askInText() {
	s.stopPaint()
	s.mu.Lock()
	s.askedInText = true
	s.mu.Unlock()
	switch s.name {
	case "codex":
		out("• %s\r\n", textQuestion)
	case "opencode":
		out("  %s\r\n", textQuestion)
	default:
		out("● %s\r\n", textQuestion)
	}
	s.appendTranscript("assistant", map[string]any{
		"role": "assistant", "type": "message", "model": "fake",
		"content":     []any{map[string]any{"type": "text", "text": textQuestion}},
		"stop_reason": "end_turn",
	}, nil)
	s.setStatus("idle", "")
	s.idleFurniture()
}

// taskInstruction is what /tasks says the person asked for, and taskSubjects
// the list the turn works through.
const taskInstruction = "Add the endpoint, test it and document it"

var taskSubjects = []string{"Add the endpoint", "Test the endpoint", "Write the docs"}

// workOnTasks is /tasks [done]: a turn that works through a task list until
// done of its tasks are completed (all of them when no number is given),
// recorded the way Claude Code 2.1 records it - the instruction as a user
// record, a TaskCreate per task with the answer that names its id (the first
// turn only: the list is the session's, and its ids go on counting), a
// TaskUpdate per task it finished, and the answer. What the turn left open is
// what Socrates reads its outcome from (internal/server/outcome.go).
func (s *sessionState) workOnTasks(spec string) {
	done := len(taskSubjects)
	if n, err := strconv.Atoi(spec); err == nil && n >= 0 && n <= len(taskSubjects) {
		done = n
	}
	s.appendTranscript("user", map[string]any{"role": "user", "content": taskInstruction}, nil)
	s.mu.Lock()
	first := len(s.taskIDs) == 0
	s.mu.Unlock()
	if first {
		for i, subject := range taskSubjects {
			use := "toolu_task_" + strconv.Itoa(i+1)
			s.appendTranscript("assistant", map[string]any{
				"role": "assistant", "type": "message", "model": "fake", "stop_reason": "tool_use",
				"content": []any{map[string]any{"type": "tool_use", "id": use, "name": "TaskCreate",
					"input": map[string]any{"subject": subject, "description": subject + ".", "activeForm": subject}}},
			}, nil)
			s.appendTranscript("user", map[string]any{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": use,
				"content": fmt.Sprintf("Task #%d created successfully: %s", i+1, subject)}}}, nil)
		}
		s.mu.Lock()
		s.taskIDs = make([]bool, len(taskSubjects))
		s.mu.Unlock()
	}
	s.mu.Lock()
	var finish []int
	for i := 0; i < done; i++ {
		if !s.taskIDs[i] {
			s.taskIDs[i] = true
			finish = append(finish, i+1)
		}
	}
	s.mu.Unlock()
	for _, id := range finish {
		use := "toolu_update_" + uuid.NewString()
		s.appendTranscript("assistant", map[string]any{
			"role": "assistant", "type": "message", "model": "fake", "stop_reason": "tool_use",
			"content": []any{map[string]any{"type": "tool_use", "id": use, "name": "TaskUpdate",
				"input": map[string]any{"taskId": strconv.Itoa(id), "status": "completed"}}},
		}, nil)
		s.appendTranscript("user", map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": use, "content": fmt.Sprintf("Updated task #%d status", id)}}}, nil)
	}
	answer := fmt.Sprintf("Finished %d of %d tasks.", done, len(taskSubjects))
	s.appendTranscript("assistant", map[string]any{
		"role": "assistant", "type": "message", "model": "fake", "stop_reason": "end_turn",
		"content": []any{map[string]any{"type": "text", "text": answer}},
	}, nil)
	s.busyThen(1500*time.Millisecond, true, func() {
		out("● %s\r\n", answer)
		s.setStatus("idle", "")
		s.paintIdle()
	})
}

// answerFeedback takes the line typed after a permission prompt was declined
// through its last row as the words of that decline, logs them, and reports
// whether it was one. An empty line is no words.
func (s *sessionState) answerFeedback(line string) bool {
	s.mu.Lock()
	q := s.feedback
	if q != nil && line != "" {
		s.feedback = nil
	}
	s.mu.Unlock()
	if q == nil || line == "" {
		return false
	}
	appendLog(map[string]any{
		"event": "answer", "harness": s.name, "kind": q.kind, "id": q.id,
		"pid": os.Getpid(), "at": time.Now().UnixMilli(), "via": "keys", "declined": false, "feedback": true,
		"answers": []answer{{Question: q.items[0].Text, Choices: []string{feedbackLabel(s.name)}, Text: line}},
	})
	return true
}

// answerInText takes the line typed after /textq as its answer, logs it, and
// reports whether it was one. An empty line answers nothing.
func (s *sessionState) answerInText(line string) bool {
	s.mu.Lock()
	asked := s.askedInText && line != ""
	if asked {
		s.askedInText = false
	}
	s.mu.Unlock()
	if !asked {
		return false
	}
	appendLog(map[string]any{
		"event": "answer", "harness": s.name, "kind": "text",
		"pid": os.Getpid(), "at": time.Now().UnixMilli(), "via": "keys", "declined": false,
		"answers": []answer{{Question: textQuestion, Choices: []string{}, Text: line}},
	})
	s.appendTranscript("user", map[string]any{"role": "user", "content": line}, nil)
	return true
}
