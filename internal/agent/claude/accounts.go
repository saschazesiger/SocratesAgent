package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SharedDirs are the parts of a CLAUDE_CONFIG_DIR every account of the agent
// shares, so that a conversation moves between accounts (spike S1,
// research/08 §7): the transcripts, the task lists the task tools keep, plan
// mode's plans, and the todo folder older versions wrote. Everything else -
// .credentials.json, .claude.json, sessions/, caches, policy files - stays
// the account's own.
var SharedDirs = []string{"projects", "tasks", "plans", "todos"}

// PrepareConfigDir makes accountDir a Claude Code config directory whose
// shared folders are relative symlinks into sharedRoot
// (<data>/agents/claude). It is idempotent. A shared folder that already
// exists in the account as a real directory is moved into the shared root
// when the shared one does not exist yet, and refused otherwise: merging two
// transcript trees is a migration's decision, not this helper's.
func PrepareConfigDir(accountDir, sharedRoot string) error {
	if err := os.MkdirAll(accountDir, 0o700); err != nil {
		return err
	}
	for _, name := range SharedDirs {
		if err := linkShared(accountDir, sharedRoot, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func linkShared(accountDir, sharedRoot, name string) error {
	link := filepath.Join(accountDir, name)
	target := filepath.Join(sharedRoot, name)
	rel, err := filepath.Rel(accountDir, target)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(link)
	switch {
	case err == nil && fi.Mode()&os.ModeSymlink != 0:
		if cur, _ := os.Readlink(link); cur == rel {
			return os.MkdirAll(target, 0o700)
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	case err == nil && fi.IsDir():
		if _, serr := os.Stat(target); serr == nil {
			if empty, _ := isEmptyDir(link); !empty {
				return errors.New("the account has its own folder and the shared one exists too")
			}
			if err := os.Remove(link); err != nil {
				return err
			}
		} else {
			if err := os.MkdirAll(sharedRoot, 0o700); err != nil {
				return err
			}
			if err := os.Rename(link, target); err != nil {
				return err
			}
		}
	case err == nil:
		return errors.New("is a file where a folder belongs")
	case !os.IsNotExist(err):
		return err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	return os.Symlink(rel, link)
}

func isEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	return len(entries) == 0, err
}
