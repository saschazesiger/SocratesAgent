package accounts

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
//   - OpenCode: a provider's OAuth. A serve of the account's own lists the
//     providers' methods, one is chosen, and the page it gives is opened;
//     either OpenCode finds out by itself when the person is done ("auto"),
//     or the person pastes the code the page shows ("code").
//
// A flow lives in memory for at most FlowTimeout. It is never needed again
// once it ended: the login is in the account's folder, which is where the
// account's state is read from.

// FlowTimeout bounds a sign-in.
const FlowTimeout = 15 * time.Minute

// The states of a flow.
const (
	// FlowChoose: OpenCode's providers are listed, and one is to be chosen.
	FlowChoose = "choose"
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
	Account      string     `json:"account"`
	Agent        string     `json:"agent"`
	State        string     `json:"state"`
	URL          string     `json:"url,omitempty"`
	Code         string     `json:"code,omitempty"`
	Instructions string     `json:"instructions,omitempty"`
	Providers    []Provider `json:"providers,omitempty"`
	Provider     string     `json:"provider,omitempty"`
	Error        string     `json:"error,omitempty"`
	Started      int64      `json:"started"`
}

// Provider is an OpenCode provider and the ways a subscription signs in to it.
type Provider struct {
	ID      string       `json:"id"`
	Methods []AuthMethod `json:"methods"`
}

// AuthMethod is one way to sign in: its place in the provider's list (what
// OpenCode calls it by), its label, and the questions it asks first.
type AuthMethod struct {
	Index   int          `json:"index"`
	Type    string       `json:"type"`
	Label   string       `json:"label"`
	Prompts []AuthPrompt `json:"prompts,omitempty"`
}

// AuthPrompt is one question of a method: a text, or a choice of options,
// asked only when another answer is what When says.
type AuthPrompt struct {
	Type        string       `json:"type"`
	Key         string       `json:"key"`
	Message     string       `json:"message"`
	Placeholder string       `json:"placeholder,omitempty"`
	Options     []AuthOption `json:"options,omitempty"`
	When        *AuthWhen    `json:"when,omitempty"`
}

// AuthOption is one choice of a prompt.
type AuthOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

// AuthWhen is the condition of a prompt.
type AuthWhen struct {
	Key   string `json:"key"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type openCodeSignIn interface {
	Methods(ctx context.Context) (map[string][]AuthMethod, error)
	Authorize(ctx context.Context, provider string, index int, inputs map[string]string) (url, method, instructions string, err error)
	Callback(ctx context.Context, provider string, index int, code string) error
	Close()
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
	oc     openCodeSignIn
	claude *claudeSignIn
	// methods are OpenCode's, by provider, as Choose finds them.
	methods map[string][]AuthMethod
	index   int
	onDone  func(error)
	ended   bool
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
	v := f.view
	v.Providers = append([]Provider(nil), f.view.Providers...)
	return v
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
	oc, claude, onDone := f.oc, f.claude, f.onDone
	f.mu.Unlock()
	f.cancel()
	if oc != nil {
		oc.Close()
	}
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

// StartOpenCode starts an OpenCode sign-in for an account: a serve of its
// own, and the providers whose subscription can be signed in to there. The
// API-key methods are the Accounts card's form, not a sign-in.
func (s *SignIns) StartOpenCode(ctx context.Context, dataDir, account string, env []string, binary string, onDone func(error)) (Flow, error) {
	f := newFlow(account, OpenCode, onDone)
	oc, err := startOpenCodeSignIn(ctx, dataDir, account, env, binary)
	if err != nil {
		f.cancel()
		return Flow{}, err
	}
	methods, err := oc.Methods(ctx)
	if err != nil {
		oc.Close()
		f.cancel()
		return Flow{}, err
	}
	f.mu.Lock()
	f.oc, f.methods = oc, map[string][]AuthMethod{}
	for provider, all := range methods {
		var oauth []AuthMethod
		for i, m := range all {
			if m.Type != "oauth" {
				continue
			}
			m.Index = i
			oauth = append(oauth, m)
		}
		if len(oauth) > 0 {
			f.methods[provider] = oauth
			f.view.Providers = append(f.view.Providers, Provider{ID: provider, Methods: oauth})
		}
	}
	sort.Slice(f.view.Providers, func(i, j int) bool { return f.view.Providers[i].ID < f.view.Providers[j].ID })
	f.view.State = FlowChoose
	f.mu.Unlock()
	s.put(account, f)
	return f.snapshot(), nil
}

// Choose starts the OAuth method index of provider in an account's OpenCode
// sign-in, with the answers to its prompts.
func (s *SignIns) Choose(ctx context.Context, account, provider string, index int, inputs map[string]string) (Flow, error) {
	f := s.get(account)
	if f == nil {
		return Flow{}, errors.New("there is no sign-in under way for this account")
	}
	f.mu.Lock()
	oc, ended := f.oc, f.ended
	var method *AuthMethod
	for i := range f.methods[provider] {
		if f.methods[provider][i].Index == index {
			method = &f.methods[provider][i]
		}
	}
	f.mu.Unlock()
	switch {
	case ended || oc == nil:
		return Flow{}, errors.New("this sign-in has ended; start it again")
	case method == nil:
		return Flow{}, fmt.Errorf("%s has no such way to sign in", provider)
	}
	clean := map[string]string{}
	for k, v := range inputs {
		if v = strings.TrimSpace(v); v != "" {
			clean[k] = v
		}
	}
	url, kind, instructions, err := oc.Authorize(ctx, provider, index, clean)
	if err != nil {
		return Flow{}, err
	}
	f.mu.Lock()
	f.view.Provider, f.view.URL, f.view.Instructions, f.index = provider, url, instructions, index
	if kind == "code" {
		f.view.State = FlowCode
	} else {
		f.view.State = FlowWaiting
	}
	f.mu.Unlock()
	if kind != "code" {
		// OpenCode answers the callback of an "auto" method once the person
		// is done, or gives up; the flow's own time is its bound.
		go func() { f.end(oc.Callback(f.ctx, provider, index, "")) }()
	}
	return f.snapshot(), nil
}

// Code finishes a sign-in whose page shows a code to paste: Claude Code's,
// or an OpenCode one.
func (s *SignIns) Code(ctx context.Context, account, code string) (Flow, error) {
	f := s.get(account)
	if f == nil {
		return Flow{}, errors.New("there is no sign-in under way for this account")
	}
	f.mu.Lock()
	oc, claude, state, provider, index := f.oc, f.claude, f.view.State, f.view.Provider, f.index
	f.mu.Unlock()
	if state != FlowCode || (oc == nil && claude == nil) {
		return Flow{}, errors.New("this sign-in is not waiting for a code")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return Flow{}, errors.New("paste the code the page shows")
	}
	if claude != nil {
		return f.claudeCode(ctx, claude, code)
	}
	if err := oc.Callback(ctx, provider, index, code); err != nil {
		// A wrong code can be pasted again.
		return Flow{}, fmt.Errorf("the code was not taken: %w", err)
	}
	f.end(nil)
	return f.snapshot(), nil
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
