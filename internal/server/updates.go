package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The updates card of the dashboard: what scripts/update.sh did the last time
// it ran, when it runs next, and a button that runs it now.
//
// The updater is not part of this process and must not be. It replaces the
// Socrates binary and restarts socrates.service, so it runs as a unit of its
// own - socrates-update.service, started daily by socrates-update.timer - and
// this file only reads what it leaves behind and asks systemd to start it. A
// child of this server would be killed by the very restart it performs.

const (
	updateService = "socrates-update.service"
	updateTimer   = "socrates-update.timer"

	// updatesFile and updatesLog are the updater's report and the full
	// output of its last run, both in the data directory.
	updatesFile = "updates.json"
	updatesLog  = "updates.log"
	// nextTriggerFile is where the admin page leaves the name of the next
	// run's trigger. update.sh reads it when it starts, deletes it, and
	// writes it into the report, so a run somebody asked for here says
	// "admin" rather than "manual". It lives in the updater's own state
	// directory, which is scripts/lib.sh's STATE.
	nextTriggerFile = "update-state/next-trigger"

	// logTailLines is how much of the last run the card shows. The end is
	// where a failure is; the whole file is on disk for anybody who wants it.
	logTailLines = 60
	// logTailBytes bounds what is read to find those lines, so a run that
	// printed a megabyte of progress bars costs no more than one that did not.
	logTailBytes = 64 << 10

	// systemctlTimeout is how long one question to systemd may take. It
	// answers in milliseconds; a systemd that does not is a machine in
	// trouble, and the card must still be drawn.
	systemctlTimeout = 5 * time.Second

	// updatesStale is how old a successful run may be before the setup check
	// stops calling it good: two days is one missed timer and some slack.
	updatesStale = 48 * time.Hour
)

// commandRunner runs a program and returns what it printed on stdout. It is a
// field rather than a call to exec so that the tests can be a machine with or
// without systemd, and with or without the updater installed.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// execRunner is the real machine. A failure carries what the program said on
// stderr, because "exit status 1" alone tells the dashboard nothing.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out, err
}

// updatesAdmin is the card's state. Like tmuxAdmin its zero value is the real
// machine, so it needs nothing from New; the tests set run and now.
type updatesAdmin struct {
	run commandRunner
	now func() time.Time
}

func (u *updatesAdmin) runner() commandRunner {
	if u.run != nil {
		return u.run
	}
	return execRunner
}

func (u *updatesAdmin) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

// updateComponent is one program the updater looked after. Its fields are the
// updater's contract (scripts/update.sh), not this server's choice.
type updateComponent struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Previous string `json:"previous"`
	Status   string `json:"status"`
	Error    string `json:"error"`
	Detail   string `json:"detail"`
}

// updateStatus is updates.json: the last run, or the one running now.
type updateStatus struct {
	Schema     int               `json:"schema"`
	State      string            `json:"state"`
	Trigger    string            `json:"trigger"`
	StartedAt  string            `json:"started_at"`
	FinishedAt string            `json:"finished_at"`
	OK         bool              `json:"ok"`
	Components []updateComponent `json:"components"`
}

// updateTimerState is what systemd says about the timer that runs the updater.
type updateTimerState struct {
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Next      string `json:"next"`
	Last      string `json:"last"`
}

// readUpdateStatus is the updater's last report, or nil when there is none
// that can be read. A missing file is the ordinary case on a machine that has
// never updated; a torn or foreign one is treated the same, because the card
// would rather say "never" than draw half a report.
func readUpdateStatus(dataDir string) *updateStatus {
	raw, err := os.ReadFile(filepath.Join(dataDir, updatesFile))
	if err != nil {
		return nil
	}
	var status updateStatus
	if err := json.Unmarshal(raw, &status); err != nil || status.State == "" {
		return nil
	}
	return &status
}

// readLogTail is the end of the last run's output, cleaned for a page: the
// escapes an installer colours its output with are dropped, and a line a
// progress bar redrew with carriage returns is kept as it last looked.
func readLogTail(dataDir string, lines int) string {
	f, err := os.Open(filepath.Join(dataDir, updatesLog))
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := info.Size() - logTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	text := StripANSI(string(raw))
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	// A read that started mid-file started mid-line, and that first piece is
	// not a line anybody printed.
	if offset > 0 && len(all) > 1 {
		all = all[1:]
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	for i, line := range all {
		line = strings.TrimRight(line, "\r")
		if cut := strings.LastIndexByte(line, '\r'); cut >= 0 {
			line = line[cut+1:]
		}
		all[i] = line
	}
	return strings.Join(all, "\n")
}

// systemctlShow asks systemd for some properties of one unit. A machine
// without systemctl answers with an error the callers read as "not
// installed": a development run on a laptop is the ordinary case.
func (u *updatesAdmin) systemctlShow(ctx context.Context, unit string, props ...string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, systemctlTimeout)
	defer cancel()
	out, err := u.runner()(ctx, "systemctl", "show", unit, "-p", strings.Join(props, ","))
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	return values, nil
}

// unitLoaded is whether systemd knows a unit at all. systemctl show succeeds
// for a unit that does not exist and says so in LoadState, which is why the
// answer is read rather than the exit code.
func unitLoaded(values map[string]string) bool {
	state := values["LoadState"]
	return state != "" && state != "not-found"
}

// timer reads the timer that runs the updater.
func (u *updatesAdmin) timer(ctx context.Context) updateTimerState {
	values, err := u.systemctlShow(ctx, updateTimer,
		"LoadState", "UnitFileState", "NextElapseUSecRealtime", "LastTriggerUSec")
	if err != nil || !unitLoaded(values) {
		return updateTimerState{}
	}
	fileState := values["UnitFileState"]
	return updateTimerState{
		Installed: true,
		Enabled:   fileState == "enabled" || fileState == "enabled-runtime",
		Next:      systemdTime(values["NextElapseUSecRealtime"]),
		Last:      systemdTime(values["LastTriggerUSec"]),
	}
}

// service reads the updater's own unit: whether it is there, and whether it
// is running now. A oneshot unit is "activating" for the whole of its run.
func (u *updatesAdmin) service(ctx context.Context) (installed, running bool) {
	values, err := u.systemctlShow(ctx, updateService, "LoadState", "ActiveState")
	if err != nil || !unitLoaded(values) {
		return false, false
	}
	state := values["ActiveState"]
	return true, state == "activating" || state == "active"
}

// systemdTimeLayout is how systemctl show prints a timestamp, in the
// machine's own zone: "Thu 2026-10-23 04:17:00 UTC".
const systemdTimeLayout = "Mon 2006-01-02 15:04:05 MST"

// systemdTime turns a systemd timestamp into RFC 3339, so the page can say
// "in 9 h" without knowing systemd. The zone is the machine's, which is why
// it is parsed in time.Local: there an abbreviation such as CEST has its real
// offset, where time.Parse would invent a zone of offset zero for it. What
// cannot be parsed is passed through, because a raw date is still better than
// none; what means "never" becomes empty.
func systemdTime(raw string) string {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "", "n/a", "0":
		return ""
	}
	at, err := time.ParseInLocation(systemdTimeLayout, raw, time.Local)
	if err != nil {
		return raw
	}
	return at.UTC().Format(time.RFC3339)
}

// autoUpdate is what the updater is allowed to update by itself, as the
// environment both units share says. Unset means everything, which is the
// default install.
func autoUpdate() string {
	if value := strings.TrimSpace(os.Getenv("SOCRATES_AUTO_UPDATE")); value != "" {
		return value
	}
	return "all"
}

// handleUpdates is the updates card: the last report, the end of its output,
// the timer, and whether a run is going on now. Nothing is cached: the card
// polls slowly unless a run is going, and two systemctl calls are cheap.
func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	timer := s.updates.timer(r.Context())
	_, running := s.updates.service(r.Context())
	var status any
	if report := readUpdateStatus(s.dataDir); report != nil {
		status = report
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   status,
		"log_tail": readLogTail(s.dataDir, logTailLines),
		"timer":    timer,
		"running":  running,
		"auto":     autoUpdate(),
	})
}

// handleUpdatesRun starts the updater now. It asks systemd to start the unit
// and returns without waiting, because the run takes minutes and ends, when it
// updated Socrates, by restarting the process that would be waiting for it.
// The card polls for the outcome instead.
func (s *Server) handleUpdatesRun(w http.ResponseWriter, r *http.Request) {
	installed, running := s.updates.service(r.Context())
	if !installed {
		writeError(w, http.StatusConflict,
			"the updater is not installed on this machine; scripts/install.sh sets it up")
		return
	}
	// A second start of a oneshot that is running is no new run: systemd
	// folds it into the one going on, which would then be labelled as asked
	// for here when it was not. The card already shows that run.
	if running {
		writeError(w, http.StatusConflict, "an update is already running")
		return
	}
	marker := filepath.Join(s.dataDir, nextTriggerFile)
	if err := writeNextTrigger(marker, "admin"); err != nil {
		writeError(w, http.StatusInternalServerError, "could not label the run: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), systemctlTimeout)
	defer cancel()
	if _, err := s.updates.runner()(ctx, "systemctl", "start", "--no-block", updateService); err != nil {
		// The label is for the run that did not start. Left behind, it
		// would name the next timer run as one somebody asked for.
		os.Remove(marker)
		writeError(w, http.StatusBadGateway, "systemd would not start the updater: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// writeNextTrigger leaves the trigger for the updater's next run. The
// directory is the updater's state, which nobody but the two services has
// any business in, hence the modes. update.sh keeps only lower-case letters
// of what it reads, so the newline is for a person reading the file.
func writeNextTrigger(path, trigger string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(trigger+"\n"), 0o600)
}

// updatesCheck is the setup check's row for the updater. It is good when the
// last run finished, every component with it, and not so long ago that the
// timer has evidently stopped firing.
func (s *Server) updatesCheck(ctx context.Context) checkResult {
	row := checkResult{Name: "Updates"}
	timer := s.updates.timer(ctx)
	report := readUpdateStatus(s.dataDir)
	if report != nil {
		row.Detail = componentLines(report.Components)
	}
	_, running := s.updates.service(ctx)
	switch {
	case !timer.Installed:
		row.Summary = "the update timer is not installed"
	case running || (report != nil && report.State == "running"):
		// A run in progress is not a failure, and the report it is writing
		// says nothing yet about how it will end.
		row.OK, row.Summary = true, "running now"
	case report == nil:
		row.Summary = "no update has run yet"
	case report.State == "failed":
		row.Summary = "the last run was aborted"
		if failed := firstFailed(report.Components); failed != "" {
			row.Summary = "last run failed: " + failed
		}
	case !report.OK:
		row.Summary = "last run failed"
		if failed := firstFailed(report.Components); failed != "" {
			row.Summary += ": " + failed
		}
	default:
		finished, err := time.Parse(time.RFC3339, report.FinishedAt)
		age := s.updates.clock().Sub(finished)
		switch {
		case err != nil:
			row.Summary = "the last run has no end time"
		case age > updatesStale:
			row.Summary = "last run " + agoText(age)
		case !timer.Enabled:
			row.Summary = "the update timer is off"
		default:
			row.OK, row.Summary = true, "checked "+agoText(age)
		}
	}
	return row
}

// firstFailed names the first component that failed, with its error, for the
// line the setup check shows.
func firstFailed(components []updateComponent) string {
	for _, c := range components {
		if c.Status == "failed" {
			if c.Error == "" {
				return c.Name
			}
			return c.Name + ": " + clean(c.Error)
		}
	}
	return ""
}

// componentLines is the report one component per line, for the row's "i".
func componentLines(components []updateComponent) string {
	lines := make([]string, 0, len(components))
	for _, c := range components {
		line := c.Name + " " + c.Version
		switch {
		case c.Status == "updated" && c.Previous != "":
			line += " (updated from " + c.Previous + ")"
		case c.Status != "":
			line += " (" + c.Status + ")"
		}
		if c.Error != "" {
			line += ": " + c.Error
		} else if c.Detail != "" {
			line += ": " + c.Detail
		}
		lines = append(lines, clean(line))
	}
	return strings.Join(lines, "\n")
}

// agoText is a past duration as somebody reads it.
func agoText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
	}
}
