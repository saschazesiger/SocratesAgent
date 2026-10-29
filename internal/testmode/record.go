// Package testmode runs the test catalogs of repositories
// (docs/design/TEST-MODE.md): a run expands a catalog (internal/testmode/catalog)
// into cells, leases the tools' accounts, starts one agent session per cell
// with as many at once as the settings allow, takes the agents' judgements
// through the test tools (internal/agent/testtools), has doubtful cells
// reviewed and the run analysed, and keeps everything in the data directory.
//
// The files are the truth: a run's run.json and one record.json per attempt
// of a cell, beside the evidence its tools wrote. What the Tests page lists
// and searches is an index of them in memory, made again from them at every
// start.
package testmode

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// The states of a run.
const (
	RunPreparing = "preparing" // snapshot, secrets, setup
	RunRunning   = "running"   // cells queued or running
	RunFinishing = "finishing" // the cells are done; review and analysis
	RunDone      = "done"
	RunStopped   = "stopped"
	RunFailed    = "failed"  // could not be prepared
	RunPaused    = "paused"  // its secrets could not be read again after a restart
	RunWaiting   = "waiting" // another run of the repository is active
)

// Active is whether a run in this state still has work.
func Active(state string) bool {
	return state == RunPreparing || state == RunRunning || state == RunFinishing || state == RunWaiting || state == RunPaused
}

// The states of an attempt.
const (
	AttemptQueued   = "queued"
	AttemptWaiting  = "waiting"
	AttemptStarting = "starting"
	AttemptRunning  = "running"
	AttemptEnded    = "ended"
	AttemptAborted  = "aborted"
	AttemptStopped  = "stopped"
	AttemptFailed   = "failed" // could not be started
)

// Final is whether an attempt in this state is over.
func Final(state string) bool {
	return state == AttemptEnded || state == AttemptAborted || state == AttemptStopped || state == AttemptFailed
}

// The states of a cell's review.
const (
	ReviewPending = "pending"
	ReviewRunning = "running"
	ReviewDone    = "done"
	ReviewSkipped = "skipped"
)

// Role is who runs a kind of session: harness, model and effort, and the
// accounts of the harness it may run on (Accounts; none ticked is every
// account), of which the runner takes the best for each session. A
// session's record has the one it took in Account.
type Role struct {
	Harness  string   `json:"harness"`
	Model    string   `json:"model,omitempty"`
	Effort   string   `json:"effort,omitempty"`
	Accounts []string `json:"accounts,omitempty"`
	Account  string   `json:"account,omitempty"`
}

// Run is a run's run.json.
type Run struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
	// Number is the run's number in its repository: 1, 2, 3 ... in the order
	// the runs were created ("Run 12").
	Number    int       `json:"number"`
	Path      string    `json:"path"`
	Name      string    `json:"name,omitempty"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	Selection Selection `json:"selection"`
	Settings  Settings  `json:"settings"`
	// Commit is the repository's HEAD when the run started, Dirty whether its
	// working tree had changes.
	Commit    string `json:"commit,omitempty"`
	Dirty     bool   `json:"dirty,omitempty"`
	CreatedAt int64  `json:"created_at"`
	StartedAt int64  `json:"started_at,omitempty"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	Pinned    bool   `json:"pinned,omitempty"`
	// Order is the run's attempts in the order they were queued.
	Order []AttemptRef `json:"order"`
	// Reviews are the reviewer sessions, Analysis the analyst's.
	Reviews  []*Helper `json:"reviews,omitempty"`
	Analysis *Helper   `json:"analysis,omitempty"`
	// Analysed is when the analyst last finished.
	Analysed int64 `json:"analysed,omitempty"`
	// Notified is whether the end of the run was pushed.
	Notified bool `json:"notified,omitempty"`
}

// AttemptRef names an attempt and where its record is.
type AttemptRef struct {
	ID   string `json:"id"`
	Test string `json:"test"`
	Cell string `json:"cell"`
	N    int    `json:"n"`
}

// Helper is a reviewer's or the analyst's session.
type Helper struct {
	ID    string   `json:"id"`
	Chat  string   `json:"chat,omitempty"`
	Cells []string `json:"cells,omitempty"`
	// Attempts is the attempt of each of Cells a reviewer was given.
	Attempts  []string `json:"attempts,omitempty"`
	State     string   `json:"state"` // running, done, failed
	Role      Role     `json:"role"`
	StartedAt int64    `json:"started_at"`
	EndedAt   int64    `json:"ended_at,omitempty"`
	Nudged    int      `json:"nudged,omitempty"`
	Error     string   `json:"error,omitempty"`
	// Wrote is what the analyst has written: "bugs", "conclusions".
	Wrote []string `json:"wrote,omitempty"`
}

// Attempt is one attempt of a cell, its record.json.
type Attempt struct {
	ID       string   `json:"id"`
	Run      string   `json:"run"`
	Repo     string   `json:"repo"`
	Test     string   `json:"test"`
	Area     string   `json:"area"`
	Title    string   `json:"title"`
	Priority string   `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
	// Cell is the cell's dimension values joined ("pixel.es"), CellID the
	// test's id and it ("web-browser/header-navigation@pixel.es").
	Cell      string            `json:"cell"`
	CellID    string            `json:"cell_id"`
	N         int               `json:"attempt"`
	Dims      []Dim             `json:"dims,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Tools     []string          `json:"tools,omitempty"`
	Exclusive []string          `json:"exclusive,omitempty"`
	Evidence  string            `json:"evidence"`
	TimeoutMS int64             `json:"timeout_ms"`
	// Context is the test's text besides its steps, which the agent's first
	// message carries.
	Context string `json:"context,omitempty"`

	State     string  `json:"state"`
	EndReason string  `json:"end_reason,omitempty"`
	Wait      string  `json:"wait,omitempty"`
	Chat      string  `json:"chat,omitempty"`
	Role      Role    `json:"role"`
	Leases    []Lease `json:"leases,omitempty"`
	QueuedAt  int64   `json:"queued_at"`
	StartedAt int64   `json:"started_at,omitempty"`
	EndedAt   int64   `json:"ended_at,omitempty"`
	WaitedMS  int64   `json:"waited_ms,omitempty"`

	Steps   []Step   `json:"steps"`
	Actions []Action `json:"actions,omitempty"`
	// Offset is how much of the evidence's actions.jsonl has been read, and
	// Judged the number of the last action a judgement took.
	Offset int64 `json:"offset,omitempty"`
	Judged int   `json:"judged,omitempty"`

	Verdict  string `json:"verdict,omitempty"`
	Reviewed string `json:"reviewed,omitempty"`
	// Override is a person's verdict of the attempt, which counts over the
	// agent's and the reviewer's.
	Override *Override         `json:"override,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Review   *Review           `json:"review,omitempty"`
	Bugs     []string          `json:"bugs,omitempty"`
	Links    map[string]string `json:"links,omitempty"`
	Nudged   int               `json:"nudged,omitempty"`
	// Retried is the id of the attempt queued after this one.
	Retried string `json:"retried,omitempty"`
}

// Dim is one of a cell's dimension values.
type Dim struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// Lease is a pool member an attempt holds.
type Lease struct {
	Tool   string `json:"tool"`
	Member int    `json:"member"` // 1-based
	Label  string `json:"label,omitempty"`
	// MCP marks a lease of an MCP server built into Socrates (Tool is its
	// name), whose pool keeps the leases (Host.MCPPool); Account is the
	// account's id there.
	MCP     bool   `json:"mcp,omitempty"`
	Account string `json:"account,omitempty"`
}

// Step is a step of the test with its judgement.
type Step struct {
	N        int         `json:"n"`
	Do       string      `json:"do"`
	Expect   string      `json:"expect"`
	Verdict  string      `json:"verdict,omitempty"`
	Observed string      `json:"observed,omitempty"`
	Actions  []int       `json:"actions,omitempty"`
	Attach   []string    `json:"attach,omitempty"`
	At       int64       `json:"at,omitempty"`
	Review   *StepReview `json:"review,omitempty"`
}

// StepReview is a reviewer's correction of a step.
type StepReview struct {
	Verdict string `json:"verdict"`
	Note    string `json:"note"`
	At      int64  `json:"at"`
}

// Review is a cell's review.
type Review struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Note   string `json:"note,omitempty"`
	Helper string `json:"helper,omitempty"`
	At     int64  `json:"at,omitempty"`
}

// Action is one thing a tool did, as it wrote it into the evidence
// (actions.jsonl), numbered from 1 in the attempt.
type Action struct {
	N           int             `json:"n"`
	At          string          `json:"at"`
	Tool        string          `json:"tool,omitempty"`
	Action      string          `json:"action"`
	Summary     string          `json:"summary"`
	URL         string          `json:"url,omitempty"`
	Screenshot  string          `json:"screenshot,omitempty"`
	MS          int64           `json:"ms,omitempty"`
	Error       string          `json:"error,omitempty"`
	Interaction bool            `json:"interaction,omitempty"`
	Details     json.RawMessage `json:"details,omitempty"`
}

// Override is a person's verdict of an attempt, with why.
type Override struct {
	Verdict string `json:"verdict"`
	Note    string `json:"note,omitempty"`
	At      int64  `json:"at"`
}

// Final verdict of an attempt, as it counts: a person's override, else the
// reviewed one where a reviewer changed it, else the agent's.
func (a *Attempt) Final() string {
	if a.Override != nil && a.Override.Verdict != "" {
		return a.Override.Verdict
	}
	if a.Reviewed != "" {
		return a.Reviewed
	}
	return a.Verdict
}

// StepVerdict is a step's verdict as it counts: the reviewed one if any.
func (s *Step) StepVerdict() string {
	if s.Review != nil && s.Review.Verdict != "" {
		return s.Review.Verdict
	}
	return s.Verdict
}

// worst is the worst verdict of the steps as they count, and whether every
// step is judged.
func (a *Attempt) worst(reviewed bool) (string, bool) {
	v, all := "", true
	for i := range a.Steps {
		s := &a.Steps[i]
		sv := s.Verdict
		if reviewed {
			sv = s.StepVerdict()
		}
		if sv == "" {
			all = false
			continue
		}
		v = testtools.Worse(v, sv)
	}
	return v, all
}

// passed is whether a verdict counts as passed for the review rule.
func passed(v string) bool { return v == testtools.Pass || v == testtools.NA }

// Layout names the files of the test runs in the data directory.
type Layout struct{ Data string }

// RepoDir is a repository's folder.
func (l Layout) RepoDir(repo string) string {
	return filepath.Join(l.Data, paths.Tests, repo)
}

// RunDir is a run's folder.
func (l Layout) RunDir(repo, run string) string {
	return filepath.Join(l.RepoDir(repo), "runs", run)
}

// AttemptDir is an attempt's folder: the cell's folder in the run, and the
// attempt's number in it.
func (l Layout) AttemptDir(a *Attempt) string {
	cell := a.Cell
	if cell == "" {
		cell = "_"
	}
	return filepath.Join(l.RunDir(a.Repo, a.Run), "cells", filepath.FromSlash(a.Test), cell, fmt.Sprint(a.N))
}

// EvidenceDir is where the attempt's tools write.
func (l Layout) EvidenceDir(a *Attempt) string { return filepath.Join(l.AttemptDir(a), "evidence") }

// RuntimeDir is the attempt's runtime folder, short so that a socket in it
// stays within sun_path.
func (l Layout) RuntimeDir(id string) string {
	return filepath.Join(l.Data, paths.TestsRuntime, id)
}

// RunRuntimeDir is a run's runtime folder: its vault and its tools.
func (l Layout) RunRuntimeDir(run string) string {
	return filepath.Join(l.Data, paths.TestsRuntime, run)
}

// ToolCache is the installed copies of the tools.
func (l Layout) ToolCache() string { return filepath.Join(l.Data, paths.TestsCache, "tools") }

var keyUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// RepoKey is a repository's key: its folder's name and a short hash of its
// path, so that two checkouts of one repository are two keys.
func RepoKey(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	name := keyUnsafe.ReplaceAllString(strings.ToLower(filepath.Base(path)), "-")
	name = strings.Trim(name, "-")
	if len(name) > 32 {
		name = name[:32]
	}
	if name == "" {
		name = "repo"
	}
	return name + "-" + hex.EncodeToString(sum[:4])
}

// ValidRepoKey reports whether key could be a RepoKey, so that it can name a
// folder.
func ValidRepoKey(key string) bool {
	return key != "" && len(key) <= 48 && !keyUnsafe.MatchString(key) && !strings.HasPrefix(key, "-")
}

var runIDPattern = regexp.MustCompile(`^r-[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// ValidRunID reports whether id is a run id.
func ValidRunID(id string) bool { return runIDPattern.MatchString(id) }

func newRunID(now time.Time) string {
	return "r-" + now.UTC().Format("20060102-150405") + "-" + randomHex(2)
}

var attemptIDPattern = regexp.MustCompile(`^a[0-9a-f]{10}$`)

// ValidAttemptID reports whether id is an attempt id.
func ValidAttemptID(id string) bool { return attemptIDPattern.MatchString(id) }

func newAttemptID() string { return "a" + randomHex(5) }

func newHelperID() string { return "h" + randomHex(5) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// writeJSON writes v as JSON, atomically.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
