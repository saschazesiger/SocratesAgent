package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// keys are the W3C key codes of the keys Key can press, in the order its
// error lists them.
var keys = []struct{ name, value string }{
	{"enter", ""}, {"tab", ""}, {"escape", ""}, {"backspace", ""},
	{"up", ""}, {"down", ""}, {"left", ""}, {"right", ""}, {"space", " "},
}

func keyValue(name string) string {
	for _, k := range keys {
		if k.name == name {
			return k.value
		}
	}
	return ""
}

// TypeOptions say what Type types and where.
type TypeOptions struct {
	// Into is the field; nil types into the focused one.
	Into *Target
	// Native types through the device keyboard after a native tap (phones
	// and tablets).
	Native, Clear, Submit bool
	// Secret types a value of Config.Secrets by name (never recorded).
	Secret string
	// TOTP types the current authenticator code of a provider (google, ms).
	TOTP string
	// Address types the session's mailbox address; Code the code of the
	// last mail read.
	Address, Code bool
}

type findResult struct {
	Found      bool   `json:"found"`
	Nonce      string `json:"nonce"`
	Name       string `json:"name"`
	Index      *int   `json:"index"`
	Role       string `json:"role"`
	Tag        string `json:"tag"`
	Exact      bool   `json:"exact"`
	Count      int    `json:"count"`
	Disabled   bool   `json:"disabled"`
	Candidates []struct {
		Name  string `json:"name"`
		Role  string `json:"role"`
		Tag   string `json:"tag"`
		Exact bool   `json:"exact"`
	} `json:"candidates"`
	Covered *struct {
		By      string `json:"by"`
		Ad      bool   `json:"ad"`
		Consent bool   `json:"consent"`
	} `json:"covered"`
}

// findQuery is what browser/find.js receives.
type findQuery struct {
	Text  string `json:"text,omitempty"`
	Role  string `json:"role,omitempty"`
	CSS   string `json:"css,omitempty"`
	Nth   *int   `json:"nth,omitempty"`
	Exact bool   `json:"exact,omitempty"`
	Any   bool   `json:"any,omitempty"`
}

// Session is one BrowserStack device session, writing every action into the
// evidence. Its actions are meant to run one at a time.
type Session struct {
	wd        *webDriver
	cfg       Config
	startedAt time.Time
	lastCmd   atomic.Int64

	mu            sync.Mutex
	lastMail      *Mail
	lastNativeTap *point
	freshAddress  string
}

// actionError is an action that cannot be carried out; the action is still
// recorded with its screenshot.
func actionError(format string, args ...any) error { return fmt.Errorf(format, args...) }

// Start opens the WebDriver session for the device, sets the script timeout
// and maximises desktop windows.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Consent == "" {
		cfg.Consent = ConsentAccept
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = DeviceStartTimeout
	}
	if cfg.Evidence == nil {
		return nil, errors.New("the session has no evidence folder")
	}
	options := map[string]any{}
	for k, v := range cfg.Device.Options {
		options[k] = v
	}
	for k, v := range map[string]string{"buildName": cfg.Build, "projectName": cfg.Project, "sessionName": cfg.Name} {
		if v != "" {
			options[k] = v
		}
	}
	options["idleTimeout"] = IdleTimeoutS
	options["video"] = true
	options["networkLogs"] = true
	options["consoleLogs"] = "errors"
	capabilities := map[string]any{
		"browserName": cfg.Device.BrowserName,
		// Navigation returns at DOMContentLoaded; the settle wait follows the
		// page, ads never hold a command.
		"pageLoadStrategy": "eager",
		"bstack:options":   options,
	}
	wd, err := createWebDriver(ctx, cfg.Account, capabilities, cfg.StartTimeout)
	if err != nil {
		return nil, err
	}
	s := &Session{wd: wd, cfg: cfg, startedAt: time.Now()}
	s.lastCmd.Store(time.Now().UnixMilli())
	if err := wd.setScriptTimeout(ctx, SettleMax+10*time.Second); err != nil {
		// Do not leave a device behind that nobody holds.
		quitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = wd.quit(quitCtx)
		return nil, err
	}
	if cfg.Device.Kind == Desktop {
		_ = wd.maximize(ctx)
	}
	return s, nil
}

// QuitSession closes a session another process left behind.
func QuitSession(ctx context.Context, a Account, sessionID string) error {
	return attachWebDriver(a, sessionID).quit(ctx)
}

// ID is the WebDriver session id.
func (s *Session) ID() string { return s.wd.sessionID }

// StartedAt is when the device became ready.
func (s *Session) StartedAt() time.Time { return s.startedAt }

// LastCommandAt is when the last action began or ended.
func (s *Session) LastCommandAt() time.Time { return time.UnixMilli(s.lastCmd.Load()) }

func (s *Session) touch() { s.lastCmd.Store(time.Now().UnixMilli()) }

// url resolves a path the agent gave against the site: a full URL as it is,
// a path that names a locale (or starts with /_) on the base URL, any other
// path with the session's locale in front.
func (s *Session) url(path string) (string, error) {
	if fullURL.MatchString(path) {
		return path, nil
	}
	if s.cfg.BaseURL == "" {
		return "", actionError("no site is set for this session: open a full URL (https://...), not the path %s", path)
	}
	base := strings.TrimSuffix(s.cfg.BaseURL, "/")
	clean := path
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	first := strings.SplitN(strings.TrimPrefix(clean, "/"), "/", 2)[0]
	for _, l := range s.cfg.Locales {
		if l != "" && first == l {
			return base + clean, nil
		}
	}
	if strings.HasPrefix(clean, "/_") || s.cfg.Locale == "" {
		return base + clean, nil
	}
	if clean == "/" {
		clean = ""
	}
	return base + "/" + s.cfg.Locale + clean, nil
}

var fullURL = regexp.MustCompile(`^https?://`)

// afterAction settles the page, answers the consent dialog and collects page
// errors after any action.
func (s *Session) afterAction(ctx context.Context, entry *Action) {
	entry.Settle = s.settle(ctx)
	var consent struct {
		Open   bool   `json:"open"`
		Action string `json:"action"`
	}
	if err := s.wd.run(ctx, &consent, page["consent"], s.cfg.Consent); err != nil {
		consent.Open = false
	}
	if consent.Open {
		entry.Consent = consent.Action
		if consent.Action == "accepted" || consent.Action == "rejected" {
			entry.Settle = s.settle(ctx)
		}
	}
	var collected struct {
		Entries []PageEntry `json:"entries"`
	}
	if err := s.wd.run(ctx, &collected, page["collector"]); err == nil && len(collected.Entries) > 0 {
		entry.PageErrors = collected.Entries
	}
	u, _ := s.wd.currentURL(ctx)
	entry.URL = RedactURL(u)
}

func (s *Session) settle(ctx context.Context) *Settle {
	var result struct {
		MS      float64  `json:"ms"`
		Settled bool     `json:"settled"`
		Busy    []string `json:"busy"`
		Error   string   `json:"__error"`
	}
	if err := s.wd.runAsync(ctx, &result, page["settle"], SettleQuiet.Milliseconds(), SettleMax.Milliseconds()); err != nil {
		return &Settle{Busy: []string{"settle failed: " + err.Error()}}
	}
	if result.Error != "" {
		return &Settle{MS: int64(result.MS), Busy: []string{"settle failed: " + result.Error}}
	}
	if result.Busy == nil {
		result.Busy = []string{}
	}
	return &Settle{MS: int64(result.MS), Settled: result.Settled, Busy: result.Busy}
}

type actOptions struct{ noScreenshot, noSettle bool }

// act runs one action, then settle, consent, errors and a screenshot,
// whatever happened, and appends it to the evidence. summary says what was
// asked; the body's first output line, when it gives one, replaces it.
func (s *Session) act(ctx context.Context, action, summary string, body func(ctx context.Context, entry *Action) (string, error), o actOptions) *Action {
	s.touch()
	ev := s.cfg.Evidence
	entry := &Action{N: ev.Next(), At: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), Action: action, Summary: summary}
	begin := time.Now()
	if body != nil {
		output, err := body(ctx, entry)
		if err != nil {
			entry.Error = err.Error()
		} else if output != "" {
			entry.Output = output
			entry.Summary, _, _ = strings.Cut(output, "\n")
		}
	}
	entry.MS = time.Since(begin).Milliseconds()
	if !o.noSettle {
		s.afterAction(ctx, entry)
	}
	if !o.noScreenshot {
		png, err := s.wd.screenshot(ctx)
		if err == nil {
			err = ev.WriteScreenshot(entry, png)
		}
		if err != nil && entry.Error == "" {
			entry.Error = "screenshot: " + err.Error()
		}
	}
	if err := ev.Append(entry); err != nil && entry.Error == "" {
		entry.Error = "evidence: " + err.Error()
	}
	s.touch()
	return entry
}

// Open opens a page: a path on the site (with the session's locale in front
// unless it names one) or a full URL.
func (s *Session) Open(ctx context.Context, path string) *Action {
	return s.act(ctx, "open", "opened "+RedactURL(path), func(ctx context.Context, _ *Action) (string, error) {
		u, err := s.url(path)
		if err != nil {
			return "", err
		}
		return "", s.wd.navigate(ctx, u)
	}, actOptions{})
}

// OpenMailLink opens link n (from 1) of the last mail read, so a magic link's
// token never passes through the agent.
func (s *Session) OpenMailLink(ctx context.Context, n int) *Action {
	return s.act(ctx, "open", fmt.Sprintf("opened link %d of the last mail", n), func(ctx context.Context, _ *Action) (string, error) {
		if s.cfg.Mail == nil {
			return "", errNoMailbox
		}
		s.mu.Lock()
		mail := s.lastMail
		s.mu.Unlock()
		if mail == nil || n < 1 || n > len(mail.Links) {
			return "", actionError("the last mail has no link %d (read one with qa mail first)", n)
		}
		link := mail.Links[n-1]
		if err := s.wd.navigate(ctx, link); err != nil {
			return "", err
		}
		return "opened " + RedactURL(link), nil
	}, actOptions{})
}

var errNoMailbox = errors.New("this session has no mailbox")

var mailAddress = regexp.MustCompile(`(?i)^[a-z0-9._+-]+@[a-z0-9.-]+$`)

// Address is the session's own mailbox address (sign-up, invitations), made
// once from Config.Address; "" when there is none.
func (s *Session) Address() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.freshAddress == "" && s.cfg.Address != "" {
		stamp := strconv.FormatInt(time.Now().UnixMilli(), 36)
		s.freshAddress = strings.NewReplacer("{stamp}", stamp, "{member}", strconv.Itoa(s.cfg.Account.Member)).Replace(s.cfg.Address)
	}
	return s.freshAddress
}

// Mail waits up to wait for the newest mail received since the session
// started, to the session's address unless to names another one ("" and
// "address" are the session's).
func (s *Session) Mail(ctx context.Context, to string, wait time.Duration) *Action {
	if to == "" || to == "address" {
		to = s.Address()
	}
	return s.act(ctx, "mail", "read the mail to "+to, func(ctx context.Context, _ *Action) (string, error) {
		if s.cfg.Mail == nil {
			return "", errNoMailbox
		}
		if to == "" {
			return "", actionError("this session has no mailbox address: name the address the mail went to")
		}
		if !mailAddress.MatchString(to) {
			return "", actionError("not an address: %s", to)
		}
		since := int(math.Ceil(time.Since(s.startedAt).Seconds())) + 60
		until := time.Now().Add(wait)
		var mails []Mail
		for {
			var err error
			mails, err = s.cfg.Mail.Read(ctx, to, since, 3)
			if err != nil {
				return "", err
			}
			if len(mails) > 0 || !time.Now().Before(until) {
				break
			}
			if err := sleep(ctx, MailPoll); err != nil {
				return "", err
			}
			if _, err := s.wd.currentURL(ctx); err != nil {
				return "", err
			}
		}
		if len(mails) == 0 {
			return "", actionError("no mail to %s within %s s", to, formatNumber(wait.Seconds()))
		}
		mail := mails[0]
		s.mu.Lock()
		s.lastMail = &mail
		s.mu.Unlock()
		code := mail.Code
		if code == "" {
			code = "none"
		}
		out := fmt.Sprintf("mail %s from %s to %s\nsubject: %s\ncode: %s", mail.Received, mail.From, mail.To, mail.Title, code)
		if len(mail.Links) > 0 {
			out += "\nlinks (open with qa open --mail-link <n>):"
			for i, link := range mail.Links {
				out += fmt.Sprintf("\n  [%d] %s", i+1, RedactURL(link))
			}
		}
		return out, nil
	}, actOptions{noScreenshot: true, noSettle: true})
}

// locate finds a control by text, role or CSS selector (text and selector
// together narrow each other); a control the consent dialog covers is found
// again after the dialog is answered.
func (s *Session) locate(ctx context.Context, t Target) (*findResult, error) {
	query := findQuery{Text: t.Text, Role: t.Role, CSS: t.CSS, Nth: t.Nth, Exact: t.Exact, Any: t.Any}
	var found findResult
	if err := s.wd.run(ctx, &found, page["find"], query); err != nil {
		return nil, err
	}
	if found.Found && found.Covered != nil && found.Covered.Consent {
		policy := s.cfg.Consent
		if policy == ConsentLeave {
			policy = ConsentAccept
		}
		if err := s.wd.run(ctx, nil, page["consent"], policy); err != nil {
			return nil, err
		}
		query.Text, query.Exact = found.Name, true
		found = findResult{}
		if err := s.wd.run(ctx, &found, page["find"], query); err != nil {
			return nil, err
		}
	}
	return &found, nil
}

func describeMiss(found *findResult, t Target) error {
	near := make([]string, 0, len(found.Candidates))
	for _, c := range found.Candidates {
		near = append(near, c.Role+` "`+c.Name+`"`)
	}
	role := t.Role
	if role == "" {
		role = "control"
	}
	what := t.Text
	if what == "" {
		what = t.CSS
	}
	msg := fmt.Sprintf(`no %s "%s"`, role, what)
	if len(near) > 0 {
		msg += "; similar: " + strings.Join(near, ", ")
	}
	return errors.New(msg)
}

func candidateNames(found *findResult) string {
	names := make([]string, 0, len(found.Candidates))
	for _, c := range found.Candidates {
		names = append(names, `"`+c.Name+`"`)
	}
	return strings.Join(names, ", ")
}

func index(found *findResult) int {
	if found.Index == nil {
		return 1
	}
	return *found.Index + 1
}

func coveredBy(found *findResult) string {
	out := found.Covered.By
	if found.Covered.Ad {
		out += " (an ad)"
	}
	return out
}

// Find scrolls a control into view without touching it: shows that it is
// there, whether it is disabled and what covers it.
func (s *Session) Find(ctx context.Context, t Target) *Action {
	return s.act(ctx, "find", "find "+describeTarget(t), func(ctx context.Context, entry *Action) (string, error) {
		target := t
		entry.Target = &target
		found, err := s.locate(ctx, t)
		if err != nil {
			return "", err
		}
		if !found.Found {
			return "", describeMiss(found, t)
		}
		target.Matched = found.Name
		first := fmt.Sprintf(`found %s "%s"`, found.Role, found.Name)
		if found.Disabled {
			first += " (disabled)"
		}
		notes := []string{first}
		if found.Count > 1 {
			notes = append(notes, fmt.Sprintf("%d matches, showing #%d (--nth): %s", found.Count, index(found), candidateNames(found)))
		}
		if found.Covered != nil {
			notes = append(notes, "covered by "+coveredBy(found))
		}
		return strings.Join(notes, "\n"), nil
	}, actOptions{})
}

// Tap taps a control: with WebDriver, or with a real touch when the target
// says Native on a phone or tablet. A covered control is refused unless
// force.
func (s *Session) Tap(ctx context.Context, t Target, force bool) *Action {
	return s.act(ctx, "tap", "tap "+describeTarget(t), func(ctx context.Context, entry *Action) (string, error) {
		target := t
		entry.Target = &target
		found, err := s.locate(ctx, t)
		if err != nil {
			return "", err
		}
		if !found.Found || found.Nonce == "" {
			return "", describeMiss(found, t)
		}
		target.Matched = found.Name
		var notes []string
		if found.Count > 1 {
			notes = append(notes, fmt.Sprintf("%d matches, took #%d (--nth): %s", found.Count, index(found), candidateNames(found)))
		}
		if found.Disabled {
			notes = append(notes, "control is disabled")
		}
		if found.Covered != nil && !force {
			return "", actionError(`"%s" is covered by %s; use --force to tap anyway`, found.Name, coveredBy(found))
		}
		if t.Native && s.cfg.Device.Kind != Desktop {
			if err := s.nativeTap(ctx, found.Nonce, found.Name); err != nil {
				return "", err
			}
		} else {
			element, err := s.wd.find(ctx, `[data-qa-hit="`+found.Nonce+`"]`)
			if err != nil {
				return "", err
			}
			if err := s.wd.click(ctx, element); err != nil {
				return "", err
			}
		}
		return strings.Join(append([]string{fmt.Sprintf(`tapped %s "%s"`, found.Role, found.Name)}, notes...), "\n"), nil
	}, actOptions{})
}

// nativeTap is a real touch on the device screen at the control's centre
// (Appium native context). Where the control is drawn is measured, not
// computed: two markers placed on it with the page content are found in a
// native screenshot, their midpoint is its centre. Markers missing from the
// screen mean the control is not visible (under the keyboard, scrolled away).
// The page then tells which element received the touch.
func (s *Session) nativeTap(ctx context.Context, nonce, name string) error {
	type note struct {
		On   bool   `json:"on"`
		By   string `json:"by"`
		Name string `json:"name"`
	}
	hide := func() *note {
		var hit *note
		if err := s.wd.run(ctx, &hit, page["marker"], "hide"); err != nil {
			return nil
		}
		return hit
	}
	var shown struct {
		Placed bool `json:"placed"`
	}
	if err := s.wd.run(ctx, &shown, page["marker"], "show", MarkerGapCSS, nonce); err != nil {
		return err
	}
	if !shown.Placed {
		return actionError(`native: "%s" is gone from the page`, name)
	}
	web, err := s.wd.context(ctx)
	if err != nil {
		return err
	}
	if err := s.wd.setContext(ctx, "NATIVE_APP"); err != nil {
		return err
	}
	tap := func() error {
		// Only the native screenshot shows the whole screen (Android's web
		// one is the page viewport alone).
		png, err := s.wd.screenshot(ctx)
		if err != nil {
			return err
		}
		m, err := findMarkers(png)
		if err != nil {
			return err
		}
		if m == nil {
			return actionError(`native: "%s" is not visible on the screen (under the on-screen keyboard or scrolled away); scroll it into view or close the keyboard first (qa keyboard)`, name)
		}
		window, err := s.wd.windowRect(ctx)
		if err != nil {
			return err
		}
		width := math.NaN()
		if window.Width != nil {
			width = *window.Width
		}
		toNative := width / float64(m.ImageWidth)
		x := jsRound(float64(m.First.X+m.Second.X) / 2 * toNative)
		y := jsRound(float64(m.First.Y+m.Second.Y) / 2 * toNative)
		if math.IsNaN(x) || math.IsNaN(y) {
			return actionError("native: the device did not tell its window size")
		}
		err = s.wd.actions(ctx, []map[string]any{{
			"type": "pointer", "id": "finger", "parameters": map[string]any{"pointerType": "touch"},
			"actions": []map[string]any{
				{"type": "pointerMove", "duration": 0, "origin": "viewport", "x": int(x), "y": int(y)},
				{"type": "pointerDown", "button": 0},
				{"type": "pause", "duration": 80},
				{"type": "pointerUp", "button": 0},
			},
		}})
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.lastNativeTap = &point{int(x), int(y)}
		s.mu.Unlock()
		return nil
	}
	if err := tap(); err != nil {
		_ = s.wd.setContext(ctx, web)
		hide()
		return err
	}
	if err := s.wd.setContext(ctx, web); err != nil {
		return err
	}
	// What the page received: a touch that missed is an error of this
	// action, not a silent success.
	hit := hide()
	if hit != nil && !hit.On {
		by := hit.By
		if hit.Name != "" {
			by += ` "` + hit.Name + `"`
		}
		return actionError(`native: the touch landed on %s, not on "%s"`, by, name)
	}
	if hit == nil && s.cfg.BaseURL != "" {
		if u, _ := s.wd.currentURL(ctx); strings.HasPrefix(u, s.cfg.BaseURL) {
			return actionError(`native: no page element received the touch meant for "%s" (browser bar or keyboard in the way?)`, name)
		}
	}
	return nil
}

// jsRound is JavaScript's Math.round: halves round up.
func jsRound(v float64) float64 { return math.Floor(v + 0.5) }

func (s *Session) secret(name string) (string, error) {
	value := s.cfg.Secrets[name]
	if value == "" {
		return "", fmt.Errorf("secret %s is not in the vault", name)
	}
	return value, nil
}

// Type types a value, a secret, an authenticator code, the mailbox address
// or the last mail's code into a field (or the focused one).
func (s *Session) Type(ctx context.Context, value string, o TypeOptions) *Action {
	shown := value
	hidden := true
	switch {
	case o.Secret != "":
		shown = "<secret " + o.Secret + ">"
	case o.TOTP != "":
		shown = "<totp " + o.TOTP + ">"
	case o.Address:
		shown = "<fresh address>"
	case o.Code:
		shown = "<code from the last mail>"
	default:
		hidden = false
	}
	summary := "type "
	if hidden {
		summary += shown
	} else {
		summary += jsString(value)
	}
	if o.Into != nil {
		summary += " into " + describeTarget(*o.Into)
	}
	return s.act(ctx, "type", summary, func(ctx context.Context, entry *Action) (string, error) {
		text := value
		if o.Secret != "" {
			v, err := s.secret(o.Secret)
			if err != nil {
				return "", err
			}
			text = v
		}
		if o.TOTP != "" {
			name := s.cfg.TOTP[o.TOTP]
			if name == "" {
				name = "QA_" + strings.ToUpper(o.TOTP) + "_TOTP_SECRET"
			}
			key, err := s.secret(name)
			if err != nil {
				return "", err
			}
			text, _ = TOTP(key, time.Now())
		}
		if o.Address {
			if s.Address() == "" {
				return "", actionError("this session has no mailbox address")
			}
			text = s.Address()
		}
		if o.Code {
			if s.cfg.Mail == nil {
				return "", errNoMailbox
			}
			s.mu.Lock()
			mail := s.lastMail
			s.mu.Unlock()
			if mail == nil || mail.Code == "" {
				return "", actionError("the last mail has no code (read one with qa mail first)")
			}
			text = mail.Code
		}
		native := o.Native && s.cfg.Device.Kind != Desktop
		var element string
		if o.Into != nil {
			target := *o.Into
			target.Native = o.Native
			entry.Target = &target
			query := *o.Into
			if query.Role == "" {
				query.Role = "textbox"
			}
			found, err := s.locate(ctx, query)
			if err != nil {
				return "", err
			}
			if !found.Found || found.Nonce == "" {
				return "", describeMiss(found, *o.Into)
			}
			target.Matched = found.Name
			if element, err = s.wd.find(ctx, `[data-qa-hit="`+found.Nonce+`"]`); err != nil {
				return "", err
			}
			if native {
				if err := s.nativeTap(ctx, found.Nonce, found.Name); err != nil {
					return "", err
				}
				var focus struct {
					Focused bool   `json:"focused"`
					Active  string `json:"active"`
				}
				if err := s.wd.run(ctx, &focus, page["misc"], "focused", found.Nonce); err != nil {
					return "", err
				}
				if !focus.Focused {
					active := focus.Active
					if active == "" {
						active = "nothing"
					}
					return "", actionError(`the native tap on "%s" did not focus it (focus is on %s)`, found.Name, active)
				}
			} else if err := s.wd.click(ctx, element); err != nil {
				return "", err
			}
		} else {
			var err error
			if element, err = s.wd.activeElement(ctx); err != nil {
				return "", err
			}
		}
		if o.Clear {
			if err := s.wd.clear(ctx, element); err != nil {
				return "", err
			}
		}
		path := "WebDriver"
		if native {
			var err error
			if path, err = s.nativeType(ctx, text); err != nil {
				return "", err
			}
		} else if err := s.wd.sendKeys(ctx, element, text); err != nil {
			return "", err
		}
		if o.Submit {
			if err := s.wd.sendKeys(ctx, element, keyValue("enter")); err != nil {
				return "", err
			}
		}
		typed := shown
		if o.Address {
			typed = s.Address()
		}
		return "typed " + typed + " via " + path, nil
	}, actOptions{})
}

// nativeType types through the device's own input stack after the native tap
// on the field: on iOS the on-screen keyboard types it; on Android the
// focused field's accessibility node (EditText) receives it.
func (s *Session) nativeType(ctx context.Context, value string) (string, error) {
	web, err := s.wd.context(ctx)
	if err != nil {
		return "", err
	}
	if err := s.wd.setContext(ctx, "NATIVE_APP"); err != nil {
		return "", err
	}
	path, err := s.nativeTypeIn(ctx, value)
	if restore := s.wd.setContext(ctx, web); restore != nil && err == nil {
		return "", restore
	}
	return path, err
}

func (s *Session) nativeTypeIn(ctx context.Context, value string) (string, error) {
	if s.cfg.Device.Kind == IOS {
		keyboard := false
		for attempt := 0; attempt < keyboardWaitTries && !keyboard; attempt++ {
			if attempt > 0 {
				if err := sleep(ctx, 500*time.Millisecond); err != nil {
					return "", err
				}
			}
			found, err := s.wd.findXPath(ctx, "//XCUIElementTypeKeyboard")
			if err != nil {
				return "", err
			}
			keyboard = len(found) > 0
		}
		if !keyboard {
			return "", actionError("no on-screen keyboard opened")
		}
		if err := s.wd.deviceKeys(ctx, value); err != nil {
			return "", err
		}
		return "the iOS keyboard", nil
	}
	// Chrome publishes a focused web field to accessibility a moment after
	// the tap: wait for it, and fall back to the field nearest the tap (the
	// opening keyboard can move the page).
	s.mu.Lock()
	tap := s.lastNativeTap
	s.mu.Unlock()
	if tap == nil {
		return "", actionError("native typing on Android needs a native tap on the field first (--into ... --native)")
	}
	for attempt := 0; attempt < 5; attempt++ {
		focused, err := s.wd.findXPath(ctx, `//android.widget.EditText[@focused="true" and not(contains(@resource-id, "url_bar"))]`)
		if err != nil {
			return "", err
		}
		if len(focused) > 0 {
			if err := s.wd.sendKeys(ctx, focused[0], value); err != nil {
				return "", err
			}
			return "the focused Android text field", nil
		}
		if err := sleep(ctx, 700*time.Millisecond); err != nil {
			return "", err
		}
	}
	fields, err := s.wd.findXPath(ctx, `//android.widget.EditText[not(contains(@resource-id, "url_bar"))]`)
	if err != nil {
		return "", err
	}
	nearest, distance := "", math.Inf(1)
	tx, ty := float64(tap.X), float64(tap.Y)
	for _, field := range fields {
		r, err := s.wd.rect(ctx, field)
		if err != nil {
			return "", err
		}
		d := math.Inf(1)
		if x, y, w, h, ok := r.values(); ok {
			d = math.Hypot(max(x-tx, 0, tx-(x+w)), max(y-ty, 0, ty-(y+h)))
		}
		if nearest == "" || d < distance {
			nearest, distance = field, d
		}
	}
	if nearest == "" {
		return "", actionError("no text field on the screen")
	}
	if err := s.wd.sendKeys(ctx, nearest, value); err != nil {
		return "", err
	}
	away := "distance unknown"
	if !math.IsInf(distance, 0) {
		away = fmt.Sprintf("%d px away", int(jsRound(distance)))
	}
	return "the Android text field nearest the tap (" + away + ")", nil
}

// Keyboard closes the on-screen keyboard as a person would: the Done button
// above it on iOS, Back on Android.
func (s *Session) Keyboard(ctx context.Context) *Action {
	return s.act(ctx, "keyboard", "close the on-screen keyboard", func(ctx context.Context, _ *Action) (string, error) {
		if s.cfg.Device.Kind == Desktop {
			return "", actionError("a desktop has no on-screen keyboard")
		}
		web, err := s.wd.context(ctx)
		if err != nil {
			return "", err
		}
		if err := s.wd.setContext(ctx, "NATIVE_APP"); err != nil {
			return "", err
		}
		out, err := func() (string, error) {
			if s.cfg.Device.Kind == IOS {
				done, err := s.wd.findXPath(ctx, `//XCUIElementTypeToolbar//XCUIElementTypeButton[@name="Done"]`)
				if err != nil {
					return "", err
				}
				if len(done) > 0 {
					if err := s.wd.click(ctx, done[0]); err != nil {
						return "", err
					}
					return "tapped Done above the keyboard", nil
				}
			}
			if err := s.wd.hideKeyboard(ctx); err != nil {
				return "", err
			}
			return "closed the keyboard", nil
		}()
		if restore := s.wd.setContext(ctx, web); restore != nil && err == nil {
			return "", restore
		}
		return out, err
	}, actOptions{})
}

// Key presses a key: enter, tab, escape, backspace, up, down, left, right or
// space. An unknown key is an error before any action.
func (s *Session) Key(ctx context.Context, name string) (*Action, error) {
	value := keyValue(strings.ToLower(name))
	if value == "" {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.name
		}
		return nil, fmt.Errorf("unknown key %s (%s)", name, strings.Join(names, ", "))
	}
	return s.act(ctx, "key", "pressed "+name, func(ctx context.Context, _ *Action) (string, error) {
		return "", s.wd.actions(ctx, []map[string]any{{
			"type": "key", "id": "keyboard",
			"actions": []map[string]any{{"type": "keyDown", "value": value}, {"type": "keyUp", "value": value}},
		}})
	}, actOptions{}), nil
}

// Scroll scrolls down or up by most of a screen, or to the top or bottom.
func (s *Session) Scroll(ctx context.Context, where string) *Action {
	return s.act(ctx, "scroll", "scrolled "+where, func(ctx context.Context, _ *Action) (string, error) {
		var result struct {
			ScrollY json.Number `json:"scrollY"`
		}
		if err := s.wd.run(ctx, &result, page["misc"], "scroll", where); err != nil {
			return "", err
		}
		return fmt.Sprintf("scrolled %s to scrollY %s", where, result.ScrollY), nil
	}, actOptions{})
}

// Rotate rotates a phone or tablet.
func (s *Session) Rotate(ctx context.Context, landscape bool) *Action {
	orientation := "portrait"
	if landscape {
		orientation = "landscape"
	}
	return s.act(ctx, "rotate", "rotated to "+orientation, func(ctx context.Context, _ *Action) (string, error) {
		if s.cfg.Device.Kind == Desktop {
			return "", actionError("rotation needs a real device")
		}
		return "", s.wd.setOrientation(ctx, strings.ToUpper(orientation))
	}, actOptions{})
}

// Navigation goes back, forward or reloads the page (kind back, forward or
// reload).
func (s *Session) Navigation(ctx context.Context, kind string) *Action {
	summary := "went " + kind
	if kind == "reload" {
		summary = "reloaded the page"
	}
	return s.act(ctx, kind, summary, func(ctx context.Context, _ *Action) (string, error) {
		switch kind {
		case "back":
			return "", s.wd.back(ctx)
		case "forward":
			return "", s.wd.forward(ctx)
		case "reload":
			return "", s.wd.refresh(ctx)
		}
		return "", actionError("unknown navigation %s (back, forward, reload)", kind)
	}, actOptions{})
}

// Wait waits, keeping the session alive with a harmless command at least
// every 30 s.
func (s *Session) Wait(ctx context.Context, seconds float64) *Action {
	return s.act(ctx, "wait", "waited "+formatNumber(seconds)+" s", func(ctx context.Context, _ *Action) (string, error) {
		until := time.Now().Add(time.Duration(seconds * float64(time.Second)))
		for time.Now().Before(until) {
			if err := sleep(ctx, min(30*time.Second, time.Until(until))); err != nil {
				return "", err
			}
			if _, err := s.wd.currentURL(ctx); err != nil {
				return "", err
			}
		}
		return "", nil
	}, actOptions{})
}

// Look takes a screenshot and describes the controls on screen: role, name,
// state, what covers them.
func (s *Session) Look(ctx context.Context) *Action {
	return s.act(ctx, "look", "looked at the controls on screen", func(ctx context.Context, _ *Action) (string, error) {
		raw, err := s.wd.runRaw(ctx, page["look"])
		if err != nil {
			return "", err
		}
		return indentJSON(raw), nil
	}, actOptions{noSettle: true})
}

// Shot takes a screenshot only, without waiting for the page.
func (s *Session) Shot(ctx context.Context) *Action {
	return s.act(ctx, "shot", "took a screenshot", nil, actOptions{noSettle: true})
}

// Env returns the device facts: user agent, viewport, pixel ratio, system
// language, time zone, touch points.
func (s *Session) Env(ctx context.Context) (map[string]any, error) {
	var env map[string]any
	err := s.wd.run(ctx, &env, page["misc"], "env")
	return env, err
}

// KeepAlive sends the harmless command that keeps the session from timing
// out while the agent thinks.
func (s *Session) KeepAlive(ctx context.Context) {
	_, _ = s.wd.currentURL(ctx)
}

// Stop quits the BrowserStack session and returns the links of its video and
// logs.
func (s *Session) Stop(ctx context.Context) map[string]string {
	_ = s.wd.quit(ctx)
	return SessionLinks(ctx, s.wd.sessionID, s.cfg.Account)
}

// Describe is the text the agent reads for an action.
func (s *Session) Describe(a *Action) string {
	first := fmt.Sprintf("action %d %s %d ms", a.N, a.Action, a.MS)
	if a.Error != "" {
		first += " ERROR " + a.Error
	}
	lines := []string{first}
	if a.Output != "" {
		lines = append(lines, a.Output)
	}
	if a.Settle != nil && !a.Settle.Settled {
		line := fmt.Sprintf("not settled after %d ms", a.Settle.MS)
		if len(a.Settle.Busy) > 0 {
			line += ": " + strings.Join(a.Settle.Busy, ", ")
		}
		lines = append(lines, line)
	}
	if a.Consent != "" {
		lines = append(lines, "consent dialog: "+a.Consent)
	}
	if len(a.PageErrors) > 0 {
		entries := make([]string, len(a.PageErrors))
		for i, e := range a.PageErrors {
			entries[i] = "  " + e.Kind + ": " + e.Message
		}
		lines = append(lines, "page errors:\n"+strings.Join(entries, "\n"))
	}
	if a.URL != "" {
		lines = append(lines, "url: "+a.URL)
	}
	if a.Screenshot != "" {
		lines = append(lines, "screenshot: "+filepath.Join(s.cfg.Evidence.Dir, a.Screenshot))
	}
	if time.Since(s.startedAt) > SessionWarn {
		lines = append(lines, "WARNING: the session nears BrowserStack's 2 h limit; finish this cell soon")
	}
	return strings.Join(lines, "\n")
}

// SessionLinks are the session's links for links.json: video, network and
// text logs, with the public link's token cut. Empty values are left out; an
// unreachable API gives none.
func SessionLinks(ctx context.Context, sessionID string, a Account) map[string]string {
	links := map[string]string{}
	ctx, cancel := context.WithTimeout(ctx, linksTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SessionAPI+"/"+sessionID+".json", nil)
	if err != nil {
		return links
	}
	req.Header.Set("Authorization", basicAuth(a))
	resp, err := httpClient.Do(req)
	if err != nil {
		return links
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return links
	}
	var body struct {
		Session map[string]any `json:"automation_session"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return links
	}
	str := func(key string) string {
		v, _ := body.Session[key].(string)
		return v
	}
	for label, value := range map[string]string{
		"BrowserStack session": str("browser_url"),
		"Video":                str("video_url"),
		"Network log (HAR)":    str("har_logs_url"),
		"Console log":          str("browser_console_logs_url"),
		"Text log":             str("logs"),
		"Public link":          RedactURL(str("public_url")),
	} {
		if value != "" {
			links[label] = value
		}
	}
	return links
}

func describeTarget(t Target) string {
	var parts []string
	if t.Role != "" {
		parts = append(parts, t.Role)
	}
	if t.Text != "" {
		parts = append(parts, jsString(t.Text))
	}
	if t.CSS != "" {
		parts = append(parts, "css "+t.CSS)
	}
	if t.Nth != nil {
		parts = append(parts, fmt.Sprintf("#%d", *t.Nth+1))
	}
	if t.Native {
		parts = append(parts, "native")
	}
	if len(parts) == 0 {
		return "the focused field"
	}
	return strings.Join(parts, " ")
}

// formatNumber prints a number as JavaScript does (5, 2.5).
func formatNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// indentJSON is JSON.stringify(value, null, 1) of a value as the hub sent it,
// keeping its key order.
func indentJSON(raw []byte) string {
	var compact, out bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return string(raw)
	}
	if json.Indent(&out, compact.Bytes(), "", " ") != nil {
		return compact.String()
	}
	return out.String()
}

// sleep waits for d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
