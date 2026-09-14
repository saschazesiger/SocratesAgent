package harnesses

import (
	"context"
	"fmt"
	"path/filepath"
)

// VSCode runs code-server in the same persistent tmux lifecycle as the CLIs.
type VSCode struct{ Shell }

func (VSCode) Kind() Kind            { return KindVSCode }
func (VSCode) Label() string         { return "VS Code" }
func (VSCode) DefaultBinary() string { return "code-server" }

// VSCodeSocket is private to the session, inside Socrates' protected data dir.
func VSCodeSocket(dataDir, id string) string {
	return SessionFile(dataDir, id, "ide.sock")
}

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
			"--user-data-dir", SessionFile(req.DataDir, req.SessionID, "vscode-data"),
			"--extensions-dir", filepath.Join(req.DataDir, "vscode-extensions"),
			"--disable-telemetry", "--disable-update-check",
			"--abs-proxy-base-path", "/ide/" + req.SessionID, req.Cwd,
		},
		Env: env, Cwd: req.Cwd, Discover: DiscoverNone,
		Files: []GenFile{{Path: cfg, Mode: 0600, Data: []byte("{}\n")}},
	}, nil
}
