package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// The authenticated proxy also carries the IDE's WebSockets. code-server has
// no TCP listener: the only entrypoint is this route and its private socket.
func (s *Server) handleVSCode(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin IDE request refused")
		return
	}
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	if row.Harness != config.HarnessVSCode {
		http.NotFound(w, r)
		return
	}
	prefix := "/ide/" + row.ID
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", harnesses.VSCodeSocket(s.dataDir, row.ID))
	}}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "localhost"
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			pr.Out.URL.RawPath = ""
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "VS Code is not ready. Reload the editor in a moment, or restart the session.", http.StatusServiceUnavailable)
		},
	}
	proxy.ServeHTTP(w, r)
}
