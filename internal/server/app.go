package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	zdash "github.com/saschazesiger/Z-Dash/server"
	zstore "github.com/saschazesiger/Z-Dash/server/store"
)

// The HTTP foundation is the Z-Dash server module (docs/design/SERVER.md):
// both listeners, the password and the sessions, the guards in front of every
// route and the Cloudflare tunnel with its API. This file configures it for
// Socrates and carries over what the builds before it kept themselves.

const (
	// sessionsKey is where the Z-Dash server keeps the sessions: a JSON
	// object from the hex SHA-256 of each cookie value to its expiry.
	sessionsKey = "auth.sessions"
	// sessionCookie is the cookie of a signed-in browser. The Z-Dash server
	// makes the same name from the slug, so the cookies of the builds before
	// it still sign in.
	sessionCookie = "socrates_session"
)

// newApp builds the Z-Dash server around s. Nothing listens until Run.
func (s *Server) newApp(addr string) (*zdash.App, error) {
	// The logins of a build that kept them itself are the server's sessions
	// from here on, so the update signs nobody out.
	if err := importLogins(s.store); err != nil {
		return nil, fmt.Errorf("the logins could not be carried over: %w", err)
	}
	return zdash.New(zdash.Config{
		// The names the objects on Cloudflare already carry: "Socrates
		// <host>" applications and policies, the tunnel socrates-<host>, the
		// DNS record "Managed by Socrates (Cloudflare Tunnel)".
		Name:    "Socrates",
		Slug:    "socrates",
		Env:     "SOCRATES",
		Version: Version,
		DataDir: s.dataDir,
		Listen:  addr,
		// A terminal's upload and a voice recording arrive over a phone's
		// link, as slowly as it likes.
		ReadTimeout: -1,
		KV:          appKV{s.store},
		Sealer:      keySealer{s},
		// The pages gate themselves: they redirect to the sign-in and the
		// setup page (routes).
		Pages: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.pages.ServeHTTP(w, r) }),
		// The pages a chat shows in a sandboxed frame: requests from such a
		// frame carry no cookie, so a login would leave them white. Each
		// guards its files with a token of its own.
		Bypass: map[string]http.Handler{
			"/docs/":         http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.sandboxed.ServeHTTP(w, r) }),
			"/artifact-kit/": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.sandboxed.ServeHTTP(w, r) }),
		},
		BypassLabel:   "sandboxed pages",
		AccessSession: "720h", // as long as a Socrates login, so a phone does not ask every day
		// Lax, not Strict: a link to the tunnel opened from another app must
		// land in Socrates and not on its sign-in page. The CSRF guard and
		// the WebSockets' origin check do not lean on the cookie's mode.
		SameSite: http.SameSiteLaxMode,
		LegacyPassword: &zdash.LegacyPassword{
			Has: func() bool {
				v, err := s.store.GetKV(PasswordKey)
				return err == nil && v != ""
			},
			Verify: func(password string) bool {
				v, err := s.store.GetKV(PasswordKey)
				return err == nil && verifyPassword(v, password)
			},
			Drop: func() error { return s.store.DeleteKV(PasswordKey) },
		},
		// Every open stream of a login that ended stops now, not when its
		// socket happens to drop: a phone left on Home must stop hearing
		// every question the moment its owner signs everybody out.
		OnSignOut: func() {
			s.events.revoke()
			s.chats.revoke()
		},
		Headers: ownHeaders,
		Logf:    log.Printf,
	})
}

// ownHeaders takes the Z-Dash server's security headers off again. Socrates'
// pages set their own (internal/web: nosniff, the referrer policy and
// frame-ancestors 'self'; the sandboxed pages a policy each), and the
// server's strict policy would stop what they are built from: inline scripts
// and styles, the editor's frame, workers, the microphone.
func ownHeaders(h http.Header) {
	for _, name := range []string{
		"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy",
		"X-Frame-Options", "Cross-Origin-Opener-Policy", "Permissions-Policy",
	} {
		h.Del(name)
	}
}

// Run serves on the local address and the tunnel's socket until ctx ends,
// and supervises cloudflared whenever a tunnel is set up.
func (s *Server) Run(ctx context.Context) error { return s.app.Run(ctx) }

// Serving is closed once this server has answered a health check: its own
// listener serves, and not another process that holds the address.
func (s *Server) Serving() <-chan struct{} { return s.serving }

// Handler is what the local address serves, for the tests.
func (s *Server) Handler() http.Handler { return s.app.Handler() }

// TunnelHandler is what the tunnel's socket serves, for the tests: every
// request but those to the sandboxed pages needs a valid Cloudflare Access
// token, which signs the person in without the password.
func (s *Server) TunnelHandler() http.Handler { return s.app.TunnelHandler() }

// importLogins writes the logins table of the builds before the Z-Dash
// server into its sessions, once: while there are no sessions yet. The cookie
// keeps its name and its value, the server keeps only the value's hash.
func importLogins(st *store.Store) error {
	if _, err := st.GetKV(sessionsKey); !errors.Is(err, store.ErrNotFound) {
		return err
	}
	logins, err := st.Logins()
	if err != nil {
		return err
	}
	type session struct {
		Expires int64 `json:"expires"`
	}
	sessions := make(map[string]session, len(logins))
	for _, l := range logins {
		sum := sha256.Sum256([]byte(l.Token))
		sessions[hex.EncodeToString(sum[:])] = session{Expires: l.ExpiresAt}
	}
	raw, err := json.Marshal(sessions)
	if err != nil {
		return err
	}
	if _, err := st.SetKVIfMissing(sessionsKey, string(raw)); err != nil {
		return err
	}
	if len(logins) > 0 {
		log.Printf("carried %d logins over to the Z-Dash server's sessions", len(logins))
	}
	return nil
}

// appKV is the store's key-value table as the Z-Dash server keeps its state
// in it: the password (auth.password), the sessions and the tunnel
// (cloudflare:*).
type appKV struct{ st *store.Store }

func (k appKV) Get(key string) ([]byte, error) {
	v, err := k.st.GetKV(key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, zstore.ErrNotFound
	}
	return []byte(v), err
}

func (k appKV) Set(key string, value []byte) error { return k.st.SetKV(key, string(value)) }

func (k appKV) SetIfMissing(key string, value []byte) (bool, error) {
	return k.st.SetKVIfMissing(key, string(value))
}

func (k appKV) Delete(key string) error { return k.st.DeleteKV(key) }

// streamLogin is who holds a live stream (the events, a chat's stream): the
// request it was opened with, which carries the cookie of a Socrates login or
// the address Cloudflare Access let in through the tunnel.
type streamLogin struct {
	r *http.Request
	// name is the login in the rate limits' table, without the secret.
	name string
}

// loginOf is the login of a signed-in request.
func (s *Server) loginOf(r *http.Request) (streamLogin, bool) {
	if id := zdash.Who(r); id.Via == "tunnel" {
		if id.Email == "" {
			return streamLogin{}, false
		}
		return streamLogin{r: r, name: "access " + id.Email}, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return streamLogin{}, false
	}
	return streamLogin{r: r, name: loginKey(c.Value)}, true
}

// key names the login for the rate limits.
func (l streamLogin) key() string { return l.name }

// stillSignedIn is whether a stream's login still holds: the cookie's session
// was not ended, the address may still sign in through the tunnel.
func (s *Server) stillSignedIn(l streamLogin) bool {
	return l.r != nil && s.app.StillSignedIn(l.r)
}
