//go:build linux

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/accounts"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The rehearsal of the whole migration of REBUILD.md §8 - the accounts and
// the clean cut, as a start of this build runs them (migrate) - on a
// synthetic copy of today's installation: a data directory of today's main
// (schema 9) with a Shell and agent terminals, each a row and a live tmux
// session on the data directory's own socket, prepared agent terminals with
// their first prompts, a profile, the settings and s3.json, and a HOME with
// the agents' own folders and logins. Never a real HOME, and never the
// user's tmux server: every tmux command here names the fixture's socket.

// rehearsalChild is the variable that makes this test binary the start that
// is killed in the middle of its migration (TestMain); its value is the
// fixture's data directory, and HOME is the fixture's.
const rehearsalChild = "SOCRATES_REHEARSAL_CHILD"

// The fixture's logins, each a file that must exist exactly once after the
// migration, and be the same file through the old path and the new.
var oldLogins = map[string]string{
	".claude/.credentials.json":       `{"claudeAiOauth":{"accessToken":"at-old","refreshToken":"rt-old"}}`,
	".claude.json":                    `{"numStartups":3,"oauthAccount":{"emailAddress":"old@example.com"}}`,
	".codex/auth.json":                `{"OPENAI_API_KEY":"sk-old"}`,
	".local/share/opencode/auth.json": `{"openai":{"type":"oauth","refresh":"rt-oc-old","access":"at","expires":1}}`,
}

// The profile with its Claude Code folder: an account of Claude Code, and of
// nothing else.
const workLogin = `{"claudeAiOauth":{"accessToken":"at-work","refreshToken":"rt-work"}}`

const oldSettings = `{"workspace":{"root":"/srv/work"},"terminal":{"font_size":15}}`
const oldS3 = `{"endpoint":"https://s3.example.com","bucket":"socrates","prefix":"vm-1/"}`

// oldInstall is one synthetic installation of today's main.
type oldInstall struct {
	t          *testing.T
	data, home string
	tmux       string
}

func newOldInstall(t *testing.T) *oldInstall {
	t.Helper()
	bin := requireTmux(t)
	root := t.TempDir()
	o := &oldInstall{t: t, data: filepath.Join(root, "data"), home: filepath.Join(root, "home"), tmux: bin}
	for rel, content := range oldLogins {
		o.write(filepath.Join(o.home, rel), content)
	}
	// What the agent terminals said, in the agents' own folders.
	o.write(filepath.Join(o.home, ".claude/projects/-srv-work/conv-cl1.jsonl"), `{"type":"user"}`+"\n")
	o.write(filepath.Join(o.home, ".codex/sessions/2026/10/20/rollout-2026-10-20T08-00-00-conv-cx1.jsonl"), `{"type":"session_meta"}`+"\n")
	o.write(filepath.Join(o.home, ".local/share/opencode/opencode.db"), "sqlite")
	o.write(filepath.Join(o.data, "profiles/p-work/claude/.credentials.json"), workLogin)
	o.write(filepath.Join(o.data, paths.LegacyS3Config), oldS3)
	for _, id := range []string{"sh1", "cl1", "cx1", "oc1"} {
		o.write(filepath.Join(o.data, "sessions", id, "plan.json"), `{"argv":["/bin/sh"]}`)
	}

	schema, err := os.ReadFile(filepath.Join("internal", "store", "testdata", "schema-v9.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(o.data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row := func(id, harness, state, options string, startAt int64) string {
		tmuxName := ""
		if state != store.StatePrestart {
			tmuxName = termux.TmuxName(id)
		}
		return fmt.Sprintf(`INSERT INTO sessions(id, title, harness, workdir, workdir_mode, options, tmux_name, state, start_at, created_at, updated_at)
			VALUES('%s', 'Session %s', '%s', '/srv/work', 'custom', '%s', '%s', '%s', %d, 1, 1)`,
			id, id, harness, options, tmuxName, state, startAt)
	}
	later := time.Now().Add(24 * time.Hour).UnixMilli()
	for _, q := range []string{
		string(schema),
		`INSERT INTO kv(key, value) VALUES('profiles', '[{"id":"p-work","name":"Work","created_at":1}]')`,
		`INSERT INTO kv(key, value) VALUES('settings', '` + oldSettings + `')`,
		row("sh1", "shell", store.StateRunning, `{}`, 0),
		row("cl1", "claude", store.StateRunning, `{"ultracode":true}`, 0),
		row("cx1", "codex", store.StateRunning, `{"profile":"p-work"}`, 0),
		row("oc1", "opencode", store.StateExited, `{}`, 0),
		// Prepared and never started: in the profile, which has a Claude
		// Code account; in the default profile; left to the start; and in
		// the profile for an agent it has no account of.
		row("pc1", "claude", store.StatePrestart, `{"profile":"p-work","ultracode":true}`, later),
		row("pc2", "codex", store.StatePrestart, `{}`, 0),
		row("pc3", "opencode", store.StatePrestart, `{"profile_auto":true}`, 0),
		row("pc4", "opencode", store.StatePrestart, `{"profile":"p-work"}`, 0),
		`INSERT INTO drafts(session_id, text, rev, updated_at) VALUES('pc1', 'Refactor the parser', 3, 1),
			('pc2', 'Write the docs', 1, 1), ('pc3', 'Fix the flaky test', 2, 1), ('pc4', 'Port it to Go', 1, 1)`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	// The live half: a shell, and a program standing in for each agent's
	// TUI, on the data directory's own socket as today's build runs them.
	t.Cleanup(func() { _, _ = o.tmuxRun("kill-server") })
	for id, argv := range map[string][]string{"sh1": {"/bin/sh"}, "cl1": {"sleep", "3600"}, "cx1": {"sleep", "3600"}} {
		args := append([]string{"new-session", "-d", "-s", termux.TmuxName(id), "-x", "80", "-y", "24"}, argv...)
		if out, err := o.tmuxRun(args...); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	return o
}

func (o *oldInstall) write(path, content string) {
	o.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		o.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		o.t.Fatal(err)
	}
}

func (o *oldInstall) tmuxRun(args ...string) (string, error) {
	out, err := exec.Command(o.tmux, append([]string{"-S", filepath.Join(o.data, "tmux.sock")}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// live is the tmux sessions on the fixture's socket.
func (o *oldInstall) live() []string {
	out, err := o.tmuxRun("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil
	}
	names := strings.Fields(out)
	sort.Strings(names)
	return names
}

func (o *oldInstall) env() []string { return []string{"HOME=" + o.home} }

// start is one start of this build as far as the migration goes.
func (o *oldInstall) start() {
	o.t.Helper()
	st := o.store()
	defer st.Close()
	if err := migrate(o.data, st, o.env(), o.t.Logf); err != nil {
		o.t.Fatalf("the migration: %v", err)
	}
}

func (o *oldInstall) store() *store.Store {
	o.t.Helper()
	st, err := store.Open(filepath.Join(o.data, "socrates.db"))
	if err != nil {
		o.t.Fatal(err)
	}
	return st
}

// picture is what a migration left, in words two runs can be compared by:
// every row with its options (an account named by its origin, whose id is
// new in every run) and draft, every account, the live tmux sessions and the
// sessions' folders.
func (o *oldInstall) picture() []string {
	o.t.Helper()
	st := o.store()
	defer st.Close()
	list, err := st.ListAccounts()
	if err != nil {
		o.t.Fatal(err)
	}
	origin := map[string]string{}
	var out []string
	for _, a := range list {
		origin[a.ID] = a.Origin
		out = append(out, "account "+strings.Join([]string{a.Agent, a.Label, a.Origin, a.State}, "|"))
	}
	rows, err := st.ListSessions()
	if err != nil {
		o.t.Fatal(err)
	}
	for _, row := range rows {
		var options map[string]any
		_ = json.Unmarshal(row.Options, &options)
		if id, ok := options["account"].(string); ok {
			options["account"] = origin[id]
		}
		raw, _ := json.Marshal(options)
		draft, _ := st.GetDraft(row.ID)
		out = append(out, "session "+strings.Join([]string{row.ID, row.Kind, row.Harness, row.State, string(raw), draft.Text}, "|"))
	}
	for _, name := range o.live() {
		out = append(out, "tmux "+name)
	}
	folders, _ := os.ReadDir(filepath.Join(o.data, "sessions"))
	for _, f := range folders {
		out = append(out, "folder sessions/"+f.Name())
	}
	if pending, _ := st.AccountsMigrationPending(); pending {
		out = append(out, "pending")
	}
	sort.Strings(out)
	return out
}

func (o *oldInstall) accountOf(origin string) store.Account {
	o.t.Helper()
	st := o.store()
	defer st.Close()
	list, _ := st.ListAccounts()
	for _, a := range list {
		if a.Origin == origin {
			return a
		}
	}
	o.t.Fatalf("no account from %s", origin)
	return store.Account{}
}

// The whole migration on today's installation, and the start after it.
func TestTheWholeMigrationIsRehearsed(t *testing.T) {
	o := newOldInstall(t)
	o.start()
	got := o.picture()
	want := []string{
		"account claude|Default|~/.claude|" + accounts.NeedsSignIn,
		"account claude|Work|profile:p-work:claude|" + accounts.NeedsSignIn,
		"account codex|Default|~/.codex|" + accounts.NeedsSignIn,
		"account opencode|Default|~/.local/share/opencode|" + accounts.NeedsSignIn,
		`session pc1|chat|claude|prestart|{"account":"profile:p-work:claude","ultracode":true}|Refactor the parser`,
		`session pc2|chat|codex|prestart|{"account":"~/.codex"}|Write the docs`,
		`session pc3|chat|opencode|prestart|{}|Fix the flaky test`,
		`session sh1|terminal|shell|running|{}|`,
		"tmux " + termux.TmuxName("sh1"),
		"folder sessions/sh1",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("after the migration:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The agents' terminals went as a delete takes them: no tmux session, no
	// row, no folder - and what they said is in the folders every account
	// shares.
	for _, id := range []string{"cl1", "cx1", "oc1"} {
		if _, err := os.Stat(filepath.Join(o.data, "sessions", id)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the folder of %s is still there: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(o.data, "sessions", "sh1", "plan.json")); err != nil {
		t.Fatalf("the Shell lost its folder: %v", err)
	}
	for _, rel := range []string{"agents/claude/projects/-srv-work/conv-cl1.jsonl",
		"agents/codex/sessions/2026/10/20/rollout-2026-10-20T08-00-00-conv-cx1.jsonl"} {
		if _, err := os.Stat(filepath.Join(o.data, rel)); err != nil {
			t.Fatalf("a transcript of a removed terminal is not shared: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(o.home, ".claude/projects/-srv-work/conv-cl1.jsonl")); err != nil {
		t.Fatalf("a transcript is not reachable through the old path: %v", err)
	}

	// The logins are one file each, seen through the old path and the new.
	claudeDir := accounts.Folder(o.data, accounts.Claude, o.accountOf(accounts.OriginClaude).ID)
	for _, pair := range [][2]string{
		{filepath.Join(o.home, ".claude/.credentials.json"), filepath.Join(claudeDir, ".credentials.json")},
		{filepath.Join(o.home, ".claude.json"), filepath.Join(claudeDir, ".claude.json")},
		{filepath.Join(o.home, ".codex/auth.json"), filepath.Join(accounts.Folder(o.data, accounts.Codex, o.accountOf(accounts.OriginCodex).ID), "auth.json")},
		{filepath.Join(o.data, "profiles/p-work/claude/.credentials.json"), filepath.Join(accounts.Folder(o.data, accounts.Claude, o.accountOf("profile:p-work:claude").ID), ".credentials.json")},
	} {
		a, err := os.Stat(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(a, b) {
			t.Fatalf("%s and %s are two files", pair[0], pair[1])
		}
	}

	// Nothing else was lost: the settings, the profiles' record, the bucket
	// file for the backup to take over, and the backup of all of it.
	st := o.store()
	for key, want := range map[string]string{"settings": oldSettings} {
		if got, err := st.GetKV(key); err != nil || got != want {
			t.Fatalf("%s = %q, %v", key, got, err)
		}
	}
	if _, err := st.GetKV("profiles"); err != nil {
		t.Fatalf("the profiles' record: %v", err)
	}
	st.Close()
	if raw, err := os.ReadFile(filepath.Join(o.data, paths.LegacyS3Config)); err != nil || string(raw) != oldS3 {
		t.Fatalf("s3.json: %q %v", raw, err)
	}
	if fi, err := os.Stat(paths.New(o.data).PreRebuildBackup()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the backup before the migration: %v %v", fi, err)
	}

	// The next start of the server takes the Shell in as it was: running,
	// attached to the pane it had, and nothing recovered beside it.
	st = o.store()
	m, err := termux.New(st, termux.Config{DataDir: o.data, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	m.Close()
	rows, _ := st.ListSessions()
	st.Close()
	for _, row := range rows {
		if row.Title == termux.RecoveredTitle {
			t.Fatalf("a terminal was recovered: %+v", row)
		}
	}
	if got := o.picture(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("after the server took the sessions in:\n%s", strings.Join(got, "\n"))
	}

	// A second start changes nothing, and neither does a run of the whole
	// migration again.
	o.start()
	if again := o.picture(); strings.Join(again, "\n") != strings.Join(want, "\n") {
		t.Fatalf("a second start changed:\n%s", strings.Join(again, "\n"))
	}
	st = o.store()
	if err := st.SetKV("accounts.migration", "pending"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	o.start()
	if again := o.picture(); strings.Join(again, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the migration run again changed:\n%s", strings.Join(again, "\n"))
	}
}

// A start killed in the middle of its clean cut - one agent terminal gone,
// the next one being stopped - is finished by the next start, with the same
// result as a start that ran through.
func TestAKilledMigrationIsFinishedByTheNextStart(t *testing.T) {
	clean := newOldInstall(t)
	clean.start()
	want := clean.picture()

	o := newOldInstall(t)
	// A tmux that lets the first kill-session through and hangs in the
	// second, which is where the start is killed.
	wrap := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = kill-session ]; then
    if [ -e %[1]s/once ]; then : > %[1]s/ready; exec sleep 600; fi
    : > %[1]s/once
  fi
done
exec %[2]s "$@"
`, wrap, o.tmux)
	if err := os.WriteFile(filepath.Join(wrap, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^$")
	child.Env = append(os.Environ(), rehearsalChild+"="+o.data, "HOME="+o.home,
		"PATH="+wrap+string(os.PathListSeparator)+os.Getenv("PATH"))
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(wrap, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			_ = child.Wait()
			t.Fatal("the start never reached its second agent terminal")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(-child.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()

	half := o.picture()
	if !strings.Contains(strings.Join(half, "\n"), "pending") || len(o.live()) != 2 {
		t.Fatalf("the killed start is not half way:\n%s", strings.Join(half, "\n"))
	}
	o.start()
	if got := o.picture(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("after the next start:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// rehearsalStart is the start TestAKilledMigrationIsFinishedByTheNextStart
// kills: the migration of the fixture at data, with the environment it was
// given, until the kill.
func rehearsalStart(data string) int {
	st, err := store.Open(filepath.Join(data, "socrates.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	if err := migrate(data, st, []string{"HOME=" + os.Getenv("HOME")}, func(string, ...any) {}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// requireTmux skips a test where tmux is missing or older than the version
// the substrate needs.
func requireTmux(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := termux.BinaryVersion(ctx, bin)
	if err != nil || !v.OK() {
		t.Skipf("no usable tmux: %v %v", v, err)
	}
	return bin
}
