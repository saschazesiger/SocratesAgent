package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mirror"
)

// The storage card of the admin page, and the status the session page's
// banner polls. The bucket mirror itself is built in main.go before the
// server, because it has to have hydrated the database the server is about
// to read; the server is only handed it.
//
//	GET  /api/storage              mirror.Status
//	POST /api/storage/test         {"result": mirror.TestResult, "status": …}
//	POST /api/storage/sync         {"ok":true, "status": …}
//	                               502/409 {"error", "status"} when it failed
//	PUT  /api/storage/credentials  mirror.Credentials → {"status": …}
//	                               409 with an environment configuration,
//	                               400 when the bucket refuses them
//
// GET /api/state also carries a short "storage" object for a signed-in
// page, so the banner ("Storage unreachable since …, N changes waiting",
// "Another Socrates took over this bucket") costs no request of its own.

// SetMirror hands the server its bucket mirror. It also shuts the scheduler's
// gate while the mirror is fenced: a Socrates that no longer owns the bucket
// must not start sessions by itself.
func (s *Server) SetMirror(m *mirror.Mirror) {
	s.mirror.Store(m)
	if m == nil {
		s.SetAutoStartGate(nil)
		return
	}
	s.SetAutoStartGate(m.AutoStartAllowed)
}

// storageStatus is the mirror's status, or "not configured" for a server
// built without one (the tests, mostly).
func (s *Server) storageStatus() mirror.Status {
	if m := s.mirror.Load(); m != nil {
		return m.Status()
	}
	return mirror.Status{State: "none"}
}

// storageSummary is the part of the status the banner needs.
func (s *Server) storageSummary() map[string]any {
	st := s.storageStatus()
	return map[string]any{
		"configured":  st.Configured,
		"state":       st.State,
		"fenced":      st.Fenced,
		"pending":     st.Pending,
		"last_error":  st.LastError,
		"error_since": st.ErrorSince,
		"lease":       st.Lease,
	}
}

func (s *Server) handleStorageStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.storageStatus())
}

// storageMirror answers 409 for a server without a mirror.
func (s *Server) storageMirror(w http.ResponseWriter) *mirror.Mirror {
	m := s.mirror.Load()
	if m == nil {
		writeError(w, http.StatusConflict, "no storage is configured")
	}
	return m
}

func (s *Server) handleStorageTest(w http.ResponseWriter, r *http.Request) {
	m := s.storageMirror(w)
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := m.Test(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "status": m.Status()})
}

func (s *Server) handleStorageSync(w http.ResponseWriter, r *http.Request) {
	m := s.storageMirror(w)
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := m.Sync(ctx); err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, mirror.ErrFenced) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]any{"error": mirror.Explain(err), "status": m.Status()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": m.Status()})
}

func (s *Server) handleStorageCredentials(w http.ResponseWriter, r *http.Request) {
	m := s.storageMirror(w)
	if m == nil {
		return
	}
	var body mirror.Credentials
	if !readJSON(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := m.UpdateCredentials(ctx, body); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, mirror.ErrEnvConfig) {
			code = http.StatusConflict
		}
		writeError(w, code, mirror.Explain(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": m.Status()})
}
