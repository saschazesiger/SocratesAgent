package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/saschazesiger/SocratesAgent/internal/testmode"
)

// The Tests page's API (docs/design/TEST-MODE.md, "API"). Every route is
// behind the login; the runner holds the state.

func (s *Server) testRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /tests", s.page("tests.html"))
	mux.HandleFunc("GET /api/tests/repos", s.auth(s.handleTestRepos))
	mux.HandleFunc("POST /api/tests/repos", s.auth(s.handleAddTestRepo))
	mux.HandleFunc("DELETE /api/tests/repos/{repo}", s.auth(s.handleRemoveTestRepo))
	mux.HandleFunc("GET /api/tests/repos/{repo}/catalog", s.auth(s.handleTestCatalog))
	mux.HandleFunc("GET /api/tests/repos/{repo}/settings", s.auth(s.handleTestSettings))
	mux.HandleFunc("PUT /api/tests/repos/{repo}/settings", s.auth(s.handlePutTestSettings))
	mux.HandleFunc("POST /api/tests/repos/{repo}/tools/{tool}/check", s.auth(s.handleTestToolCheck))
	mux.HandleFunc("GET /api/tests/repos/{repo}/runs", s.auth(s.handleTestRuns))
	mux.HandleFunc("POST /api/tests/repos/{repo}/runs", s.auth(s.handleStartTestRun))
	mux.HandleFunc("GET /api/tests/repos/{repo}/history", s.auth(s.handleTestHistory))
	mux.HandleFunc("GET /api/tests/repos/{repo}/search", s.auth(s.handleTestSearch))
	mux.HandleFunc("GET /api/tests/active", s.auth(s.handleActiveTestRuns))
	mux.HandleFunc("GET /api/tests/global", s.auth(s.handleTestGlobal))
	mux.HandleFunc("PUT /api/tests/global", s.auth(s.handlePutTestGlobal))
	mux.HandleFunc("GET /api/tests/runs/{run}", s.auth(s.handleTestRun))
	mux.HandleFunc("DELETE /api/tests/runs/{run}", s.auth(s.handleDeleteTestRun))
	mux.HandleFunc("GET /api/tests/runs/{run}/cells", s.auth(s.handleTestCells))
	mux.HandleFunc("GET /api/tests/runs/{run}/bugs", s.auth(s.handleTestBugs))
	mux.HandleFunc("POST /api/tests/runs/{run}/stop", s.auth(s.handleTestRunAction(func(r *testmode.Runner, id string) error { return r.StopRun(id) })))
	mux.HandleFunc("POST /api/tests/runs/{run}/analyse", s.auth(s.handleTestRunAction(func(r *testmode.Runner, id string) error { return r.Analyse(id) })))
	mux.HandleFunc("POST /api/tests/runs/{run}/resume", s.auth(s.handleTestRunAction(func(r *testmode.Runner, id string) error { return r.ResumeRun(id) })))
	mux.HandleFunc("POST /api/tests/runs/{run}/pin", s.auth(s.handlePinTestRun))
	mux.HandleFunc("POST /api/tests/runs/{run}/retry", s.auth(s.handleRetryTestCell))
	mux.HandleFunc("GET /api/tests/runs/{run}/attempts/{attempt}", s.auth(s.handleTestAttempt))
	mux.HandleFunc("GET /api/tests/runs/{run}/attempts/{attempt}/files/{name...}", s.auth(s.handleTestFile))
}

// testRunner is the runner, or a 503 when it is not running.
func (s *Server) testRunner(w http.ResponseWriter) *testmode.Runner {
	if s.tests == nil {
		writeError(w, http.StatusServiceUnavailable, "the test runner is not running")
		return nil
	}
	return s.tests
}

// testError answers an error of the runner.
func testError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, testmode.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, testmode.ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// testRepo is the repository the path names, or a 404.
func (s *Server) testRepo(w http.ResponseWriter, r *http.Request) (*testmode.Runner, *testmode.Repo) {
	t := s.testRunner(w)
	if t == nil {
		return nil, nil
	}
	repo := t.Repo(r.PathValue("repo"))
	if repo == nil {
		writeError(w, http.StatusNotFound, "no such repository")
		return nil, nil
	}
	return t, repo
}

func (s *Server) handleTestRepos(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	t.Discover(s.testFolders())
	writeJSON(w, http.StatusOK, map[string]any{"repos": nonNil(t.Repos()), "global": t.Global()})
}

func (s *Server) handleAddTestRepo(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	repo, err := t.AddRepo(body.Path)
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

func (s *Server) handleRemoveTestRepo(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	if err := t.RemoveRepo(r.PathValue("repo")); err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestCatalog(w http.ResponseWriter, r *http.Request) {
	t, repo := s.testRepo(w, r)
	if repo == nil {
		return
	}
	v, err := t.Catalog(repo.Key)
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleTestSettings(w http.ResponseWriter, r *http.Request) {
	t, repo := s.testRepo(w, r)
	if repo == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": t.Settings(repo.Key), "defaults": testmode.DefaultSettings()})
}

func (s *Server) handlePutTestSettings(w http.ResponseWriter, r *http.Request) {
	t, repo := s.testRepo(w, r)
	if repo == nil {
		return
	}
	var body testmode.Settings
	if !readJSON(w, r, &body) {
		return
	}
	if msg := s.checkTestSettings(&body); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	saved, err := t.SetSettings(repo.Key, body)
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": saved})
}

// checkTestSettings refuses a role on a harness that is not offered, and
// accounts that are not the harness's.
func (s *Server) checkTestSettings(set *testmode.Settings) string {
	settings := s.Settings()
	for _, role := range []struct {
		name string
		r    *testmode.Role
	}{{"cell agent", &set.Cell}, {"reviewer", &set.Reviewer}, {"analyst", &set.Analyst}} {
		if strings.TrimSpace(role.r.Harness) == "" {
			continue
		}
		if refused := checkChatHarness(role.r.Harness, settings); refused != nil {
			return "the " + role.name + ": " + refused.msg
		}
		for _, id := range role.r.Accounts {
			a, err := s.store.GetAccount(id)
			if err != nil || a.Agent != role.r.Harness {
				return "the " + role.name + ": the account " + id + " is not an account of " + role.r.Harness
			}
		}
	}
	return ""
}

func (s *Server) handleTestToolCheck(w http.ResponseWriter, r *http.Request) {
	t, repo := s.testRepo(w, r)
	if repo == nil {
		return
	}
	v, err := t.CheckSecrets(repo.Key, r.PathValue("tool"))
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleTestRuns(w http.ResponseWriter, r *http.Request) {
	t, repo := s.testRepo(w, r)
	if repo == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": nonNil(t.Runs(repo.Key))})
}

func (s *Server) handleStartTestRun(w http.ResponseWriter, r *http.Request) {
	t, repo := s.testRepo(w, r)
	if repo == nil {
		return
	}
	var body struct {
		Name      string             `json:"name"`
		Selection testmode.Selection `json:"selection"`
		Settings  *testmode.Settings `json:"settings"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Settings != nil {
		if msg := s.checkTestSettings(body.Settings); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
	}
	run, err := t.StartRun(repo.Key, body.Name, body.Selection, body.Settings)
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) handleTestHistory(w http.ResponseWriter, r *http.Request) {
	t, repo := s.testRepo(w, r)
	if repo == nil {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": t.History(repo.Key, limit, r.URL.Query().Get("cell"))})
}

func (s *Server) handleTestSearch(w http.ResponseWriter, r *http.Request) {
	t, repo := s.testRepo(w, r)
	if repo == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": nonNil(t.Search(repo.Key, r.URL.Query().Get("q"), 200))})
}

func (s *Server) handleActiveTestRuns(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": nonNil(t.Active())})
}

func (s *Server) handleTestGlobal(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	writeJSON(w, http.StatusOK, t.Global())
}

func (s *Server) handlePutTestGlobal(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	var body testmode.Global
	if !readJSON(w, r, &body) {
		return
	}
	g, err := t.SetGlobal(body)
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleTestRun(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	v, err := t.RunDetail(r.PathValue("run"))
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteTestRun(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	if err := t.DeleteRun(r.PathValue("run")); err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestCells(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	cells, err := t.Cells(r.PathValue("run"))
	if err != nil {
		testError(w, err)
		return
	}
	writeJSONCompressed(w, r, http.StatusOK, map[string]any{"cells": cells})
}

func (s *Server) handleTestBugs(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	bugs, md, err := t.Bugs(r.PathValue("run"))
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bugs": bugs, "conclusions": md})
}

func (s *Server) handleTestRunAction(do func(*testmode.Runner, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := s.testRunner(w)
		if t == nil {
			return
		}
		if err := do(t, r.PathValue("run")); err != nil {
			testError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handlePinTestRun(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	var body struct {
		Pinned bool `json:"pinned"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := t.PinRun(r.PathValue("run"), body.Pinned); err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRetryTestCell(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	var body struct {
		Cell string `json:"cell"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	a, err := t.RetryCell(r.PathValue("run"), body.Cell)
	if err != nil {
		testError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleTestAttempt(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	v, err := t.AttemptDetail(r.PathValue("run"), r.PathValue("attempt"))
	if err != nil {
		testError(w, err)
		return
	}
	writeJSONCompressed(w, r, http.StatusOK, v)
}

// handleTestFile serves a file of an attempt's evidence: resolved by the
// runner inside the attempt's folder, a regular file only, looked at again
// once open (as handleChatPartImage does), never sniffed into HTML.
func (s *Server) handleTestFile(w http.ResponseWriter, r *http.Request) {
	t := s.testRunner(w)
	if t == nil {
		return
	}
	path, redact, err := t.EvidencePath(r.PathValue("run"), r.PathValue("attempt"), r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > testmode.MaxEvidenceFile {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	head := make([]byte, 512)
	n, _ := f.Read(head)
	if _, err := f.Seek(0, 0); err != nil {
		writeError(w, http.StatusInternalServerError, "the file could not be read")
		return
	}
	mime := http.DetectContentType(head[:n])
	text := false
	switch {
	case strings.HasPrefix(mime, "image/"):
	case strings.HasPrefix(mime, "text/") || mime == "application/octet-stream":
		// Logs and the like are shown as plain text, never as a page.
		mime, text = "text/plain; charset=utf-8", true
	default:
		w.Header().Set("Content-Disposition", "attachment")
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
	if text {
		// A text file of a cell that still runs is not redacted on disk
		// yet: it is redacted on its way out, and never cached.
		raw, err := io.ReadAll(io.LimitReader(f, testmode.MaxEvidenceFile))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "the file could not be read")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(redact(raw))
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// nonNil is a list as JSON: [] rather than null.
func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}
