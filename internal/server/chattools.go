package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// The chat tools (internal/agent/chattools): the agents of the chats in one
// repository list, read and write to each other, whatever their harness and
// account. Their calls come in on a Unix socket of the server's own
// (agenthost.ToolsSocketPath), each with the token of the chat that makes
// it (chatToolsToken); a chat reaches only the chats of its repository
// (chatScope), and sends at most chatSendsPerHour messages an hour.

// chatSendsPerHour is how many messages a chat's agent may send to other
// chats in an hour: enough for any work two agents do together, and a stop
// to two that answer each other for ever.
const chatSendsPerHour = 40

// chatReadRunes is how much of one message read_chat gives.
const chatReadRunes = 4000

// chatToolServer serves the chat tools on the socket.
type chatToolServer struct {
	srv  *Server
	path string
	http *http.Server

	mu    sync.Mutex
	sends map[string][]time.Time
}

// startChatTools listens on the socket and serves the calls until close. A
// server that cannot listen goes on without the tools: the chats work, and
// a call says the other chats cannot be reached.
func (s *Server) startChatTools() *chatToolServer {
	t := &chatToolServer{srv: s, path: agenthost.ToolsSocketPath(s.dataDir), sends: map[string][]time.Time{}}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		log.Printf("chat tools: %v", err)
		return t
	}
	// A socket left by a server that did not close it is in the way.
	_ = os.Remove(t.path)
	ln, err := net.Listen("unix", t.path)
	if err != nil {
		log.Printf("chat tools: %v", err)
		return t
	}
	if err := os.Chmod(t.path, 0o600); err != nil {
		log.Printf("chat tools: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+chattools.Path, t.handle)
	mux.HandleFunc("POST "+mcpserver.EndpointPath, t.handleMCP)
	// A connection a client keeps between calls goes after a minute idle.
	t.http = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	go func() {
		if err := t.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("chat tools: %v", err)
		}
	}()
	return t
}

func (t *chatToolServer) close() {
	if t == nil || t.http == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.http.Shutdown(ctx)
}

// chatToolsObject is what a chat's token is sealed for.
func chatToolsObject(chatID string) string { return "chat-tools:" + chatID }

// chatToolsEndpoint is where a chat's tools reach the server, with its token:
// the chat's id, and the id sealed with the server's key, which only this
// server can make and open.
func (s *Server) chatToolsEndpoint(chatID string) (chattools.Endpoint, error) {
	keys, err := s.accounts.keys()
	if err != nil {
		return chattools.Endpoint{}, err
	}
	sealed, err := vault.Seal(keys.Key(), chatToolsObject(chatID), []byte(chatID))
	if err != nil {
		return chattools.Endpoint{}, err
	}
	return chattools.Endpoint{Socket: agenthost.ToolsSocketPath(s.dataDir),
		Token: chatID + "." + base64.RawURLEncoding.EncodeToString(sealed)}, nil
}

// chatOfToken is the chat a token names, "" for one this server did not make.
func (s *Server) chatOfToken(token string) string {
	id, sealed, ok := strings.Cut(token, ".")
	if !ok || !agenthost.ValidChatID(id) {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return ""
	}
	keys, err := s.accounts.keys()
	if err != nil {
		return ""
	}
	plain, err := vault.Open(keys.Key(), chatToolsObject(id), raw)
	if err != nil || string(plain) != id {
		return ""
	}
	return id
}

// chatScope is the repository a folder belongs to: the nearest folder at or
// above it with a .git, else the folder itself. Two chats with the same
// scope reach each other.
func chatScope(workdir string) string {
	dir := filepath.Clean(workdir)
	if workdir == "" {
		return ""
	}
	for at := dir; ; {
		if _, err := os.Stat(filepath.Join(at, ".git")); err == nil {
			return at
		}
		parent := filepath.Dir(at)
		if parent == at {
			return dir
		}
		at = parent
	}
}

func (t *chatToolServer) handle(w http.ResponseWriter, r *http.Request) {
	answer := func(code int, text string, isError bool) {
		writeJSON(w, code, chattools.Response{Text: text, Error: isError})
	}
	caller := t.srv.chatOfToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if caller == "" {
		answer(http.StatusUnauthorized, "This chat is not known to Socrates.", true)
		return
	}
	var req chattools.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		answer(http.StatusBadRequest, "The call could not be read.", true)
		return
	}
	row, err := t.srv.store.GetSession(caller)
	if err != nil || !row.IsChat() {
		answer(http.StatusOK, "This chat is gone.", true)
		return
	}
	// A test session has the test tools and no chat tools (testmode).
	if row.IsTest() {
		if t.srv.tests == nil {
			answer(http.StatusOK, "The test runner is not running.", true)
			return
		}
		text, isErr := t.srv.tests.Call(r.Context(), row.ID, req.Tool, req.Arguments)
		answer(http.StatusOK, text, isErr)
		return
	}
	var res chattools.Response
	switch req.Tool {
	case chattools.List:
		res = t.list(row)
	case chattools.Read:
		res = t.read(row, req.Arguments)
	case chattools.Send:
		res = t.send(r.Context(), row, req.Arguments)
	case chattools.TestsFormat, chattools.TestsCheck, chattools.RunTests, chattools.TestResults:
		res = t.srv.testChatCall(row, req.Tool, req.Arguments)
	default:
		res = chattools.Response{Text: "There is no tool " + req.Tool + ".", Error: true}
	}
	answer(http.StatusOK, res.Text, res.Error)
}

// peers is the other chats of a chat's repository that are at work (awake),
// newest first, and the repository.
func (t *chatToolServer) peers(row *store.Session) ([]*store.Session, string, error) {
	scope := chatScope(row.Workdir)
	rows, err := t.srv.store.ListSessions()
	if err != nil {
		return nil, scope, err
	}
	var out []*store.Session
	for i := range rows {
		other := rows[i]
		if other.ID == row.ID || !other.IsChat() || scope == "" || chatScope(other.Workdir) != scope || !t.awake(&other) {
			continue
		}
		out = append(out, &other)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, scope, nil
}

// awake says whether a chat is one the others see: not filed in the
// background or done, and started - a chat nobody has sent anything yet is
// on its folder's Not started shelf (sessionView.Fresh). The others neither
// list, read nor write to the rest, as if they were not there.
func (t *chatToolServer) awake(row *store.Session) bool {
	if row.Status == store.StatusBackground || row.Status == store.StatusDone {
		return false
	}
	p, err := t.srv.chats.peer(row.ID)
	return err == nil && p.summary().Asked
}

// peer is the chat a call names, when it is one of the caller's peers.
func (t *chatToolServer) peer(row *store.Session, id string) (*store.Session, *chatPeer, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil, errors.New("Name the chat: its id, as list_chats gives it.")
	}
	if id == row.ID {
		return nil, nil, errors.New("That is this chat.")
	}
	list, _, err := t.peers(row)
	if err != nil {
		return nil, nil, err
	}
	for _, other := range list {
		if other.ID == id {
			p, err := t.srv.chats.peer(id)
			if err != nil {
				return nil, nil, err
			}
			return other, p, nil
		}
	}
	return nil, nil, fmt.Errorf("There is no chat %q in this repository; list_chats names those there are.", id)
}

// chatName is how a chat is named to an agent: its title and its program.
func chatName(row *store.Session) string {
	return "“" + row.Title + "” (" + chatLabel(row.Harness) + ")"
}

func (t *chatToolServer) list(row *store.Session) chattools.Response {
	list, scope, err := t.peers(row)
	if err != nil {
		return chattools.Response{Text: err.Error(), Error: true}
	}
	if len(list) == 0 {
		return chattools.Response{Text: "No other chat works in this repository (" + scope + ")."}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d other chat(s) work in this repository (%s)", len(list), scope)
	// The chats of a repository share its working tree, so they are all on
	// the branch it has checked out.
	if branch := gitBranch(scope); branch != "" {
		fmt.Fprintf(&b, ", in the same working tree as this chat, on the branch %s", branch)
	}
	b.WriteString(":\n")
	for _, other := range list {
		state := "waiting"
		model := other.Model
		if p, err := t.srv.chats.peer(other.ID); err == nil {
			sum := p.summary()
			if sum.TurnRunning {
				state = "working"
			}
			if sum.Model != "" {
				model = sum.Model
			}
		}
		where := "."
		if rel, err := filepath.Rel(scope, other.Workdir); err == nil {
			where = rel
		}
		fmt.Fprintf(&b, "- id %s: %s", other.ID, chatName(other))
		if model != "" {
			fmt.Fprintf(&b, " on %s", model)
		}
		fmt.Fprintf(&b, ", %s, folder %s\n", state, where)
	}
	return chattools.Response{Text: b.String()}
}

// gitBranch is the branch a working tree has checked out, read from its
// HEAD rather than by running git, whose config the agents can write; ""
// when it is not a Git working tree or not on a branch.
func gitBranch(dir string) string {
	gitDir := filepath.Join(dir, ".git")
	if data, err := os.ReadFile(gitDir); err == nil {
		// A linked worktree's .git is a file naming its git directory.
		path, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return ""
		}
		gitDir = strings.TrimSpace(path)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
	if !ok {
		return ""
	}
	return ref
}

func (t *chatToolServer) read(row *store.Session, args json.RawMessage) chattools.Response {
	var a struct {
		Chat  string `json:"chat"`
		Count int    `json:"count"`
	}
	_ = json.Unmarshal(args, &a)
	other, p, err := t.peer(row, a.Chat)
	if err != nil {
		return chattools.Response{Text: err.Error(), Error: true}
	}
	count := a.Count
	if count <= 0 {
		count = chattools.DefaultRead
	}
	count = min(count, chattools.MaxRead)
	type line struct{ who, text string }
	var lines []line
	if err := p.view(func(proj *agent.Projection) {
		for i := len(proj.Messages) - 1; i >= 0 && len(lines) < count; i-- {
			m := proj.Messages[i]
			text := m.Text
			who := "The person"
			if m.Role != agent.RoleUser {
				who = "Its agent"
				var parts []string
				for _, part := range m.Parts {
					if part.Kind == agent.PartText && strings.TrimSpace(part.Text) != "" {
						parts = append(parts, part.Text)
					}
				}
				text = strings.Join(parts, "\n\n")
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			lines = append(lines, line{who, cutRunes(text, chatReadRunes)})
		}
	}); err != nil {
		return chattools.Response{Text: err.Error(), Error: true}
	}
	if len(lines) == 0 {
		return chattools.Response{Text: "The chat " + chatName(other) + " has no messages yet."}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The last %d message(s) of the chat %s, oldest first:\n", len(lines), chatName(other))
	for i := len(lines) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "\n%s:\n%s\n", lines[i].who, lines[i].text)
	}
	return chattools.Response{Text: b.String()}
}

// cutRunes is s cut to n runes, with an ellipsis when it was longer.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// allowSend counts a send of a chat against its hour, and says whether it
// may go.
func (t *chatToolServer) allowSend(chatID string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	recent := t.sends[chatID][:0]
	for _, at := range t.sends[chatID] {
		if now.Sub(at) < time.Hour {
			recent = append(recent, at)
		}
	}
	if len(recent) >= chatSendsPerHour {
		t.sends[chatID] = recent
		return false
	}
	t.sends[chatID] = append(recent, now)
	return true
}

func (t *chatToolServer) send(ctx context.Context, row *store.Session, args json.RawMessage) chattools.Response {
	var a struct {
		Chat string `json:"chat"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(args, &a)
	text := strings.TrimSpace(a.Text)
	if text == "" {
		return chattools.Response{Text: "The message is empty.", Error: true}
	}
	if len([]rune(text)) > chattools.MaxText {
		return chattools.Response{Text: fmt.Sprintf("The message is longer than %d characters.", chattools.MaxText), Error: true}
	}
	other, p, err := t.peer(row, a.Chat)
	if err != nil {
		return chattools.Response{Text: err.Error(), Error: true}
	}
	if !t.allowSend(row.ID, time.Now()) {
		return chattools.Response{Text: fmt.Sprintf("This chat has sent %d messages to other chats in the last hour, "+
			"the most it may. Tell the person instead, or go on without an answer.", chatSendsPerHour), Error: true}
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	body := &chatMessageBody{
		ClientID: "chat-tools-" + hex.EncodeToString(id[:]),
		Text: "[A message from the chat " + chatName(row) + ", id " + row.ID + ", sent with " + chattools.Send +
			". Answer it with " + chattools.Send + " to that id if it needs an answer.]\n\n" + text,
		Notice: agent.NoticeChatMessage,
		Reason: "A message from the chat " + chatName(row) + ".",
	}
	code, res := t.srv.sendMessage(ctx, p, body)
	switch code {
	case http.StatusOK, http.StatusAccepted:
		t.srv.events.poke()
		if p.summary().TurnRunning || code == http.StatusAccepted {
			return chattools.Response{Text: "Sent to " + chatName(other) + ". Its agent is busy and reads it when it is done."}
		}
		return chattools.Response{Text: "Sent to " + chatName(other) + ". Its agent is working on it now."}
	}
	msg, _ := res["error"].(string)
	if msg == "" {
		msg = http.StatusText(code)
	}
	return chattools.Response{Text: "The message could not be delivered: " + msg, Error: true}
}
