package accounts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Claude Code's sign-in is its own `claude auth login --claudeai`, which
// only talks to a terminal: it prints the page to open, and reads the code
// that page shows. Socrates runs it on a pseudo terminal of its own, reads
// the page off what it prints, and types the code the person pastes in the
// Accounts card. The login is written by Claude Code into the account's
// CLAUDE_CONFIG_DIR and never seen by Socrates.
//
// What it prints (2.1.x, on a terminal):
//
//	Opening browser to sign in…
//	If the browser didn't open, visit: ESC]8;;https://claude.com/cai/oauth/authorize?…BEL ESC[94mhttps://…ESC[39m ESC]8;;BEL
//	Paste code here if prompted >
//
// A code that is not one ("Invalid code. Please make sure the full code was
// copied.") is asked for again; one the server refuses ends it with "Login
// failed: …" and exit status 1; a login ends it with exit status 0.

// claudeCols is the width of the pseudo terminal: wide enough that the page,
// which is longer than any real terminal, is printed on one line.
const claudeCols = 2000

// claudeOutputCap bounds what is kept of the CLI's output.
const claudeOutputCap = 64 << 10

// claudeSignIn is one `claude auth login` under way.
type claudeSignIn struct {
	cmd *exec.Cmd
	tty *os.File

	mu  sync.Mutex
	out []byte
	// more is closed, and replaced, whenever output arrives.
	more chan struct{}
	// typed is how much of out there was when the last code was typed.
	typed int
	coded bool

	done chan struct{}
	err  error // the exit, once done is closed
}

// startClaudeLogin runs bin's sign-in with env on a pseudo terminal.
func startClaudeLogin(bin string, env []string) (*claudeSignIn, error) {
	cmd := exec.Command(bin, "auth", "login", "--claudeai")
	// A terminal it can draw on, and no browser of the server's: the page is
	// opened by the person, wherever they are.
	cmd.Env = append(append([]string(nil), env...), "TERM=xterm-256color", "BROWSER=true")
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: claudeCols, Rows: 50})
	if err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	c := &claudeSignIn{cmd: cmd, tty: tty, more: make(chan struct{}), done: make(chan struct{})}
	read := make(chan struct{})
	go func() {
		defer close(read)
		c.read()
	}()
	go func() {
		err := cmd.Wait()
		// What it printed last is read before it counts as ended, unless
		// something it started holds the terminal open.
		select {
		case <-read:
		case <-time.After(2 * time.Second):
		}
		tty.Close()
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	}()
	return c, nil
}

// read keeps what the terminal shows, until it is closed (EIO on Linux, once
// the process is gone).
func (c *claudeSignIn) read() {
	buf := make([]byte, 4096)
	for {
		n, err := c.tty.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.out = append(c.out, buf[:n]...)
			if over := len(c.out) - claudeOutputCap; over > 0 {
				c.out = append([]byte(nil), c.out[over:]...)
				c.typed = max(0, c.typed-over)
			}
			close(c.more)
			c.more = make(chan struct{})
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Close kills the sign-in, if it is still running, and waits for it.
func (c *claudeSignIn) Close() {
	select {
	case <-c.done:
		return
	default:
	}
	_ = c.cmd.Process.Kill()
	<-c.done
}

// output is what was printed so far, with the channel closed when more is,
// and the length of it when the last code was typed.
func (c *claudeSignIn) output() (string, chan struct{}, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.out), c.more, c.typed
}

// result is how the sign-in ended: nil for a login, the CLI's own words
// otherwise.
func (c *claudeSignIn) result() error {
	c.mu.Lock()
	err, coded, out := c.err, c.coded, string(c.out)
	c.mu.Unlock()
	words := lastWords(out)
	switch {
	case err == nil && coded:
		return nil
	case err == nil:
		return fmt.Errorf("Claude Code ended before a code was pasted%s", colonWords(words))
	case words != "":
		return errors.New(words)
	}
	return fmt.Errorf("Claude Code's sign-in ended: %v", err)
}

// waitURL waits for the page to open, and answers it.
func (c *claudeSignIn) waitURL(ctx context.Context) (string, error) {
	for {
		out, more, _ := c.output()
		if url := claudeLoginURL(out); url != "" {
			return url, nil
		}
		select {
		case <-more:
		case <-c.done:
			out, _, _ := c.output()
			return "", fmt.Errorf("it ended without a page to sign in on%s", colonWords(lastWords(out)))
		case <-ctx.Done():
			return "", fmt.Errorf("it showed no page to sign in on: %w", ctx.Err())
		}
	}
}

// typeCode types code, and Enter, on the terminal.
func (c *claudeSignIn) typeCode(code string) error {
	c.mu.Lock()
	c.typed, c.coded = len(c.out), true
	c.mu.Unlock()
	// Enter is a carriage return on a terminal in raw mode.
	_, err := c.tty.Write([]byte(code + "\r"))
	return err
}

// errCodeRefused is a code Claude Code asks again for.
var errCodeRefused = errors.New("refused")

// waitCode waits for what became of the code typed: the end of the
// process (done), or a line saying the code is not one, which leaves it
// asking for another (errCodeRefused with the line). ctx ending first answers
// ctx's error: the flow ends by itself later.
func (c *claudeSignIn) waitCode(ctx context.Context, code string) (done bool, err error) {
	for {
		out, more, typed := c.output()
		for _, line := range strings.Split(plainText(out[min(typed, len(out)):]), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(strings.ToLower(line), "invalid") && !strings.Contains(line, code) {
				return false, fmt.Errorf("%w: %s", errCodeRefused, line)
			}
		}
		select {
		case <-more:
		case <-c.done:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

var (
	// osc8 is a hyperlink's start: ESC ] 8 ; params ; target, ended by BEL or
	// ESC \.
	osc8 = regexp.MustCompile("\x1b\\]8;[^;\x07\x1b]*;([^\x07\x1b]*)(?:\x07|\x1b\\\\)")
	// escapes are the terminal's control sequences: CSI (colours, cursor),
	// OSC (titles, hyperlinks), and the two-byte ones.
	escapes = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]")
	// breaks are the ends of lines: "\n" as a terminal gives it ("\r\n", or
	// "\r\r\n" for a program that wrote "\r\n" itself), and a carriage
	// return alone, which starts the line again.
	breaks = regexp.MustCompile("\r*\n|\r+")
	// urlChars are the characters a URL is made of.
	urlChars = regexp.MustCompile(`^[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+$`)
)

// plainText is out without its control sequences, its lines ended by "\n".
func plainText(out string) string {
	return breaks.ReplaceAllString(escapes.ReplaceAllString(out, ""), "\n")
}

// claudeLoginURL is the page Claude Code's sign-in asks to be opened, read
// off what it printed: the target of its hyperlink when it made one, the
// address in its text otherwise. A terminal narrower than the address wraps
// it; a line that is all address characters right after the line it ends
// is the rest of it.
func claudeLoginURL(out string) string {
	for _, m := range osc8.FindAllStringSubmatch(out, -1) {
		if target := strings.TrimSpace(m[1]); strings.HasPrefix(target, "https://") {
			return target
		}
	}
	lines := strings.Split(plainText(out), "\n")
	for i, line := range lines {
		at := strings.Index(line, "https://")
		if at < 0 {
			continue
		}
		rest := line[at:]
		end := strings.IndexFunc(rest, func(r rune) bool { return !urlChars.MatchString(string(r)) })
		if end < 0 {
			end = len(rest)
		}
		url := rest[:end]
		// A URL followed by more text on its line was not wrapped; one that
		// ends its line goes on on the lines of address characters after it,
		// up to a line that is not one. The last line printed may still be
		// growing (or be followed by more of the URL), so a URL is only taken
		// once a line that cannot be part of it follows.
		if strings.TrimSpace(rest[end:]) == "" {
			if i == len(lines)-1 {
				return ""
			}
			for k := i + 1; k < len(lines); k++ {
				next := strings.TrimSpace(lines[k])
				more := next != "" && urlChars.MatchString(next)
				// (A control sequence left on it is one cut short.)
				if k == len(lines)-1 && (next == "" || more || strings.Contains(next, "\x1b")) {
					return ""
				}
				if !more {
					break
				}
				url += next
			}
		}
		if len(url) > len("https://") {
			return url
		}
	}
	return ""
}

// lastWords is the last line out printed that says something.
func lastWords(out string) string {
	lines := strings.Split(plainText(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.HasPrefix(line, "Paste code here") {
			return line
		}
	}
	return ""
}

func colonWords(words string) string {
	if words == "" {
		return ""
	}
	return ": " + words
}
