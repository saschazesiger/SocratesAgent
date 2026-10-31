package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/chattests"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/tests"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Chat tests (docs/design/CHAT-TESTS.md): a chat with Socrates Tests on
// writes and runs its own tests, kept in its folder (internal/chattests).
// The agent works them through the server's tools (internal/mcpserver/tests);
// the chat's page reads and changes them here, and every change of either
// is a chattests frame on /api/events.

// newChatTests is the store of every chat's tests, in the chat's folder:
// they go with the chat and are backed up with it.
func (s *Server) newChatTests() *chattests.Store {
	st := chattests.New(func(chat string) string { return agenthost.ChatDir(s.dataDir, chat) })
	st.SetOnChange(func(chat string) {
		rev, _ := st.Rev(chat)
		s.events.publish(map[string]any{"t": "chattests", "session": chat, "rev": rev})
	})
	return st
}

// chatTestsOf is the chat a request names, when it is one; it answers the
// request itself when not.
func (s *Server) chatTestsOf(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	row, _, ok := s.chatOf(w, r)
	return row, ok
}

// chatTest is the test a request names in the chat; it answers 404 itself
// when there is none.
func (s *Server) chatTest(w http.ResponseWriter, r *http.Request, chat string) (chattests.Test, bool) {
	t, err := s.chatTests.Get(chat, r.PathValue("test"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return chattests.Test{}, false
	}
	return t, true
}

// handleChatTests lists a chat's tests:
//
//	GET /api/chats/{id}/tests  {"tests": [...], "rev", "on"}
func (s *Server) handleChatTests(w http.ResponseWriter, r *http.Request) {
	row, ok := s.chatTestsOf(w, r)
	if !ok {
		return
	}
	list, err := s.chatTests.List(row.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rev, _ := s.chatTests.Rev(row.ID)
	writeJSON(w, http.StatusOK, map[string]any{"tests": chattests.Views(list, false), "rev": rev,
		"on": s.chatHasMCPOn(row, tests.Name)})
}

// handleChatTest is one test with every run:
//
//	GET /api/chats/{id}/tests/{test}
func (s *Server) handleChatTest(w http.ResponseWriter, r *http.Request) {
	row, ok := s.chatTestsOf(w, r)
	if !ok {
		return
	}
	if t, ok := s.chatTest(w, r, row.ID); ok {
		writeJSON(w, http.StatusOK, map[string]any{"test": chattests.View(t, true)})
	}
}

// chatTestPatch is the person's edit of a test; a field left out stays.
type chatTestPatch struct {
	Title       *string           `json:"title"`
	Goal        *string           `json:"goal"`
	Environment *string           `json:"environment"`
	Tags        *[]string         `json:"tags"`
	Steps       *[]chattests.Step `json:"steps"`
	Skip        *string           `json:"skip"`
}

// handleChatTestPatch is the person's edit of a test:
//
//	PATCH /api/chats/{id}/tests/{test}  {title?, goal?, environment?, tags?, steps?, skip?}
func (s *Server) handleChatTestPatch(w http.ResponseWriter, r *http.Request) {
	row, ok := s.chatTestsOf(w, r)
	if !ok {
		return
	}
	t, ok := s.chatTest(w, r, row.ID)
	if !ok {
		return
	}
	var body chatTestPatch
	if !readJSON(w, r, &body) {
		return
	}
	t, err := s.chatTests.Update(row.ID, t.ID, chattests.Patch{Title: body.Title, Goal: body.Goal,
		Environment: body.Environment, Tags: body.Tags, Steps: body.Steps, Skip: body.Skip}, chattests.ByPerson)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"test": chattests.View(t, true)})
}

// handleChatTestOverride sets the person's status of a test, or with an
// empty status removes it:
//
//	POST /api/chats/{id}/tests/{test}/override  {status, note}
func (s *Server) handleChatTestOverride(w http.ResponseWriter, r *http.Request) {
	row, ok := s.chatTestsOf(w, r)
	if !ok {
		return
	}
	t, ok := s.chatTest(w, r, row.ID)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	t, err := s.chatTests.SetOverride(row.ID, t.ID, body.Status, body.Note)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"test": chattests.View(t, true)})
}

// handleChatTestDelete deletes a test with its runs and files:
//
//	DELETE /api/chats/{id}/tests/{test}
func (s *Server) handleChatTestDelete(w http.ResponseWriter, r *http.Request) {
	row, ok := s.chatTestsOf(w, r)
	if !ok {
		return
	}
	t, ok := s.chatTest(w, r, row.ID)
	if !ok {
		return
	}
	if err := s.chatTests.Delete(row.ID, t.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChatTestRerun asks the chat's agent to run a test again: a message
// of the person's, sent as the composer sends one (client_id makes a second
// press of the same request one message).
//
//	POST /api/chats/{id}/tests/{test}/rerun  {client_id?}
func (s *Server) handleChatTestRerun(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	t, ok := s.chatTest(w, r, row.ID)
	if !ok {
		return
	}
	var body struct {
		ClientID string `json:"client_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	code, answer := s.sendMessage(r.Context(), p, &chatMessageBody{ClientID: body.ClientID, Text: rerunText(t)})
	writeJSON(w, code, answer)
}

// rerunText is the person's message that asks for a run of t again.
func rerunText(t chattests.Test) string {
	text := fmt.Sprintf("Run test %s “%s” again.", t.ID, t.Title)
	if o := t.Override; o != nil {
		text += fmt.Sprintf(" I had set it to %s", chattests.StatusLabel(o.Status))
		if note := strings.TrimSpace(o.Note); note != "" {
			text += ": " + note
		} else {
			text += "."
		}
	}
	return text
}

// chatTestFileMax is the largest evidence file served (the store takes
// none larger).
const chatTestFileMax = chattests.MaxFileSize

// handleChatTestFile serves an evidence file of a chat's test. Images and
// videos go as what they are; every other file as plain text, or as a
// download when it is not text, and never as a page of this origin.
//
//	GET /api/chats/{id}/tests/{test}/files/{file...}
func (s *Server) handleChatTestFile(w http.ResponseWriter, r *http.Request) {
	row, ok := s.chatTestsOf(w, r)
	if !ok {
		return
	}
	path, err := s.chatTests.FilePath(row.ID, r.PathValue("test"), r.PathValue("file"))
	if err != nil {
		writeError(w, http.StatusNotFound, "the test has no such file")
		return
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > chatTestFileMax {
		writeError(w, http.StatusNotFound, "the file is no longer there")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "the file is no longer there")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		writeError(w, http.StatusNotFound, "the file is no longer there")
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "the file could not be read")
		return
	}
	sniffed := http.DetectContentType(head[:n])
	byName := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	switch {
	case strings.HasPrefix(sniffed, "image/") && sniffed != "image/svg+xml":
		w.Header().Set("Content-Type", sniffed)
	case strings.HasPrefix(byName, "video/"):
		w.Header().Set("Content-Type", byName)
	case strings.HasPrefix(sniffed, "text/"):
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(filepath.Base(path), "\"", "")+"\"")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// forgetTestMode deletes what test mode, which chat tests replaced, left
// behind: its sessions (cells, reviewers and analysts of test runs) with
// their hosts and folders, and its runs, runtime and installed tools. It
// runs at every start and finds nothing after the first.
func (s *Server) forgetTestMode(ctx context.Context) {
	ids, err := s.store.TestModeSessions()
	if err != nil {
		log.Printf("tests: could not read test mode's sessions: %v", err)
	}
	// A host that still runs is stopped, which may take seconds each:
	// several at once.
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, id := range ids {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			s.chats.remove(ctx, id)
			if err := s.store.DeleteSession(id); err != nil {
				log.Printf("tests: could not delete test mode's session %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	if len(ids) > 0 {
		log.Printf("tests: deleted %d sessions of test mode", len(ids))
	}
	layout := paths.New(s.dataDir)
	for _, rel := range []string{paths.TestMode, paths.TestModeRuntime, paths.TestModeCache} {
		dir := layout.Path(rel)
		if _, err := os.Lstat(dir); err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("tests: could not delete test mode's %s: %v", dir, err)
			continue
		}
		log.Printf("tests: deleted test mode's %s", dir)
	}
}
