package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/testmode"
)

// Test mode (docs/design/TEST-MODE.md): the runner (internal/testmode) runs
// the test catalogs of repositories, one chat per cell; this is what it gets
// of the server (testHost), what a test session is launched with besides a
// chat's own (testSpec), and where its activity and its tool calls go
// instead of the paths of a chat a person reads.

// testHost is the server as the runner sees it.
type testHost struct{ s *Server }

// StartSession creates a test session: a chat whose options carry its
// reference (store.TestOption), which keeps it off every list a person
// reads, on the role's harness, model and account, in the folder the runner
// says, with its first message.
func (h testHost) StartSession(ctx context.Context, spec testmode.SessionSpec) (string, error) {
	s := h.s
	settings := s.Settings()
	harness := strings.TrimSpace(spec.Role.Harness)
	if refused := checkChatHarness(harness, settings); refused != nil {
		return "", errors.New(refused.msg)
	}
	ref, err := json.Marshal(spec.Ref)
	if err != nil {
		return "", err
	}
	id := termux.NewID()
	model, effort := chatModel(settings, harness, spec.Role.Model, spec.Role.Effort)
	// A session keeps the account the runner chose: a switch at a limit
	// could take an account the run may not use, and a cell that ran into a
	// limit is retried on another.
	options := withAutoContinue(withChatAccount(withOption(nil, store.TestOption, json.RawMessage(ref)), spec.Role.Account, false), false)
	row := &store.Session{
		ID: id, Title: spec.Title, TitleSource: store.TitleUser, Harness: harness, Model: model, Effort: effort,
		Workdir: spec.Workdir, WorkdirMode: harnesses.WorkdirCustom, Kind: store.KindChat, State: store.StateRunning,
		Options: options,
	}
	if err := s.store.CreateSession(row); err != nil {
		return "", err
	}
	if spec.Created != nil {
		spec.Created(row.ID)
	}
	p, err := s.chats.peer(row.ID)
	if err != nil {
		// The runner removes a session whose start failed, by the id.
		return row.ID, err
	}
	if spec.Message != "" {
		if code, answer := s.sendMessage(ctx, p, &chatMessageBody{ClientID: uuid.NewString(), Text: spec.Message}); code >= 400 {
			return row.ID, fmt.Errorf("the first message was refused: %v", answer["error"])
		}
	}
	return row.ID, nil
}

// SessionStatus is whether a test session's row is there, and whether a
// turn of it runs as its journal says.
func (h testHost) SessionStatus(chat string) (bool, bool) {
	if _, err := h.s.store.GetSession(chat); err != nil {
		return false, false
	}
	p, err := h.s.chats.peer(chat)
	if err != nil {
		return false, false
	}
	return true, p.summary().TurnRunning
}

// SendMessage sends a test session a message of the runner's.
func (h testHost) SendMessage(ctx context.Context, chat, text string) error {
	p, err := h.s.chats.peer(chat)
	if err != nil {
		return err
	}
	if code, answer := h.s.sendMessage(ctx, p, &chatMessageBody{ClientID: uuid.NewString(), Text: text,
		Notice: "Socrates' test runner wrote this.", Reason: "test"}); code >= 400 {
		return fmt.Errorf("refused: %v", answer["error"])
	}
	return nil
}

// StopSession interrupts a test session's turn and lets its host park.
func (h testHost) StopSession(ctx context.Context, chat string) {
	p, err := h.s.chats.peer(chat)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p.command(ctx, agenthost.Command{Op: agenthost.OpInterrupt, ClientID: uuid.NewString()})
	h.s.chats.park(chat)
}

// RemoveSession deletes a test session with its folder.
func (h testHost) RemoveSession(ctx context.Context, chat string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	h.s.chats.remove(ctx, chat)
	if err := h.s.store.DeleteSession(chat); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("tests: could not delete the session %s: %v", chat, err)
	}
}

// PickAccount is the account a test session runs on (accountHub.forTests).
func (h testHost) PickAccount(harness string, allowed []string, load map[string]int) (string, string) {
	return h.s.accounts.forTests(harness, allowed, load)
}

func (h testHost) Publish(frame map[string]any) { h.s.events.publish(frame) }

func (h testHost) Notify(title, body, url string) {
	h.s.push.notify(strings.TrimPrefix(url, "/tests#"), title, body, pushTestRun)
}

func (h testHost) GetKV(key string) (string, error) {
	v, err := h.s.store.GetKV(key)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return v, err
}

func (h testHost) SetKV(key, value string) error { return h.s.store.SetKV(key, value) }

// forTests is the account a test session of harness runs on: of the
// accounts the run may use (allowed; every account of the harness when it
// is empty) and that are signed in and not at a limit, the one the fewest of
// the server's test sessions run on, in the order accounts are recommended
// in. "" with why when none can be taken now.
func (h *accountHub) forTests(harness string, allowed []string, load map[string]int) (string, string) {
	list, err := h.srv.store.ListAccounts()
	if err != nil {
		return "", "waiting: the accounts could not be read"
	}
	now := time.Now()
	var views []accountView
	for i := range list {
		if list[i].Agent != harness || len(allowed) > 0 && !slices.Contains(allowed, list[i].ID) {
			continue
		}
		a := h.refreshed(list[i])
		v := viewOf(a, 0)
		v.Limits = current(storedLimits(&a), now)
		views = append(views, v)
	}
	if len(views) == 0 {
		if len(allowed) > 0 {
			return "", "waiting: none of the accounts chosen for " + harness + " is there"
		}
		a, err := h.pick(harness)
		if err != nil {
			return "", "waiting: " + harness + " has no account: " + err.Error()
		}
		return a.ID, ""
	}
	// Accounts with room first; then, as a chat does (pick), the ones
	// Socrates does not know signed in, which may well be (a CLI signed in
	// on its own); never one at its limit.
	ranked := rankAccounts(views, harness, now)
	for _, usable := range []func(int) bool{
		func(tier int) bool { return tier <= standUnknown },
		func(tier int) bool { return tier == standOut },
	} {
		best := ""
		for _, v := range ranked {
			if tier, _ := standing(v, now); !usable(tier) {
				continue
			}
			if best == "" || load[v.ID] < load[best] {
				best = v.ID
			}
		}
		if best != "" {
			return best, ""
		}
	}
	return "", "waiting: every account of " + harness + " that may be used is at its limit"
}

// pushTestRun is the kind of a push about a test run; its id is the page's
// hash (repo=…&run=…).
const pushTestRun = "testrun"

// testSpec puts what a test session is launched with besides a chat's own
// into its spec: its role's tools and instructions, its environment, the
// tools on its PATH and only the MCP servers its area allows.
func (p *chatPeer) testSpec(row *store.Session, ref json.RawMessage, spec *agenthost.Spec) error {
	s := p.hub.srv
	if s.tests == nil {
		return errors.New("the test runner is not running")
	}
	l, err := s.tests.LaunchFor(ref)
	if err != nil {
		return err
	}
	if !spec.ChatTools.Usable() {
		return errors.New("the test tools cannot reach Socrates")
	}
	spec.ChatTools.Kind = l.Kind
	spec.TestInstructions = l.Instructions
	env := spec.Env
	if len(env) == 0 {
		env = os.Environ()
	}
	env = append([]string{}, env...)
	if len(l.Path) > 0 {
		path := os.Getenv("PATH")
		for i := len(env) - 1; i >= 0; i-- {
			if strings.HasPrefix(env[i], "PATH=") {
				path = strings.TrimPrefix(env[i], "PATH=")
				env = append(env[:i], env[i+1:]...)
			}
		}
		env = append(env, "PATH="+strings.Join(append(append([]string{}, l.Path...), path), string(os.PathListSeparator)))
	}
	spec.Env = append(env, l.Env...)
	// Every MCP server of Admin and of the folder is off unless the area
	// names it.
	folder, _ := agent.ReadFolderMCP(row.Workdir)
	list, _ := agent.ChatMCP(sharedMCP(s.Settings()), folder, nil)
	choices := map[string]bool{}
	for _, c := range list {
		choices[c.Name] = slices.Contains(l.MCP, c.Name)
	}
	spec.MCPChoices = choices
	return nil
}

// testObserve tells the runner what a test session does now, instead of the
// detector a chat a person reads reports to.
func (p *chatPeer) testObserve(sum agent.Summary) {
	s := p.hub.srv
	if s.tests == nil {
		return
	}
	s.tests.Observe(p.id, testmode.Observation{
		TurnRunning: sum.TurnRunning,
		Idle:        !sum.TurnRunning && sum.Activity == agent.ActivityIdle,
		Failed:      sum.Process == agent.ProcessFailed,
		Detail:      p.hostErr,
	})
}

// isTest is whether the chat is a test session, read once from its row.
func (p *chatPeer) isTest() bool {
	p.testOnce.Do(func() {
		if row, err := p.hub.srv.store.GetSession(p.id); err == nil {
			p.test = row.IsTest()
		}
	})
	return p.test
}

// testFolders is where repositories with a catalog are looked for: Admin's
// presets and every chat's folder.
func (s *Server) testFolders() []string {
	var out []string
	for _, p := range s.Settings().Workspace.Presets {
		out = append(out, p.Path)
	}
	if rows, err := s.store.ListSessions(); err == nil {
		for _, row := range rows {
			if row.Workdir != "" {
				out = append(out, row.Workdir)
			}
		}
	}
	return out
}
