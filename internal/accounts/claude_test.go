//go:build !windows

package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// claudeURL is the page of a real `claude auth login --claudeai` (2.1.310),
// whose login was given up.
const claudeURL = "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e" +
	"&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback" +
	"&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference+user%3Asessions%3Aclaude_code+user%3Amcp_servers+user%3Afile_upload+user%3Aplugins" +
	"&code_challenge=TNfWXmMut5C31mZueDB2yjqWJbr_mQEkKZKaWI3Hc7E&code_challenge_method=S256&state=6YymHhTsjC5RsIJnbp5oyVS5U4F_ylptzzHFhV3CS2E"

// The page is read off what Claude Code prints, however it prints it: as a
// hyperlink on a terminal, as bare text on a pipe, wrapped and coloured by a
// narrow terminal; and not before the line it is on has ended.
func TestTheClaudeSignInPageIsReadOffItsOutput(t *testing.T) {
	cut := func(s string, at ...int) []string {
		var parts []string
		last := 0
		for _, i := range at {
			parts = append(parts, s[last:i])
			last = i
		}
		return append(parts, s[last:])
	}
	wrapped := cut(claudeURL, 80, 160, 240)
	cases := []struct {
		name, out, want string
	}{
		{"a terminal (captured)", "Opening browser to sign in…\r\nIf the browser didn't open, visit: \x1b]8;;" + claudeURL + "\x07\x1b[94m" +
			claudeURL + "\x1b[39m\x1b]8;;\x07\r\nPaste code here if prompted > ", claudeURL},
		{"a pipe (captured)", "Opening browser to sign in…\nIf the browser didn't open, visit: " + claudeURL + "\nPaste code here if prompted > ", claudeURL},
		{"a hyperlink ended by ESC \\", "visit: \x1b]8;id=1;" + claudeURL + "\x1b\\link\x1b]8;;\x1b\\", claudeURL},
		{"wrapped and coloured", "If the browser didn't open, visit:\r\n\x1b[94m" + strings.Join(wrapped, "\x1b[39m\r\n\x1b[94m") +
			"\x1b[39m\r\n\r\nPaste code here if prompted > ", claudeURL},
		{"wrapped, indented, redrawn", "\x1b[2K\x1b[1G  visit: " + wrapped[0] + "\r\n  " + wrapped[1] + "\n\x1b[2K  " + wrapped[2] + "\r  " +
			wrapped[3] + "\r\nPaste code here if prompted > ", claudeURL},
		{"a line of words after it", "visit: \x1b[94m" + claudeURL + "\x1b[39m\r\nPaste code here if prompted > ", claudeURL},
		{"text after it on its line", "visit: " + claudeURL + " (or press c to copy)\r\nnext\r\n", claudeURL},
		{"its line not ended yet", "visit: \x1b[94m" + wrapped[0], ""},
		{"a hyperlink cut short", "visit: \x1b]8;;" + wrapped[0], ""},
		{"a colour cut short", "visit: " + wrapped[0] + "\x1b[39m\r\n\x1b[9", ""},
		{"line ends a program wrote itself", "visit: " + wrapped[0] + "\r\r\n" + wrapped[1] + "\r\r\n" + wrapped[2] + wrapped[3] + "\r\r\nPaste code here if prompted > ", claudeURL},
		{"no page", "Opening browser to sign in…\r\n", ""},
	}
	for _, c := range cases {
		if got := claudeLoginURL(c.out); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// fakeClaudeLogin writes a stand-in for `claude auth login --claudeai`: it
// prints its page wrapped and coloured, asks for the code until it is one,
// and then signs in ("good…", written to CLAUDE_CONFIG_DIR) or is refused.
func fakeClaudeLogin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
[ "$*" = "auth login --claudeai" ] || { echo "unexpected arguments: $*"; exit 2; }
echo $$ > "$CLAUDE_CONFIG_DIR/pid"
printf 'Opening browser to sign in...\r\nIf the browser did not open, visit:\r\n'
printf '\033[94mhttps://claude.example/oauth/authorize?code=true&\033[39m\r\n\033[94mstate=xyz\033[39m\r\n'
while :; do
  printf 'Paste code here if prompted > '
  IFS= read -r code || exit 0
  case "$code" in
    good*) printf '%s' '{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt","subscriptionType":"max"}}' > "$CLAUDE_CONFIG_DIR/.credentials.json"
      printf 'Login successful.\r\n'; exit 0 ;;
    *'#'*) printf 'Login failed: Request failed with status code 400\r\n'; exit 1 ;;
    *) printf 'Invalid code. Please make sure the full code was copied.\r\n' ;;
  esac
done
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func claudeAccount(t *testing.T) (string, *store.Account, []string) {
	t.Helper()
	data := t.TempDir()
	a := &store.Account{ID: NewID(), Agent: Claude, Kind: Subscription}
	if err := Prepare(data, a); err != nil {
		t.Fatal(err)
	}
	return data, a, Env(data, a, os.Environ())
}

// Claude Code's sign-in shows its page, asks again for a code that is not
// one, and ends signed in when the code is taken; the login is Claude Code's
// own, in the account's folder.
func TestAClaudeSignInTakesThePastedCode(t *testing.T) {
	bin := fakeClaudeLogin(t)
	data, a, env := claudeAccount(t)
	ended := make(chan error, 1)
	s := NewSignIns()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	flow, err := s.StartClaude(ctx, a.ID, bin, env, func(err error) { ended <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Cancel(a.ID)
	if flow.State != FlowCode || flow.URL != "https://claude.example/oauth/authorize?code=true&state=xyz" || flow.Agent != Claude {
		t.Fatalf("the flow: %+v", flow)
	}
	if _, err := s.Code(ctx, a.ID, "   "); err == nil {
		t.Fatal("an empty code was taken")
	}
	if _, err := s.Code(ctx, a.ID, "not-a-code"); err == nil || !strings.Contains(err.Error(), "Invalid code") {
		t.Fatalf("a code that is not one: %v", err)
	}
	if got, _ := s.Get(a.ID); got.State != FlowCode {
		t.Fatalf("after a code that is not one: %+v", got)
	}
	flow, err = s.Code(ctx, a.ID, " good\n#state ")
	if err != nil || flow.State != FlowDone {
		t.Fatalf("the good code: %+v %v", flow, err)
	}
	if err := <-ended; err != nil {
		t.Fatalf("onDone: %v", err)
	}
	if got := Detect(data, a); !got.SignedIn || got.Plan != "max" {
		t.Fatalf("after the sign-in: %+v", got)
	}
}

// A code the server refuses ends the sign-in with Claude Code's own words.
func TestAClaudeSignInRefusedSaysWhy(t *testing.T) {
	bin := fakeClaudeLogin(t)
	data, a, env := claudeAccount(t)
	ended := make(chan error, 1)
	s := NewSignIns()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := s.StartClaude(ctx, a.ID, bin, env, func(err error) { ended <- err }); err != nil {
		t.Fatal(err)
	}
	flow, err := s.Code(ctx, a.ID, "wrong#state")
	if err != nil || flow.State != FlowFailed || flow.Error != "Login failed: Request failed with status code 400" {
		t.Fatalf("a refused code: %+v %v", flow, err)
	}
	if err := <-ended; err == nil {
		t.Fatal("onDone was told of a login")
	}
	if got := Detect(data, a); got.SignedIn {
		t.Fatalf("signed in after a refusal: %+v", got)
	}
}

// A sign-in that is given up kills Claude Code's; one that never shows a
// page is not started.
func TestAClaudeSignInGivenUpKillsIt(t *testing.T) {
	bin := fakeClaudeLogin(t)
	data, a, env := claudeAccount(t)
	ended := make(chan error, 1)
	s := NewSignIns()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := s.StartClaude(ctx, a.ID, bin, env, func(err error) { ended <- err }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(Folder(data, Claude, a.ID), "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	s.Cancel(a.ID)
	if err := <-ended; err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("onDone: %v", err)
	}
	if got, _ := s.Get(a.ID); got.State != FlowFailed {
		t.Fatalf("after cancelling: %+v", got)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("claude %d still runs after the cancel: %v", pid, err)
	}

	quiet := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(quiet, []byte("#!/bin/sh\necho 'Not logged in: no network'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartClaude(ctx, a.ID, quiet, env, nil); err == nil || !strings.Contains(err.Error(), "no network") {
		t.Fatalf("a sign-in without a page: %v", err)
	}
}
