package harnesses

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// codexOriginator is what Socrates stamps into every rollout file it causes,
// and one of the two discriminators the watcher matches on.
const codexOriginator = "socrates"

// How patient the rollout watcher is. Fifteen minutes is not generosity: it is
// verified behaviour that neither a rollout file nor an index row exists until
// a real user turn has happened, so "nothing yet" is the normal state of a
// session somebody opened and has not typed into. While it waits, the session
// works; only reboot-resume is not yet armed.
const (
	codexWatchInterval = 2 * time.Second
	codexWatchBudget   = 15 * time.Minute
)

// codexIndex is the SQLite index beside the rollout files. The name is
// version-stamped - the 5 is a schema generation - which is exactly why it is
// the second source and never the first: the next bump renames the file, and a
// verification that asked it first would answer "could not tell" for every
// session, for ever, in silence.
const codexIndex = "state_5.sqlite"

// WatchRollout waits for the rollout file of a session started in cwd and
// returns the session id from it.
//
// The uuid in the file name is the same value as payload.session_id, so once
// the working directory and the originator have been confirmed from line 0,
// the name is enough.
func WatchRollout(ctx context.Context, codexHome string, d Discovery) (string, error) {
	if codexHome == "" {
		return "", errors.New("codex: there is no CODEX_HOME to watch")
	}
	d.Cwd = filepath.Clean(d.Cwd)
	deadline := time.Now().Add(codexWatchBudget)
	for {
		id, err := findRollout(codexHome, d)
		if err != nil {
			return "", err
		}
		if id != "" {
			return id, nil
		}
		if time.Now().After(deadline) {
			return "", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(codexWatchInterval):
		}
	}
}

// findRollout is one pass over the rollout tree. A file older than the launch
// is skipped without being opened, and a whole day's directory is skipped
// without being entered, which is what keeps a home with years of sessions in
// it cheap to poll every two seconds.
func findRollout(codexHome string, d Discovery) (string, error) {
	root := filepath.Join(codexHome, "sessions")
	newest := time.Time{}
	found := ""
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A directory that vanished under the walk is not a failure: Codex
			// writes into this tree while we read it.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			if staleDateDir(root, path, d.Since) {
				return fs.SkipDir
			}
			return nil
		}
		if !isRolloutName(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || d.startedBefore(info.ModTime()) {
			return nil
		}
		meta, err := readSessionMeta(path)
		if err != nil || meta.Payload.SessionID == "" {
			return nil
		}
		if filepath.Clean(meta.Payload.Cwd) != d.Cwd || meta.Payload.Originator != codexOriginator {
			return nil
		}
		// Another session of this harness, in this directory, got there
		// first. Codex offers no per-session handle to tell two of them
		// apart, so the ids already spoken for are the discriminator.
		if d.claimed(meta.Payload.SessionID) {
			return nil
		}
		if info.ModTime().After(newest) {
			newest, found = info.ModTime(), meta.Payload.SessionID
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return found, nil
}

// staleDateDir reports whether a directory of the YYYY/MM/DD tree is entirely
// older than the launch. Only a full date - three levels below the root - is
// judged, because a year or a month directory can hold a day that is not.
func staleDateDir(root, path string, since time.Time) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) != 3 {
		return false
	}
	day, err := time.ParseInLocation("2006/01/02", strings.Join(parts, "/"), time.Local)
	if err != nil {
		return false
	}
	// Two whole days after this one have to be behind the launch before the
	// directory can be skipped. One day covers the ordinary case - a file
	// written at 23:59 is stamped on the day it belongs to, and the launch may
	// be minutes later. The second day is the margin for a clock that does not
	// agree with ours: the directory name is parsed in this process's local
	// zone, Codex stamps it in its own, and a launch in the first hours of a
	// day east of UTC would otherwise skip the directory the session is about
	// to be written into. Two days of extra reading costs a handful of
	// os.Stat calls; skipping the right directory costs the session id.
	return day.AddDate(0, 0, 2).Before(since.Add(-discoverySkew))
}

func isRolloutName(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

// sessionMeta is line 0 of a rollout file, and only the fields the watcher
// matches on.
type sessionMeta struct {
	Type    string `json:"type"`
	Payload struct {
		SessionID  string `json:"session_id"`
		Cwd        string `json:"cwd"`
		Originator string `json:"originator"`
	} `json:"payload"`
}

func readSessionMeta(path string) (sessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return sessionMeta{}, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	// A session_meta record carries the whole base-instructions block, which
	// is far past the scanner's default line budget.
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	if !sc.Scan() {
		return sessionMeta{}, sc.Err()
	}
	var meta sessionMeta
	if err := json.Unmarshal(sc.Bytes(), &meta); err != nil {
		return sessionMeta{}, err
	}
	if meta.Type != "session_meta" {
		return sessionMeta{}, errors.New("codex: the first record of the rollout is not its metadata")
	}
	return meta, nil
}

// VerifyCLISession reports whether a Codex session can still be resumed.
//
// The rollout file is asked first and the index second, and that order is the
// point: the file names have been stable and carry the uuid, while the index's
// file name carries a schema generation that will one day change.
func (Codex) VerifyCLISession(ctx context.Context, req PlanRequest) (bool, error) {
	id := strings.TrimSpace(req.CLISession)
	if id == "" {
		return false, nil
	}
	home := codexHome()
	if home == "" {
		return false, errors.New("codex: there is no CODEX_HOME to look in")
	}
	rollout := false
	rolloutErr := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && isRolloutName(d.Name()) && strings.HasSuffix(d.Name(), "-"+id+".jsonl") {
			rollout = true
			return fs.SkipAll
		}
		return nil
	})
	if rollout {
		return true, nil
	}

	known, indexErr := codexIndexHas(ctx, filepath.Join(home, codexIndex), id)
	if indexErr == nil {
		return known, nil
	}
	if rolloutErr != nil {
		// Neither source could be read. That is "could not tell", not "gone",
		// and the caller resumes anyway: a refused resume shows in the
		// terminal, which is honest, and a wrongly abandoned conversation does
		// not.
		return false, indexErr
	}
	return false, nil
}

// codexIndexHas asks the thread index whether it still knows an id. The
// database is opened strictly read-only, twice over: it belongs to Codex and
// Socrates has no business writing a byte of it.
func codexIndexHas(ctx context.Context, path, id string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		return false, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	row := db.QueryRowContext(ctx, "SELECT 1 FROM threads WHERE id = ?", id)
	var one int
	switch err := row.Scan(&one); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}
