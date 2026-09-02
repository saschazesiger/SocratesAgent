package harnesses

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// Every test in this package builds argv and files. None of them starts a CLI:
// the binaries on PATH are one-line shell scripts that exist only so that the
// launchers have an absolute path to put in argv[0].

func fakePATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"claude", "codex", "opencode"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+name+" 0.0.0-test\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// lab is one session's worth of scaffolding: a home nothing real can see, a
// data directory, and settings straight out of the defaults.
type lab struct {
	t        *testing.T
	home     string
	data     string
	cwd      string
	settings config.Settings
}

func newLab(t *testing.T) *lab {
	t.Helper()
	fakePATH(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return &lab{t: t, home: home, data: t.TempDir(), cwd: cwd, settings: config.Default()}
}

func (l *lab) req() PlanRequest {
	return PlanRequest{
		SessionID: "abcdef0123456789",
		Title:     "a session",
		Cwd:       l.cwd,
		Settings:  l.settings,
		DataDir:   l.data,
	}
}

func (l *lab) plan(h Harness) LaunchPlan {
	l.t.Helper()
	plan, err := h.Plan(context.Background(), l.req())
	if err != nil {
		l.t.Fatalf("%s could not be planned: %v", h.Kind(), err)
	}
	return plan
}

// has reports whether argv carries a flag with that value, as two adjacent
// elements - which is how tmux receives them, with no shell in between.
func has(argv []string, flag, value string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
	}
	return false
}

func carries(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

func valueOf(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// ------------------------------------------------------------------- claude

func TestClaudeArgvHasSessionID(t *testing.T) {
	l := newLab(t)
	plan := l.plan(Claude{})

	id := valueOf(plan.Argv, "--session-id")
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("--session-id = %q, which is not a uuid: %v", id, err)
	}
	if plan.CLISession != id || plan.Discover != DiscoverPreSet {
		t.Fatalf("the plan does not carry the id it chose: %q / %q", plan.CLISession, plan.Discover)
	}
	// --fallback-model is print-mode only per the binary's own help, so it is
	// a flag that would look like it worked and do nothing.
	if carries(plan.Argv, "--fallback-model") {
		t.Error("--fallback-model was passed to an interactive session")
	}
	for _, never := range []string{"--tmux", "--ide", "--print", "-p", "--bg", "--background", "--cloud", "--teleport"} {
		if carries(plan.Argv, never) {
			t.Errorf("%s was passed", never)
		}
	}
	if !has(plan.Argv, "--name", "a session") {
		t.Errorf("the session title is not the display name: %v", plan.Argv)
	}
	if !has(plan.Argv, "--settings", SessionFile(l.data, "abcdef0123456789", claudeSettingsFile)) {
		t.Errorf("the generated settings file is not passed: %v", plan.Argv)
	}
	if len(plan.Files) != 1 || plan.Files[0].Mode != 0o600 {
		t.Errorf("files = %#v", plan.Files)
	}
}

// The effort levels are not one list. config.NormalizeEffort admits minimal
// and ultra because other harnesses name them; --effort rejects both, and a
// launch that fails is not how somebody should find that out.
func TestClaudeEffortNeverCarriesALevelTheFlagRejects(t *testing.T) {
	l := newLab(t)
	for _, level := range []string{"ultra", "minimal"} {
		l.settings.Harnesses.Claude.DefaultEffort = level
		if got := valueOf(l.plan(Claude{}).Argv, "--effort"); got != "" {
			t.Errorf("--effort %q reached the command line", got)
		}
	}
	l.settings.Harnesses.Claude.DefaultEffort = "xhigh"
	if got := valueOf(l.plan(Claude{}).Argv, "--effort"); got != "xhigh" {
		t.Errorf("--effort = %q, want xhigh", got)
	}
}

func TestClaudeResumeArgv(t *testing.T) {
	l := newLab(t)
	id := uuid.NewString()
	req := l.req()
	req.CLISession = id
	plan, err := Claude{}.ResumePlan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !has(plan.Argv, "--resume", id) {
		t.Fatalf("resume argv = %v", plan.Argv)
	}
	if carries(plan.Argv, "--session-id") {
		t.Fatal("a resume also passed --session-id")
	}
	if carries(plan.Argv, "--fork-session") {
		t.Fatal("a continue was forked")
	}
	// -c/--continue is cwd-relative and picks "most recent", which races as
	// soon as two sessions share a directory.
	if carries(plan.Argv, "-c") || carries(plan.Argv, "--continue") {
		t.Fatal("the resume used --continue")
	}

}

// A session id may be used exactly once: the binary answers `Error: Session ID
// <id> is already in use.` and stops. So a restart resumes when the transcript
// is there and starts with a *new* id when it is not - never with the old one.
func TestClaudeRestartNeverReusesSessionID(t *testing.T) {
	l := newLab(t)
	first := l.plan(Claude{})
	id := first.CLISession

	req := l.req()
	req.CLISession = id
	ctx := context.Background()

	// The transcript is there: the restart resumes.
	transcript := filepath.Join(claudeConfigDir(), "projects", "-work", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := Claude{}.VerifyCLISession(ctx, req)
	if err != nil || !ok {
		t.Fatalf("an existing transcript was not found: %v %v", ok, err)
	}
	resumed, err := Claude{}.ResumePlan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !has(resumed.Argv, "--resume", id) {
		t.Fatalf("resume argv = %v", resumed.Argv)
	}

	// The transcript is gone: the restart starts fresh, and the id it starts
	// with is not the one that is now in use.
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	ok, err = Claude{}.VerifyCLISession(ctx, req)
	if err != nil || ok {
		t.Fatalf("a missing transcript was reported as %v (%v)", ok, err)
	}
	// The request still carries the id whose transcript has just gone. Plan is
	// what has to refuse to reuse it: a caller that forgot to clear the field
	// would otherwise build `--session-id <an id already in use>`, and the
	// pane would die on the binary's own refusal before anybody could type.
	fresh, err := Claude{}.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if again := valueOf(fresh.Argv, "--session-id"); again == id || again == "" {
		t.Fatalf("the fresh launch reused the id %q", again)
	}
	// And twice over: two plans from one request are two conversations.
	second, err := Claude{}.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if valueOf(second.Argv, "--session-id") == valueOf(fresh.Argv, "--session-id") {
		t.Fatal("two fresh plans were given the same session id")
	}
	if carries(fresh.Argv, "--resume") {
		t.Fatal("a fresh launch tried to resume")
	}
}

func TestClaudeSettingsOnlyVerifiedKeys(t *testing.T) {
	l := newLab(t)
	doc := claudeSettingsDoc(t, l.plan(Claude{}))

	// Documented but absent from the 2.1.258 binary. A timeout of zero might
	// well mean "dismiss every question at once", which would break
	// AskUserQuestion in every session Socrates ever starts.
	for _, never := range []string{"askUserQuestionTimeout", "autoContinueAtUsageLimit",
		"attribution", "skipAutoPermissionPrompt", "permissions"} {
		if _, found := doc[never]; found {
			t.Errorf("%s was written into the settings file", never)
		}
	}
	if doc["cleanupPeriodDays"] != float64(claudeTranscriptDays) {
		t.Errorf("cleanupPeriodDays = %v", doc["cleanupPeriodDays"])
	}
	// Without this, a launch with --dangerously-skip-permissions stops on a
	// confirmation dialog before the user can type a single character.
	if doc["skipDangerousModePermissionPrompt"] != true {
		t.Errorf("the bypass dialog was not suppressed: %v", doc)
	}
	// The flag not being passed is only half of turning Remote Control off.
	if doc["disableRemoteControl"] != true {
		t.Errorf("Remote Control was not disabled: %v", doc)
	}
	// The environment the pane was given reaches every subprocess Claude Code
	// starts, which is what makes a tool's shell look like the pane.
	env, _ := doc["env"].(map[string]any)
	if env["COLORFGBG"] != "0;15" {
		t.Errorf("the pane's environment is not in the settings file: %v", doc["env"])
	}
}

func claudeSettingsDoc(t *testing.T, plan LaunchPlan) map[string]any {
	t.Helper()
	if len(plan.Files) != 1 {
		t.Fatalf("files = %#v", plan.Files)
	}
	var doc map[string]any
	if err := json.Unmarshal(plan.Files[0].Data, &doc); err != nil {
		t.Fatalf("the generated settings file is not JSON: %v", err)
	}
	return doc
}

// The theme and the Remote Control preference live in the global
// configuration file, and the file belongs to the user: those keys change and
// everything else survives.
func TestClaudeGlobalConfigPinKeepsTheRestOfTheFile(t *testing.T) {
	l := newLab(t)
	path := claudeGlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	stored := `{"oauthAccount":{"uuid":"keep me"},"theme":"dark",` +
		`"remoteControlAtStartup":true,"projects":{"` + l.cwd + `":` +
		`{"hasTrustDialogAccepted":true,"remoteControlAtStartup":true}}}`
	if err := os.WriteFile(path, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	l.plan(Claude{})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["theme"] != "light" {
		t.Errorf("theme = %v", doc["theme"])
	}
	// A person who once turned Remote Control on by hand has a stored
	// preference that starts it again with no flag at all.
	if doc["remoteControlAtStartup"] != false {
		t.Errorf("remoteControlAtStartup = %v", doc["remoteControlAtStartup"])
	}
	projects, _ := doc["projects"].(map[string]any)
	entry, _ := projects[l.cwd].(map[string]any)
	if entry["remoteControlAtStartup"] != false {
		t.Errorf("the project entry still starts on Remote Control: %v", entry)
	}
	if entry["hasTrustDialogAccepted"] != true {
		t.Errorf("the pin rewrote the project entry: %v", entry)
	}
	account, _ := doc["oauthAccount"].(map[string]any)
	if account["uuid"] != "keep me" {
		t.Fatalf("the pin rewrote the whole file: %v", doc)
	}

	// Only this session's directory is touched. Somebody else's project entry
	// is not invented and not changed.
	if _, found := projects["/somewhere/else"]; found {
		t.Errorf("the pin invented a project entry for a directory nothing asked about: %v", projects)
	}
}

// The regression this file exists for: a session works in a directory Claude
// Code has never been opened in - every dynamic workspace directory is brand
// new - and without `hasTrustDialogAccepted` on its own project entry the
// binary opens on the blocking "Accessing workspace: … Is this a project you
// created or one you trust?" screen, whose highlighted answer is "No, exit".
// Verified against 2.1.258: with the entry the same launch goes straight to
// the prompt.
func TestClaudeGlobalConfigTrustsAFreshWorkingDirectory(t *testing.T) {
	l := newLab(t)
	path := claudeGlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A global config from a real machine: Claude Code has been run, but never
	// in this session's directory.
	stored := `{"theme":"light","projects":{"/elsewhere":{"hasTrustDialogAccepted":true}}}`
	if err := os.WriteFile(path, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	l.plan(Claude{})

	entry := claudeProjectEntry(t, path, l.cwd)
	if entry["hasTrustDialogAccepted"] != true {
		t.Errorf("the working directory is not trusted, so the session opens on the workspace question: %v", entry)
	}
	if entry["remoteControlAtStartup"] != false {
		t.Errorf("remoteControlAtStartup = %v", entry["remoteControlAtStartup"])
	}

	// The same has to hold for a resume: a machine that rebooted relaunches
	// with --resume, in the same directory, through the same path.
	if err := os.WriteFile(path, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	req := l.req()
	req.CLISession = "9c2e6b1a-0000-4000-8000-000000000001"
	if _, err := (Claude{}).ResumePlan(context.Background(), req); err != nil {
		t.Fatalf("ResumePlan: %v", err)
	}
	if entry := claudeProjectEntry(t, path, l.cwd); entry["hasTrustDialogAccepted"] != true {
		t.Errorf("a resumed session is not trusted either: %v", entry)
	}
}

// And with no global config at all - a machine where Claude Code has never
// been started - the file that is grown from nothing still carries the entry.
func TestClaudeGlobalConfigTrustsWithNoFileAtAll(t *testing.T) {
	l := newLab(t)
	path := claudeGlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	l.plan(Claude{})
	if entry := claudeProjectEntry(t, path, l.cwd); entry["hasTrustDialogAccepted"] != true {
		t.Errorf("a config grown from nothing does not trust the directory: %v", entry)
	}
}

// Claude Code refuses --dangerously-skip-permissions as root -
// `--dangerously-skip-permissions cannot be used with root/sudo privileges for
// security reasons`, exit 1, the pane dead before it is looked at - unless
// IS_SANDBOX is "1". The pane's environment is built here and not inherited,
// so it has to be set here; a server whose own environment happened to carry
// it would otherwise be the only one that worked.
func TestClaudeSandboxMarkerForRoot(t *testing.T) {
	l := newLab(t)
	plan := l.plan(Claude{})
	if os.Geteuid() == 0 {
		if plan.Env["IS_SANDBOX"] != "1" {
			t.Errorf("as root the pane would die on --dangerously-skip-permissions: IS_SANDBOX = %q",
				plan.Env["IS_SANDBOX"])
		}
		return
	}
	// A normal account has no such check, and claiming a sandbox there would
	// be a statement about a machine nobody asked us about.
	if _, set := plan.Env["IS_SANDBOX"]; set {
		t.Errorf("IS_SANDBOX was set for a session that is not root: %q", plan.Env["IS_SANDBOX"])
	}
}

// A key exported into Socrates' own environment reaches the pane through tmux
// and stops the session on "Detected a custom API key … ❯ No (recommended)".
// Verified against 2.1.258: an empty value is read as no key at all.
func TestClaudeClearsAnAmbientAPIKey(t *testing.T) {
	l := newLab(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-not-a-real-key")
	plan := l.plan(Claude{})
	value, set := plan.Env["ANTHROPIC_API_KEY"]
	if !set || value != "" {
		t.Errorf("the pane would be asked about an ambient API key: %q (set: %v)", value, set)
	}
}

// Claude Code looks the trust entry up under the working directory it reads
// back from the process, which has no symlinks in it. An entry written under
// the name Socrates was given is one the binary never asks about.
func TestClaudeTrustsTheResolvedWorkingDirectory(t *testing.T) {
	l := newLab(t)
	real := filepath.Join(l.home, "real-work")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(l.home, "linked-work")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	l.cwd = link

	path := claudeGlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	l.plan(Claude{})

	if entry := claudeProjectEntry(t, path, real); entry["hasTrustDialogAccepted"] != true {
		t.Errorf("the resolved directory is not trusted: %v", entry)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	projects, _ := doc["projects"].(map[string]any)
	if _, found := projects[link]; found {
		t.Errorf("an entry was written under the symlink, which nothing looks up: %v", projects)
	}
}

// The same for Codex, whose trust override is a command-line argument keyed
// the same way.
func TestCodexTrustsTheResolvedWorkingDirectory(t *testing.T) {
	l := newLab(t)
	real := filepath.Join(l.home, "real-work")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(l.home, "linked-work")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	l.cwd = link
	plan := l.plan(Codex{})
	want := "projects={" + tomlString(real) + "={trust_level=" + tomlString("trusted") + "}}"
	if !has(plan.Argv, "-c", want) {
		t.Errorf("Codex was told to trust the symlink rather than the directory: %v", plan.Argv)
	}
}

// An interrupted first run leaves a zero-byte ~/.claude.json behind. It is not
// JSON, and treating it as broken would cost every session on that machine its
// trust entry for ever.
func TestClaudeGlobalConfigTreatsAnEmptyFileAsMissing(t *testing.T) {
	l := newLab(t)
	path := claudeGlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pinClaudeGlobalConfig(path, l.cwd); err != nil {
		t.Fatalf("an empty file was treated as broken: %v", err)
	}
	if entry := claudeProjectEntry(t, path, l.cwd); entry["hasTrustDialogAccepted"] != true {
		t.Errorf("the directory is not trusted: %v", entry)
	}
}

// A file that is JSON but not a shape this understands is reported rather than
// half-rewritten, and the caller writes the reason to the log.
func TestClaudeGlobalConfigReportsAFileItCannotChange(t *testing.T) {
	l := newLab(t)
	path := claudeGlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pinClaudeGlobalConfig(path, l.cwd); err == nil {
		t.Error("a file that could not be parsed was reported as pinned")
	}
	if err := os.WriteFile(path, []byte(`{"projects":"not an object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pinClaudeGlobalConfig(path, l.cwd); err == nil {
		t.Error(`a "projects" that is not an object was reported as pinned`)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != `{"projects":"not an object"}` {
		t.Errorf("the file was rewritten anyway: %s", raw)
	}
}

func claudeProjectEntry(t *testing.T, path, cwd string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	projects, _ := doc["projects"].(map[string]any)
	entry, _ := projects[cwd].(map[string]any)
	if entry == nil {
		t.Fatalf("there is no project entry for %s in %v", cwd, doc)
	}
	return entry
}

// -------------------------------------------------------------------- codex

// The dotted key carries an absolute path, and paths have spaces and dots in
// them. This is the quoting the trust override depends on.
func TestCodexTrustLevelQuoting(t *testing.T) {
	l := newLab(t)
	l.cwd = "/a b/c.d"
	plan, err := Codex{}.Plan(context.Background(), l.req())
	if err != nil {
		t.Fatal(err)
	}
	// The path is quoted as a TOML basic string, inside an inline table
	// rather than a dotted key: Codex splits a dotted override on every dot,
	// quotes or not, and every default Socrates workspace lives under a
	// directory with one.
	want := `projects={"/a b/c.d"={trust_level="trusted"}}`
	if !has(plan.Argv, "-c", want) {
		t.Fatalf("argv = %v, want a -c %s", plan.Argv, want)
	}
}

func TestCodexArgvHasStrictConfig(t *testing.T) {
	l := newLab(t)
	plan := l.plan(Codex{})
	if !carries(plan.Argv, "--strict-config") {
		t.Fatalf("argv = %v", plan.Argv)
	}
	if !has(plan.Argv, "-C", l.cwd) {
		t.Errorf("the working root is not passed: %v", plan.Argv)
	}
	if plan.Env["CODEX_INTERNAL_ORIGINATOR_OVERRIDE"] != codexOriginator {
		t.Errorf("the originator is %q", plan.Env["CODEX_INTERNAL_ORIGINATOR_OVERRIDE"])
	}
	// None of these is a flag of the interactive TUI in 0.152.1; each is an
	// `unexpected argument` and a session that never starts. (--yolo is
	// accepted as an alias of the bypass flag, but the long spelling is what
	// is passed, because it says what it does.)
	for _, gone := range []string{"--full-auto", "--json", "--color", "--model-provider", "--skip-git-repo-check", "--no-project-doc"} {
		if carries(plan.Argv, gone) {
			t.Errorf("%s was passed", gone)
		}
	}
	if !has(plan.Argv, "-c", `tui.theme="light-gray"`) {
		t.Errorf("the light theme is not pinned: %v", plan.Argv)
	}
	if !carries(plan.Argv, "--no-alt-screen") {
		t.Errorf("the default keeps scrollback: %v", plan.Argv)
	}
}

// The bypass replaces both -s and -a. Codex refuses a command line that names
// a sandbox and then bypasses it, so passing either beside the bypass is a
// session that never starts.
func TestCodexBypassesApprovalsAndSandbox(t *testing.T) {
	plan := newLab(t).plan(Codex{})
	if !carries(plan.Argv, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("argv = %v", plan.Argv)
	}
	for _, conflicting := range []string{"-s", "-a", "--sandbox", "--ask-for-approval", "--approve-for-me"} {
		if carries(plan.Argv, conflicting) {
			t.Errorf("%s was passed beside the bypass: %v", conflicting, plan.Argv)
		}
	}
	// A Codex TUI that talks to somebody else's app server is not the session
	// this app started.
	for _, remote := range []string{"--remote", "--remote-auth-token-env"} {
		if carries(plan.Argv, remote) {
			t.Errorf("%s was passed: %v", remote, plan.Argv)
		}
	}
}

func TestCodexResumeKeepsTheOptionBlock(t *testing.T) {
	l := newLab(t)
	id := uuid.NewString()
	req := l.req()
	req.CLISession = id
	plan, err := Codex{}.ResumePlan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Argv) < 3 || plan.Argv[1] != "resume" || plan.Argv[2] != id {
		t.Fatalf("argv = %v", plan.Argv)
	}
	if !carries(plan.Argv, "--strict-config") || !has(plan.Argv, "-c", trustLevelOverride(l.cwd)) {
		t.Fatalf("the resume dropped part of the option block: %v", plan.Argv)
	}
	// --last is cwd-filtered and picks "most recent", which is a race.
	if carries(plan.Argv, "--last") {
		t.Fatal("the resume used --last")
	}
}

// The rollout file is the durable record and its name carries the uuid. The
// index is version-stamped and is only ever the second question.
func TestCodexVerifyPrefersRolloutGlob(t *testing.T) {
	l := newLab(t)
	id := uuid.NewString()
	dir := filepath.Join(codexHome(), "sessions", "2026", "09", "02")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-09-02T08-00-00-"+id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := l.req()
	req.CLISession = id
	// There is no state_5.sqlite anywhere near this home, and the answer is
	// still true.
	ok, err := Codex{}.VerifyCLISession(context.Background(), req)
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}

	req.CLISession = uuid.NewString()
	ok, err = Codex{}.VerifyCLISession(context.Background(), req)
	if ok {
		t.Fatalf("an id nobody ever wrote was found (%v)", err)
	}
}

// The watcher matches on the working directory and on the originator, and it
// treats "nothing yet" as normal rather than as an error - a rollout file does
// not exist until a real turn has happened.
func TestCodexWatchRolloutMatchesCwdAndOriginator(t *testing.T) {
	l := newLab(t)
	home := codexHome()
	dir := filepath.Join(home, "sessions", "2026", "09", "02")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id, cwd, originator string) {
		meta, err := json.Marshal(map[string]any{
			"type":    "session_meta",
			"payload": map[string]any{"session_id": id, "cwd": cwd, "originator": originator},
		})
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(dir, "rollout-2026-09-02T08-00-00-"+id+".jsonl")
		if err := os.WriteFile(name, append(meta, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(uuid.NewString(), l.cwd, "codex_cli_rs")
	write(uuid.NewString(), filepath.Join(l.home, "elsewhere"), codexOriginator)
	mine := uuid.NewString()
	write(mine, l.cwd, codexOriginator)

	got, err := findRollout(home, Discovery{Cwd: l.cwd, Since: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if got != mine {
		t.Fatalf("the watcher matched %q, want %q", got, mine)
	}

	// A file written before this launch belongs to an earlier session. An
	// hour, not a minute: the skew allowance is five seconds.
	got, err = findRollout(home, Discovery{Cwd: l.cwd, Since: time.Now().Add(time.Hour)})
	if err != nil || got != "" {
		t.Fatalf("an older rollout was claimed: %q %v", got, err)
	}
}

// ----------------------------------------------------------------- opencode

func TestOpenCodePositionalCwdIsLast(t *testing.T) {
	l := newLab(t)
	l.settings.Harnesses.OpenCode.DefaultModel = "anthropic/claude-sonnet-4-5"
	plan := l.plan(OpenCode{})
	if plan.Argv[len(plan.Argv)-1] != l.cwd {
		t.Fatalf("the project path is not last: %v", plan.Argv)
	}
	if plan.Port == 0 || !has(plan.Argv, "--port", strconv.Itoa(plan.Port)) {
		t.Fatalf("the port is not passed: %d in %v", plan.Port, plan.Argv)
	}
	if !has(plan.Argv, "--hostname", "127.0.0.1") {
		t.Errorf("the server is not bound to loopback: %v", plan.Argv)
	}
}

func TestOpenCodeNeverPassesPrintLogs(t *testing.T) {
	plan := newLab(t).plan(OpenCode{})
	// --print-logs writes to stderr, which is the pane the person is reading,
	// and --log-level without it only fills a file nobody asked for.
	for _, never := range []string{"--print-logs", "--log-level"} {
		if carries(plan.Argv, never) {
			t.Fatalf("%s was passed: %v", never, plan.Argv)
		}
	}
}

// The TUI serves the whole OpenCode API on its port, so the port is a door.
// This is the lock on it.
func TestOpenCodeServerPasswordIsSet(t *testing.T) {
	l := newLab(t)
	first := l.plan(OpenCode{})
	password := first.Env["OPENCODE_SERVER_PASSWORD"]
	if len(password) < 43 {
		t.Fatalf("the password is %d characters, which is not 32 bytes of entropy", len(password))
	}
	if first.Env["OPENCODE_SERVER_USERNAME"] != openCodeUser {
		t.Errorf("username = %q", first.Env["OPENCODE_SERVER_USERNAME"])
	}

	second := l.plan(OpenCode{})
	if second.Env["OPENCODE_SERVER_PASSWORD"] == password {
		t.Fatal("two sessions were given the same password")
	}

	// And the discoverer is the other half of the lock: it sends exactly the
	// credentials the session was started with.
	access, ok := OpenCodeAccess("abcdef0123456789")
	if !ok || access.Password != second.Env["OPENCODE_SERVER_PASSWORD"] {
		t.Fatalf("the discoverer does not know the session's password: %#v", access)
	}
	t.Cleanup(func() { ForgetOpenCodeAccess("abcdef0123456789") })

	launched := time.Now()
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, given := r.BasicAuth()
		if !given || user != access.Username || pass != access.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Secure Area"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sawAuth = true
		// One conversation from long before this pane, one from after it.
		// Only the second is this pane's.
		_, _ = w.Write([]byte(`[{"id":"ses_old","directory":"` + l.cwd + `","time":{"created":1}},` +
			`{"id":"ses_one","directory":"` + l.cwd + `","time":{"created":` + strconv.FormatInt(launched.Add(time.Second).UnixMilli(), 10) + `}}]`))
	}))
	defer srv.Close()
	port := serverPort(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := DiscoverOpenCodeSession(ctx, ServerAccess{Port: port, Username: access.Username, Password: access.Password},
		Discovery{Cwd: l.cwd, Since: launched})
	if err != nil || id != "ses_one" {
		t.Fatalf("discovery = %q, %v", id, err)
	}
	if !sawAuth {
		t.Fatal("the discoverer never sent the Authorization header")
	}

	// A wrong password is a launch failure and not something a retry mends.
	_, err = DiscoverOpenCodeSession(ctx, ServerAccess{Port: port, Username: access.Username, Password: "wrong"},
		Discovery{Cwd: l.cwd, Since: launched})
	if err == nil {
		t.Fatal("a 401 was treated as something to wait out")
	}
}

// OpenCode's ids descend, so the newest is the lexicographically smallest -
// the opposite of every other id in this application, and exactly the sort of
// thing a later reader would quietly "fix".
func TestOpenCodeNewestIsByCreationTime(t *testing.T) {
	launched := time.Now()
	at := func(id, dir string, offset time.Duration) openCodeSession {
		s := openCodeSession{ID: id, Directory: dir}
		s.Time.Created = launched.Add(offset).UnixMilli()
		return s
	}
	// Two sessions of this pane and one somebody else's directory. The ids
	// descend as OpenCode mints them, so the newest is the smallest.
	sessions := []openCodeSession{
		at("ses_zzz", "/w", time.Second),
		at("ses_yyy", "/w", 2*time.Second),
		at("ses_mmm", "/elsewhere", 3*time.Second),
	}
	d := Discovery{Cwd: "/w", Since: launched}
	if got := newestIn(sessions, d); got != "ses_yyy" {
		t.Fatalf("newest = %q", got)
	}
	if got := newestIn(sessions, Discovery{Cwd: "/nothing", Since: launched}); got != "" {
		t.Fatalf("a directory with no session answered %q", got)
	}
}

// A directory with history is the normal case for a preset or a typed-in
// path: GET /session lists every session in the shared database, and a pane
// that claimed the newest of those would show the user a conversation they
// never had here.
func TestOpenCodeIgnoresSessionsOlderThanTheLaunch(t *testing.T) {
	launched := time.Now()
	old := openCodeSession{ID: "ses_old", Directory: "/w"}
	old.Time.Created = launched.Add(-time.Hour).UnixMilli()
	d := Discovery{Cwd: "/w", Since: launched}

	if got := newestIn([]openCodeSession{old}, d); got != "" {
		t.Fatalf("a session from before the launch was claimed: %q", got)
	}
	fresh := openCodeSession{ID: "ses_new", Directory: "/w"}
	fresh.Time.Created = launched.Add(time.Second).UnixMilli()
	if got := newestIn([]openCodeSession{old, fresh}, d); got != "ses_new" {
		t.Fatalf("newest = %q, want the one this pane started", got)
	}
	// The stamps have coarser resolution than the launch time, so a session
	// minted a moment "before" the launch is still this pane's.
	skewed := openCodeSession{ID: "ses_skew", Directory: "/w"}
	skewed.Time.Created = launched.Add(-time.Second).UnixMilli()
	if got := newestIn([]openCodeSession{skewed}, d); got != "ses_skew" {
		t.Fatalf("the skew allowance dropped this pane's own session: %q", got)
	}
}

// Two sessions of one harness can share a directory, and neither CLI offers a
// handle to tell them apart. The ids the other rows already hold are what
// keeps the second session from adopting the first one's conversation.
func TestDiscoverySkipsIdsAnotherSessionHolds(t *testing.T) {
	launched := time.Now()
	first := openCodeSession{ID: "ses_first", Directory: "/w"}
	first.Time.Created = launched.Add(time.Second).UnixMilli()
	second := openCodeSession{ID: "ses_second", Directory: "/w"}
	second.Time.Created = launched.Add(2 * time.Second).UnixMilli()

	taken := map[string]bool{"ses_second": true}
	d := Discovery{Cwd: "/w", Since: launched, Claimed: func(id string) bool { return taken[id] }}
	if got := newestIn([]openCodeSession{first, second}, d); got != "ses_first" {
		t.Fatalf("the discoverer claimed %q, which another session already holds", got)
	}
	taken["ses_first"] = true
	if got := newestIn([]openCodeSession{first, second}, d); got != "" {
		t.Fatalf("every session is spoken for and the discoverer answered %q", got)
	}

	// The same for Codex, over real rollout files in one directory.
	l := newLab(t)
	home := codexHome()
	dir := filepath.Join(home, "sessions", "2026", "09", "02")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mine, theirs := uuid.NewString(), uuid.NewString()
	for _, id := range []string{mine, theirs} {
		meta, err := json.Marshal(map[string]any{
			"type":    "session_meta",
			"payload": map[string]any{"session_id": id, "cwd": l.cwd, "originator": codexOriginator},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rollout-2026-09-02T08-00-00-"+id+".jsonl"), append(meta, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := findRollout(home, Discovery{
		Cwd:     l.cwd,
		Since:   time.Now().Add(-time.Minute),
		Claimed: func(id string) bool { return id == theirs },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != mine {
		t.Fatalf("the watcher returned %q, want the one nobody holds", got)
	}
}

func TestOpenCodeResumeArgv(t *testing.T) {
	l := newLab(t)
	req := l.req()
	req.CLISession = "ses_one"
	plan, err := OpenCode{}.ResumePlan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !has(plan.Argv, "--session", "ses_one") {
		t.Fatalf("argv = %v", plan.Argv)
	}
	if carries(plan.Argv, "--fork") {
		t.Fatal("a continue was forked")
	}
	if plan.Argv[len(plan.Argv)-1] != l.cwd {
		t.Fatalf("the project path is not last on a resume: %v", plan.Argv)
	}

}

// ------------------------------------------------------------------- shared

func TestGeneratedFilesAreValid(t *testing.T) {
	l := newLab(t)
	claudeSettingsDoc(t, l.plan(Claude{}))

	plan := l.plan(OpenCode{})
	var tui map[string]any
	if err := json.Unmarshal(plan.Files[0].Data, &tui); err != nil {
		t.Fatalf("tui.json is not JSON: %v", err)
	}
	if tui["theme"] != openCodeTheme || tui["mouse"] != true {
		t.Errorf("tui.json = %v", tui)
	}
	attention, _ := tui["attention"].(map[string]any)
	// A server harness has no business making a desktop notification sound.
	if attention["enabled"] != false {
		t.Errorf("attention = %v", attention)
	}

	var inline map[string]any
	if err := json.Unmarshal([]byte(plan.Env["OPENCODE_CONFIG_CONTENT"]), &inline); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not JSON: %v", err)
	}
	if inline["share"] != "disabled" || inline["autoupdate"] != false {
		t.Errorf("the inline config = %v", inline)
	}
	if permission, _ := inline["permission"].(map[string]any); permission["*"] != "allow" {
		t.Errorf("the inline config does not allow everything: %v", inline["permission"])
	}
	if !json.Valid([]byte(plan.Env["OPENCODE_PERMISSION"])) {
		t.Errorf("OPENCODE_PERMISSION = %q", plan.Env["OPENCODE_PERMISSION"])
	}
}

// Every session says the background is white, whichever program it runs: three
// of the four decide from the OSC 11 answer and Claude Code decides from this.
func TestEnvAlwaysCarriesWhite(t *testing.T) {
	l := newLab(t)
	for _, h := range Registry() {
		plan := l.plan(h)
		if plan.Env["COLORFGBG"] != "0;15" {
			t.Errorf("%s: COLORFGBG = %q", h.Kind(), plan.Env["COLORFGBG"])
		}
		if plan.Env["COLORTERM"] != "truecolor" {
			t.Errorf("%s: COLORTERM = %q", h.Kind(), plan.Env["COLORTERM"])
		}
		if plan.Env["TERM"] == "" {
			t.Errorf("%s: no TERM", h.Kind())
		}
		if plan.Env["SOCRATES_SESSION"] != "abcdef0123456789" || plan.Env["SOCRATES"] != "1" {
			t.Errorf("%s: the session marks are missing: %v", h.Kind(), plan.Env)
		}
		if value, set := plan.Env["TMUX"]; !set || value != "" {
			t.Errorf("%s: TMUX = %q (%v)", h.Kind(), value, set)
		}
		if !filepath.IsAbs(plan.Argv[0]) {
			t.Errorf("%s: argv[0] = %q is not absolute", h.Kind(), plan.Argv[0])
		}
		if plan.Cwd != l.cwd {
			t.Errorf("%s: cwd = %q", h.Kind(), plan.Cwd)
		}
	}
}

// The registry is a fixed order, and every id in it is one config knows.
func TestRegistryIsTheFourInOrder(t *testing.T) {
	want := []string{config.HarnessShell, config.HarnessClaude, config.HarnessCodex, config.HarnessOpenCode}
	got := Registry()
	if len(got) != len(want) {
		t.Fatalf("the registry has %d harnesses", len(got))
	}
	for i, h := range got {
		if string(h.Kind()) != want[i] {
			t.Fatalf("registry[%d] = %s, want %s", i, h.Kind(), want[i])
		}
		if _, ok := Get(want[i]); !ok {
			t.Fatalf("%s cannot be looked up", want[i])
		}
	}
	if _, ok := Get("nothing"); ok {
		t.Fatal("an id nobody ships was found")
	}
}

// A shell is restarted rather than resumed, and that is the whole of what
// resuming a shell could mean.
func TestShellHasNothingToResume(t *testing.T) {
	l := newLab(t)
	plan := l.plan(Shell{})
	if !carries(plan.Argv, "-l") {
		t.Errorf("the default is a login shell: %v", plan.Argv)
	}
	if plan.Discover != DiscoverNone || plan.CLISession != "" {
		t.Errorf("a shell claims a session: %q %q", plan.Discover, plan.CLISession)
	}
	if _, err := (Shell{}).ResumePlan(context.Background(), l.req()); err != ErrNoResume {
		t.Fatalf("resume = %v", err)
	}
}

// -------------------------------------------------------------- working dirs

func TestResolveWorkdir(t *testing.T) {
	root := t.TempDir()
	settings := config.Default()
	settings.Workspace.Root = root
	settings.Workspace.AllowCustom = true

	dir, err := ResolveWorkdir(settings, WorkdirDynamic, "", "claude", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(dir), "claude-") || !strings.HasSuffix(dir, "-01234567") {
		t.Errorf("a dynamic directory is named %q", dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("the dynamic directory was not created: %v", err)
	}
	if err := insideRoot(root, dir); err != nil {
		t.Fatalf("a dynamic directory left its root: %v", err)
	}

	// A preset has to be one of the dashboard's own, and it has to still be
	// there: a preset that is gone is a configuration that no longer describes
	// this machine, and creating it silently would hide that.
	preset := filepath.Join(root, "preset")
	if err := os.MkdirAll(preset, 0o755); err != nil {
		t.Fatal(err)
	}
	settings.Workspace.Presets = []config.PresetDir{{Label: "Preset", Path: preset}}
	if got, err := ResolveWorkdir(settings, WorkdirPreset, preset, "shell", "id"); err != nil || got != preset {
		t.Fatalf("preset = %q, %v", got, err)
	}
	if _, err := ResolveWorkdir(settings, WorkdirPreset, filepath.Join(root, "elsewhere"), "shell", "id"); err == nil {
		t.Fatal("a path that is not a preset was accepted")
	}
	if err := os.Remove(preset); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkdir(settings, WorkdirPreset, preset, "shell", "id"); err == nil {
		t.Fatal("a preset that is gone was accepted")
	}

	// Custom: created, but never one of the places that are never an answer,
	// and never at all when the dashboard has not allowed it.
	custom := filepath.Join(t.TempDir(), "typed", "in")
	if got, err := ResolveWorkdir(settings, WorkdirCustom, custom, "codex", "id"); err != nil || got != custom {
		t.Fatalf("custom = %q, %v", got, err)
	}
	for _, blocked := range []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot",
		"/proc", "/proc/1", "/sys", "/sys/kernel", "/dev", "/dev/shm/work"} {
		if _, err := ResolveWorkdir(settings, WorkdirCustom, blocked, "codex", "id"); err == nil {
			t.Errorf("%s was accepted as a working directory", blocked)
		}
	}

	// The rule is what a path resolves to. MkdirAll follows a symlink, so a
	// harmless-looking name pointing at /etc has to be refused by the name it
	// ends up at, not the one it was typed as.
	links := t.TempDir()
	trap := filepath.Join(links, "innocent")
	if err := os.Symlink("/etc", trap); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkdir(settings, WorkdirCustom, trap, "codex", "id"); err == nil {
		t.Errorf("%s -> /etc was accepted", trap)
	}
	// The resolution works on the longest part of the path that exists, so a
	// directory that is not there yet is still judged by where its parent
	// really is.
	sysLink := filepath.Join(links, "harmless")
	if err := os.Symlink("/sys", sysLink); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkdir(settings, WorkdirCustom, filepath.Join(sysLink, "workspace"), "codex", "id"); err == nil {
		t.Errorf("%s/workspace, which is under /sys, was accepted", sysLink)
	}
	settings.Workspace.AllowCustom = false
	if _, err := ResolveWorkdir(settings, WorkdirCustom, custom, "codex", "id"); err == nil {
		t.Fatal("a typed path was accepted where the dashboard forbids it")
	}
	settings.Workspace.AllowCustom = true

	// The shape rules hold for every mode.
	for _, bad := range []string{"", "relative/path", "/tmp/../etc"} {
		if _, err := ResolveWorkdir(settings, WorkdirCustom, bad, "codex", "id"); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := ResolveWorkdir(settings, "sideways", "", "codex", "id"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// ------------------------------------------------------------------ helpers

func serverPort(t *testing.T, rawURL string) int {
	t.Helper()
	_, port, ok := strings.Cut(strings.TrimPrefix(rawURL, "http://"), ":")
	if !ok {
		t.Fatalf("no port in %q", rawURL)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("port %q: %v", port, err)
	}
	return n
}

// Step 4 of the discovery recipe: a version that does not serve the HTTP API,
// or a server that never came up, still leaves its sessions in the database.
// The same two bounds apply there - it is the same question asked of a
// different source, not a looser one.
func TestOpenCodeFallsBackToTheDatabase(t *testing.T) {
	l := newLab(t)
	dir := filepath.Join(openCodeDataDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT, time_created INTEGER)`); err != nil {
		t.Fatal(err)
	}
	launched := time.Now()
	insert := func(id, dir string, at time.Time) {
		if _, err := db.Exec(`INSERT INTO session (id, directory, time_created) VALUES (?,?,?)`, id, dir, at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	insert("ses_yesterday", l.cwd, launched.Add(-24*time.Hour))
	insert("ses_elsewhere", filepath.Join(l.home, "other"), launched.Add(time.Second))
	insert("ses_mine", l.cwd, launched.Add(time.Second))
	insert("ses_theirs", l.cwd, launched.Add(2*time.Second))

	ctx := context.Background()
	got, err := newestInDB(ctx, Discovery{
		Cwd:     l.cwd,
		Since:   launched,
		Claimed: func(id string) bool { return id == "ses_theirs" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ses_mine" {
		t.Fatalf("the database fallback answered %q", got)
	}

	// No database at all is "nothing found yet", not a failure: the session
	// stays pending and the watcher keeps looking.
	if err := os.Remove(filepath.Join(dir, "opencode.db")); err != nil {
		t.Fatal(err)
	}
	got, err = newestInDB(ctx, Discovery{Cwd: l.cwd, Since: launched})
	if got != "" || err != nil {
		t.Fatalf("a missing database answered %q, %v", got, err)
	}
}
