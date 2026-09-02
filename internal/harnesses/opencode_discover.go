package harnesses

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// How the OpenCode discoverer waits.
//
// The first budget is for the server itself: the TUI has to be listening
// before anything can be asked. The second is for a conversation to exist,
// which is a different kind of waiting - a session the user has opened and not
// typed into has no id yet, and that is normal rather than late.
const (
	openCodePoll        = 2 * time.Second
	openCodeServerWait  = 60 * time.Second
	openCodeSessionWait = 15 * time.Minute
)

// openCodeSession is one entry of GET /session, and only the fields that
// decide which one is this pane's.
type openCodeSession struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

func (s openCodeSession) created() time.Time { return time.UnixMilli(s.Time.Created) }

// DiscoverOpenCodeSession waits for the TUI's own HTTP server to name the
// session that belongs to this pane.
//
// It never creates one. A POST /session would force an id into existence that
// the TUI is not showing, and `--session <that id>` on the next boot would
// open an empty screen - which looks exactly like losing the conversation.
//
// GET /session lists every session in the shared database, not this process's,
// so the working directory is nowhere near enough to identify one: a preset or
// a typed-in directory with any OpenCode history at all would answer on the
// first poll with a conversation the user never had in this pane. The launch
// time is what narrows it down, and the ids other rows already hold are what
// separate two sessions started in the same directory.
func DiscoverOpenCodeSession(ctx context.Context, access ServerAccess, d Discovery) (string, error) {
	d.Cwd = filepath.Clean(d.Cwd)
	client := &http.Client{Timeout: 10 * time.Second}
	serverBy := time.Now().Add(openCodeServerWait)
	sessionBy := time.Now().Add(openCodeSessionWait)
	answered := false
	var lastErr error

	for {
		sessions, err := listOpenCodeSessions(ctx, client, access)
		switch {
		case err == nil:
			answered = true
			if id := newestIn(sessions, d); id != "" {
				return id, nil
			}
		case errors.Is(err, errOpenCodeAuth):
			// The password did not match. That is a launch failure and not
			// something a retry can mend.
			return "", err
		default:
			lastErr = err
		}
		// Step 4 of the recipe: a version that does not serve the API, or a
		// server that never came up, still leaves its sessions in the
		// database, which is read strictly read-only.
		if !answered && time.Now().After(serverBy) {
			id, dbErr := newestInDB(ctx, d)
			if id != "" {
				return id, nil
			}
			if dbErr != nil {
				return "", fmt.Errorf("the OpenCode server never answered on port %d (%v) and its database could not be read: %w", access.Port, lastErr, dbErr)
			}
		}
		if time.Now().After(sessionBy) {
			return "", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(openCodePoll):
		}
	}
}

// errOpenCodeAuth is a 401 from the TUI server: the password Socrates
// generated is not the one the process was started with.
var errOpenCodeAuth = errors.New("the OpenCode server refused the session password")

func listOpenCodeSessions(ctx context.Context, client *http.Client, access ServerAccess) ([]openCodeSession, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/session", access.Port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(access.Username, access.Password)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, errOpenCodeAuth
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the OpenCode server answered %s", res.Status)
	}
	var sessions []openCodeSession
	if err := json.NewDecoder(res.Body).Decode(&sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// newestIn picks this pane's session out of everything the server listed.
//
// It sorts on the creation time rather than the id, deliberately: OpenCode's
// ids are descending, so "newest" is the lexicographically smallest one, which
// is the opposite of what every other id in this application does and exactly
// the sort of thing a later reader would quietly "fix".
func newestIn(sessions []openCodeSession, d Discovery) string {
	var mine []openCodeSession
	for _, s := range sessions {
		switch {
		case s.ID == "" || filepath.Clean(s.Directory) != d.Cwd:
		case d.startedBefore(s.created()):
			// It was created before this pane was; it is somebody's earlier
			// conversation in the same folder.
		case d.claimed(s.ID):
			// Another row of this harness already holds it.
		default:
			mine = append(mine, s)
		}
	}
	if len(mine) == 0 {
		return ""
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].Time.Created > mine[j].Time.Created })
	return mine[0].ID
}

// newestInDB is the same question asked of OpenCode's own database, for the
// case where the HTTP server is unreachable. It is opened read-only, twice
// over: it belongs to OpenCode and Socrates has no business writing a byte of
// it. A missing database is not an error - it means there is nothing to find
// yet - and only a database that exists and cannot be read is.
func newestInDB(ctx context.Context, d Discovery) (string, error) {
	dir := openCodeDataDir()
	if dir == "" {
		return "", nil
	}
	path := filepath.Join(dir, "opencode.db")
	if _, err := os.Stat(path); err != nil {
		return "", nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx,
		"SELECT id, directory, time_created FROM session WHERE directory = ? ORDER BY time_created DESC LIMIT 50", d.Cwd)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var sessions []openCodeSession
	for rows.Next() {
		var s openCodeSession
		if err := rows.Scan(&s.ID, &s.Directory, &s.Time.Created); err != nil {
			return "", err
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return newestIn(sessions, d), nil
}

// VerifyCLISession asks OpenCode's own database whether a session still
// exists.
//
// For this harness "could not tell" degrades to "gone": --session with an
// unknown id exits immediately, so guessing wrong costs the user a pane that
// crashes on sight, while a fresh session costs them one conversation they can
// still find in the TUI's own picker.
func (OpenCode) VerifyCLISession(ctx context.Context, req PlanRequest) (bool, error) {
	id := strings.TrimSpace(req.CLISession)
	if id == "" {
		return false, nil
	}
	dir := openCodeDataDir()
	if dir == "" {
		return false, nil
	}
	path := filepath.Join(dir, "opencode.db")
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return false, nil
	}
	defer func() { _ = db.Close() }()
	var one int
	switch err := db.QueryRowContext(ctx, "SELECT 1 FROM session WHERE id = ?", id).Scan(&one); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, nil
	}
}
