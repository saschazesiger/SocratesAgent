//go:build linux

package accounts

import (
	"archive/tar"
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The rehearsal of REBUILD.md §8.1-8.2 on a synthetic copy of today's
// installation: a HOME with the three agents' own folders and logins, and a
// data directory of today's main (schema 9) whose profiles are partly there
// and partly lost. Never a real HOME.

// The logins of the fixture, each a file that must exist exactly once after
// the migration.
var fixtureLogins = map[string]string{
	".claude/.credentials.json":       `{"claudeAiOauth":{"accessToken":"at-default","refreshToken":"rt-default","subscriptionType":"max"}}`,
	".claude.json":                    `{"numStartups":5,"oauthAccount":{"emailAddress":"me@example.com"}}`,
	".codex/auth.json":                `{"auth_mode":"chatgpt","tokens":{"id_token":"` + fakeIDToken("codex@example.com") + `","refresh_token":"rt-codex"}}`,
	".local/share/opencode/auth.json": `{"openai":{"type":"oauth","refresh":"rt-opencode","access":"at-opencode","expires":1}}`,
}

// The folders of the multi-account tools, which the migration leaves alone.
var multiAuth = map[string]string{
	".codex-multi/one/auth.json":                     `{"tokens":{"refresh_token":"rt-multi"}}`,
	".config/opencode-multi-auth/accounts.json":      `{"rt":"rt-multi-oc"}`,
	".config/opencode-multi-auth/one/opencode.jsonc": "{}",
}

// The profile with its folders still there.
var profileLogins = map[string]string{
	"profiles/p-bbbbbbbbbbbb/claude/.credentials.json": `{"claudeAiOauth":{"accessToken":"at-work","refreshToken":"rt-work"}}`,
	"profiles/p-bbbbbbbbbbbb/codex/auth.json":          `{"OPENAI_API_KEY":"sk-work"}`,
}

func fakeIDToken(email string) string {
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"`+email+`"}`)) + ".sig"
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture is a HOME and a data directory as today's installation has them.
func fixture(t *testing.T) (data, home string) {
	t.Helper()
	root := t.TempDir()
	data, home = filepath.Join(root, "data"), filepath.Join(root, "home")
	for rel, content := range fixtureLogins {
		write(t, filepath.Join(home, rel), content)
	}
	write(t, filepath.Join(home, ".claude/projects/-srv-repo/s-default.jsonl"), `{"type":"user"}`+"\n")
	write(t, filepath.Join(home, ".claude/settings.json"), `{}`)
	write(t, filepath.Join(home, ".codex/sessions/2026/10/01/rollout-2026-10-01-default.jsonl"), `{"type":"session_meta"}`+"\n")
	write(t, filepath.Join(home, ".codex/config.toml"), "model = \"gpt\"\n")
	write(t, filepath.Join(home, ".local/share/opencode/opencode.db"), "sqlite")
	write(t, filepath.Join(home, ".local/share/opencode/opencode.db-wal"), "wal")
	write(t, filepath.Join(home, ".local/share/opencode/storage/session_diff/ses_1.json"), "[]")
	write(t, filepath.Join(home, ".local/share/opencode/snapshot/proj/abc/HEAD"), "ref")
	write(t, filepath.Join(home, ".config/opencode/opencode.jsonc"), "{}")
	for rel, content := range multiAuth {
		write(t, filepath.Join(home, rel), content)
	}
	for rel, content := range profileLogins {
		write(t, filepath.Join(data, rel), content)
	}
	write(t, filepath.Join(data, "profiles/p-bbbbbbbbbbbb/claude/projects/-srv-other/s-work.jsonl"), `{"type":"user"}`+"\n")
	write(t, filepath.Join(data, "profiles/p-bbbbbbbbbbbb/codex/sessions/2026/10/02/rollout-2026-10-02-work.jsonl"), `{}`+"\n")

	schema, err := os.ReadFile(filepath.Join("..", "store", "testdata", "schema-v9.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		string(schema),
		`INSERT INTO kv(key, value) VALUES('profiles', '[{"id":"p-aaaaaaaaaaaa","name":"Team Sascha","created_at":1},
		 {"id":"p-bbbbbbbbbbbb","name":"Work","created_at":2},{"id":"p-cccccccccccc","name":"Campus","created_at":3}]')`,
		`INSERT INTO sessions(id, harness, workdir, workdir_mode, options, created_at, updated_at)
		 VALUES('s1', 'claude', '/srv', 'custom', '{"profile":"p-aaaaaaaaaaaa"}', 1, 1)`,
		`INSERT INTO sessions(id, harness, workdir, workdir_mode, options, created_at, updated_at)
		 VALUES('s2', 'codex', '/srv', 'custom', '{"profile":"p-cccccccccccc"}', 1, 1)`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return data, home
}

// migrateOnce opens the store as a start of the new build does and runs the
// migration, stopped after stop when it is set.
func migrateOnce(t *testing.T, data, home, stop string) error {
	t.Helper()
	return migrateWith(t, MigrateOptions{DataDir: data, Env: []string{"HOME=" + home}, stopAfter: stop})
}

func migrateWith(t *testing.T, opts MigrateOptions) error {
	t.Helper()
	st, err := store.Open(filepath.Join(opts.DataDir, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	opts.Store = st
	if opts.Logf == nil {
		opts.Logf = t.Logf
	}
	if opts.StopTerminal == nil {
		// The fixture's agent terminals have no tmux session behind them:
		// stopping one is removing its row. The rehearsal in package main
		// stops live ones through the terminal substrate.
		opts.StopTerminal = st.DeleteSession
	}
	return Migrate(opts)
}

// pending reports whether the store still marks the migration as due.
func pending(t *testing.T, data string) bool {
	t.Helper()
	st, err := store.Open(filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	due, err := st.AccountsMigrationPending()
	if err != nil {
		t.Fatal(err)
	}
	return due
}

// untouched fails unless every file of the fixture under rels is still a
// file of its own in home, with its content, and no folder above it is a
// link.
func untouched(t *testing.T, home string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		for p := filepath.Join(home, rel); p != home; p = filepath.Dir(p) {
			if fi, err := os.Lstat(p); err != nil || fi.Mode()&fs.ModeSymlink != 0 {
				t.Fatalf("%s was moved: %v %v", p, fi, err)
			}
		}
		if raw, err := os.ReadFile(filepath.Join(home, rel)); err != nil || string(raw) != content {
			t.Fatalf("%s: %q %v", rel, raw, err)
		}
	}
}

// outcome is what a migration left, in words that two runs can be compared
// by.
func outcome(t *testing.T, data string) []string {
	t.Helper()
	st, err := store.Open(filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	list, err := st.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := range list {
		a := list[i]
		login := Detect(data, &a)
		out = append(out, strings.Join([]string{a.Agent, a.Label, a.Origin, boolWord(login.SignedIn), login.Email, a.StateDetail}, "|"))
	}
	sort.Strings(out)
	return out
}

func boolWord(b bool) string {
	if b {
		return "signed in"
	}
	return "not signed in"
}

func accountByOrigin(t *testing.T, data, origin string) store.Account {
	t.Helper()
	st, err := store.Open(filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	list, _ := st.ListAccounts()
	for _, a := range list {
		if a.Origin == origin {
			return a
		}
	}
	t.Fatalf("no account from %s", origin)
	return store.Account{}
}

func sameFile(t *testing.T, a, b string) {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fa, fb) {
		t.Fatalf("%s and %s are two files", a, b)
	}
}

func isLink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("%s is not a link: %v", path, err)
	}
	return target
}

// copies counts the regular files under the roots whose content is content,
// one per inode.
func copies(t *testing.T, content string, roots ...string) int {
	t.Helper()
	inodes := map[string]bool{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != content {
				return nil
			}
			fi, _ := os.Stat(path)
			inodes[inodeOf(fi)] = true
			return nil
		})
	}
	return len(inodes)
}

func TestTheMigrationMovesEveryLoginOnceAndLinksTheOldPaths(t *testing.T) {
	data, home := fixture(t)
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	got := outcome(t, data)
	want := []string{
		"claude|Default|~/.claude|signed in|me@example.com|",
		"claude|Team Sascha|lost:p-aaaaaaaaaaaa:claude|not signed in||" + lostDetail,
		"claude|Work|profile:p-bbbbbbbbbbbb:claude|signed in||",
		"codex|Campus|lost:p-cccccccccccc:codex|not signed in||" + lostDetail,
		"codex|Default|~/.codex|signed in|codex@example.com|",
		"codex|Work|profile:p-bbbbbbbbbbbb:codex|signed in||",
		"opencode|Default|~/.local/share/opencode|signed in||",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("accounts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Claude Code: the folder and its global config moved, the old paths are
	// links, and the login is one file seen through both.
	claudeAcct := accountByOrigin(t, data, OriginClaude)
	dir := Folder(data, Claude, claudeAcct.ID)
	if target := isLink(t, filepath.Join(home, ".claude")); target != dir {
		t.Fatalf("~/.claude -> %s, want %s", target, dir)
	}
	isLink(t, filepath.Join(home, ".claude.json"))
	sameFile(t, filepath.Join(home, ".claude/.credentials.json"), filepath.Join(dir, ".credentials.json"))
	sameFile(t, filepath.Join(home, ".claude.json"), filepath.Join(dir, ".claude.json"))
	refreshed := `{"claudeAiOauth":{"accessToken":"at-new","refreshToken":"rt-new"}}`
	write(t, filepath.Join(home, ".claude/.credentials.json"), refreshed)
	if raw, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); string(raw) != refreshed {
		t.Fatalf("a refresh through the old path is not seen in the account: %s", raw)
	}
	if target := isLink(t, filepath.Join(dir, "projects")); target != "../../agents/claude/projects" {
		t.Fatalf("projects -> %s", target)
	}
	for _, rel := range []string{"agents/claude/projects/-srv-repo/s-default.jsonl", "agents/claude/projects/-srv-other/s-work.jsonl"} {
		if _, err := os.Stat(filepath.Join(data, rel)); err != nil {
			t.Fatalf("a transcript is not in the shared folder: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/projects/-srv-repo/s-default.jsonl")); err != nil {
		t.Fatalf("a transcript is not reachable through the old path: %v", err)
	}
	work := accountByOrigin(t, data, "profile:p-bbbbbbbbbbbb:claude")
	if target := isLink(t, filepath.Join(data, "profiles/p-bbbbbbbbbbbb/claude")); target != "../../accounts/"+work.ID {
		t.Fatalf("the profile's folder -> %s", target)
	}

	// Codex.
	codexAcct := accountByOrigin(t, data, OriginCodex)
	codexDir := Folder(data, Codex, codexAcct.ID)
	if target := isLink(t, filepath.Join(home, ".codex")); target != codexDir {
		t.Fatalf("~/.codex -> %s", target)
	}
	sameFile(t, filepath.Join(home, ".codex/auth.json"), filepath.Join(codexDir, "auth.json"))
	if _, err := os.Stat(filepath.Join(data, "agents/codex/sessions/2026/10/01/rollout-2026-10-01-default.jsonl")); err != nil {
		t.Fatalf("the rollout is not shared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "agents/codex/sessions/2026/10/02/rollout-2026-10-02-work.jsonl")); err != nil {
		t.Fatalf("the profile's rollout is not shared: %v", err)
	}

	// OpenCode: the data folder is the Default account's, its database the
	// shared one, its configuration the layout's.
	ocAcct := accountByOrigin(t, data, OriginOpenCode)
	ocData := filepath.Join(Folder(data, OpenCode, ocAcct.ID), "opencode")
	if target := isLink(t, filepath.Join(home, ".local/share/opencode")); target != ocData {
		t.Fatalf("~/.local/share/opencode -> %s", target)
	}
	sameFile(t, filepath.Join(home, ".local/share/opencode/auth.json"), filepath.Join(ocData, "auth.json"))
	for _, rel := range []string{"opencode.db", "opencode.db-wal", "storage/session_diff/ses_1.json", "snapshot/proj/abc/HEAD", "config/opencode/opencode.jsonc"} {
		if _, err := os.Stat(filepath.Join(data, "agents/opencode", rel)); err != nil {
			t.Fatalf("OpenCode's %s is not in the layout: %v", rel, err)
		}
	}
	isLink(t, filepath.Join(home, ".config/opencode"))
	// The database left the account's folder for the shared one, and an
	// opencode started by hand with HOME's folders still opens it.
	sameFile(t, filepath.Join(home, ".local/share/opencode/opencode.db"), filepath.Join(data, "agents/opencode/opencode.db"))
	untouched(t, home, multiAuth)

	// Not one login exists twice (the backup tar aside, which nothing can
	// run on).
	for _, content := range []string{refreshed, fixtureLogins[".claude.json"], fixtureLogins[".codex/auth.json"],
		fixtureLogins[".local/share/opencode/auth.json"], profileLogins["profiles/p-bbbbbbbbbbbb/codex/auth.json"]} {
		if n := copies(t, content, home, data); n != 1 {
			t.Fatalf("a login exists %d times: %s", n, content)
		}
	}

	// The backup holds what was moved, as it was.
	entries := tarEntries(t, data+".pre-rebuild.tar")
	for _, name := range []string{"data/socrates.db", "home/.claude/.credentials.json", "home/.claude.json",
		"home/.codex/auth.json", "home/.local/share/opencode/auth.json", "home/.config/opencode/opencode.jsonc",
		"data/profiles/p-bbbbbbbbbbbb/claude/.credentials.json"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("the backup has no %s", name)
		}
	}
	if entries["home/.claude/.credentials.json"] != fixtureLogins[".claude/.credentials.json"] {
		t.Fatalf("the backup's login: %q", entries["home/.claude/.credentials.json"])
	}
	if fi, _ := os.Stat(data + ".pre-rebuild.tar"); fi.Mode().Perm() != 0o600 {
		t.Fatalf("the backup is %v", fi.Mode())
	}

	// Once: a second start does nothing, and one that runs it again finds
	// everything done.
	if pending(t, data) {
		t.Fatal("the finished migration is still due")
	}
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	markPending(t, data)
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	if again := outcome(t, data); strings.Join(again, "\n") != strings.Join(got, "\n") {
		t.Fatalf("a second run changed the accounts:\n%s", strings.Join(again, "\n"))
	}
}

// The tar is the way back to the build before: its database is the one that
// build wrote (schema 9, with the agent terminals the cut removed), and it
// keeps what that build reads that a backup leaves out - the mirror's
// s3.json - and the agent terminals' folders with their journals, which the
// cut deletes.
func TestThePreRebuildTarIsTheWayBack(t *testing.T) {
	data, home := fixture(t)
	write(t, filepath.Join(data, "s3.json"), `{"bucket":"b1"}`)
	write(t, filepath.Join(data, "sessions/s1/journal.raw"), "the terminal's bytes")
	write(t, filepath.Join(data, "sessions/s1/journal.1.raw"), "older bytes")
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	entries := tarEntries(t, data+".pre-rebuild.tar")
	for name, want := range map[string]string{
		"data/s3.json":                   `{"bucket":"b1"}`,
		"data/sessions/s1/journal.raw":   "the terminal's bytes",
		"data/sessions/s1/journal.1.raw": "older bytes",
	} {
		if got, ok := entries[name]; !ok || got != want {
			t.Fatalf("the tar's %s: %q (%v), want %q", name, got, ok, want)
		}
	}
	for name := range entries {
		if strings.HasPrefix(name, "data/socrates.db") && name != "data/socrates.db" {
			t.Fatalf("the tar has %s beside the database", name)
		}
	}
	old := filepath.Join(t.TempDir(), "socrates.db")
	if err := os.WriteFile(old, []byte(entries["data/socrates.db"]), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+old)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, terminals int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id IN ('s1', 's2')`).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if version != 9 || terminals != 2 {
		t.Fatalf("the tar's database is at schema %d with %d of the 2 agent terminals, want 9 with both", version, terminals)
	}
}

// OpenCode's database is one database through both its paths: a write made
// through HOME's old one, as an opencode started by hand makes it, is read
// through the shared one every chat is given, and SQLite keeps its
// write-ahead log beside the file the link names, not beside the link.
func TestOpenCodesDatabaseIsOneThroughTheOldPath(t *testing.T) {
	data, home := fixture(t)
	oldPath := filepath.Join(home, ".local/share/opencode/opencode.db")
	for _, suffix := range []string{"", "-wal"} {
		if err := os.Remove(oldPath + suffix); err != nil {
			t.Fatal(err)
		}
	}
	run := func(path string, queries ...string) {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, q := range queries {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	run(oldPath, `CREATE TABLE session(id TEXT)`, `INSERT INTO session VALUES('before')`)
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(data, "agents/opencode/opencode.db")
	run(oldPath, `INSERT INTO session VALUES('by hand')`)
	db, err := sql.Open("sqlite", "file:"+shared)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ids []string
	rows, err := db.Query(`SELECT id FROM session ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if strings.Join(ids, ",") != "before,by hand" {
		t.Fatalf("the shared database has %v", ids)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(oldPath + suffix); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("SQLite made %s beside the link: %v", filepath.Base(oldPath+suffix), err)
		}
	}
}

// The clean cut says each terminal it removed once, also when the start that
// removed it was cut short before the others.
func TestEachTerminalTheCutRemovesIsSaidOnce(t *testing.T) {
	data, home := fixture(t)
	var said []string
	logf := func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) }
	env := []string{"HOME=" + home}
	if err := migrateWith(t, MigrateOptions{DataDir: data, Env: env, Logf: logf, stopAfter: "cut:s1"}); !errors.Is(err, errStopped) {
		t.Fatalf("the run stopped after the first terminal with %v", err)
	}
	if err := migrateWith(t, MigrateOptions{DataDir: data, Env: env, Logf: logf}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s2"} {
		n := 0
		for _, line := range said {
			if strings.Contains(line, "removed") && strings.Contains(line, "("+id+")") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("the removal of %s was said %d times:\n%s", id, n, strings.Join(said, "\n"))
		}
	}
}

// A migration cut short after any step is finished by the next start, with
// the same result as one that ran through.
func TestAnInterruptedMigrationIsFinishedByTheNextStart(t *testing.T) {
	clean, cleanHome := fixture(t)
	if err := migrateOnce(t, clean, cleanHome, ""); err != nil {
		t.Fatal(err)
	}
	want := outcome(t, clean)
	for _, step := range []string{
		"backup", "cut:s1", "cut:s2", "account:" + OriginClaude, "moved:" + OriginClaude, "json:" + OriginClaude, "shared:" + OriginClaude,
		"account:" + OriginCodex, "moved:" + OriginCodex, "moved:" + OriginOpenCode, "shared:" + OriginOpenCode,
		"account:profile:p-bbbbbbbbbbbb:claude", "moved:profile:p-bbbbbbbbbbbb:codex", "lost:lost:p-aaaaaaaaaaaa:claude",
	} {
		t.Run(strings.ReplaceAll(step, "/", "_"), func(t *testing.T) {
			data, home := fixture(t)
			if err := migrateOnce(t, data, home, step); !errors.Is(err, errStopped) {
				t.Fatalf("the run stopped after %s with %v", step, err)
			}
			if err := migrateOnce(t, data, home, ""); err != nil {
				t.Fatalf("the next start: %v", err)
			}
			if got := outcome(t, data); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("after a stop at %s:\n%s\nwant:\n%s", step, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			claudeAcct := accountByOrigin(t, data, OriginClaude)
			sameFile(t, filepath.Join(home, ".claude/.credentials.json"), filepath.Join(Folder(data, Claude, claudeAcct.ID), ".credentials.json"))
			sameFile(t, filepath.Join(home, ".claude.json"), filepath.Join(Folder(data, Claude, claudeAcct.ID), ".claude.json"))
			if n := copies(t, fixtureLogins[".claude/.credentials.json"], home, data); n != 1 {
				t.Fatalf("the login exists %d times", n)
			}
			if _, err := os.Stat(filepath.Join(data, "agents/claude/projects/-srv-repo/s-default.jsonl")); err != nil {
				t.Fatalf("a transcript was lost: %v", err)
			}
		})
	}
}

// A data directory this build makes - a new installation, a trial with
// -data /tmp/x, an e2e run - never takes the agents' folders out of HOME,
// however many there are: only a database of the build before accounts is
// marked for the migration.
func TestANewDataDirectoryLeavesHomeAlone(t *testing.T) {
	_, home := fixture(t)
	data := filepath.Join(t.TempDir(), "data")
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	if got := outcome(t, data); len(got) != 0 {
		t.Fatalf("accounts %v", got)
	}
	untouched(t, home, fixtureLogins)
	untouched(t, home, map[string]string{".config/opencode/opencode.jsonc": "{}"})
	if _, err := os.Stat(data + ".pre-rebuild.tar"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a backup of nothing: %v", err)
	}
}

// An old data directory copied into the temporary directory - a rehearsal -
// has its profiles migrated, but a HOME outside the temporary directory is
// not moved into it: the copy is about to be deleted, and the logins with
// it.
func TestATemporaryDataDirectoryLeavesARealHomeAlone(t *testing.T) {
	data, home := fixture(t)
	root := t.TempDir()
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	trial := filepath.Join(tmp, "data")
	if err := os.Rename(data, trial); err != nil {
		t.Fatal(err)
	}
	if err := migrateWith(t, MigrateOptions{DataDir: trial, Env: []string{"HOME=" + home}, tempDir: tmp}); err != nil {
		t.Fatal(err)
	}
	untouched(t, home, fixtureLogins)
	got := outcome(t, trial)
	for _, line := range got {
		if strings.Contains(line, "|Default|") {
			t.Fatalf("a Default account was made of the real HOME: %v", got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("the profiles' accounts: %v", got)
	}
	if pending(t, trial) {
		t.Fatal("the migration is still due")
	}
}

// CODEX_HOME pointing into ~/.codex-multi is one of the multi-account
// tool's accounts, which stays where it is.
func TestTheMultiAccountFoldersStayWhereTheyAre(t *testing.T) {
	data, home := fixture(t)
	env := []string{"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex-multi", "one")}
	if err := migrateWith(t, MigrateOptions{DataDir: data, Env: env}); err != nil {
		t.Fatal(err)
	}
	untouched(t, home, multiAuth)
	for _, line := range outcome(t, data) {
		if strings.HasPrefix(line, "codex|Default|") {
			t.Fatalf("the multi-account tool's folder became an account: %s", line)
		}
	}
}

// A crash between the move of a folder and the link back - where the file
// system cannot swap the two in one step - leaves nothing at the old path.
// The account made before the move is what tells the next start that the
// link and the rest are still to do.
func TestACrashBetweenTheMoveAndTheLinkIsFinished(t *testing.T) {
	clean, cleanHome := fixture(t)
	if err := migrateOnce(t, clean, cleanHome, ""); err != nil {
		t.Fatal(err)
	}
	want := outcome(t, clean)
	for _, c := range []struct{ origin, from string }{
		{OriginCodex, ".codex"},
		{OriginOpenCode, ".local/share/opencode"},
		{"profile:p-bbbbbbbbbbbb:claude", "profiles/p-bbbbbbbbbbbb/claude"},
	} {
		t.Run(strings.ReplaceAll(c.origin, "/", "_"), func(t *testing.T) {
			data, home := fixture(t)
			if err := migrateOnce(t, data, home, "account:"+c.origin); !errors.Is(err, errStopped) {
				t.Fatalf("the run stopped with %v", err)
			}
			a := accountByOrigin(t, data, c.origin)
			from := filepath.Join(home, c.from)
			if strings.HasPrefix(c.origin, "profile:") {
				from = filepath.Join(data, c.from)
			}
			dst := Folder(data, a.Agent, a.ID)
			if a.Agent == OpenCode {
				dst = filepath.Join(dst, "opencode")
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(from, dst); err != nil {
				t.Fatal(err)
			}
			if err := migrateOnce(t, data, home, ""); err != nil {
				t.Fatalf("the next start: %v", err)
			}
			if got := outcome(t, data); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("after the crash:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			isLink(t, from)
			if _, err := os.Stat(filepath.Join(data, "agents/codex/sessions/2026/10/01/rollout-2026-10-01-default.jsonl")); err != nil {
				t.Fatalf("the Default Codex account's sessions are not shared: %v", err)
			}
		})
	}
}

// A swap cut short after its link was made at the new place, before the
// two names were exchanged, is made again by the next start: the link in
// the way is the migration's own.
func TestASwapCutShortBeforeItsExchangeIsFinished(t *testing.T) {
	data, home := fixture(t)
	if err := migrateOnce(t, data, home, "account:"+OriginClaude); !errors.Is(err, errStopped) {
		t.Fatalf("the run stopped with %v", err)
	}
	a := accountByOrigin(t, data, OriginClaude)
	dst := Folder(data, Claude, a.ID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dst, dst); err != nil {
		t.Fatal(err)
	}
	// And one of the shared folders of the profile's account, set aside.
	work := filepath.Join(data, "profiles/p-bbbbbbbbbbbb/claude")
	if err := os.Symlink("../../agents/claude/projects", filepath.Join(work, "projects.pre-rebuild")); err != nil {
		t.Fatal(err)
	}
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatalf("the next start: %v", err)
	}
	sameFile(t, filepath.Join(home, ".claude/.credentials.json"), filepath.Join(dst, ".credentials.json"))
	for _, rel := range []string{"agents/claude/projects/-srv-repo/s-default.jsonl", "agents/claude/projects/-srv-other/s-work.jsonl"} {
		if _, err := os.Stat(filepath.Join(data, rel)); err != nil {
			t.Fatalf("a transcript is not in the shared folder: %v", err)
		}
	}
}

// A folder that cannot move holds up nothing else: a ~/.claude that is a
// link of the person's own stays, with ~/.claude.json beside it, and its
// account says why; one that fails is tried again by the next start, and
// the rest moves meanwhile.
func TestOneFolderThatCannotMoveHoldsUpNothingElse(t *testing.T) {
	data, home := fixture(t)
	dotfiles := filepath.Join(t.TempDir(), "dotfiles", "claude")
	if err := os.MkdirAll(filepath.Dir(dotfiles), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(home, ".claude"), dotfiles); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	// Codex's account is made, and then its folder is in the way.
	if err := migrateOnce(t, data, home, "account:"+OriginCodex); !errors.Is(err, errStopped) {
		t.Fatalf("the run stopped with %v", err)
	}
	blocked := Folder(data, Codex, accountByOrigin(t, data, OriginCodex).ID)
	write(t, filepath.Join(blocked, "in-the-way"), "x")
	if err := migrateOnce(t, data, home, ""); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("the run said %v", err)
	}
	if !pending(t, data) {
		t.Fatal("the migration counts as done with a folder not moved")
	}
	if target := isLink(t, filepath.Join(home, ".claude")); target != dotfiles {
		t.Fatalf("~/.claude -> %s", target)
	}
	untouched(t, home, map[string]string{".claude.json": fixtureLogins[".claude.json"]})
	claudeAcct := accountByOrigin(t, data, OriginClaude)
	if claudeAcct.State != NeedsSignIn || !strings.Contains(claudeAcct.StateDetail, "link") {
		t.Fatalf("the Default Claude account: %+v", claudeAcct)
	}
	ocAcct := accountByOrigin(t, data, OriginOpenCode)
	sameFile(t, filepath.Join(home, ".local/share/opencode/auth.json"), filepath.Join(Folder(data, OpenCode, ocAcct.ID), "opencode", "auth.json"))
	accountByOrigin(t, data, "lost:p-aaaaaaaaaaaa:claude")
	accountByOrigin(t, data, "profile:p-bbbbbbbbbbbb:claude")

	if err := os.RemoveAll(blocked); err != nil {
		t.Fatal(err)
	}
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatalf("the next start: %v", err)
	}
	isLink(t, filepath.Join(home, ".codex"))
	if pending(t, data) {
		t.Fatal("the migration is still due")
	}
}

// While a folder moves, its old path is always there - the folder or the
// link to it - so a CLI that writes by path never makes a second one.
func TestTheOldPathIsNeverMissingWhileItMoves(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		from, dst := filepath.Join(root, "home", strconv.Itoa(i)), filepath.Join(root, "data", strconv.Itoa(i))
		write(t, filepath.Join(from, ".credentials.json"), "login")
		stop, missing := make(chan struct{}), make(chan bool, 1)
		go func() {
			gone := false
			for {
				select {
				case <-stop:
					missing <- gone
					return
				default:
				}
				if _, err := os.Lstat(from); err != nil {
					gone = true
				}
			}
		}()
		err := swap(from, dst, dst)
		close(stop)
		if err != nil {
			t.Fatal(err)
		}
		if <-missing {
			t.Fatalf("%s was missing while it moved", from)
		}
		sameFile(t, filepath.Join(from, ".credentials.json"), filepath.Join(dst, ".credentials.json"))
	}
}

// markPending marks the migration as due again, as a database of the build
// before would be.
func markPending(t *testing.T, data string) {
	t.Helper()
	st, err := store.Open(filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetKV("accounts.migration", "pending"); err != nil {
		t.Fatal(err)
	}
}

func tarEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if _, err := io.Copy(&b, tr); err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(hdr.Name, "/")] = b.String()
	}
}

func inodeOf(fi fs.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fi.Name()
	}
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10)
}
