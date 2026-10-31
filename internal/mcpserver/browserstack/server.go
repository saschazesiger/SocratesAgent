// Package browserstack is the BrowserStack MCP server built into Socrates:
// real phones, tablets and desktop browsers on BrowserStack Automate, driven
// one action at a time by a chat's agent, with every action recorded with
// its screenshot (which the chat's tests take as evidence,
// mcpserver.Recorder). Admin keeps the accounts; each carries one session at
// a time, leased to the chat that uses it.
//
// It is the `qa` driver of browser.lol's Device QA, moved into Socrates:
// the commands are its tools, the daemon that held a device between them is
// a device of this server (device.go), the slots are the pool (pool.go),
// and what was the repository's (the site, its logins, its mailbox, its
// logs) is RepoFile.
package browserstack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/browserstack/driver"
)

// Name is the server's name.
const Name = "browserstack"

// Deps is what the server gets of Socrates.
type Deps struct {
	// DataDir is the data directory: a chat's evidence and the facts of
	// open sessions go below it. "" for a server that runs nothing (the
	// catalog check).
	DataDir string
	// Accounts is the accounts Admin keeps for the server.
	Accounts func() []mcpserver.Account
}

// Server is the BrowserStack MCP server.
type Server struct {
	deps Deps
	pool *pool

	mu      sync.Mutex
	devices map[string]*device // by session
	holders sync.Map           // holder → *sync.Mutex (holderLock)
}

// New is the server; it closes what a server before it left open.
func New(deps Deps) *Server {
	s := &Server{deps: deps, devices: map[string]*device{}}
	s.pool = newPool(s)
	if deps.DataDir != "" {
		go s.closeOrphans()
	}
	return s
}

func (s *Server) accounts() []mcpserver.Account {
	if s.deps.Accounts == nil {
		return nil
	}
	return s.deps.Accounts()
}

// account is an Admin account as the driver has it.
func account(a mcpserver.Account, member int) driver.Account {
	return driver.Account{Member: member, User: a.Values["user"], Key: a.Values["key"]}
}

func label(values map[string]string) string { return values["user"] }

func (s *Server) Name() string  { return Name }
func (s *Server) Title() string { return "BrowserStack" }
func (s *Server) Description() string {
	return "Real phones, tablets and desktop browsers on BrowserStack: the agent opens pages, taps, types and " +
		"judges screenshots; every action is recorded. Each account carries one session at a time."
}

// --------------------------------------------------------------- accounts

func (s *Server) AccountFields() []mcpserver.Field {
	return []mcpserver.Field{
		{Name: "user", Label: "Username", Placeholder: "jane_AbCdEf", Hint: "Automate's username (Account, Settings)."},
		{Name: "key", Label: "Access key", Secret: true, Hint: "Automate's access key, beside the username."},
	}
}

func (s *Server) AccountLabel(values map[string]string) string { return label(values) }

// CheckAccount reads the account's Automate plan.
func (s *Server) CheckAccount(ctx context.Context, values map[string]string) mcpserver.Check {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	plan, err := driver.PlanOf(ctx, driver.Account{User: values["user"], Key: values["key"]})
	switch {
	case err == nil && plan != nil:
		detail := fmt.Sprintf("Automate: %d of %d parallel sessions running", plan.Running, plan.Max)
		if plan.Queued > 0 {
			detail += fmt.Sprintf(", %d queued", plan.Queued)
		}
		return mcpserver.Check{State: mcpserver.AccountValid, Detail: detail}
	case err != nil && isUnauthorized(err):
		return mcpserver.Check{State: mcpserver.AccountInvalid, Detail: "BrowserStack refused the username or the access key"}
	case err != nil:
		return mcpserver.Check{State: mcpserver.AccountUnknown, Detail: "BrowserStack could not be asked: " + err.Error()}
	}
	return mcpserver.Check{State: mcpserver.AccountUnknown, Detail: "BrowserStack gave no plan"}
}

// ------------------------------------------------------------------- pool

func (s *Server) Lease(holder string) (mcpserver.Lease, string) { return s.pool.Lease(holder) }
func (s *Server) Release(holder string)                         { s.pool.Release(holder) }

var consents = []string{"accept", "reject", "leave"}

func deviceIDs() string {
	var ids []string
	for _, d := range driver.Devices {
		ids = append(ids, d.ID)
	}
	return strings.Join(ids, ", ")
}

// ------------------------------------------------------------------- uses

// Uses is the devices running and the leases held, for Admin.
func (s *Server) Uses() []mcpserver.Use {
	s.mu.Lock()
	devices := make([]*device, 0, len(s.devices))
	for _, d := range s.devices {
		devices = append(devices, d)
	}
	s.mu.Unlock()
	var out []mcpserver.Use
	seen := map[string]bool{}
	for _, d := range devices {
		state, since := d.status()
		what := d.dev.Label + ", " + state
		if !since.IsZero() {
			what += " since " + since.Format("15:04")
		}
		seen[d.account] = true
		out = append(out, mcpserver.Use{Account: d.account, Session: d.session, What: what})
	}
	for _, a := range s.accounts() {
		if seen[a.ID] {
			continue
		}
		if h := s.pool.holderOf(a.ID); h != "" {
			out = append(out, mcpserver.Use{Account: a.ID, Session: h, What: "leased, no device running"})
		}
	}
	return out
}

// Plan is the last reading of an account's Automate plan, for Admin.
func (s *Server) Plan(id string) *driver.Plan { return s.pool.plan(id) }

// -------------------------------------------------------------- the tools

// Tools is the qa commands as tools.
func (s *Server) Tools() []mcpserver.Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	obj := func(props map[string]any, required ...string) map[string]any {
		o := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			o["required"] = required
		}
		return o
	}
	target := func(extra map[string]any) map[string]any {
		props := map[string]any{
			"text":  str("The control's visible text or accessible name. Exact names win over partial ones, then one visible on top."),
			"role":  str("Narrows to a role: button, link, textbox, checkbox, combobox, tab, menuitem, ..."),
			"exact": boolean("Only the exact name."),
			"any":   boolean("Also match plain text (a heading, a paragraph)."),
			"css":   str("A CSS selector: alone it is what is taken; with a text it is the area the text is searched in."),
			"nth":   map[string]any{"type": "integer", "minimum": 1, "description": "Which of the matches the answer lists, from 1."},
		}
		for k, v := range extra {
			props[k] = v
		}
		return props
	}
	none := obj(map[string]any{})
	return []mcpserver.Tool{
		{Name: "start", Title: "Start the device", Description: "Starts the device and answers `device ready: ...`. When BrowserStack queues the device (the account " +
			"is busy elsewhere) or every account is in use, it answers `still queued` after about 8 minutes while Socrates keeps " +
			"waiting: call start again, as often as it says so.",
			Schema: obj(map[string]any{
				"device":   str("The device (see devices): iphone, pixel, win-chrome, ... Required in a chat."),
				"locale":   str("The locale paths are opened in (en, de, ...), when the repository's site has locales."),
				"consent":  map[string]any{"type": "string", "enum": consents, "description": "How the ad consent dialog is answered after every action (default accept)."},
				"base_url": str("The site paths are opened on, when the repository does not name one (https://...)."),
			})},
		{Name: "devices", Title: "List the devices", Description: "Lists the devices start takes, with their ids.", Schema: none},
		{Name: "open", Title: "Open a page", Description: "Opens a page: the session's locale is put in front of a path " +
			"(/create opens /es/create), a path that names a locale or a full URL is taken as it is. mail_link opens link n of " +
			"the last mail read, so a magic link never passes through you.",
			Schema: obj(map[string]any{"path": str("A path or a full URL."), "mail_link": map[string]any{"type": "integer", "minimum": 1, "description": "Link n of the last mail read."}})},
		{Name: "find", Title: "Find a control", Description: "Scrolls a control into view without touching it: shows that it is " +
			"there, whether it is disabled and what covers it.", Schema: obj(target(nil))},
		{Name: "tap", Title: "Tap a control", Description: "Taps a control. native: a real touch on phones and tablets; force: " +
			"tap one an ad covers.", Schema: obj(target(map[string]any{
			"native": boolean("A real touch on the device screen (phones and tablets)."),
			"force":  boolean("Tap even though something covers it."),
		}))},
		{Name: "type", Title: "Type", Description: "Types into a field (into names it; without into, the focused field). native " +
			"types through the device keyboard, clear empties the field first, submit presses Enter after. Instead of a value: " +
			"secret types a login of the repository by name (QA_GOOGLE_PASSWORD, ...), totp the current authenticator code " +
			"(google, ms), address this session's mailbox address, code the code of the last mail.",
			Schema: obj(target(map[string]any{
				"value":   str("What to type."),
				"into":    str("The field, by its label or name."),
				"native":  boolean("Tap the field with a real touch and type through the device keyboard (phones and tablets)."),
				"clear":   boolean("Empty the field first."),
				"submit":  boolean("Press Enter after."),
				"secret":  str("Type this login of the repository's secrets, by name."),
				"totp":    str("Type the current authenticator code of this provider (google, ms)."),
				"address": boolean("Type this session's own mailbox address."),
				"code":    boolean("Type the code of the last mail read."),
			}))},
		{Name: "key", Title: "Press a key", Description: "Presses enter, tab, escape, backspace, up, down, left, right or space.",
			Schema: obj(map[string]any{"name": map[string]any{"type": "string", "enum": []string{"enter", "tab", "escape", "backspace", "up", "down", "left", "right", "space"}}}, "name")},
		{Name: "keyboard", Title: "Close the keyboard", Description: "Closes the on-screen keyboard as a person would (Done on iOS, Back on Android).", Schema: none},
		{Name: "scroll", Title: "Scroll", Description: "Scrolls by most of a screen, or to the top or bottom.",
			Schema: obj(map[string]any{"where": map[string]any{"type": "string", "enum": []string{"down", "up", "top", "bottom"}}}, "where")},
		{Name: "rotate", Title: "Rotate", Description: "Rotates a phone or tablet (refused on a desktop).",
			Schema: obj(map[string]any{"orientation": map[string]any{"type": "string", "enum": []string{"portrait", "landscape"}}}, "orientation")},
		{Name: "back", Title: "Go back", Description: "Browser back.", Schema: none},
		{Name: "forward", Title: "Go forward", Description: "Browser forward.", Schema: none},
		{Name: "reload", Title: "Reload", Description: "Reloads the page.", Schema: none},
		{Name: "wait", Title: "Wait", Description: "Waits, keeping the session alive.",
			Schema: obj(map[string]any{"seconds": map[string]any{"type": "number", "minimum": 0, "maximum": 600}}, "seconds")},
		{Name: "look", Title: "Look", Description: "A screenshot and the controls on screen: role, name, state, what covers them.", Schema: none},
		{Name: "shot", Title: "Screenshot", Description: "A screenshot only, without waiting for the page.", Schema: none},
		{Name: "env", Title: "Device facts", Description: "The device facts: user agent, viewport, pixel ratio, system language.", Schema: none},
		{Name: "address", Title: "Mailbox address", Description: "This session's own mailbox address, fresh per session.", Schema: none},
		{Name: "mail", Title: "Read mail", Description: "Waits (90 s by default) for the newest mail to this session's address, " +
			"or to `to`, and shows sender, subject, code and links.",
			Schema: obj(map[string]any{"to": str("Another address to read."), "wait": map[string]any{"type": "integer", "minimum": 0, "maximum": 600, "description": "Seconds to wait, 90 when left out."}})},
		{Name: "logs", Title: "Application logs", Description: "The application's WARN and ERROR lines since the device started; kept in the evidence.", Schema: none},
		{Name: "stop", Title: "Stop the device", Description: "Closes the device session. Socrates closes it when the chat is deleted and " +
			"after 20 minutes without a command; you need it only to start over with start.", Schema: none},
	}
}

// Instructions is TOOL.md's guidance for the agent, with the tools' names.
func (s *Server) Instructions() string { return instructions }

// args is a tool's arguments, as every tool takes some of them.
type args struct {
	Device   string  `json:"device"`
	Locale   string  `json:"locale"`
	Consent  string  `json:"consent"`
	BaseURL  string  `json:"base_url"`
	Path     string  `json:"path"`
	MailLink int     `json:"mail_link"`
	Text     string  `json:"text"`
	Role     string  `json:"role"`
	Exact    bool    `json:"exact"`
	Any      bool    `json:"any"`
	CSS      string  `json:"css"`
	Nth      int     `json:"nth"`
	Native   bool    `json:"native"`
	Force    bool    `json:"force"`
	Value    string  `json:"value"`
	Into     string  `json:"into"`
	Clear    bool    `json:"clear"`
	Submit   bool    `json:"submit"`
	Secret   string  `json:"secret"`
	TOTP     string  `json:"totp"`
	Address  bool    `json:"address"`
	Code     bool    `json:"code"`
	Name     string  `json:"name"`
	Where    string  `json:"where"`
	Orient   string  `json:"orientation"`
	Seconds  float64 `json:"seconds"`
	To       string  `json:"to"`
	Wait     *int    `json:"wait"`
}

func (a args) target(text string) driver.Target {
	t := driver.Target{Text: text, Role: a.Role, CSS: a.CSS, Exact: a.Exact, Any: a.Any, Native: a.Native}
	if a.Nth > 0 {
		n := a.Nth - 1
		t.Nth = &n
	}
	return t
}

// Call runs one tool.
func (s *Server) Call(ctx context.Context, sess mcpserver.Session, tool string, raw json.RawMessage) mcpserver.Result {
	var a args
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcpserver.Error("The arguments could not be read: " + err.Error())
	}
	switch tool {
	case "start":
		return s.start(ctx, sess, a)
	case "stop":
		return mcpserver.Text(s.stop(ctx, sess.ID, "stop"))
	case "devices":
		var b strings.Builder
		for _, d := range driver.Devices {
			extra := ""
			if d.Extra {
				extra = " (outside the usual matrix)"
			}
			fmt.Fprintf(&b, "%s: %s, %s%s\n", d.ID, d.Label, d.Kind, extra)
		}
		return mcpserver.Text(strings.TrimSpace(b.String()))
	}
	d := s.device(sess.ID)
	if d == nil {
		return mcpserver.Error("no device session: call start first")
	}
	if tool == "logs" {
		return d.logs(ctx)
	}
	return d.command(ctx, func(ds *driver.Session) mcpserver.Result {
		switch tool {
		case "open":
			if a.MailLink > 0 {
				return d.result(ds, ds.OpenMailLink(ctx, a.MailLink))
			}
			path := a.Path
			if path == "" {
				path = "/"
			}
			return d.result(ds, ds.Open(ctx, path))
		case "find":
			return d.result(ds, ds.Find(ctx, a.target(a.Text)))
		case "tap":
			return d.result(ds, ds.Tap(ctx, a.target(a.Text), a.Force))
		case "type":
			o := driver.TypeOptions{Native: a.Native, Clear: a.Clear, Submit: a.Submit, Secret: a.Secret, TOTP: a.TOTP, Address: a.Address, Code: a.Code}
			if a.Into != "" || a.CSS != "" {
				t := a.target(a.Into)
				o.Into = &t
			}
			return d.result(ds, ds.Type(ctx, a.Value, o))
		case "key":
			act, err := ds.Key(ctx, a.Name)
			if err != nil {
				return mcpserver.Error(err.Error())
			}
			return d.result(ds, act)
		case "keyboard":
			return d.result(ds, ds.Keyboard(ctx))
		case "scroll":
			where := a.Where
			if where == "" {
				where = "down"
			}
			return d.result(ds, ds.Scroll(ctx, where))
		case "rotate":
			return d.result(ds, ds.Rotate(ctx, a.Orient == "landscape"))
		case "back", "forward", "reload":
			return d.result(ds, ds.Navigation(ctx, tool))
		case "wait":
			sec := a.Seconds
			if sec <= 0 {
				sec = 5
			}
			return d.result(ds, ds.Wait(ctx, sec))
		case "look":
			return d.result(ds, ds.Look(ctx))
		case "shot":
			return d.result(ds, ds.Shot(ctx))
		case "env":
			env, err := ds.Env(ctx)
			if err != nil {
				return mcpserver.Error(err.Error())
			}
			out, _ := json.MarshalIndent(env, "", " ")
			return mcpserver.Text(string(out))
		case "address":
			if addr := ds.Address(); addr != "" {
				return mcpserver.Text(addr)
			}
			return mcpserver.Error("this repository has no mailbox (" + RepoFile + ", mail)")
		case "mail":
			wait := 90
			if a.Wait != nil {
				wait = *a.Wait
			}
			return d.result(ds, ds.Mail(ctx, a.To, time.Duration(wait)*time.Second))
		}
		return mcpserver.Error("there is no tool " + tool)
	})
}

// previewSize is the longest side of a screenshot shown to the model.
const previewSize = 1568

// result is an action as the agent reads it: the text qa printed, and the
// screenshot, which it is to look at.
func (d *device) result(ds *driver.Session, act *driver.Action) mcpserver.Result {
	res := mcpserver.Result{Content: []mcpserver.Content{{Type: "text", Text: ds.Describe(act)}}, IsError: act.Error != ""}
	if act.Screenshot != "" {
		if png, err := os.ReadFile(filepath.Join(d.evidence.Dir, act.Screenshot)); err == nil {
			if jpg, err := driver.Preview(png, previewSize); err == nil {
				res.Content = append(res.Content, mcpserver.Content{Type: "image", MimeType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(jpg)})
			}
		}
	}
	return res
}

// End closes the device of a session, whatever state it is in.
func (s *Server) End(ctx context.Context, session string) string {
	what := ""
	if s.device(session) != nil {
		what = s.stop(ctx, session, "the session ended")
	}
	s.forgetChat(session)
	return what
}

func isUnauthorized(err error) bool { return errors.Is(err, driver.ErrUnauthorized) }

func (s *Server) device(session string) *device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devices[session]
}

// chatEvidence is where a chat's actions are recorded.
func (s *Server) chatEvidence(session string) string {
	return filepath.Join(s.deps.DataDir, "mcp", Name, "chats", session)
}

// EvidenceDir is the folder a chat's actions are recorded in, which its
// tests take their screenshots from (mcpserver.Recorder).
func (s *Server) EvidenceDir(session string) string {
	if s.deps.DataDir == "" || session == "" || strings.ContainsAny(session, `/\.`) {
		return ""
	}
	return s.chatEvidence(session)
}

// forgetChat removes what a chat's devices recorded: it is gone.
func (s *Server) forgetChat(session string) {
	if s.deps.DataDir != "" && session != "" && !strings.ContainsAny(session, `/\.`) {
		_ = os.RemoveAll(s.chatEvidence(session))
	}
}
