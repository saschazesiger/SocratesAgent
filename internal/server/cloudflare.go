package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/cloudflare"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// identity says how a request reached Socrates.
type identity struct {
	Via   string // "local" or "tunnel"
	Email string // signed in through Cloudflare Access; empty on the local address
}

type identityKeyT struct{}

func via(r *http.Request) identity {
	if id, ok := r.Context().Value(identityKeyT{}).(identity); ok {
		return id
	}
	return identity{Via: "local"}
}

// accessLogout ends the Cloudflare Access session of the browser.
const accessLogout = "/cdn-cgi/access/logout"

// tunnelState is the listener cloudflared reaches and the supervisor of
// cloudflared itself.
type tunnelState struct {
	mu     sync.Mutex
	srv    *http.Server
	cancel context.CancelFunc
	done   chan struct{}
}

// newCloudflare builds the tunnel's service around the store.
func (s *Server) newCloudflare() *cloudflare.Service {
	sock := s.TunnelSocket()
	return cloudflare.New(cloudflare.Options{
		KV:     cfKV{s.store},
		Sealer: keySealer{s},
		Origin: "unix:" + sock,
		BinDir: filepath.Join(s.dataDir, paths.Bin),
		Logf:   log.Printf,
	})
}

// TunnelSocket is the Unix socket cloudflared sends the requests to.
func (s *Server) TunnelSocket() string {
	dir := s.dataDir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Join(dir, paths.TunnelSocket)
}

// StartTunnel listens on the tunnel's socket and supervises cloudflared
// whenever a tunnel is set up. The local address is not touched by any of
// it.
func (s *Server) StartTunnel() error {
	s.tun.mu.Lock()
	defer s.tun.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	s.tun.cancel, s.tun.done = cancel, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		s.cf.Run(ctx)
	}(s.tun.done)

	ln, err := listenUnix(s.TunnelSocket())
	if err != nil {
		return fmt.Errorf("the Cloudflare tunnel cannot reach Socrates: %w", err)
	}
	s.tun.srv = &http.Server{Handler: s.TunnelHandler(), ReadHeaderTimeout: 20 * time.Second}
	go func(srv *http.Server) {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("cloudflare tunnel listener: %v", err)
		}
	}(s.tun.srv)
	return nil
}

// StopTunnel stops cloudflared and the tunnel's listener, on shutdown.
func (s *Server) StopTunnel() {
	s.tun.mu.Lock()
	cancel, done, srv := s.tun.cancel, s.tun.done, s.tun.srv
	s.tun.cancel, s.tun.done, s.tun.srv = nil, nil, nil
	s.tun.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(12 * time.Second):
		}
	}
	if srv != nil {
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = srv.Shutdown(ctx)
	}
}

// listenUnix listens on a socket only this user may open. A socket left
// behind by a Socrates that did not stop cleanly is replaced.
func listenUnix(path string) (net.Listener, error) {
	if len(path) > 100 {
		return nil, fmt.Errorf("the socket path %s is too long for a Unix socket; use a shorter data directory", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// TunnelHandler serves what comes through the tunnel. The sandboxed pages
// (cloudflare.BypassPaths) pass as they are, guarded by their own tokens;
// every other request needs a valid Cloudflare Access token, which signs the
// person in without the password.
func (s *Server) TunnelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(cloudflare.MarkerHeader, "1")
		// The socket has no peer address; Cloudflare names the caller, and
		// nobody but cloudflared can reach this socket to say otherwise.
		r.RemoteAddr = "0.0.0.0:0"
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip"))); ip != nil {
			r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
		}
		if cloudflare.Bypassed(r.URL.Path) {
			ctx := context.WithValue(r.Context(), identityKeyT{}, identity{Via: "tunnel"})
			s.mux.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		email, err := s.cf.Authenticate(r)
		if err != nil || email == "" {
			log.Printf("cloudflare tunnel: refused %s %s: %v", r.Method, r.URL.Path, err)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "Forbidden: sign in through Cloudflare Access with an e-mail address that may use this Socrates.\n"+
				"Signed in with another address? Sign out at "+accessLogout+" and try again.\n")
			return
		}
		ctx := context.WithValue(r.Context(), identityKeyT{}, identity{Via: "tunnel", Email: email})
		s.mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// streamLogin is who holds a live stream (the events, a chat's stream): a
// Socrates login, by its cookie, or a person Cloudflare Access let in through
// the tunnel, by the address.
type streamLogin struct {
	token string
	email string
}

// loginOf is the login of a signed-in request.
func (s *Server) loginOf(r *http.Request) (streamLogin, bool) {
	if id := via(r); id.Via == "tunnel" && id.Email != "" {
		return streamLogin{email: id.Email}, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return streamLogin{}, false
	}
	return streamLogin{token: c.Value}, true
}

// key names the login for the rate limits, without the secret.
func (l streamLogin) key() string {
	if l.email != "" {
		return "access " + l.email
	}
	return loginKey(l.token)
}

// stillSignedIn is whether a stream's login still holds: the cookie's login
// is not signed out, the address may still sign in through the tunnel.
func (s *Server) stillSignedIn(l streamLogin) bool {
	if l.email != "" {
		return s.cf.Allows(l.email)
	}
	return s.store.ValidLogin(l.token)
}

// cfKV is the store as the tunnel keeps its state in it.
type cfKV struct{ st *store.Store }

func (k cfKV) GetKV(key string) (string, error) {
	v, err := k.st.GetKV(key)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return v, err
}

func (k cfKV) SetKV(key, value string) error { return k.st.SetKV(key, value) }

func (k cfKV) DeleteKV(key string) error {
	if err := k.st.DeleteKV(key); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------- handlers

func (s *Server) cloudflarePayload(r *http.Request) map[string]any {
	id := via(r)
	return map[string]any{
		"status":      s.cf.Status(),
		"token_url":   cloudflare.TokenURL(),
		"permissions": cloudflare.Permissions(),
		"via":         id.Via,
		"email":       id.Email,
	}
}

// handleCloudflare reports the tunnel.
func (s *Server) handleCloudflare(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cloudflarePayload(r))
}

// handleCloudflareCheck validates an API token and lists its domains. An
// empty token checks the stored one, for a repair.
func (s *Server) handleCloudflareCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIToken string `json:"api_token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	info, err := s.cf.Check(ctx, body.APIToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleCloudflareSetup starts the setup, or a repair, in the background.
func (s *Server) handleCloudflareSetup(w http.ResponseWriter, r *http.Request) {
	var body cloudflare.SetupRequest
	if !readJSON(w, r, &body) {
		return
	}
	if err := keepsOwnAddress(r, body.Emails); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.cf.StartSetup(body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.cloudflarePayload(r))
}

// handleCloudflareEmails changes who may sign in from outside.
func (s *Server) handleCloudflareEmails(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Emails []string `json:"emails"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := keepsOwnAddress(r, body.Emails); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := s.cf.SetEmails(ctx, body.Emails); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.cloudflarePayload(r))
}

// keepsOwnAddress refuses a list of addresses that would lock out the
// person who sends it through the tunnel.
func keepsOwnAddress(r *http.Request, emails []string) error {
	id := via(r)
	if id.Via != "tunnel" || id.Email == "" {
		return nil
	}
	for _, e := range emails {
		if strings.EqualFold(strings.TrimSpace(e), id.Email) {
			return nil
		}
	}
	return errors.New("keep your own address (" + id.Email + ") on the list: without it, you would lock yourself out. " +
		"To take it off, do it on the local address")
}

// handleCloudflareRemove deletes the tunnel and what belongs to it on
// Cloudflare, in the background.
func (s *Server) handleCloudflareRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.cf.StartRemove(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.cloudflarePayload(r))
}
