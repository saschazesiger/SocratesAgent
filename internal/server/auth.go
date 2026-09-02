package server

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/tunnel"
)

const (
	passwordKey    = "password_hash"
	sessionCookie  = "socrates_session"
	sessionTTL     = 30 * 24 * time.Hour
	pbkdf2Rounds   = 210000
	minPasswordLen = 6
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Rounds, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Rounds,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

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

func (s *Server) hasPassword() bool {
	v, err := s.store.GetKV(passwordKey)
	return err == nil && strings.TrimSpace(v) != ""
}

func (s *Server) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return s.store.ValidLogin(c.Value)
}

// auth wraps an API handler with session and CSRF checks.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
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

// startLogin hands the browser a fresh cookie. The cookie is still called
// socrates_session, because renaming a cookie signs everybody out and the
// table it came from is the only thing that was renamed.
func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	if err := s.store.CreateLogin(token, sessionTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// Strict rather than Lax: the WebSocket handshake carries this cookie
		// and is checked for origin, and no second CSRF token is worth the
		// reconnect it would break. The only visible effect is that following
		// a link from another site lands on the login page.
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		MaxAge:   int(sessionTTL.Seconds()),
	})
	return nil
}

func clientIP(r *http.Request) string {
	// Cloudflare puts the real caller here; it is the value to rate limit on
	// when Socrates is published through a tunnel.
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
		return cf
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// throttle slows down password guessing.
func (s *Server) throttle(ip string) error {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	a := s.loginFail[ip]
	if a == nil {
		return nil
	}
	if time.Now().Before(a.until) {
		return fmt.Errorf("too many attempts, try again in %d seconds", int(time.Until(a.until).Seconds())+1)
	}
	return nil
}

func (s *Server) noteLogin(ip string, ok bool) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if ok {
		delete(s.loginFail, ip)
		return
	}
	a := s.loginFail[ip]
	if a == nil {
		a = &attempt{}
		s.loginFail[ip] = a
	}
	a.count++
	if a.count >= 5 {
		delay := time.Duration(a.count-4) * 15 * time.Second
		if delay > 10*time.Minute {
			delay = 10 * time.Minute
		}
		a.until = time.Now().Add(delay)
	}
}

// ---------------------------------------------------------------- handlers

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	setupRequired := !s.hasPassword()
	payload := map[string]any{
		"setup_required": setupRequired,
		"authenticated":  s.authenticated(r),
		"version":        Version,
	}
	if setupRequired || s.authenticated(r) {
		// The setup wizard asks two things about cloudflared: is it here, and
		// could it be fetched if it is not. Which binary it is and what version
		// it reports is a dashboard question, and the dashboard has
		// /api/tunnel for it.
		installed, _, _ := s.tunnel.Probe()
		payload["cloudflared"] = map[string]any{
			"installed":   installed,
			"can_install": tunnel.Supported(),
		}
		payload["local_url"] = s.LocalURL()
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.hasPassword() {
		writeError(w, http.StatusConflict, "a password is already set")
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross origin request rejected")
		return
	}
	var body struct {
		Password      string                 `json:"password"`
		OpenRouterKey string                 `json:"openrouter_key"`
		Tunnel        *config.TunnelSettings `json:"tunnel"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len([]rune(body.Password)) < minPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the password needs at least %d characters", minPasswordLen))
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetKV(passwordKey, hash); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings := s.Settings()
	changed := false
	if v := strings.TrimSpace(body.OpenRouterKey); v != "" {
		settings.OpenRouter.APIKey = v
		changed = true
	}
	if body.Tunnel != nil {
		settings.Tunnel = *body.Tunnel
		changed = true
	}
	if changed {
		if err := s.saveSettings(settings); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.startLogin(w, r); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// A tunnel requested during setup starts right away, so the operator sees
	// the public URL on the very next screen.
	tunnelErr := ""
	if s.Settings().Tunnel.Enabled && !s.tunnel.Running() {
		if err := s.tunnel.Start(); err != nil {
			tunnelErr = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tunnel_error": tunnelErr})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.hasPassword() {
		writeError(w, http.StatusConflict, "no password is set yet")
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross origin request rejected")
		return
	}
	ip := clientIP(r)
	if err := s.throttle(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	stored, err := s.store.GetKV(passwordKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the stored password")
		return
	}
	if !verifyPassword(stored, body.Password) {
		s.noteLogin(ip, false)
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	s.noteLogin(ip, true)
	_ = s.store.PurgeExpiredLogins()
	if err := s.startLogin(w, r); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Signing out is a state change like every other one, and it is the single
	// endpoint that sits outside the wrapper which normally says so.
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross origin request rejected")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteLogin(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	stored, err := s.store.GetKV(passwordKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the stored password")
		return
	}
	if !verifyPassword(stored, body.Current) {
		writeError(w, http.StatusUnauthorized, "the current password is wrong")
		return
	}
	if len([]rune(body.Next)) < minPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the new password needs at least %d characters", minPasswordLen))
		return
	}
	hash, err := hashPassword(body.Next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetKV(passwordKey, hash); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.DeleteAllLogins(); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.startLogin(w, r); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
