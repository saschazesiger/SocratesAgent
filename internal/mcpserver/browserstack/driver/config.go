// Package driver drives one real device on BrowserStack over WebDriver: it
// starts the device session, carries out the actions an agent asks for (open,
// find, tap, type, look, ...) and writes every action with its screenshot into
// the evidence. It is the Go port of the browserstack test tool's TypeScript
// driver; the page scripts in browser/ are that tool's, unchanged.
package driver

import (
	"embed"
	"time"
)

// BrowserStack Automate: the W3C hub and the REST API of sessions and plans.
// Variables so that tests can point them at a local server.
var (
	HubURL     = "https://hub-cloud.browserstack.com/wd/hub"
	SessionAPI = "https://api.browserstack.com/automate/sessions"
	PlanAPI    = "https://api.browserstack.com/automate/plan.json"
)

const (
	// IdleTimeoutS is how long BrowserStack keeps a session without a
	// command, in seconds (its maximum).
	IdleTimeoutS = 300
	// KeepAliveAfter is how long a holder of the session may stay silent before it
	// sends a harmless command (Session.KeepAlive).
	KeepAliveAfter = 60 * time.Second
	// SessionWarn is when the driver starts warning about BrowserStack's hard
	// limit of 2 h.
	SessionWarn = 100 * time.Minute
	// DeviceStartTimeout is how long Start waits for a device, BrowserStack's
	// queue included.
	DeviceStartTimeout = 40 * time.Minute
	// SettleQuiet is how long the page must be quiet after an action;
	// SettleMax is when it is reported as not settled.
	SettleQuiet = 800 * time.Millisecond
	SettleMax   = 20 * time.Second
	// MailPoll is how often Mail looks into the mailbox while it waits.
	MailPoll = 5 * time.Second
	// MailWaitDefault is the usual wait of Mail.
	MailWaitDefault = 90 * time.Second
	// MarkerGapCSS is how far apart (diagonally, css px) the two calibration
	// markers of a native tap sit around the control's centre.
	MarkerGapCSS = 30
	// DesktopResolution is the resolution desktop sessions run at; they are
	// also maximised.
	DesktopResolution = "1920x1080"
)

// The timeouts of single WebDriver calls.
const (
	callTimeout    = 120 * time.Second
	executeTimeout = 180 * time.Second
	planTimeout    = 10 * time.Second
	linksTimeout   = 30 * time.Second
)

// keyboardWaitTries is how often (every 0.5 s) native typing on iOS looks for
// the keyboard to slide in.
const keyboardWaitTries = 6

// The consent policies: how the ad consent dialog is answered after every
// action.
const (
	ConsentAccept = "accept"
	ConsentReject = "reject"
	ConsentLeave  = "leave"
)

// Config is everything one device session needs.
type Config struct {
	Device Device
	// Locale is the session's locale, e.g. "es"; may be "".
	Locale string
	// Consent is accept, reject or leave; "" is accept.
	Consent string
	// BaseURL is the site paths are opened on, without a trailing slash; ""
	// means only full URLs can be opened.
	BaseURL string
	// Locales are the path prefixes taken as they are: a path whose first
	// segment is one of them (or that starts with /_) is not given the
	// session's locale.
	Locales []string
	Account Account
	// Project, Build and Name are BrowserStack's projectName, buildName and
	// sessionName.
	Project, Build, Name string
	// Secrets is what Type with Secret may type, by name. Values are never
	// printed or recorded.
	Secrets map[string]string
	// TOTP maps a provider (google, ms, ...) to the name of its authenticator
	// key in Secrets; a provider not in it falls back to
	// QA_<PROVIDER>_TOTP_SECRET.
	TOTP map[string]string
	// Mail reads the mailbox; nil means the session has none.
	Mail MailSource
	// Address is the template of the session's mailbox address, e.g.
	// "qa{stamp}m{member}-beta@goog.re": {stamp} becomes the time in
	// milliseconds in base 36, {member} the account's member number. "" means
	// no address.
	Address  string
	Evidence *Evidence
	// StartTimeout bounds Start; 0 is DeviceStartTimeout.
	StartTimeout time.Duration
}

//go:embed browser/*.js
var browserFS embed.FS

// page holds the page scripts, each prefixed with common.js.
var page = func() map[string]string {
	common := mustScript("common")
	out := map[string]string{}
	for _, name := range []string{"settle", "collector", "consent", "find", "look", "misc", "marker"} {
		out[name] = common + "\n" + mustScript(name)
	}
	return out
}()

func mustScript(name string) string {
	data, err := browserFS.ReadFile("browser/" + name + ".js")
	if err != nil {
		panic(err)
	}
	return string(data)
}
