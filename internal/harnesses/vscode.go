package harnesses

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// VSCode runs code-server in the same persistent tmux lifecycle as the CLIs.
//
// There is one editor per project folder rather than one per session, so what
// VS Code remembers - open editors, layout, unsaved changes, terminals - is
// kept per folder, outside every session's own directory: deleting the
// session, or a restart that replaces it, leaves the editor's memory in place
// for the next one opened on the same folder.
type VSCode struct{ Shell }

func (VSCode) Kind() Kind            { return KindVSCode }
func (VSCode) Label() string         { return "VS Code" }
func (VSCode) DefaultBinary() string { return "code-server" }

// VSCodeSocket is private to the session, inside Socrates' protected data dir.
func VSCodeSocket(dataDir, id string) string {
	return SessionFile(dataDir, id, "ide.sock")
}

// VSCodeDataDir is the user-data-dir of the editor of one folder. The folder
// is hashed rather than spelled out: a path is not a directory name, and two
// spellings of one folder are cleaned into one before they get here.
func VSCodeDataDir(dataDir, folder string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(folder)))
	return filepath.Join(dataDir, "vscode", hex.EncodeToString(sum[:8]))
}

// VSCodeSessionID is the session id of a folder's editor. It is the same
// every time the folder is given one, because the id is in the editor's
// address - /ide/<id>/ - and the browser keeps what the workbench remembers
// (open editors, layout, recent files) under a name made from that address.
// A new id would be a browser that has never seen the folder before.
func VSCodeSessionID(folder string) string {
	sum := sha256.Sum256([]byte("vscode:" + filepath.Clean(folder)))
	return hex.EncodeToString(sum[:16])
}

// VSCodeBlankID is the session id of the editor with no folder open. It is
// fixed for the same reason a folder's is: the browser keeps what the
// workbench remembers under its address.
var VSCodeBlankID = func() string {
	sum := sha256.Sum256([]byte("vscode:blank"))
	return hex.EncodeToString(sum[:16])
}()

// vscodeDefaults is the settings.json a folder's editor starts with. Both are
// about the editor surviving a restart of Socrates or of the machine: unsaved
// edits come back from their backups, and the integrated terminals come back
// in the directory they were in.
const vscodeDefaults = `{
  "files.hotExit": "onExitAndWindowClose",
  "terminal.integrated.enablePersistentSessions": true,
  "terminal.integrated.persistentSessionReviveProcess": "onExitAndWindowClose"
}
`

func (v VSCode) Plan(_ context.Context, req PlanRequest) (LaunchPlan, error) {
	bin, err := resolveBinary(req.Settings.Harnesses.VSCode.Binary, v.DefaultBinary())
	if err != nil {
		return LaunchPlan{}, err
	}
	socket := VSCodeSocket(req.DataDir, req.SessionID)
	if len(socket) > 100 {
		return LaunchPlan{}, fmt.Errorf("VS Code socket path is too long; use a shorter Socrates data directory")
	}
	envBin, err := resolveBinary("", "env")
	if err != nil {
		return LaunchPlan{}, err
	}
	cfg := SessionFile(req.DataDir, req.SessionID, "code-server.yaml")
	userData := VSCodeDataDir(req.DataDir, req.Cwd)
	adoptSessionData(SessionFile(req.DataDir, req.SessionID, "vscode-data"), userData)
	files := []GenFile{{Path: cfg, Mode: 0600, Data: []byte("{}\n")}}
	// Only a folder's first editor is given the defaults: from then on the
	// file is the person's, and what they changed in it is theirs to keep.
	settings := filepath.Join(userData, "User", "settings.json")
	if _, err := os.Stat(settings); errors.Is(err, fs.ErrNotExist) {
		files = append(files, GenFile{Path: settings, Mode: 0600, Data: []byte(vscodeDefaults)})
	}
	env := baseEnv(req)
	// An invocation from an existing VS Code terminal must start a new server,
	// rather than forward the folder to that terminal's parent editor.
	for _, key := range []string{"VSCODE_IPC_HOOK_CLI", "VSCODE_OPTIONS", "VSCODE_PROXY_URI", "CODE_SERVER_SESSION_SOCKET", "CODE_SERVER_IDLE_TIMEOUT_SECONDS"} {
		env[key] = ""
	}
	return LaunchPlan{
		Argv: []string{
			envBin, "-u", "CODE_SERVER_PARENT_PID", bin,
			"--config", cfg, "--auth", "none",
			"--socket", socket, "--socket-mode", "0600",
			"--session-socket", SessionFile(req.DataDir, req.SessionID, "ipc.sock"),
			"--user-data-dir", userData,
			"--extensions-dir", filepath.Join(req.DataDir, "vscode-extensions"),
			"--disable-telemetry", "--disable-update-check",
			"--abs-proxy-base-path", "/ide/" + req.SessionID, req.Cwd,
		},
		Env: env, Cwd: req.Cwd, Discover: DiscoverNone,
		Files: files,
	}, nil
}

// adoptSessionData moves the editor state of a session from before editors
// were kept per folder into its folder, so that what it remembered is not
// lost to the upgrade. A folder that already has a state of its own keeps it.
func adoptSessionData(legacy, userData string) {
	if _, err := os.Stat(userData); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if info, err := os.Stat(legacy); err != nil || !info.IsDir() {
		return
	}
	if err := os.MkdirAll(filepath.Dir(userData), 0o700); err != nil {
		return
	}
	_ = os.Rename(legacy, userData)
}
