package browserstack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/browserstack/driver"
)

// RepoFile is where a repository says what the BrowserStack server needs to
// know about it, relative to its root: the site under test, its locales, the
// logins a page may be typed and where they come from, its mailbox and its
// logs. Everything is optional; without the file a session opens full URLs
// only.
const RepoFile = ".socrates/mcp/browserstack.json"

// RepoConfig is RepoFile.
type RepoConfig struct {
	// BaseURL is the site a path is opened on (https://beta.browser.lol).
	BaseURL string `json:"base_url"`
	// Locales are the locales the site's paths start with: a path that
	// names none gets the session's locale in front (/create opens
	// /es/create).
	Locales []string `json:"locales"`
	// Project is BrowserStack's project name of the sessions.
	Project string `json:"project"`
	// Secrets is the logins a page may be typed (type with secret).
	Secrets *RepoSecrets `json:"secrets"`
	// TOTP names the secret of each authenticator (google:
	// QA_GOOGLE_TOTP_SECRET).
	TOTP map[string]string `json:"totp"`
	// Mail is the repository's mailbox.
	Mail *RepoMail `json:"mail"`
	// Logs is the repository's application logs.
	Logs *RepoLogs `json:"logs"`
}

// RepoSecrets is a command that prints NAME=value lines (as `secretctl show`
// does), run in the repository root, and the names of them that may be
// typed; a name ending in ? may be missing.
type RepoSecrets struct {
	Command string   `json:"command"`
	Names   []string `json:"names"`
}

// RepoMail is a command that reads the newest mails of the repository's
// mailbox, run in the repository root as
//
//	<command> --to <address or %> --since <seconds> --limit <n>
//
// and printing a JSON list of {"received","from","to","title","code","text","html"},
// newest first. Address is the template of a session's own address
// ({stamp}, {member}); LinkHosts the hosts whose links mail shows.
type RepoMail struct {
	Command   string   `json:"command"`
	Address   string   `json:"address"`
	LinkHosts []string `json:"link_hosts"`
}

// RepoLogs is a command that prints the application's warnings and errors
// between two times, run in the repository root as
//
//	<command> --from <RFC 3339> --to <RFC 3339>
//
// one line each; Name is the file it is kept as in the evidence
// (logs/<name>.txt) and what the logs tool calls it; Note is said before
// the lines (that other sessions use the same site, say).
type RepoLogs struct {
	Command string `json:"command"`
	Name    string `json:"name"`
	Note    string `json:"note"`
}

// commandTimeout is how long a repository command may take.
const commandTimeout = 2 * time.Minute

// repoRoot is the repository a folder is in: the nearest folder at or above
// it with a .git; the folder itself outside one.
func repoRoot(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	for at := dir; ; {
		if _, err := os.Stat(filepath.Join(at, ".git")); err == nil {
			return at
		}
		parent := filepath.Dir(at)
		if parent == at {
			return dir
		}
		at = parent
	}
}

// LoadRepo reads RepoFile of a repository; a zero config when it has none.
func LoadRepo(root string) (*RepoConfig, error) {
	c := &RepoConfig{}
	if root == "" {
		return c, nil
	}
	data, err := os.ReadFile(filepath.Join(root, RepoFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("%s: %v", RepoFile, err)
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.BaseURL != "" && !strings.HasPrefix(c.BaseURL, "https://") && !strings.HasPrefix(c.BaseURL, "http://") {
		return nil, fmt.Errorf("%s: base_url %q is not an http(s) address", RepoFile, c.BaseURL)
	}
	return c, nil
}

// secretLine is a NAME=value line; an export prefix is dropped.
var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseSecrets reads NAME=value lines as `secretctl show` prints them: split at the first =, the value taken raw.
func parseSecrets(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(strings.TrimPrefix(name, "export "))
		if secretName.MatchString(name) {
			out[name] = value
		}
	}
	return out
}

// readSecrets runs the secrets command and keeps the names it lists. A name
// without ? that is missing is an error. Its output is never shown: only the
// last line of its error output, when it fails.
func (c *RepoConfig) readSecrets(ctx context.Context, root string) (map[string]string, error) {
	if c.Secrets == nil || strings.TrimSpace(c.Secrets.Command) == "" {
		return map[string]string{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := shell(ctx, c.Secrets.Command)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limited{w: &stdout, n: 1 << 20}, &limited{w: &stderr, n: 64 << 10}
	if err := cmd.Run(); err != nil {
		// Its output is never shown, its error output neither: either may
		// hold a secret.
		return nil, fmt.Errorf("the secrets command (%s) failed: %v", c.Secrets.Command, err)
	}
	all := parseSecrets(stdout.String())
	out := map[string]string{}
	var missing []string
	for _, n := range c.Secrets.Names {
		name, optional := strings.CutSuffix(strings.TrimSpace(n), "?")
		if v := all[name]; v != "" {
			out[name] = v
		} else if !optional {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the secrets command (%s) did not print %s", c.Secrets.Command, strings.Join(missing, ", "))
	}
	return out, nil
}

// run runs a repository command (a path relative to the root, no shell)
// with args and gives its output.
func run(ctx context.Context, root, command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	path := command
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.FromSlash(command))
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = root
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limited{w: &stdout, n: 16 << 20}, &limited{w: &stderr, n: 64 << 10}
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("it did not finish within %s", commandTimeout)
		}
		return "", fmt.Errorf("%s failed: %v%s", command, err, lastLine(stderr.String()))
	}
	return stdout.String(), nil
}

// repoMail reads the repository's mailbox (driver.MailSource).
type repoMail struct {
	root string
	conf *RepoMail
}

var mailAddress = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]*@[a-zA-Z0-9.-]+$`)

func (m repoMail) Read(ctx context.Context, to string, sinceSeconds, limit int) ([]driver.Mail, error) {
	if to != "" && to != "%" && !mailAddress.MatchString(to) {
		return nil, fmt.Errorf("not an address: %s", to)
	}
	if to == "" {
		to = "%"
	}
	out, err := run(ctx, m.root, m.conf.Command, "--to", to, "--since", strconv.Itoa(sinceSeconds), "--limit", strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Received string  `json:"received"`
		From     string  `json:"from"`
		To       string  `json:"to"`
		Title    string  `json:"title"`
		Code     *string `json:"code"`
		Text     string  `json:"text"`
		HTML     string  `json:"html"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		return nil, fmt.Errorf("%s printed no list of mails: %v", m.conf.Command, err)
	}
	mails := make([]driver.Mail, 0, len(rows))
	for _, r := range rows {
		code := ""
		if r.Code != nil {
			code = *r.Code
		}
		if code == "" {
			code = driver.FallbackCode(r.Title + "\n" + r.Text)
		}
		mails = append(mails, driver.Mail{Received: r.Received, From: r.From, To: r.To, Title: r.Title, Code: code,
			Links: driver.ExtractLinks(r.Text, r.HTML, m.conf.LinkHosts)})
	}
	return mails, nil
}

type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n > 0 {
		q := p
		if len(q) > l.n {
			q = q[:l.n]
		}
		l.n -= len(q)
		_, _ = l.w.Write(q)
	}
	return len(p), nil
}

// lastLine is the last line of an error output, after ": ", or "".
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return ""
	}
	if len(last) > 300 {
		last = last[:300]
	}
	return ": " + last
}
