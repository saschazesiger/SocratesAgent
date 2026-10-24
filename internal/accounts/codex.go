package accounts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A short-lived `codex app-server` on an account's CODEX_HOME, for what an
// account needs of Codex outside a chat: signing in (a ChatGPT device code,
// or an API key, which codex writes into the account's auth.json itself),
// who the account is, and its limits. Codex exits as soon as its stdin
// closes; Close does that, and kills it if it does not go.

// codexServer is one such process.
type codexServer struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	next  atomic.Int64
	// writeMu keeps one line on stdin at a time.
	writeMu sync.Mutex

	mu      sync.Mutex
	waiting map[int64]chan codexReply
	notes   chan codexNote
	done    chan struct{}
	err     error
}

type codexReply struct {
	result json.RawMessage
	err    error
}

// codexNote is one notification codex sent.
type codexNote struct {
	Method string
	Params json.RawMessage
}

// startCodex starts the app-server at bin with env, and does the handshake.
func startCodex(ctx context.Context, bin string, env []string) (*codexServer, error) {
	cmd := exec.Command(bin, "app-server", "--listen", "stdio://", "-c", "check_for_update_on_startup=false")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}
	s := &codexServer{cmd: cmd, stdin: stdin, waiting: map[int64]chan codexReply{},
		notes: make(chan codexNote, 64), done: make(chan struct{})}
	go s.read(stdout)
	var init json.RawMessage
	if err := s.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "socrates", "title": "Socrates", "version": "accounts"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &init); err != nil {
		s.Close()
		return nil, fmt.Errorf("codex refused the handshake: %w", err)
	}
	if err := s.write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *codexServer) read(r io.Reader) {
	defer close(s.done)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.ID != nil && msg.Method == "":
			s.mu.Lock()
			ch := s.waiting[*msg.ID]
			delete(s.waiting, *msg.ID)
			s.mu.Unlock()
			if ch == nil {
				continue
			}
			if msg.Error != nil {
				ch <- codexReply{err: errors.New(msg.Error.Message)}
			} else {
				ch <- codexReply{result: msg.Result}
			}
		case msg.ID != nil:
			// A request of codex's own; nothing here asks for one, and an
			// answer keeps codex from waiting on it.
			_ = s.write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID,
				"error": map[string]any{"code": -32601, "message": "not supported"}})
		default:
			select {
			case s.notes <- codexNote{Method: msg.Method, Params: msg.Params}:
			default:
			}
		}
	}
	s.mu.Lock()
	s.err = errors.New("codex exited")
	for id, ch := range s.waiting {
		ch <- codexReply{err: s.err}
		delete(s.waiting, id)
	}
	s.mu.Unlock()
}

func (s *codexServer) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(append(raw, '\n'))
	return err
}

// call sends one request and decodes its result into out.
func (s *codexServer) call(ctx context.Context, method string, params, out any) error {
	id := s.next.Add(1)
	ch := make(chan codexReply, 1)
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return s.err
	}
	s.waiting[id] = ch
	s.mu.Unlock()
	if err := s.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out != nil && len(r.result) > 0 {
			return json.Unmarshal(r.result, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close ends codex: stdin closed, then killed if it has not gone after a
// moment.
func (s *codexServer) Close() {
	_ = s.stdin.Close()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	_ = s.cmd.Wait()
}

// CodexKeyLogin logs an API key in into the CODEX_HOME env names, with
// codex's own account/login/start: codex writes its auth.json, Socrates never
// does.
func CodexKeyLogin(ctx context.Context, bin string, env []string, key string) error {
	s, err := startCodex(ctx, bin, env)
	if err != nil {
		return err
	}
	defer s.Close()
	var res struct {
		Type string `json:"type"`
	}
	if err := s.call(ctx, "account/login/start", map[string]any{"type": "apiKey", "apiKey": key}, &res); err != nil {
		return fmt.Errorf("codex did not take the key: %w", err)
	}
	return nil
}

// CodexDevice is a ChatGPT sign-in by device code in progress: the page to
// open and the code to type there. Done is closed when it ended, and Err
// says how.
type CodexDevice struct {
	URL  string
	Code string
	Done chan struct{}

	s       *codexServer
	loginID string
	mu      sync.Mutex
	err     error
	cancel  context.CancelFunc
}

// Err is why the sign-in did not work, nil once it did; meaningful after Done.
func (d *CodexDevice) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// Cancel gives the sign-in up.
func (d *CodexDevice) Cancel() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.s.call(ctx, "account/login/cancel", map[string]any{"loginId": d.loginID}, nil)
	d.cancel()
}

// CodexDeviceLogin starts a ChatGPT sign-in by device code on the CODEX_HOME
// env names (REBUILD.md §4.2): codex asks OpenAI for a code, the person types
// it at the page, and codex writes the login into its home when it is done.
// The app-server stays up until then, for at most within.
func CodexDeviceLogin(ctx context.Context, bin string, env []string, within time.Duration) (*CodexDevice, error) {
	s, err := startCodex(ctx, bin, env)
	if err != nil {
		return nil, err
	}
	var res struct {
		LoginID         string `json:"loginId"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if err := s.call(ctx, "account/login/start", map[string]any{"type": "chatgptDeviceCode"}, &res); err != nil {
		s.Close()
		return nil, fmt.Errorf("codex did not start a sign-in: %w", err)
	}
	if res.UserCode == "" || res.VerificationURL == "" {
		s.Close()
		return nil, errors.New("codex gave no code to sign in with")
	}
	wctx, cancel := context.WithTimeout(context.Background(), within)
	d := &CodexDevice{URL: res.VerificationURL, Code: res.UserCode, Done: make(chan struct{}), s: s,
		loginID: res.LoginID, cancel: cancel}
	go func() {
		defer close(d.Done)
		defer s.Close()
		defer cancel()
		end := func(err error) {
			d.mu.Lock()
			d.err = err
			d.mu.Unlock()
		}
		for {
			select {
			case n := <-s.notes:
				if n.Method != "account/login/completed" {
					continue
				}
				var done struct {
					LoginID *string `json:"loginId"`
					Success bool    `json:"success"`
					Error   *string `json:"error"`
				}
				if json.Unmarshal(n.Params, &done) != nil || (done.LoginID != nil && *done.LoginID != d.loginID) {
					continue
				}
				if done.Success {
					end(nil)
				} else {
					msg := "the sign-in did not finish"
					if done.Error != nil && strings.TrimSpace(*done.Error) != "" {
						msg = *done.Error
					}
					end(errors.New(msg))
				}
				return
			case <-s.done:
				end(errors.New("codex stopped before the sign-in finished"))
				return
			case <-wctx.Done():
				end(errors.New("the sign-in was not finished in time"))
				return
			}
		}
	}()
	return d, nil
}

// CodexUsage is what codex says about an account: its address and plan, and
// its limits.
type CodexUsage struct {
	Email   string
	Plan    string
	Windows []Window
}

// Window is one usage window of an account: "5h", "7d", or another length
// as "<n>m"; how much of it is used, in percent; and when it renews (Unix
// seconds, zero when unknown).
type Window struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    int64   `json:"resets_at,omitempty"`
}

// WindowLabel is the label of a window named the way the agents name them in
// a chat's journal (Claude's five_hour and seven_day, Codex's weekly).
func WindowLabel(name string) string {
	switch name {
	case "five_hour", "5h", "300m":
		return "5h"
	case "seven_day", "weekly", "7d", "10080m":
		return "7d"
	}
	return name
}

// ReadCodexUsage asks codex, on the CODEX_HOME env names, who the account is
// (account/read) and how much of its limits is used (account/rateLimits/read).
func ReadCodexUsage(ctx context.Context, bin string, env []string) (CodexUsage, error) {
	var out CodexUsage
	s, err := startCodex(ctx, bin, env)
	if err != nil {
		return out, err
	}
	defer s.Close()
	var acct struct {
		Account *struct {
			Type     string  `json:"type"`
			Email    *string `json:"email"`
			PlanType string  `json:"planType"`
		} `json:"account"`
	}
	if err := s.call(ctx, "account/read", map[string]any{}, &acct); err == nil && acct.Account != nil {
		if acct.Account.Email != nil {
			out.Email = *acct.Account.Email
		}
		out.Plan = acct.Account.PlanType
	}
	type window struct {
		UsedPercent        float64 `json:"usedPercent"`
		WindowDurationMins *int64  `json:"windowDurationMins"`
		ResetsAt           *int64  `json:"resetsAt"`
	}
	var limits struct {
		RateLimits struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
		} `json:"rateLimits"`
	}
	if err := s.call(ctx, "account/rateLimits/read", nil, &limits); err != nil {
		return out, err
	}
	for _, w := range []*window{limits.RateLimits.Primary, limits.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		label := ""
		if w.WindowDurationMins != nil {
			label = WindowLabel(fmt.Sprintf("%dm", *w.WindowDurationMins))
		}
		if label == "" {
			continue
		}
		one := Window{Label: label, UsedPercent: w.UsedPercent}
		if w.ResetsAt != nil {
			one.ResetsAt = *w.ResetsAt
		}
		out.Windows = append(out.Windows, one)
	}
	return out, nil
}
