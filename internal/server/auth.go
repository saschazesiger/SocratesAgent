package server

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strconv"
	"strings"

	zdash "github.com/saschazesiger/Z-Dash/server"
	"github.com/saschazesiger/Z-Dash/server/cloudflare"
)

// The password and the sessions are the Z-Dash server's (app.go): it hashes
// the password with Argon2id under auth.password, keeps the sessions, limits
// the sign-in attempts and answers /api/login, /api/logout and /api/password.
// What is here is Socrates' own around it: the setup page's first step, the
// state every page starts from, and the password of the builds before.

// PasswordKey holds the PBKDF2 hash of a password set before the Z-Dash
// server kept it. It still signs in; the first sign-in with it stores the
// password in the server's format and deletes this key (app.go,
// LegacyPassword).
const PasswordKey = "password_hash"

// verifyPassword checks a password against a hash of the builds before the
// Z-Dash server ("pbkdf2-sha256$rounds$salt$key").
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	rounds, err := strconv.Atoi(parts[1])
	if err != nil || rounds <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, rounds, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (s *Server) hasPassword() bool { return s.app.HasPassword() }

// authenticated is a signed-in request: one that came through the tunnel
// with a valid Cloudflare Access token, or one with a session's cookie.
func (s *Server) authenticated(r *http.Request) bool { return s.app.SignedIn(r) }

// auth is what every signed-in route of Socrates adds to the Z-Dash server's
// own checks (the session, the CSRF guard): nothing is served before the
// setup is finished, which a person who came through the tunnel could
// otherwise skip.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The server marks every API answer no-store; Socrates' handlers say
		// themselves what may be kept (a chat's images, its files).
		w.Header().Del("Cache-Control")
		if !s.hasPassword() {
			writeError(w, http.StatusForbidden, "setup is not finished yet")
			return
		}
		next(w, r)
	}
}

// signedIn guards a page that is no API route (the editor's proxy): the
// session, and the origin of a request that changes something.
func (s *Server) signedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasPassword() {
			writeError(w, http.StatusForbidden, "setup is not finished yet")
			return
		}
		if !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if r.Method != http.MethodGet && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross origin request rejected")
			return
		}
		next(w, r)
	}
}

// open is a route anybody may call, with the handler's own caching.
func open(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Del("Cache-Control")
		next(w, r)
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Cloudflare and a reverse proxy put the real caller in a header, and it
	// is the value to rate limit on when Socrates is published through one.
	// The header is believed only when the connection itself came from a
	// machine that could be that proxy - loopback, or a private network a
	// reverse proxy sits on. From anywhere else it is a caller choosing its own
	// identity, which is a rate limit that limits nothing.
	if forwarder(host) {
		if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
			return cf
		}
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			return strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0])
		}
	}
	return host
}

// forwarder reports whether a peer is close enough to be the proxy in front of
// Socrates: cloudflared runs beside it, and a reverse proxy is on the same
// private network.
func forwarder(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// ---------------------------------------------------------------- handlers

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	setupRequired := !s.hasPassword()
	payload := map[string]any{
		"setup_required": setupRequired,
		"authenticated":  s.authenticated(r),
		"version":        Version,
	}
	if s.authenticated(r) {
		payload["backup"] = s.backupSummary()
	}
	if setupRequired {
		// A new machine is set up with a password or from a backup; the
		// second only while nothing here would be overwritten.
		payload["restore_available"] = s.restoreAvailable()
		payload["restore"] = s.restoreJob.snapshot()
	}
	if setupRequired || s.authenticated(r) {
		// The setup page offers the tunnel as its second step, and says
		// whether cloudflared would have to be downloaded for it.
		st := s.app.Tunnel().Status()
		payload["cloudflared"] = map[string]any{
			"installed":   st.Cloudflared.Installed,
			"can_install": cloudflare.Supported(),
		}
		payload["local_url"] = s.LocalURL()
	}
	// Who is signed in: through the tunnel, the Cloudflare Access user.
	id := zdash.Who(r)
	payload["via"] = id.Via
	if s.authenticated(r) && id.Email != "" {
		payload["email"] = id.Email
	}
	writeJSON(w, http.StatusOK, payload)
}

// handleSetup is the setup page's first step: the first password, with the
// OpenRouter key when the person has one, and the browser signed in.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.hasPassword() {
		writeError(w, http.StatusConflict, "a password is already set")
		return
	}
	if s.restoreJob.restoring() {
		writeError(w, http.StatusConflict, "a backup is being restored onto this Socrates; its password is the backup's")
		return
	}
	var body struct {
		Password      string `json:"password"`
		OpenRouterKey string `json:"openrouter_key"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	won, err := s.app.SetFirstPassword(body.Password)
	if err != nil {
		// The server's rule, in its words: "Use at least 10 characters."
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !won {
		writeError(w, http.StatusConflict, "a password is already set")
		return
	}
	settings := s.Settings()
	if v := strings.TrimSpace(body.OpenRouterKey); v != "" {
		settings.OpenRouter.APIKey = v
		if err := s.saveSettings(settings); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.app.StartSession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
