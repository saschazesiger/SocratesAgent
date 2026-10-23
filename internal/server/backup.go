package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/backup"
)

// The backup: the admin page's Backup card, and the restore the setup page
// offers a new machine. The engine itself is built in main beside the server
// and handed over; the server only shows it, configures it and asks it to
// run.
//
//	GET  /api/backup             backup.Status
//	POST /api/backup/run         202 {"status"}; 409 while one runs or with none configured
//	GET  /api/backup/snapshots   {"snapshots": [backup.Summary]}
//	POST /api/backup/test        {"result": backup.TestResult}
//	PUT  /api/backup/config      {"status"}; 400 with what the bucket said
//	DELETE /api/backup/config    {"status"}: backups off, the bucket left as it is
//
// The restore is the one part of it that is reached without a password,
// because it is what a machine with none is set up from - and only while
// there is none and the data directory holds nothing (backup.HoldsStore):
//
//	POST /api/restore/snapshots  {config} → {"snapshots": [backup.Summary]}
//	POST /api/restore            {config, snapshot} → 202 {"restore"}
//	GET  /api/restore            {"restore": restoreState}
//
// A finished restore writes backup.json - so the machine goes on backing up
// into the bucket it came from, as a machine of its own - and restarts
// Socrates, which moves the restored files into place before it opens the
// database (backup.ApplyPending).
//
// GET /api/state carries "backup": {"warning"} for a signed-in page, so the
// notice that no backup has succeeded for a day costs no request of its own.

// SetBackup hands the server its backup engine.
func (s *Server) SetBackup(e *backup.Engine) { s.backup = e }

// SetRestart installs what a finished restore calls to start Socrates again
// on the restored data directory.
func (s *Server) SetRestart(restart func()) { s.restart = restart }

// BackupExclusions are the folders inside the data directory the backup
// leaves out besides its own rules: the workspace root and the preset
// directories, where the code is - which lives in git, not in a backup.
func (s *Server) BackupExclusions() []string {
	settings := s.Settings()
	out := []string{settings.Workspace.Root}
	for _, p := range settings.Workspace.Presets {
		out = append(out, p.Path)
	}
	for i, p := range out {
		out[i] = filepath.Clean(p)
	}
	return out
}

// backupSummary is the part of the status /api/state carries.
func (s *Server) backupSummary() map[string]any {
	if s.backup == nil {
		return map[string]any{"configured": false}
	}
	return map[string]any{"configured": s.backup.Config() != nil, "warning": s.backup.Warning()}
}

func (s *Server) backupEngine(w http.ResponseWriter) *backup.Engine {
	if s.backup == nil {
		writeError(w, http.StatusConflict, "this Socrates has no backup engine")
	}
	return s.backup
}

func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	e := s.backupEngine(w)
	if e == nil {
		return
	}
	writeJSON(w, http.StatusOK, e.Status())
}

func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	e := s.backupEngine(w)
	if e == nil {
		return
	}
	// The run belongs to the server, not to the request: it goes on after
	// the answer, which is that it started.
	if err := e.RunNow(context.Background()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "status": e.Status()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": e.Status()})
}

func (s *Server) handleBackupSnapshots(w http.ResponseWriter, r *http.Request) {
	e := s.backupEngine(w)
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	list, err := e.Snapshots(ctx)
	if errors.Is(err, backup.ErrNotConfigured) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, backup.Explain(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

// backupForm is the Backup card's form. Empty secrets keep the saved ones.
type backupForm struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	PathStyle       bool   `json:"path_style"`
	Passphrase      string `json:"passphrase"`
	IntervalMinutes int    `json:"interval_minutes"`
}

func (f backupForm) config() backup.Config {
	return backup.Config{
		Endpoint: f.Endpoint, Region: f.Region, Bucket: f.Bucket, Prefix: f.Prefix,
		AccessKeyID: f.AccessKeyID, SecretAccessKey: f.SecretAccessKey, PathStyle: f.PathStyle,
		Passphrase: f.Passphrase, IntervalMinutes: f.IntervalMinutes,
	}
}

func (s *Server) handleBackupTest(w http.ResponseWriter, r *http.Request) {
	e := s.backupEngine(w)
	if e == nil {
		return
	}
	var body backupForm
	if !readJSON(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"result": e.Test(ctx, body.config())})
}

func (s *Server) handleBackupConfig(w http.ResponseWriter, r *http.Request) {
	e := s.backupEngine(w)
	if e == nil {
		return
	}
	var body backupForm
	if !readJSON(w, r, &body) {
		return
	}
	if body.IntervalMinutes != 0 && body.IntervalMinutes < int(backup.MinInterval/time.Minute) {
		writeError(w, http.StatusBadRequest, "a backup runs at most every 5 minutes")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := e.SetConfig(ctx, body.config()); err != nil {
		writeError(w, http.StatusBadRequest, backup.Explain(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": e.Status()})
}

// handleBackupOff turns backups off. What the bucket holds stays there, and
// saving the card again turns them back on.
func (s *Server) handleBackupOff(w http.ResponseWriter, r *http.Request) {
	e := s.backupEngine(w)
	if e == nil {
		return
	}
	if err := e.Disable(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": e.Status()})
}

// ------------------------------------------------------------------ restore

// restoreJob is the one restore a new machine may run.
type restoreJob struct {
	mu       sync.Mutex
	state    string // "", "running", "done", "failed"
	progress backup.Progress
	err      string
}

// restoreState is GET /api/restore.
type restoreState struct {
	State    string          `json:"state"`
	Progress backup.Progress `json:"progress"`
	Error    string          `json:"error,omitempty"`
}

func (j *restoreJob) snapshot() restoreState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return restoreState{State: j.state, Progress: j.progress, Error: j.err}
}

// restoring reports whether a restore runs or has finished, which is when
// the setup page must not create a password over it.
func (j *restoreJob) restoring() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == "running" || j.state == "done"
}

// restoreAllowed answers whether this machine may be restored onto, and
// writes the refusal when it may not.
func (s *Server) restoreAllowed(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross origin request rejected")
		return false
	}
	if s.hasPassword() {
		writeError(w, http.StatusConflict, "this Socrates is already set up; a backup is restored onto a new one")
		return false
	}
	return true
}

// restoreAvailable is whether the setup page offers a restore at all.
func (s *Server) restoreAvailable() bool {
	if s.hasPassword() {
		return false
	}
	holds, _, err := backup.HoldsStore(s.dataDir)
	return err == nil && !holds
}

// restoreForm is what the setup page sends: where the backup is, and which
// snapshot to restore.
type restoreForm struct {
	backupForm
	Snapshot string `json:"snapshot"`
}

func (s *Server) handleRestoreSnapshots(w http.ResponseWriter, r *http.Request) {
	if !s.restoreAllowed(w, r) {
		return
	}
	var body restoreForm
	if !readJSON(w, r, &body) {
		return
	}
	cfg := body.config()
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	list, err := backup.List(ctx, cfg, s.dataDir)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, backup.ErrWrongPassphrase) {
			code = http.StatusUnauthorized
		} else if errors.Is(err, backup.ErrNoBackup) {
			code = http.StatusNotFound
		}
		writeError(w, code, backup.Explain(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

func (s *Server) handleRestoreStart(w http.ResponseWriter, r *http.Request) {
	if !s.restoreAllowed(w, r) {
		return
	}
	var body restoreForm
	if !readJSON(w, r, &body) {
		return
	}
	cfg := body.config()
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if holds, why, err := backup.HoldsStore(s.dataDir); err != nil || holds {
		if err == nil {
			err = errors.New(why)
		}
		writeError(w, http.StatusConflict, backup.ErrNotEmpty.Error()+": "+err.Error())
		return
	}
	j := &s.restoreJob
	j.mu.Lock()
	if j.state == "running" || j.state == "done" {
		j.mu.Unlock()
		writeError(w, http.StatusConflict, "a restore is already running")
		return
	}
	j.state, j.err, j.progress = "running", "", backup.Progress{Snapshot: body.Snapshot}
	j.mu.Unlock()

	go s.runRestore(cfg, body.Snapshot)
	writeJSON(w, http.StatusAccepted, map[string]any{"restore": j.snapshot()})
}

// runRestore stages the snapshot, writes backup.json for the machine it now
// is, and restarts Socrates onto it.
func (s *Server) runRestore(cfg backup.Config, snapshot string) {
	j := &s.restoreJob
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()
	summary, err := backup.Restore(ctx, backup.RestoreOptions{
		Config: cfg, Snapshot: snapshot, DataDir: s.dataDir,
		Progress: func(p backup.Progress) {
			j.mu.Lock()
			j.progress = p
			j.mu.Unlock()
		},
	})
	if err == nil {
		// A restored machine is a machine of its own: the same bucket, a
		// host id of its own.
		cfg.HostID = ""
		err = backup.SaveConfig(s.dataDir, cfg)
	}
	j.mu.Lock()
	if err != nil {
		j.state, j.err = "failed", backup.Explain(err)
		j.mu.Unlock()
		log.Printf("restore: %v", err)
		return
	}
	j.state = "done"
	j.mu.Unlock()
	log.Printf("restore: snapshot %s (%d files) is staged; restarting onto it", summary.ID, summary.Files)
	if s.restart != nil {
		// A moment for the page to hear that it is done.
		time.AfterFunc(time.Second, s.restart)
	}
}

func (s *Server) handleRestoreStatus(w http.ResponseWriter, r *http.Request) {
	if s.hasPassword() && !s.restoreJob.restoring() {
		writeJSON(w, http.StatusOK, map[string]any{"restore": restoreState{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restore": s.restoreJob.snapshot()})
}
