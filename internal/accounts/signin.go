package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Signing an account in from the web (REBUILD.md §4.2); an API key is a
// form instead.
//
//   - Claude Code: its own `claude auth login`, on a pseudo terminal of the
//     server's (claude.go). It prints the page to open; the person pastes
//     the code that page shows, which is typed into it.
//   - Codex: a ChatGPT device code. A short-lived app-server on the
//     account's home asks for one; the person opens the page and types the
//     code; codex writes the login when it is done.
//
// OpenCode signs in in a Shell instead, in its own TUI on the account's
// folder (OpenCodeTerminal): every provider it offers, in its own words.
//
// A flow lives in memory for at most FlowTimeout. It is never needed again
// once it ended: the login is in the account's folder, which is where the
// account's state is read from.

// FlowTimeout bounds a sign-in.
const FlowTimeout = 15 * time.Minute

// The states of a flow.
const (
	// FlowWaiting: the page is to be opened; the flow ends by itself.
	FlowWaiting = "waiting"
	// FlowCode: the page is to be opened and the code it shows pasted here.
	FlowCode = "code"
	FlowDone = "done"
	// FlowFailed: it ended without a login; Error says why.
	FlowFailed = "failed"
)

// Flow is one sign-in as the Accounts card shows it.
type Flow struct {
	Account string `json:"account"`
	Agent   string `json:"agent"`
	State   string `json:"state"`
	URL     string `json:"url,omitempty"`
	Code    string `json:"code,omitempty"`
	Error   string `json:"error,omitempty"`
	Started int64  `json:"started"`
}

// SignIns is every sign-in under way, one per account.
type SignIns struct {
	mu    sync.Mutex
	flows map[string]*flow
}

type flow struct {
	mu     sync.Mutex
	view   Flow
	ctx    context.Context
	cancel context.CancelFunc
	claude *claudeSignIn
	onDone func(error)
	ended  bool
}

// NewSignIns is an empty set.
func NewSignIns() *SignIns { return &SignIns{flows: map[string]*flow{}} }

func (s *SignIns) put(account string, f *flow) {
	s.mu.Lock()
	old := s.flows[account]
	s.flows[account] = f
	s.mu.Unlock()
	if old != nil {
		old.end(errors.New("another sign-in was started"))
	}
}

func (s *SignIns) get(account string) *flow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flows[account]
}

// Get is the sign-in of an account, if one was started and has not been
// forgotten.
func (s *SignIns) Get(account string) (Flow, bool) {
	f := s.get(account)
	if f == nil {
		return Flow{}, false
	}
	return f.snapshot(), true
}

// Cancel gives an account's sign-in up.
func (s *SignIns) Cancel(account string) {
	if f := s.get(account); f != nil {
		f.end(errors.New("the sign-in was cancelled"))
	}
}

func newFlow(account, agentName string, onDone func(error)) *flow {
	ctx, cancel := context.WithTimeout(context.Background(), FlowTimeout)
	f := &flow{view: Flow{Account: account, Agent: agentName, Started: time.Now().UnixMilli()},
		ctx: ctx, cancel: cancel, onDone: onDone}
	go func() {
		<-ctx.Done()
		f.end(errors.New("the sign-in was not finished in time"))
	}()
	return f
}

func (f *flow) snapshot() Flow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

// end closes the flow, done when err is nil. Only the first end counts.
func (f *flow) end(err error) {
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return
	}
	f.ended = true
	if err == nil {
		f.view.State = FlowDone
	} else {
		f.view.State, f.view.Error = FlowFailed, err.Error()
	}
	claude, onDone := f.claude, f.onDone
	f.mu.Unlock()
	f.cancel()
	if claude != nil {
		claude.Close()
	}
	if onDone != nil {
		onDone(err)
	}
}

// StartCodex starts a ChatGPT sign-in by device code for an account, with
// codex at bin and the account's environment. onDone is called once, when
// the flow ends, with nil for a login.
func (s *SignIns) StartCodex(ctx context.Context, account, bin string, env []string, onDone func(error)) (Flow, error) {
	f := newFlow(account, Codex, onDone)
	d, err := CodexDeviceLogin(ctx, bin, env, FlowTimeout)
	if err != nil {
		f.cancel()
		return Flow{}, err
	}
	f.mu.Lock()
	f.view.State, f.view.URL, f.view.Code = FlowWaiting, d.URL, d.Code
	f.mu.Unlock()
	s.put(account, f)
	go func() {
		select {
		case <-d.Done:
			f.end(d.Err())
		case <-f.ctx.Done():
			d.Cancel()
			<-d.Done
		}
	}()
	return f.snapshot(), nil
}

// StartClaude starts Claude Code's sign-in for an account: claude at bin, in
// the account's environment (its CLAUDE_CONFIG_DIR the account's folder),
// until it shows the page to open. onDone is called once, when the flow
// ends, with nil for a login.
func (s *SignIns) StartClaude(ctx context.Context, account, bin string, env []string, onDone func(error)) (Flow, error) {
	f := newFlow(account, Claude, onDone)
	c, err := startClaudeLogin(bin, env)
	if err != nil {
		f.cancel()
		return Flow{}, err
	}
	url, err := c.waitURL(ctx)
	if err != nil {
		c.Close()
		f.cancel()
		return Flow{}, err
	}
	f.mu.Lock()
	f.claude, f.view.State, f.view.URL = c, FlowCode, url
	f.mu.Unlock()
	s.put(account, f)
	go func() {
		select {
		case <-c.done:
			f.end(c.result())
		case <-f.ctx.Done():
			c.Close()
		}
	}()
	return f.snapshot(), nil
}

// Code finishes a sign-in whose page shows a code to paste: Claude Code's.
func (s *SignIns) Code(ctx context.Context, account, code string) (Flow, error) {
	f := s.get(account)
	if f == nil {
		return Flow{}, errors.New("there is no sign-in under way for this account")
	}
	f.mu.Lock()
	claude, state := f.claude, f.view.State
	f.mu.Unlock()
	if state != FlowCode || claude == nil {
		return Flow{}, errors.New("this sign-in is not waiting for a code")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return Flow{}, errors.New("paste the code the page shows")
	}
	return f.claudeCode(ctx, claude, code)
}

// claudeCode types code into Claude Code's sign-in and waits for what it
// makes of it. A code it does not take can be pasted again; a sign-in that
// takes longer than ctx ends by itself, and the card finds out by asking.
func (f *flow) claudeCode(ctx context.Context, c *claudeSignIn, code string) (Flow, error) {
	// A pasted line break would be a second Enter.
	code = strings.Join(strings.Fields(code), "")
	if err := c.typeCode(code); err != nil {
		return Flow{}, fmt.Errorf("the code could not be typed: %w", err)
	}
	// Claude Code answers in a second or two; the card asks again after.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done, err := c.waitCode(ctx, code)
	switch {
	case done:
		f.end(c.result())
	case errors.Is(err, errCodeRefused):
		return Flow{}, fmt.Errorf("Claude Code did not take the code%s", strings.TrimPrefix(err.Error(), errCodeRefused.Error()))
	}
	return f.snapshot(), nil
}
