package testmode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// What a tool needs to run (TEST-MODE.md, "The tool contract"): its secrets,
// read by the command it names; an installed copy of it, made by its setup;
// its hooks; and the vault file through which the secrets reach it.

// The limits of the commands a run starts itself.
var (
	secretsTimeout = 2 * time.Minute
	hookTimeout    = 10 * time.Minute
)

// maxHookOutput is how much of a hook's output is kept to say why it failed.
const maxHookOutput = 8 << 10

// runCommand runs a shell command line in dir with env and returns its
// output (stdout, and stderr after it), cut to max bytes.
func runCommand(ctx context.Context, dir, line string, env []string, timeout time.Duration, max int) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", line)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, n: max}
	cmd.Stderr = &limitWriter{w: &stderr, n: max}
	setProcessGroup(cmd)
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("it did not finish within %s", timeout)
	}
	return stdout.String(), stderr.String(), err
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, _ = l.w.Write(q)
	return len(p), nil
}

// readSecrets runs every tool's secrets command once (a command two tools
// share runs once) in the repository root, and returns the secrets each tool
// named, by tool. A required name missing is an error.
func readSecrets(ctx context.Context, repoPath string, tools []*catalog.Tool) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	ran := map[string]map[string]string{}
	for _, t := range tools {
		cmdLine := strings.TrimSpace(t.Secrets.Command)
		got := map[string]string{}
		if cmdLine != "" {
			all, ok := ran[cmdLine]
			if !ok {
				stdout, stderr, err := runCommand(ctx, repoPath, cmdLine, os.Environ(), secretsTimeout, 1<<20)
				if err != nil {
					// The output may hold secrets; only the error output's last
					// line says why, and it is redacted against what came back.
					return nil, fmt.Errorf("the secrets command of %s (%s) failed: %v%s", t.Name, cmdLine, err, lastLine(stderr))
				}
				all = catalog.ParseSecrets([]byte(stdout))
				ran[cmdLine] = all
			}
			for _, n := range t.Secrets.Names {
				if v, ok := all[n.Name]; ok && v != "" {
					got[n.Name] = v
				}
			}
		}
		var missing []string
		for _, n := range t.Secrets.Names {
			if !n.Optional && got[n.Name] == "" {
				missing = append(missing, n.Name)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("the secrets of %s lack %s", t.Name, strings.Join(missing, ", "))
		}
		out[t.Name] = got
	}
	return out, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300]
	}
	return ": " + l
}

// members is a pool's members: the entries of its secret.
func members(p *catalog.Pool, secrets map[string]string) []string {
	if p == nil {
		return nil
	}
	sep := p.Separator
	if sep == "" {
		sep = ","
	}
	var out []string
	for _, m := range strings.Split(secrets[p.Secret], sep) {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// memberLabel is how a member is shown: the name of a name:secret value,
// else its number.
func memberLabel(value string, n int) string {
	if name, _, ok := strings.Cut(value, ":"); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "#" + strconv.Itoa(n)
}

// toolHash is a hash of a tool's files, without what its setup makes
// (node_modules, .git): an installed copy is kept per hash.
func toolHash(dir string) (string, error) {
	h := sha256.New()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		fi, err := os.Stat(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%o\x00", filepath.ToSlash(rel), fi.Mode().Perm()&0o111)
		in, err := os.Open(f)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, in)
		in.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// readyMark is the file that says a cached copy's setup went through.
const readyMark = ".socrates-ready"

// installTool is the run's copy of a tool: a copy in the cache, made and set
// up once per version of the tool (toolHash), so that a run never runs the
// checkout's tool and its setup never touches the checkout.
func installTool(ctx context.Context, l Layout, t *catalog.Tool, repoPath string) (string, error) {
	sum, err := toolHash(t.Dir)
	if err != nil {
		return "", fmt.Errorf("read the tool %s: %w", t.Name, err)
	}
	dest := filepath.Join(l.ToolCache(), t.Name+"-"+sum)
	if _, err := os.Stat(filepath.Join(dest, readyMark)); err == nil {
		now := time.Now()
		_ = os.Chtimes(dest, now, now)
		return dest, nil
	}
	_ = os.RemoveAll(dest)
	tmp := dest + ".tmp-" + randomHex(3)
	if err := catalog.CopyTool(t, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("copy the tool %s: %w", t.Name, err)
	}
	if strings.TrimSpace(t.Setup) != "" {
		env := append(os.Environ(), "SOCRATES_TEST=1", "SOCRATES_TEST_REPO="+repoPath)
		stdout, stderr, err := runCommand(ctx, tmp, t.Setup, env, hookTimeout, maxHookOutput)
		if err != nil {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("the setup of %s (%s) failed: %v\n%s", t.Name, t.Setup, err, tail(stdout+stderr, 2000))
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, readyMark), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.RemoveAll(tmp)
		if _, statErr := os.Stat(filepath.Join(dest, readyMark)); statErr == nil {
			// Another run installed the same version meanwhile.
			return dest, nil
		}
		return "", err
	}
	pruneCache(l.ToolCache(), t.Name, 4)
	return dest, nil
}

// pruneCache keeps the keep newest installed copies of a tool.
func pruneCache(dir, name string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type copyOf struct {
		path string
		at   time.Time
	}
	var list []copyOf
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), name+"-") || strings.Contains(e.Name(), ".tmp-") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			list = append(list, copyOf{filepath.Join(dir, e.Name()), fi.ModTime()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.After(list[j].at) })
	// A copy a run still runs from is used at most a few days after its
	// install: older ones beyond the newest keep go.
	for i := keep; i < len(list); i++ {
		if time.Since(list[i].at) > 7*24*time.Hour {
			os.RemoveAll(list[i].path)
		}
	}
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// writeVault writes a vault file: NAME=value lines, mode 0600.
func writeVault(path string, values map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		// A value with a line break would read as two lines; such a value is
		// no NAME=value secret anyway.
		b.WriteString(k + "=" + strings.ReplaceAll(values[k], "\n", " ") + "\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// poolBusy asks a pool's status hook which members are busy outside
// Socrates: {"busy": [n, ...]}. A hook that fails or says nothing readable
// counts as none busy.
func poolBusy(ctx context.Context, toolDir, line string, env []string) map[int]bool {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	stdout, _, err := runCommand(ctx, toolDir, line, env, 30*time.Second, 64<<10)
	if err != nil {
		return nil
	}
	var res struct {
		Busy []int `json:"busy"`
	}
	if jsonUnmarshalLast(stdout, &res) != nil {
		return nil
	}
	out := map[int]bool{}
	for _, n := range res.Busy {
		out[n] = true
	}
	return out
}

// errNoJSON is output without a JSON object in it.
var errNoJSON = errors.New("no JSON object in the output")

// jsonUnmarshalLast reads the last line of out that is a JSON object.
func jsonUnmarshalLast(out string, v any) error {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "{") {
			return jsonUnmarshal([]byte(l), v)
		}
	}
	return errNoJSON
}
