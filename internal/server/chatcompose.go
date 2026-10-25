package server

// What the chat view's composer and header send besides messages: a model
// change, a slash command, and the files a message carries. Each is a command
// with a client_id, sent exactly once through the chat's outbox like a message
// (chats.go), except the upload, which is a plain file write that answers the
// paths a message may then name.
//
//	POST /api/chats/{id}/model         {"client_id","model","effort"}       as messages
//	POST /api/chats/{id}/command       {"client_id","name","args"}          as messages;
//	                                   409 {"code":"unsupported"} for a command the agent does not have
//	POST /api/chats/{id}/attachments   multipart, one or more "file" parts  {"files":[attachment]}

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// attachmentsDir is where a chat's files are kept, inside its own directory:
// with the journal, backed up with it, and removed with the chat.
const attachmentsDir = "attachments"

// chatAttachments is the attachments directory of one chat.
func (s *Server) chatAttachments(id string) string {
	return filepath.Join(agenthost.ChatDir(s.dataDir, id), attachmentsDir)
}

// chatLabel is the name a chat's agent goes by.
func chatLabel(harness string) string {
	if h, ok := harnesses.Get(harness); ok {
		return h.Label()
	}
	if harness == agent.FakeHarness {
		return "Scripted agent"
	}
	return harness
}

func (s *Server) handleChatModel(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		ClientID string `json:"client_id"`
		Model    string `json:"model"`
		Effort   string `json:"effort"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.ClientID, body.Model = strings.TrimSpace(body.ClientID), strings.TrimSpace(body.Model)
	if body.ClientID == "" || body.Model == "" {
		writeError(w, http.StatusBadRequest, "a model change needs a client_id and a model")
		return
	}
	code, answer := p.command(r.Context(), agenthost.Command{Op: agenthost.OpSetModel, ClientID: body.ClientID,
		Model: body.Model, Effort: strings.TrimSpace(body.Effort)})
	writeJSON(w, code, answer)
}

func (s *Server) handleChatCommand(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		ClientID string `json:"client_id"`
		Name     string `json:"name"`
		Args     string `json:"args"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.ClientID = strings.TrimSpace(body.ClientID)
	body.Name = strings.TrimPrefix(strings.TrimSpace(body.Name), "/")
	if body.ClientID == "" || body.Name == "" {
		writeError(w, http.StatusBadRequest, "a command needs a client_id and a name")
		return
	}
	code, answer := p.command(r.Context(), agenthost.Command{Op: agenthost.OpCommand, ClientID: body.ClientID,
		Name: body.Name, Args: body.Args})
	writeJSON(w, code, answer)
}

// handleChatAttachments keeps the files of a message that is being written.
// Each one is stored in the chat's attachments directory under a name of the
// server's choosing, and a copy goes into the working directory's uploads
// folder, where the agent finds it beside the code. The answer is what the
// message names them by: a message may only carry attachments this route
// handed out (checkAttachments).
func (s *Server) handleChatAttachments(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	dir := s.chatAttachments(row.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, uploadMax)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart form")
		return
	}
	var files []agent.Attachment
	fail := func(code int, msg string) {
		for _, f := range files {
			os.Remove(f.Path)
		}
		writeError(w, code, msg)
	}
	now := time.Now()
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				fail(http.StatusRequestEntityTooLarge, "that is more than 64 MB")
				return
			}
			fail(http.StatusBadRequest, "the upload was cut off")
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		if len(files) == uploadFilesMax {
			part.Close()
			fail(http.StatusBadRequest, "at most 20 files at once")
			return
		}
		name, f, err := freeName(dir, uploadName(part.FileName(), now))
		if err != nil {
			part.Close()
			fail(http.StatusInternalServerError, err.Error())
			return
		}
		path := filepath.Join(dir, name)
		n, err := io.Copy(f, part)
		part.Close()
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		files = append(files, agent.Attachment{Name: name, Path: path, Size: n, Mime: mimeOf(name, part.Header.Get("Content-Type"))})
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				fail(http.StatusRequestEntityTooLarge, "that is more than 64 MB")
				return
			}
			fail(http.StatusInternalServerError, err.Error())
			return
		}
	}
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "there was no file in the upload")
		return
	}
	// The copy beside the code is a convenience for the agent; a working
	// directory that cannot take it (gone, read-only) leaves the file where
	// the message names it.
	if info, err := os.Stat(row.Workdir); err == nil && info.IsDir() {
		if up, err := prepareUploadDir(row.Workdir); err == nil {
			for _, f := range files {
				copyFile(f.Path, filepath.Join(up, f.Name))
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// mimeOf is a file's type by its name, else what the browser said it was.
func mimeOf(name, said string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	if said != "" {
		return said
	}
	return "application/octet-stream"
}

// copyFile copies src to dst unless dst exists.
func copyFile(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return
	}
	out.Close()
}

// checkAttachments holds a message's attachments to the files the upload
// route handed out for this chat: a path anywhere else on the disk is
// refused, whatever the browser says, and the name, type and size are taken
// from the file itself.
func (s *Server) checkAttachments(chatID string, list []agent.Attachment) (int, string) {
	dir := s.chatAttachments(chatID)
	for i, a := range list {
		path := filepath.Clean(a.Path)
		if a.Path == "" || filepath.Dir(path) != dir {
			return http.StatusBadRequest, "an attachment has to be uploaded to this chat first"
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return http.StatusBadRequest, "an attachment of this message is not there any more"
		}
		list[i] = agent.Attachment{Name: filepath.Base(path), Path: path, Size: info.Size(), Mime: mimeOf(path, a.Mime)}
	}
	return 0, ""
}

// chatAgent says whether a new session of this harness is a chat rather than
// a terminal: every agent with an adapter is one (REBUILD.md §1), and the
// three coding agents have no terminal of their own any more.
func chatAgent(harness string) bool {
	_, adapter := agent.Lookup(harness)
	return adapter
}

// chatAgents is every harness a new session is a chat with, sorted: what the
// catalogue tells the sheet.
func chatAgents() []string {
	out := []string{}
	for _, harness := range agent.Harnesses() {
		if chatAgent(harness) {
			out = append(out, harness)
		}
	}
	return out
}

// chatModel is the model and effort a new chat runs on: the ones asked for,
// else the dashboard's defaults for its agent (an effort asked for without a
// model goes with the default model).
func chatModel(settings config.Settings, harness, model, effort string) (string, string) {
	model, effort = strings.TrimSpace(model), strings.TrimSpace(effort)
	if model != "" {
		return model, effort
	}
	defaultModel, defaultEffort := settings.Harnesses.Defaults(harness)
	if effort == "" {
		effort = defaultEffort
	}
	return defaultModel, effort
}

// checkChatHarness is the rule a chat's agent is held to: it has an adapter,
// and the dashboard has not switched it off.
func checkChatHarness(harness string, settings config.Settings) *launchError {
	if _, ok := agent.Lookup(harness); !ok {
		return refuse(http.StatusBadRequest, "there is no chat for %s yet", quoteOr(harness, "no harness"))
	}
	if entry, known := settings.Harnesses.Entry(harness); known && !entry.Enabled {
		return refuse(http.StatusBadRequest, "%s is switched off in the dashboard", harness)
	}
	return nil
}

// chatUltracode is whether a chat runs with Ultracode: asked for, and Claude
// Code's. Another agent has no such option and does not carry it along.
func chatUltracode(harness string, on bool) bool {
	return on && harness == string(harnesses.KindClaude)
}

// handleChatOptions changes a started chat's session options: Ultracode, for
// a Claude Code chat. It is a launch setting of the CLI, so the agent is
// parked as soon as it is idle and the next message resumes the conversation
// with it - nothing that runs is cut short.
//
//	POST /api/chats/{id}/options   {"ultracode": bool}   {"session":…}
func (s *Server) handleChatOptions(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Ultracode *bool `json:"ultracode"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Ultracode == nil {
		writeError(w, http.StatusBadRequest, "there is no option to change")
		return
	}
	if *body.Ultracode && !chatUltracode(row.Harness, true) {
		writeError(w, http.StatusBadRequest, "Ultracode is an option of Claude Code chats")
		return
	}
	changed, err := s.setChatOption(row.ID, "ultracode", *body.Ultracode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if changed {
		s.events.poke()
		s.chats.park(p.id)
	}
	s.answerWithSession(w, row.ID)
}
