//go:build !windows

package opencode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// DefaultAccount is the account a chat runs on when its spec names none.
const DefaultAccount = "default"

// sharedDirs are the parts of an OpenCode data home that belong to every
// account: the snapshot git store revert and diffs need, session diffs, tool
// output, plans, worktrees and repositories. Each account's data home links
// them to the one copy (research/06 §3.2). auth.json, mcp-auth.json and log/
// stay with the account.
var sharedDirs = []string{"snapshot", "storage", "tool-output", "plans", "worktree", "repos"}

// accountPattern is what an account id may look like: it becomes a directory
// name.
var accountPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidAccount reports whether id is usable as an account id.
func ValidAccount(id string) bool { return accountPattern.MatchString(id) }

// Layout is OpenCode's store inside the Socrates data folder:
//
//	<data>/agents/opencode/
//	  opencode.db (+ -wal, -shm)       OPENCODE_DB of every process
//	  snapshot/ storage/ tool-output/ plans/ worktree/ repos/
//	  config/                          XDG_CONFIG_HOME, shared
//	  cache/                           XDG_CACHE_HOME, shared
//	  accounts/<acct>/                 XDG_DATA_HOME of the account
//	    opencode/auth.json             that account's credentials (0600)
//	    opencode/snapshot -> ../../../snapshot   (and the other shared dirs)
//	    state/                         XDG_STATE_HOME of the account
type Layout struct {
	// Root is <data>/agents/opencode.
	Root string
}

// NewLayout is the layout in a Socrates data folder.
func NewLayout(dataDir string) Layout {
	return Layout{Root: filepath.Join(dataDir, "agents", "opencode")}
}

// DB is the database every process shares.
func (l Layout) DB() string { return filepath.Join(l.Root, "opencode.db") }

// ConfigHome and CacheHome are shared by every account. The config home's
// node_modules/@opencode-ai/plugin is what OpenCode installs on its first
// start; a stop that catches that install half done is what TERM → KILL is
// for.
func (l Layout) ConfigHome() string { return filepath.Join(l.Root, "config") }

// CacheHome is the shared XDG_CACHE_HOME (models.json, binaries OpenCode
// fetches for itself).
func (l Layout) CacheHome() string { return filepath.Join(l.Root, "cache") }

// AccountHome is the account's XDG_DATA_HOME.
func (l Layout) AccountHome(account string) string {
	return filepath.Join(l.Root, "accounts", account)
}

// AuthFile is the account's credentials file, where OAuth sign-ins and their
// token refreshes are written.
func (l Layout) AuthFile(account string) string {
	return filepath.Join(l.AccountHome(account), "opencode", "auth.json")
}

// Env is the part of a process's environment that puts it on this layout
// as account.
func (l Layout) Env(account string) []string {
	home := l.AccountHome(account)
	return []string{
		"XDG_DATA_HOME=" + home,
		"XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"XDG_CONFIG_HOME=" + l.ConfigHome(),
		"XDG_CACHE_HOME=" + l.CacheHome(),
		"OPENCODE_DB=" + l.DB(),
	}
}

// PrepareAccount makes the account's data home, with the shared folders
// linked into it by relative symlinks so the data folder can move or be
// restored elsewhere. It is idempotent; WP7 calls it when an account is
// created, and every start calls it again.
func (l Layout) PrepareAccount(account string) error {
	if !ValidAccount(account) {
		return fmt.Errorf("%q is not an account id", account)
	}
	for _, dir := range append([]string{l.ConfigHome(), l.CacheHome()}, sharedDirs...) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(l.Root, dir)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data := filepath.Join(l.AccountHome(account), "opencode")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(l.AccountHome(account), "state"), 0o700); err != nil {
		return err
	}
	for _, name := range sharedDirs {
		link := filepath.Join(data, name)
		target := filepath.Join("..", "..", "..", name)
		current, err := os.Readlink(link)
		switch {
		case err == nil && current == target:
			continue
		case err == nil:
			if err := os.Remove(link); err != nil {
				return err
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			// A real directory OpenCode made before the account was linked:
			// what is in it belongs in the shared one.
			if err := mergeInto(link, filepath.Join(l.Root, name)); err != nil {
				return fmt.Errorf("move %s into the shared folder: %w", link, err)
			}
		}
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	return nil
}

// mergeInto moves every entry of dir into dst, keeping what dst already has,
// and removes dir.
func mergeInto(dir, dst string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		to := filepath.Join(dst, e.Name())
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		if err := os.Rename(filepath.Join(dir, e.Name()), to); err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}

// migrateStamp records the binary the shared database was last migrated
// with.
const migrateStamp = "migrated"

// Migrate brings the shared database up to the schema of the binary at bin,
// once per binary: concurrent first starts of `opencode serve` on a new or
// older database race on the migrations and some of them die (research/06
// §3.1), so the migrations run here, under a lock, before any serve. env is
// the environment to run it with (an account's, see Env), and spawn starts
// it (StartSpec.Spawn: a host that dies takes the migration with it, and the
// next one, which takes the free lock, does not race a leftover). When ctx
// ends, its process group is killed.
func (l Layout) Migrate(ctx context.Context, spawn func(*exec.Cmd) error, bin string, env []string) error {
	if err := os.MkdirAll(l.Root, 0o700); err != nil {
		return err
	}
	id, err := binaryIdentity(bin)
	if err != nil {
		return err
	}
	stamp := filepath.Join(l.Root, migrateStamp)
	if raw, err := os.ReadFile(stamp); err == nil && string(raw) == id {
		return nil
	}
	lock, err := os.OpenFile(filepath.Join(l.Root, "migrate.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := flockContext(ctx, lock); err != nil {
		return err
	}
	if raw, err := os.ReadFile(stamp); err == nil && string(raw) == id {
		return nil
	}
	cmd := exec.Command(bin, "db", "select 1")
	cmd.Env = env
	cmd.Dir = l.Root
	out := &ring{max: 8 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	if err := spawn(cmd); err != nil {
		return fmt.Errorf("migrate the OpenCode database: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("migrate the OpenCode database: %v: %s", err, strings.TrimSpace(tail(out.String(), 400)))
		}
	case <-ctx.Done():
		if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return fmt.Errorf("migrate the OpenCode database: %w", ctx.Err())
	}
	tmp := stamp + ".tmp"
	if err := os.WriteFile(tmp, []byte(id), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, stamp)
}

// binaryIdentity names one build of the binary: where it really is, its size
// and its time. An update replaces the file, which changes all three.
func binaryIdentity(bin string) (string, error) {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %d %d", real, fi.Size(), fi.ModTime().UnixNano()), nil
}

// flockContext takes an exclusive lock on f, giving up when ctx ends.
func flockContext(ctx context.Context, f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
