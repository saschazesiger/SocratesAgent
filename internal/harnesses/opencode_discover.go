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

// DiscoverOpenCodeSession waits for the TUI's own HTTP server to name the
// session that belongs to this working directory.
//
// It never creates one. A POST /session would force an id into existence that
// the TUI is not showing, and `--session <that id>` on the next boot would
// open an empty screen - which looks exactly like losing the conversation.
func DiscoverOpenCodeSession(ctx context.Context, access ServerAccess, cwd string) (string, error) {
	cwd = filepath.Clean(cwd)
	client := &http.Client{Timeout: 10 * time.Second}
	serverBy := time.Now().Add(openCodeServerWait)
	sessionBy := time.Now().Add(openCodeSessionWait)
	answered := false

	for {
		sessions, err := listOpenCodeSessions(ctx, client, access)
		switch {
		case err == nil:
			answered = true
			if id := newestIn(sessions, cwd); id != "" {
				return id, nil
			}
		case errors.Is(err, errOpenCodeAuth):
			// The password did not match. That is a launch failure and not
			// something a retry can mend.
			return "", err
		case !answered && time.Now().After(serverBy):
			return "", fmt.Errorf("the OpenCode server never answered on port %d: %w", access.Port, err)
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

// newestIn picks this directory's most recent session.
//
// It sorts on the creation time rather than the id, deliberately: OpenCode's
// ids are descending, so "newest" is the lexicographically smallest one, which
// is the opposite of what every other id in this application does and exactly
// the sort of thing a later reader would quietly "fix".
func newestIn(sessions []openCodeSession, cwd string) string {
	var mine []openCodeSession
	for _, s := range sessions {
		if s.ID != "" && filepath.Clean(s.Directory) == cwd {
			mine = append(mine, s)
		}
	}
	if len(mine) == 0 {
		return ""
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].Time.Created > mine[j].Time.Created })
	return mine[0].ID
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
