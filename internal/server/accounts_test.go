//go:build linux

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/accounts"
	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// rawCall is a request whose whole answer is kept as it came, so that a test
// can say what it does not contain.
func (c *chatEnv) rawCall(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, c.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func (c *chatEnv) newAccount(t *testing.T, label, kind, key string) string {
	t.Helper()
	body := map[string]any{"agent": accounts.Fake, "kind": kind, "label": label}
	if key != "" {
		body["key"] = key
	}
	code, raw := c.rawCall(t, "POST", "/api/accounts", body)
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", label, code, raw)
	}
	if key != "" && strings.Contains(raw, key) {
		t.Fatalf("the answer holds the key: %s", raw)
	}
	var out struct {
		Account accountView `json:"account"`
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out.Account.ID
}

func (c *chatEnv) listAccounts(t *testing.T) ([]accountView, map[string]string, string) {
	t.Helper()
	code, raw := c.rawCall(t, "GET", "/api/accounts", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, raw)
	}
	var out struct {
		Accounts    []accountView     `json:"accounts"`
		Recommended map[string]string `json:"recommended"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Accounts, out.Recommended, raw
}

func (c *chatEnv) chatOn(t *testing.T, account string, autoSwitch bool) string {
	t.Helper()
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "create-" + t.Name() + account, "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data, "account": account, "auto_switch": autoSwitch})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	return body["session"].(map[string]any)["id"].(string)
}

func (c *chatEnv) reply(t *testing.T, id, clientID, text string) string {
	t.Helper()
	before := count(c.journal(t, id), isEv(agent.TypeTurnEnded))
	c.send(t, id, clientID, text)
	events := c.waitJournal(t, id, 15*time.Second, "the reply to "+text, turnsEnded(before+1))
	return lastText(events)
}

// Accounts are added, renamed, ordered and deleted from the dashboard, and an
// API key goes in and never comes out again: not in an answer, and not in
// the clear in the store.
func TestAccountsAreManagedAndTheirKeysNeverShown(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	sub := c.newAccount(t, "Personal", accounts.Subscription, "")
	key := c.newAccount(t, "Work key", accounts.APIKey, "sk-test-secret-1234")
	list, recommended, raw := c.listAccounts(t)
	if strings.Contains(raw, "sk-test-secret-1234") {
		t.Fatalf("the list holds the key: %s", raw)
	}
	if len(list) != 2 || list[0].ID != sub || list[1].ID != key || list[0].State != accounts.SignedIn ||
		list[1].State != accounts.SignedIn || recommended[accounts.Fake] == "" {
		t.Fatalf("list %+v recommended %v", list, recommended)
	}
	stored, err := c.store.GetAccount(key)
	if err != nil || len(stored.Secret) == 0 || strings.Contains(string(stored.Secret), "sk-test-secret-1234") {
		t.Fatalf("the stored key is not sealed: %q %v", stored.Secret, err)
	}
	if fi, err := os.Stat(paths.New(c.data).SecretsKey()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the data directory's key: %v %v", fi, err)
	}

	for _, bad := range []map[string]any{
		{"agent": "nope", "kind": "subscription", "label": "x"},
		{"agent": "fake", "kind": "other", "label": "x"},
		{"agent": "fake", "kind": "subscription", "label": " "},
		{"agent": "fake", "kind": "api_key", "label": "x"},
		{"agent": "opencode", "kind": "api_key", "label": "x", "key": "sk-or"},
	} {
		if code, raw := c.rawCall(t, "POST", "/api/accounts", bad); code != http.StatusBadRequest {
			t.Fatalf("%v: %d %s", bad, code, raw)
		}
	}
	if code, raw := c.rawCall(t, "PATCH", "/api/accounts/"+sub, map[string]any{"label": "Private"}); code != http.StatusOK ||
		!strings.Contains(raw, `"label":"Private"`) {
		t.Fatalf("rename: %d %s", code, raw)
	}
	if code, raw := c.rawCall(t, "PATCH", "/api/accounts/"+key, map[string]any{"key": "sk-new-secret-5678"}); code != http.StatusOK ||
		strings.Contains(raw, "sk-new") {
		t.Fatalf("a new key: %d %s", code, raw)
	}
	if code, _ := c.rawCall(t, "PATCH", "/api/accounts/"+sub, map[string]any{"key": "sk-x"}); code != http.StatusBadRequest {
		t.Fatalf("a key for a subscription: %d", code)
	}
	if code, _ := c.rawCall(t, "PUT", "/api/accounts/order", map[string]any{"order": []string{key}}); code != http.StatusConflict {
		t.Fatalf("an order without every account: %d", code)
	}
	if code, _ := c.rawCall(t, "PUT", "/api/accounts/order", map[string]any{"order": []string{key, sub}}); code != http.StatusOK {
		t.Fatalf("reorder: %d", code)
	}
	if list, _, _ := c.listAccounts(t); list[0].ID != key {
		t.Fatalf("the order did not hold: %+v", list)
	}

	// An account a chat runs on stays; once the chat is gone, it goes with
	// its folder.
	id := c.chatOn(t, sub, false)
	if code, raw := c.rawCall(t, "DELETE", "/api/accounts/"+sub, nil); code != http.StatusConflict {
		t.Fatalf("deleting an account in use: %d %s", code, raw)
	}
	if code, raw := c.rawCall(t, "DELETE", "/api/sessions/"+id, nil); code != http.StatusOK {
		t.Fatalf("delete the chat: %d %s", code, raw)
	}
	if code, raw := c.rawCall(t, "DELETE", "/api/accounts/"+sub, nil); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if _, err := os.Stat(accounts.Folder(c.data, accounts.Fake, sub)); !os.IsNotExist(err) {
		t.Fatalf("the folder is still there: %v", err)
	}
	// The agent's own sign-in, moved in by the migration, stays: the
	// agent's old folder is a link to it.
	moved := &store.Account{ID: accounts.NewID(), Agent: accounts.Fake, Label: accounts.DefaultLabel,
		Kind: accounts.Subscription, State: accounts.SignedIn, Origin: accounts.OriginClaude}
	if err := c.store.CreateAccount(moved); err != nil {
		t.Fatal(err)
	}
	if code, raw := c.rawCall(t, "DELETE", "/api/accounts/"+moved.ID, nil); code != http.StatusConflict {
		t.Fatalf("deleting the agent's own sign-in: %d %s", code, raw)
	}
	// The accounts of the old profiles go like any other: a lost one holds
	// nothing, and the link left in a moved one's place goes with it.
	lost := &store.Account{ID: accounts.NewID(), Agent: accounts.Claude, Label: "Team Sascha",
		Kind: accounts.Subscription, State: accounts.NeedsSignIn, Origin: "lost:p-aaaaaaaaaaaa:claude"}
	work := &store.Account{ID: accounts.NewID(), Agent: accounts.Claude, Label: "Work",
		Kind: accounts.Subscription, State: accounts.SignedIn, Origin: "profile:p-bbbbbbbbbbbb:claude"}
	for _, a := range []*store.Account{lost, work} {
		if err := c.store.CreateAccount(a); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(c.data, "profiles", "p-bbbbbbbbbbbb", "claude")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(accounts.Folder(c.data, accounts.Claude, work.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "accounts", work.ID), link); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*store.Account{lost, work} {
		if code, raw := c.rawCall(t, "DELETE", "/api/accounts/"+a.ID, nil); code != http.StatusOK {
			t.Fatalf("deleting %s: %d %s", a.Label, code, raw)
		}
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("the profile's link is still there: %v", err)
	}
}

// A chat runs on its account, switches to another on the same conversation
// from its route, and the row follows; the key reaches the CLI and none of
// the files or answers the server keeps.
func TestAChatSwitchesAccountAndTheKeyStaysSealed(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	t.Setenv("INVOCATION_ID", "the-service")
	a := c.newAccount(t, "A", accounts.APIKey, "sk-aaaa-secret")
	b := c.newAccount(t, "B", accounts.APIKey, "sk-bbbbbbbb-secret")
	id := c.chatOn(t, a, false)
	if got := c.reply(t, id, "m1", "/account"); got != "account "+a+", a key of 14 characters" {
		t.Fatalf("on A: %q", got)
	}
	conv := agent.Fold(c.journal(t, id)).ConversationID

	code, body := c.post(t, "/api/chats/"+id+"/account", map[string]any{"client_id": "sw1", "account": b})
	if code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("switch: %d %v", code, body)
	}
	c.waitJournal(t, id, 15*time.Second, "the divider", func(events []agent.Event) bool {
		return count(events, isEv(agent.TypeAccountSwitched)) == 1
	})
	if got := c.reply(t, id, "m2", "/account"); got != "account "+b+", a key of 18 characters" {
		t.Fatalf("on B: %q", got)
	}
	events := c.journal(t, id)
	p := agent.Fold(events)
	if p.ConversationID != conv || p.Account != b {
		t.Fatalf("conversation %s account %s", p.ConversationID, p.Account)
	}
	waitForCond(t, 5*time.Second, "the row to follow the switch", func() bool {
		row, _ := c.store.GetSession(id)
		return readChatOptions(row.Options).Account == b
	})
	// The same switch again is the one already made.
	if code, body := c.post(t, "/api/chats/"+id+"/account", map[string]any{"client_id": "sw1", "account": b}); code != http.StatusOK || body["dup"] != true {
		t.Fatalf("a repeated switch: %d %v", code, body)
	}
	// An account of another agent, or one not signed in, is refused.
	other := map[string]any{"agent": accounts.Codex, "kind": accounts.Subscription, "label": "Codex"}
	_, raw := c.rawCall(t, "POST", "/api/accounts", other)
	var made struct {
		Account accountView `json:"account"`
	}
	_ = json.Unmarshal([]byte(raw), &made)
	if code, _ := c.post(t, "/api/chats/"+id+"/account", map[string]any{"client_id": "sw2", "account": made.Account.ID}); code != http.StatusConflict {
		t.Fatalf("an account of another agent: %d", code)
	}

	// Parked and started again, the chat comes up on the account it was
	// switched to, without a second divider.
	c.srv.chats.park(id)
	waitForCond(t, 10*time.Second, "the host to park", func() bool {
		alive, _ := c.srv.chats.launcher.Alive(id)
		return !alive
	})
	if got := c.reply(t, id, "m3", "/account"); got != "account "+b+", a key of 18 characters" {
		t.Fatalf("after the park: %q", got)
	}
	if n := count(c.journal(t, id), isEv(agent.TypeAccountSwitched)); n != 1 {
		t.Fatalf("%d dividers after the park", n)
	}
	spec, err := agenthost.ReadSpec(c.data, id)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Account != b || spec.SecretEnv == "" {
		t.Fatalf("spec account %s sealed %v", spec.Account, spec.SecretEnv != "")
	}
	for _, kv := range spec.Env {
		if strings.HasPrefix(kv, "INVOCATION_ID=") || strings.HasPrefix(kv, agent.FakeKeyEnv) {
			t.Fatalf("the spec's environment has %s", kv)
		}
	}
	for _, name := range []string{paths.ChatSpec, agent.JournalFile, paths.ChatLog} {
		raw, err := os.ReadFile(filepath.Join(agenthost.ChatDir(c.data, id), name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "sk-aaaa-secret") || strings.Contains(string(raw), "sk-bbbbbbbb-secret") {
			t.Fatalf("%s holds a key", name)
		}
	}
	for _, path := range []string{"/api/chats/" + id + "?history=1", "/api/sessions", "/api/accounts"} {
		if _, raw := c.rawCall(t, "GET", path, nil); strings.Contains(raw, "-secret") {
			t.Fatalf("%s answers a key: %s", path, raw)
		}
	}
	outbox, _ := c.store.Outbox(id)
	for _, e := range outbox {
		if strings.Contains(string(e.Command), "-secret") {
			t.Fatalf("the outbox holds a key: %s", e.Command)
		}
	}
}

// A limit stops a chat without the automatic switch: the account is marked
// limited, and nothing moves. With it, the chat goes on with the account that
// has headroom, told to continue; with no such account it stops as well.
func TestALimitStopsTheChatOrSwitchesIt(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	sent := catch(c.srv.push)
	if err := c.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	limitPushes := func(within time.Duration) []pushNote {
		var out []pushNote
		deadline := time.After(within)
		for {
			select {
			case one := <-sent:
				if strings.HasPrefix(one.note.Body, "Limit reached") {
					out = append(out, one.note)
				}
			case <-deadline:
				return out
			}
		}
	}
	a := c.newAccount(t, "A", accounts.Subscription, "")
	id := c.chatOn(t, a, true)
	c.reply(t, id, "m0", "/limit")
	if got := limitPushes(time.Second); len(got) != 1 || got[0].ID != id || !strings.HasSuffix(got[0].Body, "(A)") {
		t.Fatalf("the push of a limit with nowhere to go: %+v", got)
	}
	if n := count(c.journal(t, id), isEv(agent.TypeAccountSwitched)); n != 0 {
		t.Fatalf("a switch with nowhere to go: %d", n)
	}
	b := c.newAccount(t, "B", accounts.Subscription, "")
	res, payload := c.do(t, c.client, "PUT", "/api/chats/"+id+"/auto-switch", `{"on":false}`)
	if res.StatusCode != http.StatusOK || payload["session"] == nil {
		t.Fatalf("auto-switch off: %d %v", res.StatusCode, payload)
	}
	c.reply(t, id, "m1", "/limit")
	acct, _ := c.store.GetAccount(a)
	if acct.LimitedUntil <= time.Now().UnixMilli() {
		t.Fatalf("A is not marked limited: %d", acct.LimitedUntil)
	}
	// The same limit again is not pushed twice.
	if got := limitPushes(time.Second); len(got) != 0 {
		t.Fatalf("the same limit was pushed again: %+v", got)
	}
	if n := count(c.journal(t, id), isEv(agent.TypeAccountSwitched)); n != 0 {
		t.Fatalf("a switch with the automatic switch off: %d", n)
	}
	if list, recommended, _ := c.listAccounts(t); recommended[accounts.Fake] != b || len(list) != 2 {
		t.Fatalf("the recommendation while A is at its limit: %v", recommended)
	}

	res, _ = c.do(t, c.client, "PUT", "/api/chats/"+id+"/auto-switch", `{"on":true}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("auto-switch on: %d", res.StatusCode)
	}
	c.send(t, id, "m2", "/limit")
	events := c.waitJournal(t, id, 20*time.Second, "the switch and the continue", func(events []agent.Event) bool {
		return count(events, isEv(agent.TypeAccountSwitched)) == 1 &&
			strings.HasPrefix(lastText(events), "you said: "+agenthost.ContinueText)
	})
	var divider agent.Event
	for _, ev := range events {
		if ev.Type == agent.TypeAccountSwitched {
			divider = ev
		}
	}
	if divider.From != a || divider.To != b || divider.Reason != "The limit of A was reached." {
		t.Fatalf("divider %+v", divider)
	}
	if p := agent.Fold(events); p.LastTurn == nil || p.LastTurn.Account != b {
		t.Fatalf("the continue ran on %+v", p.LastTurn)
	}
}

// A switch after the turn waits in the outbox, not only in the host: a host
// killed before the turn ends is followed by one on the new account, with
// the divider, and the switch is not lost.
func TestASwitchAfterTheTurnSurvivesTheHost(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	a := c.newAccount(t, "A", accounts.Subscription, "")
	b := c.newAccount(t, "B", accounts.Subscription, "")
	id := c.chatOn(t, a, false)
	c.send(t, id, "m1", "/stream 500 10")
	events := c.waitJournal(t, id, 10*time.Second, "streaming", func(evs []agent.Event) bool {
		return count(evs, isEv(agent.TypePartDelta)) > 0
	})
	res, payload := c.do(t, c.client, "POST", "/api/chats/"+id+"/account",
		`{"client_id":"s1","account":"`+b+`","when":"after_turn"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the switch: %d %v", res.StatusCode, payload)
	}
	if outbox, _ := c.store.Outbox(id); len(outbox) != 1 {
		t.Fatalf("the waiting switch is not in the outbox: %d", len(outbox))
	}
	if err := syscall.Kill(agent.Fold(events).HostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	events = c.waitJournal(t, id, 20*time.Second, "the divider", func(evs []agent.Event) bool {
		return count(evs, isEv(agent.TypeAccountSwitched)) == 1
	})
	if p := agent.Fold(events); p.Account != b {
		t.Fatalf("the chat is on %s", p.Account)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		outbox, _ := c.store.Outbox(id)
		if len(outbox) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the switch stays in the outbox: %s", outbox[0].Command)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := c.reply(t, id, "m2", "/account"); !strings.HasPrefix(got, "account "+b) {
		t.Fatalf("after the restart: %q", got)
	}
}

// A sign-in the provider refuses while a chat uses it - two chats of one
// OAuth account renewing it at the same moment (research/06) - puts the
// account out of use, saying to sign in again, though its login is still
// on disk; a new sign-in brings it back.
func TestARefusedSignInIsMarkedUntilItSignsInAgain(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	a := c.newAccount(t, "A", accounts.Subscription, "")
	b := c.newAccount(t, "B", accounts.Subscription, "")
	id := c.chatOn(t, b, false)
	c.reply(t, id, "m0", "/account")
	res, payload := c.do(t, c.client, "POST", "/api/chats/"+id+"/account", `{"client_id":"s1","account":"`+a+`"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the switch: %d %v", res.StatusCode, payload)
	}
	c.reply(t, id, "m1", "/refused")
	var got accountView
	for deadline := time.Now().Add(10 * time.Second); ; {
		list, _, _ := c.listAccounts(t)
		for _, v := range list {
			if v.ID == a {
				got = v
			}
		}
		if got.State == accounts.Failed || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.State != accounts.Failed || got.StateDetail != accounts.RefusedDetail {
		t.Fatalf("the refused account: %+v", got)
	}
	res, _ = c.do(t, c.client, "POST", "/api/chats/"+id+"/account", `{"client_id":"s2","account":"`+b+`"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("away from it: %d", res.StatusCode)
	}
	if res, _ := c.do(t, c.client, "POST", "/api/chats/"+id+"/account", `{"client_id":"s3","account":"`+a+`"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a switch to the refused account: %d", res.StatusCode)
	}
	if code, raw := c.rawCall(t, "POST", "/api/accounts/"+a+"/signin", map[string]any{}); code != http.StatusOK {
		t.Fatalf("sign in again: %d %s", code, raw)
	}
	if acct, _ := c.store.GetAccount(a); acct.State != accounts.SignedIn || acct.StateDetail != "" {
		t.Fatalf("after signing in again: %+v", acct)
	}
}

// A Codex subscription signs in by device code from the dashboard: the page
// and the code are shown, and the account is signed in once codex says so.
func TestACodexAccountSignsInByDeviceCode(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	bin := filepath.Join(t.TempDir(), "codex")
	build := exec.Command("go", "build", "-o", bin, "./e2e/fakebin/fakecodex")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakecodex: %v\n%s", err, out)
	}
	t.Setenv("SOCRATES_CODEX_BIN", bin)
	t.Setenv("FAKECODEX_LOGIN_MS", "300")
	code, raw := c.rawCall(t, "POST", "/api/accounts", map[string]any{"agent": accounts.Codex, "kind": accounts.Subscription, "label": "ChatGPT"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, raw)
	}
	var made struct {
		Account accountView `json:"account"`
	}
	_ = json.Unmarshal([]byte(raw), &made)
	if made.Account.State != accounts.NeedsSignIn {
		t.Fatalf("a new subscription: %+v", made.Account)
	}
	code, raw = c.rawCall(t, "POST", "/api/accounts/"+made.Account.ID+"/signin", map[string]any{})
	var flow struct {
		SignIn accounts.Flow `json:"signin"`
	}
	_ = json.Unmarshal([]byte(raw), &flow)
	if code != http.StatusOK || flow.SignIn.State != accounts.FlowWaiting || !strings.HasPrefix(flow.SignIn.Code, "FAKE-") {
		t.Fatalf("sign in: %d %s", code, raw)
	}
	waitForCond(t, 10*time.Second, "the sign-in to finish", func() bool {
		_, raw := c.rawCall(t, "GET", "/api/accounts/"+made.Account.ID+"/signin", nil)
		return strings.Contains(raw, `"state":"done"`)
	})
	list, _, _ := c.listAccounts(t)
	if len(list) != 1 || list[0].State != accounts.SignedIn || list[0].Email != "device@example.com" ||
		len(list[0].Limits) != 2 {
		t.Fatalf("after the sign-in: %+v", list)
	}
}
