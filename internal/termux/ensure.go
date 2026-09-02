package termux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// startingGrace is how long a session that is still being created gets before
// a viewer is told it will not start. Creating one is a handful of tmux
// commands, so ten seconds is not patience, it is the point at which something
// is wrong.
const startingGrace = 10 * time.Second

// Ensure makes a session ready to be watched, and is what every viewer calls
// before it attaches.
//
// It is the whole of the recovery story seen from the browser: a session that
// is running is returned as it is, one whose machine was rebooted is started
// again on its own conversation, and one that failed is handed back with the
// reason so the page can offer a restart rather than an empty terminal.
//
// Nothing is resumed eagerly anywhere else: coming back from a reboot with
// forty stored sessions must not start forty programs, so the relaunch happens
// here, when somebody actually opens one.
func (m *Manager) Ensure(ctx context.Context, sessionID string) (*store.Session, error) {
	row, err := m.st.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	switch row.State {
	case store.StateRunning, store.StateExited, store.StateFailed:
		// Exited attaches too: the pane is dead but the screen it left is
		// still there, and the exit overlay is drawn over it.
		return row, nil
	case store.StateStarting:
		return m.waitForStart(ctx, row)
	case store.StateNeedsResume:
		return m.resume(ctx, row)
	}
	return row, nil
}

// Restart is the exit overlay's button: the session is put back into the state
// a reboot would have left it in, and resumed from there. A session whose pane
// is still alive is refused rather than replaced.
func (m *Manager) Restart(ctx context.Context, sessionID string) (*store.Session, error) {
	row, err := m.st.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if err := m.st.SetSessionState(row.ID, store.StateNeedsResume, -1, ""); err != nil {
		return nil, err
	}
	row.State, row.ExitStatus, row.FailReason = store.StateNeedsResume, -1, ""
	return m.resume(ctx, row)
}

// waitForStart gives a create that is still running its grace, and calls it a
// failure when the grace runs out. A row left in starting for ever is the one
// outcome a viewer cannot do anything with.
func (m *Manager) waitForStart(ctx context.Context, row *store.Session) (*store.Session, error) {
	deadline := time.Now().Add(startingGrace)
	for {
		select {
		case <-ctx.Done():
			return row, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		current, err := m.st.GetSession(row.ID)
		if err != nil {
			return nil, err
		}
		if current.State != store.StateStarting {
			return m.Ensure(ctx, row.ID)
		}
		if time.Now().After(deadline) {
			reason := fmt.Sprintf("the session did not start within %s", startingGrace)
			_ = m.st.SetSessionState(row.ID, store.StateFailed, -1, reason)
			current.State, current.FailReason = store.StateFailed, reason
			return current, nil
		}
	}
}

// resume starts the program again on the conversation the row remembers.
//
// The verification before it is not a nicety: an id that is gone makes
// `claude --resume` and `opencode --session` exit immediately, so a resume
// that cannot work has to become a fresh session here rather than a pane that
// dies on sight. Only a proven id is passed on; anything else - gone, or a
// question that could not be answered - starts fresh, which costs the user one
// conversation they can still find in the CLI's own picker.
func (m *Manager) resume(ctx context.Context, row *store.Session) (*store.Session, error) {
	if err := m.unavailable; err != nil {
		return nil, err
	}
	h, ok := harnesses.Get(row.Harness)
	if !ok {
		return nil, fmt.Errorf("%q is not a harness this Socrates knows", row.Harness)
	}
	if m.cfg.Settings == nil {
		return nil, errors.New("this manager was built without settings, so it cannot plan a resume")
	}
	req := harnesses.PlanRequest{
		SessionID:  row.ID,
		Title:      row.Title,
		Cwd:        row.Workdir,
		Model:      row.Model,
		Effort:     row.Effort,
		CLISession: strings.TrimSpace(row.CLISessionID),
		Settings:   m.cfg.Settings(),
		DataDir:    m.cfg.DataDir,
	}
	if row.CLISessionState == store.CLILost {
		req.CLISession = ""
	}
	verified := false
	if req.CLISession != "" {
		alive, err := h.VerifyCLISession(ctx, req)
		if err != nil {
			m.logf("could not tell whether session %s can still be resumed: %v", row.ID, err)
			alive = false
		}
		if alive {
			verified = true
		} else {
			_ = m.st.SetSessionCLI(row.ID, "", store.CLILost)
			req.CLISession = ""
		}
	}

	var plan harnesses.LaunchPlan
	var err error
	if req.CLISession != "" {
		plan, err = h.ResumePlan(ctx, req)
		if errors.Is(err, harnesses.ErrNoResume) {
			req.CLISession = ""
			verified = false
		} else if err != nil {
			return m.planFailed(row, err)
		}
	}
	if req.CLISession == "" {
		if plan, err = h.Plan(ctx, req); err != nil {
			return m.planFailed(row, err)
		}
	}

	if err := m.Relaunch(ctx, row, plan); err != nil {
		return m.st.GetSession(row.ID)
	}
	cliState := cliStateFor(plan)
	if verified {
		cliState = store.CLIVerified
	}
	if err := m.st.SetSessionCLI(row.ID, plan.CLISession, cliState); err != nil {
		m.logf("could not record the conversation id of session %s: %v", row.ID, err)
	}
	if err := m.st.NoteResume(row.ID); err != nil {
		m.logf("could not record the resume of session %s: %v", row.ID, err)
	}
	return m.st.GetSession(row.ID)
}

// planFailed records a launch that could not even be planned - a binary that
// is gone, a generated file that could not be written - as a failure with its
// reason, which is what the browser draws its overlay from.
func (m *Manager) planFailed(row *store.Session, err error) (*store.Session, error) {
	_ = m.st.SetSessionState(row.ID, store.StateFailed, -1, err.Error())
	return nil, err
}

// ---------------------------------------------------------- id discovery

// watchCLISession learns the program's own session id for the two harnesses
// that will not be told one, and is what arms a later resume.
//
// It runs detached because it can take as long as the user takes: neither
// Codex nor OpenCode has a session to name until a real turn has happened, so
// "nothing yet" is the ordinary state of a session somebody has opened and not
// typed into. The session works throughout; only reboot-resume is not armed
// until this returns. Both watchers give up on their own after a quarter of an
// hour, and the manager's context ends them when Socrates stops.
func (m *Manager) watchCLISession(id string, plan harnesses.LaunchPlan, since time.Time) {
	if plan.CLISession != "" {
		return // Chosen by us, or resumed: there is nothing to find out.
	}
	switch plan.Discover {
	case harnesses.DiscoverCodexRollout, harnesses.DiscoverOpenCodeAPI:
	default:
		return
	}
	ctx := m.discoverCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		found, err := m.discoverCLISession(ctx, id, plan, since)
		switch {
		case errors.Is(err, context.Canceled):
			// Socrates is shutting down; the session is not, and the id is
			// looked for again the next time it is opened.
		case err != nil:
			m.logf("could not learn the conversation id of session %s: %v", id, err)
		case found == "":
			m.logf("session %s has no conversation to resume yet", id)
		default:
			if err := m.st.SetSessionCLI(id, found, store.CLIKnown); err != nil {
				m.logf("could not record the conversation id of session %s: %v", id, err)
			}
		}
	}()
}

func (m *Manager) discoverCLISession(ctx context.Context, id string, plan harnesses.LaunchPlan, since time.Time) (string, error) {
	switch plan.Discover {
	case harnesses.DiscoverCodexRollout:
		return harnesses.WatchRollout(ctx, codexHome(plan.Env), plan.Cwd, since)
	case harnesses.DiscoverOpenCodeAPI:
		access, ok := harnesses.OpenCodeAccess(id)
		if !ok {
			return "", errors.New("this process did not start that OpenCode server")
		}
		return harnesses.DiscoverOpenCodeSession(ctx, access, plan.Cwd)
	}
	return "", nil
}

// codexHome is where Codex keeps its rollout files, as the launched process
// will see it: the plan's own environment first, because that is what the pane
// was given, then ours, then the default beside the home directory.
func codexHome(env map[string]string) string {
	if dir := strings.TrimSpace(env["CODEX_HOME"]); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}
