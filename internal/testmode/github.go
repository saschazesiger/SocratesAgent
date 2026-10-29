package testmode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// GitHub issues from the bug list (TEST-MODE.md, "GitHub issues"): through
// the GitHub CLI the server's user has signed in, in the repository, for
// the repository its origin names.

// GitHubStatus is whether issues can be made for a repository: its origin
// is on GitHub (Repo, owner/name) and gh is installed and signed in; else
// why not.
type GitHubStatus struct {
	Available bool   `json:"available"`
	Repo      string `json:"repo,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type githubReading struct {
	status GitHubStatus
	at     time.Time
}

// githubEvery is how long a repository's GitHub status is used.
var githubEvery = time.Minute

// issueTimeout is how long gh may take to make an issue.
const issueTimeout = 60 * time.Second

// githubRemote is a remote URL on github.com: https (with or without
// credentials), ssh:// or scp-like, with or without .git.
var githubRemote = regexp.MustCompile(`^(?:https?://(?:[^@/\s]+@)?github\.com/|ssh://git@github\.com(?::\d+)?/|git@github\.com:)` +
	`([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// githubSlug is the owner/name of a remote URL on GitHub, "" for another.
func githubSlug(url string) string {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// GitHub is whether issues can be made for a repository, read again at most
// once every githubEvery.
func (r *Runner) GitHub(repoKey string) (*GitHubStatus, error) {
	repo := r.Repo(repoKey)
	if repo == nil {
		return nil, ErrNotFound
	}
	r.mu.Lock()
	reading, ok := r.github[repoKey]
	r.mu.Unlock()
	if ok && r.now().Sub(reading.at) < githubEvery {
		st := reading.status
		return &st, nil
	}
	st := githubStatus(r.ctx, repo.Path)
	r.mu.Lock()
	r.github[repoKey] = githubReading{status: st, at: r.now()}
	r.mu.Unlock()
	return &st, nil
}

// githubStatus asks git and gh. The remote's URL is never in the answer: it
// may hold credentials.
func githubStatus(ctx context.Context, dir string) GitHubStatus {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return GitHubStatus{Reason: "the repository has no remote called origin"}
	}
	slug := githubSlug(string(out))
	if slug == "" {
		return GitHubStatus{Reason: "the repository's origin is not on GitHub"}
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return GitHubStatus{Repo: slug, Reason: "the GitHub CLI (gh) is not installed on the server"}
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "status", "--hostname", "github.com")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	if err := cmd.Run(); err != nil {
		return GitHubStatus{Repo: slug, Reason: "gh is not signed in to GitHub on the server (gh auth login)"}
	}
	return GitHubStatus{Available: true, Repo: slug}
}

// ErrIssueExists is a bug that has an issue already, or whose issue is being
// made: none is made again. URL is the issue's, empty while it is made.
type ErrIssueExists struct{ URL string }

func (e *ErrIssueExists) Error() string {
	if e.URL == "" {
		return "the bug's issue is being made"
	}
	return "the bug has an issue already: " + e.URL
}

// CreateIssue makes a GitHub issue of a bug with gh, in the repository, and
// keeps its URL on the bug; a bug that has one, or whose issue is being
// made, gets none (ErrIssueExists).
func (r *Runner) CreateIssue(repoKey, bugID, title, body string) (string, error) {
	title = clip(strings.Join(strings.Fields(title), " "), 256)
	if title == "" {
		return "", errors.New("the issue needs a title")
	}
	if len(body) > 60000 {
		return "", errors.New("the issue's text is longer than 60000 characters")
	}
	r.mu.Lock()
	b := r.bugListLocked(repoKey).find(bugID)
	if r.repos[repoKey] == nil || b == nil {
		r.mu.Unlock()
		return "", ErrNotFound
	}
	key := repoKey + "/" + b.ID
	if b.Issue != "" || r.issuing[key] {
		r.mu.Unlock()
		return "", &ErrIssueExists{URL: b.Issue}
	}
	if r.issuing == nil {
		r.issuing = map[string]bool{}
	}
	r.issuing[key] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.issuing, key)
		r.mu.Unlock()
	}()
	st, err := r.GitHub(repoKey)
	if err != nil {
		return "", err
	}
	if !st.Available {
		return "", errors.New("no GitHub issue can be made: " + st.Reason)
	}
	repo := r.Repo(repoKey)
	if repo == nil {
		return "", ErrNotFound
	}
	ctx, cancel := context.WithTimeout(r.ctx, issueTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "create", "--repo", st.Repo, "--title", title, "--body-file", "-")
	cmd.Dir = repo.Path
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	cmd.Stdin = strings.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("gh issue create took longer than %s", issueTimeout)
		}
		return "", fmt.Errorf("gh issue create failed: %v %s", err, tail(stderr.String()+stdout.String(), 400))
	}
	url := ""
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "https://") {
			url = line
		}
	}
	if url == "" {
		return "", fmt.Errorf("gh issue create said no issue address: %s", tail(stdout.String(), 400))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if b = r.bugListLocked(repoKey).find(bugID); b == nil {
		return url, nil
	}
	b.Issue = url
	b.event(r.now().UnixMilli(), "", "GitHub issue created: "+url)
	r.saveBugsLocked(repoKey)
	return url, nil
}
