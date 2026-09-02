package harnesses

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// OpenCode is the OpenCode TUI launcher.
//
// The TUI is also the whole OpenCode HTTP server, which is how its session id
// is discovered - and which is why every session gets a password. That server
// carries POST /session/{id}/shell, /tui/submit-prompt and a hundred and sixty
// more paths, with no authentication at all unless OPENCODE_SERVER_PASSWORD is
// set, so without one any process on the machine could drive the agent.
type OpenCode struct{}

func (OpenCode) Kind() Kind            { return KindOpenCode }
func (OpenCode) Label() string         { return "OpenCode" }
func (OpenCode) DefaultBinary() string { return "opencode" }
func (OpenCode) VersionArgs() []string { return []string{"--version"} }

// openCodeUser is the username the Basic-auth layer defaults to.
const openCodeUser = "opencode"

// openCodeTUIFile is the generated tui.json, one per session.
const openCodeTUIFile = "tui.json"

// openCodeTheme is the TUI palette family. The light or dark half of it is
// chosen by the pane's OSC 11 answer, not by this name.
const openCodeTheme = "github"

// openCodePermission is the answer to every permission OpenCode knows how to
// ask about. A prompt in a pane nobody is watching is a session that has
// stopped, so there are none.
//
// The wildcard is what OpenCode's own built-in agents use ("*":"deny" with
// named exceptions), and it is merged into config.permission from the
// OPENCODE_PERMISSION variable. The named keys are listed as well as the
// wildcard because external_directory is asked for through a path of its own
// and a wildcard that did not cover it would be a blocking prompt nobody
// would find.
var openCodePermission = map[string]any{
	"*":                  "allow",
	"bash":               "allow",
	"edit":               "allow",
	"write":              "allow",
	"patch":              "allow",
	"read":               "allow",
	"webfetch":           "allow",
	"websearch":          "allow",
	"task":               "allow",
	"todowrite":          "allow",
	"external_directory": "allow",
}

// openCodeSecrets remembers each session's server password for as long as the
// process lives. It is deliberately not in the store: the password is only
// meaningful while the TUI it belongs to is running, and a secret with a
// longer life than its use is a secret waiting to leak.
var openCodeSecrets sync.Map // session id -> serverAccess

// ServerAccess is what the discoverer needs in order to talk to one session's
// TUI server.
type ServerAccess struct {
	Port     int
	Username string
	Password string
}

// OpenCodeAccess returns the loopback address and credentials of a session's
// TUI server, and whether this process started it.
func OpenCodeAccess(sessionID string) (ServerAccess, bool) {
	v, ok := openCodeSecrets.Load(sessionID)
	if !ok {
		return ServerAccess{}, false
	}
	return v.(ServerAccess), true
}

// ForgetOpenCodeAccess drops a session's credentials, which is what deleting
// or stopping one does.
func ForgetOpenCodeAccess(sessionID string) { openCodeSecrets.Delete(sessionID) }

// Plan builds a fresh session on a port of its own.
func (o OpenCode) Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	return o.plan(req, nil)
}

// ResumePlan continues a stored session, on a new port: the old one belonged
// to a process that is gone.
//
// --session with an unknown id is a hard failure - OpenCode prints
// `Error: Session not found` and exits at once - which is why verification
// before a resume is mandatory here rather than merely wise.
func (o OpenCode) ResumePlan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	id := strings.TrimSpace(req.CLISession)
	if id == "" {
		return LaunchPlan{}, ErrNoResume
	}
	// --fork is deliberately not offered: a resume continues the conversation
	// it names, and a branch of it is a second session the user never asked
	// for.
	return o.plan(req, []string{"--session", id})
}

func (o OpenCode) plan(req PlanRequest, session []string) (LaunchPlan, error) {
	opts := req.Settings.Harnesses.OpenCode
	bin, err := resolveBinary(opts.Binary, o.DefaultBinary())
	if err != nil {
		return LaunchPlan{}, err
	}
	port, err := freePort()
	if err != nil {
		return LaunchPlan{}, err
	}
	password, err := serverPassword()
	if err != nil {
		return LaunchPlan{}, err
	}

	argv := []string{bin, "--port", strconv.Itoa(port), "--hostname", "127.0.0.1"}
	argv = append(argv, session...)
	argv = flag(argv, "-m", pick(req.Model, opts.DefaultModel))
	// --auto is not passed: it approves only what is not explicitly denied,
	// which leaves the answer to a configuration file the user may have.
	// OPENCODE_PERMISSION below says allow to everything by name instead.
	//
	// --print-logs is never passed either: it writes the log to stderr, which
	// is the very pane the user is reading.
	//
	// The project path is positional and goes last, after every flag.
	argv = append(argv, req.Cwd)

	tui, err := openCodeTUI()
	if err != nil {
		return LaunchPlan{}, err
	}
	inline, err := openCodeConfig(opts, req)
	if err != nil {
		return LaunchPlan{}, err
	}
	permission, err := json.Marshal(openCodePermission)
	if err != nil {
		return LaunchPlan{}, err
	}
	tuiPath := SessionFile(req.DataDir, req.SessionID, openCodeTUIFile)

	env := baseEnv(req)
	env["OPENCODE_DISABLE_AUTOUPDATE"] = "1"
	env["OPENCODE_DISABLE_TERMINAL_TITLE"] = "1"
	// This is what lets OpenCode start with no network at all, which is the
	// difference between a usable session in a car and a spinner.
	env["OPENCODE_DISABLE_MODELS_FETCH"] = "1"
	// The permissions are set twice on purpose: this variable is merged over
	// every configuration file OpenCode found, and the generated config below
	// is what a session carries if the variable is ever dropped.
	env["OPENCODE_PERMISSION"] = string(permission)
	// The two generated documents are what make the session look and behave
	// the way it does, and the credentials are the only thing between the
	// agent and any process on the machine: the TUI is the whole OpenCode HTTP
	// server, and without a password every path on it is open.
	env["OPENCODE_TUI_CONFIG"] = tuiPath
	env["OPENCODE_CONFIG_CONTENT"] = string(inline)
	env["OPENCODE_SERVER_USERNAME"] = openCodeUser
	env["OPENCODE_SERVER_PASSWORD"] = password

	openCodeSecrets.Store(req.SessionID, ServerAccess{Port: port, Username: openCodeUser, Password: password})

	return LaunchPlan{
		Argv:       argv,
		Env:        env,
		Cwd:        req.Cwd,
		Files:      []GenFile{{Path: tuiPath, Mode: 0o600, Data: tui}},
		CLISession: strings.TrimSpace(req.CLISession),
		Discover:   DiscoverOpenCodeAPI,
		Port:       port,
	}, nil
}

// openCodeTUI is the generated tui.json: the theme family, a captured mouse,
// and no attention noises, because a server harness has no business trying to
// make a desktop notification sound.
//
// A light background is a per-theme mode rather than a theme of its own -
// every built-in theme carries a {dark,light} pair - so the name here chooses
// the palette family and the OSC 11 answer, which tmux gives from the window
// style, chooses the mode.
func openCodeTUI() ([]byte, error) {
	return json.MarshalIndent(map[string]any{
		"$schema":   "https://opencode.ai/tui.json",
		"theme":     openCodeTheme,
		"mouse":     true,
		"attention": map[string]any{"enabled": false},
	}, "", "  ")
}

// openCodeConfig is OPENCODE_CONFIG_CONTENT, which is merged last of every
// file source and is therefore the one lever that reliably wins.
func openCodeConfig(opts config.OpenCodeOptions, req PlanRequest) ([]byte, error) {
	doc := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		// Nothing a session does is published anywhere.
		"share":      "disabled",
		"autoupdate": false,
		"permission": openCodePermission,
	}
	if model := pick(req.Model, opts.DefaultModel); model != "" {
		doc["model"] = model
	}
	return json.Marshal(doc)
}

// freePort asks the kernel for a port nobody is using, then closes it and
// hands the number to OpenCode.
//
// There is a window between the two in which something else could take it. It
// cannot be closed from here - only the process that binds the port finds out
// - so what is retried here is the ask, not the race; the race shows up as a
// session that failed to start, and belongs to whoever watches for that.
func freePort() (int, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			lastErr = err
			continue
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if err := ln.Close(); err != nil {
			lastErr = err
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free loopback port for the OpenCode server: %w", lastErr)
}

// serverPassword is 32 bytes of entropy per session. It lives in memory and in
// the session's plan.json, which is mode 0600; it is never in the store, never
// in a log line, never in an API response, and the tunnel never proxies the
// port it protects.
func serverPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// openCodeDataDir is where OpenCode keeps its session database.
func openCodeDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, "opencode")
	}
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode")
}

// DiscoverModels asks the binary what it can run; see opencode_models.go.
func (OpenCode) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return openCodeModels(ctx, bin)
}
