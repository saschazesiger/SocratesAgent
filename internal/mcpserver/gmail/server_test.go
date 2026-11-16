package gmail

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

// fakeGmail is enough of Gmail's IMAP server for the tools: a German
// account (its All Mail is "[Gmail]/Alle Nachrichten"), messages with
// Gmail's ids and labels, and every command it was sent.
type fakeGmail struct {
	t        *testing.T
	password string

	mu       sync.Mutex
	msgs     []*fakeMsg
	nextUID  map[string]uint32
	logins   int
	commands []string
}

type fakeMsg struct {
	uid    map[string]uint32 // by box
	msgID  uint64
	thrID  uint64
	labels []string
	flags  []string
	raw    string
}

const (
	fakeAll    = "[Gmail]/Alle Nachrichten"
	fakeDrafts = "[Gmail]/Entwürfe"
	fakeTrash  = "[Gmail]/Papierkorb"
)

func newFakeGmail(t *testing.T) *fakeGmail {
	f := &fakeGmail{t: t, password: "abcdefghijklmnop", nextUID: map[string]uint32{}}
	f.add(0x1a00000000000001, 0x1a00000000000001, []string{`\Inbox`}, []string{`\Seen`},
		"From: Anna <anna@example.com>\r\nTo: janis@example.com\r\nSubject: Rechnung November\r\n"+
			"Date: Mon, 2 Nov 2026 10:00:00 +0100\r\nMessage-ID: <r1@example.com>\r\n"+
			"Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
			"Hier die Rechnung.\r\n--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=rechnung.pdf\r\n"+
			"Content-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--b--\r\n")
	f.add(0x1a00000000000002, 0x1a00000000000002, []string{`\Inbox`, "Antworten?"}, nil,
		"From: Shop <noreply@shop.example>\r\nReply-To: help@shop.example\r\nTo: janis@example.com\r\nSubject: Bitte bestätigen\r\n"+
			"Date: Tue, 3 Nov 2026 09:00:00 +0000\r\nMessage-ID: <s1@shop.example>\r\nContent-Type: text/html; charset=utf-8\r\n\r\n"+
			`<p>Bitte <a href="https://shop.example/ok">hier</a> bestätigen.</p>`+"\r\n")
	f.add(0x1a00000000000003, 0x1a00000000000002, []string{`\Sent`}, []string{`\Seen`},
		"From: janis@example.com\r\nTo: help@shop.example\r\nSubject: Re: Bitte bestätigen\r\n"+
			"Date: Tue, 3 Nov 2026 11:00:00 +0000\r\nMessage-ID: <j1@example.com>\r\nIn-Reply-To: <s1@shop.example>\r\n\r\nErledigt.\r\n")
	return f
}

func (f *fakeGmail) add(msgID, thrID uint64, labels, flags []string, raw string) *fakeMsg {
	m := &fakeMsg{uid: map[string]uint32{}, msgID: msgID, thrID: thrID, labels: labels, flags: flags, raw: raw}
	f.place(m, fakeAll)
	f.msgs = append(f.msgs, m)
	return m
}

func (f *fakeGmail) place(m *fakeMsg, box string) {
	f.nextUID[box]++
	m.uid[box] = f.nextUID[box]
}

func (f *fakeGmail) dial(ctx context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	go f.serve(server)
	return client, nil
}

func (f *fakeGmail) serve(nc net.Conn) {
	defer nc.Close()
	r, w := bufio.NewReader(nc), bufio.NewWriter(nc)
	out := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\r\n", args...)
	}
	out("* OK Gimap ready")
	w.Flush()
	selected := ""
	for {
		text, lits, err := f.readCommand(r, w)
		if err != nil {
			return
		}
		tag, rest, _ := strings.Cut(text, " ")
		p := &parser{s: rest, lits: lits}
		args, err := p.list(0)
		if err != nil || len(args) == 0 {
			out("%s BAD %v", tag, err)
			w.Flush()
			continue
		}
		cmd := strings.ToUpper(asString(args[0]))
		args = args[1:]
		if cmd == "UID" {
			cmd += " " + strings.ToUpper(asString(args[0]))
			args = args[1:]
		}
		f.mu.Lock()
		f.commands = append(f.commands, cmd+" "+fmt.Sprint(args))
		switch cmd {
		case "LOGIN":
			if asString(args[1]) != f.password {
				out("%s NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)", tag)
				break
			}
			f.logins++
			out("%s OK janis@example.com authenticated (Success)", tag)
		case "CAPABILITY":
			out("* CAPABILITY IMAP4rev1 UNSELECT IDLE NAMESPACE QUOTA ID XLIST CHILDREN X-GM-EXT-1 UIDPLUS ENABLE MOVE UTF8=ACCEPT SPECIAL-USE")
			out("%s OK Thats all she wrote!", tag)
		case "ENABLE":
			out("* ENABLED UTF8=ACCEPT")
			out("%s OK Success", tag)
		case "LIST":
			out(`* LIST (\HasNoChildren) "/" "INBOX"`)
			out(`* LIST (\HasNoChildren) "/" "Antworten?"`)
			out(`* LIST (\HasChildren \Noselect) "/" "[Gmail]"`)
			out(`* LIST (\All \HasNoChildren) "/" "%s"`, fakeAll)
			out(`* LIST (\Drafts \HasNoChildren) "/" "%s"`, fakeDrafts)
			out(`* LIST (\HasNoChildren \Sent) "/" "[Gmail]/Gesendet"`)
			out(`* LIST (\HasNoChildren \Junk) "/" "[Gmail]/Spam"`)
			out(`* LIST (\HasNoChildren \Trash) "/" "%s"`, fakeTrash)
			out("%s OK Success", tag)
		case "SELECT", "EXAMINE":
			selected = asString(args[0])
			out("* %d EXISTS", len(f.inBox(selected)))
			out("%s OK [READ-WRITE] %s selected. (Success)", tag, selected)
		case "STATUS":
			n, unseen := 0, 0
			for _, m := range f.msgs {
				if slices.Contains(m.labels, `\Inbox`) {
					n++
					if !slices.Contains(m.flags, `\Seen`) {
						unseen++
					}
				}
			}
			out(`* STATUS "INBOX" (MESSAGES %d UNSEEN %d)`, n, unseen)
			out("%s OK Success", tag)
		case "UID SEARCH":
			var uids []string
			key := strings.ToUpper(asString(args[0]))
			for _, m := range f.inBox(selected) {
				match := false
				switch key {
				case "X-GM-RAW":
					q := asString(args[1])
					if q == "in:inbox" {
						match = slices.Contains(m.labels, `\Inbox`)
					} else {
						match = strings.Contains(strings.ToLower(m.raw), strings.ToLower(q))
					}
				case "X-GM-MSGID":
					match = strconv.FormatUint(m.msgID, 10) == asString(args[1])
				case "X-GM-THRID":
					match = strconv.FormatUint(m.thrID, 10) == asString(args[1])
				}
				if match {
					uids = append(uids, strconv.Itoa(int(m.uid[selected])))
				}
			}
			out("* SEARCH %s", strings.Join(uids, " "))
			out("%s OK SEARCH completed (Success)", tag)
		case "UID FETCH":
			full := strings.Contains(fmt.Sprint(args[1]), "BODY.PEEK[]")
			for i, m := range f.byUIDs(selected, asString(args[0])) {
				body, item := m.raw, "BODY[]"
				if !full {
					body, _, _ = strings.Cut(m.raw, "\r\n\r\n")
					body += "\r\n\r\n"
					item = "BODY[HEADER.FIELDS (DATE FROM TO CC SUBJECT MESSAGE-ID REFERENCES REPLY-TO)]"
				}
				var labels []string
				for _, l := range m.labels {
					labels = append(labels, strconv.Quote(l))
				}
				fmt.Fprintf(w, "* %d FETCH (X-GM-THRID %d X-GM-MSGID %d X-GM-LABELS (%s) UID %d FLAGS (%s) INTERNALDATE \"03-Nov-2026 09:00:00 +0000\" RFC822.SIZE %d %s {%d}\r\n%s)\r\n",
					i+1, m.thrID, m.msgID, strings.Join(labels, " "), m.uid[selected], strings.Join(m.flags, " "), len(m.raw), item, len(body), body)
			}
			out("%s OK Success", tag)
		case "UID STORE":
			item := strings.TrimSuffix(strings.ToUpper(asString(args[1])), ".SILENT")
			vals := asStrings(args[2])
			for _, m := range f.byUIDs(selected, asString(args[0])) {
				switch item {
				case "+X-GM-LABELS":
					m.labels = append(m.labels, vals...)
				case "-X-GM-LABELS":
					m.labels = slices.DeleteFunc(m.labels, func(l string) bool { return slices.Contains(vals, l) })
				case "+FLAGS":
					m.flags = append(m.flags, vals...)
				case "-FLAGS":
					m.flags = slices.DeleteFunc(m.flags, func(l string) bool { return slices.Contains(vals, l) })
				}
			}
			out("%s OK Success", tag)
		case "APPEND":
			id := uint64(0x1a00000000000100 + len(f.msgs))
			raw := asString(args[2])
			thr := id
			if m := regexp.MustCompile(`In-Reply-To: (\S+)`).FindStringSubmatch(raw); m != nil {
				for _, o := range f.msgs {
					if strings.Contains(o.raw, "Message-ID: "+m[1]) {
						thr = o.thrID
					}
				}
			}
			m := f.add(id, thr, []string{`\Draft`}, asStrings(args[1]), raw)
			f.place(m, asString(args[0]))
			out("%s OK [APPENDUID 1 %d] (Success)", tag, m.uid[asString(args[0])])
		case "LOGOUT":
			out("* BYE LOGOUT Requested")
			out("%s OK 73 good day (Success)", tag)
			f.mu.Unlock()
			w.Flush()
			return
		default:
			out("%s BAD unknown command", tag)
		}
		f.mu.Unlock()
		w.Flush()
	}
}

// readCommand reads a command line with its literals, each a NUL in text.
func (f *fakeGmail) readCommand(r *bufio.Reader, w *bufio.Writer) (string, [][]byte, error) {
	var text []byte
	var lits [][]byte
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", nil, err
		}
		b := []byte(strings.TrimRight(line, "\r\n"))
		n, at := literalTail(b)
		if n < 0 {
			return string(append(text, b...)), lits, nil
		}
		w.WriteString("+ go ahead\r\n")
		w.Flush()
		buf := make([]byte, n)
		if _, err := ioReadFull(r, buf); err != nil {
			return "", nil, err
		}
		text = append(append(text, b[:at]...), 0)
		lits = append(lits, buf)
	}
}

func ioReadFull(r *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (f *fakeGmail) inBox(box string) []*fakeMsg {
	var out []*fakeMsg
	for _, m := range f.msgs {
		if _, ok := m.uid[box]; ok {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeGmail) byUIDs(box, set string) []*fakeMsg {
	var out []*fakeMsg
	for _, s := range strings.Split(set, ",") {
		for _, m := range f.inBox(box) {
			if strconv.Itoa(int(m.uid[box])) == s {
				out = append(out, m)
			}
		}
	}
	return out
}

func (f *fakeGmail) msg(id uint64) *fakeMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.msgs {
		if m.msgID == id {
			return m
		}
	}
	return nil
}

func (f *fakeGmail) sent(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.commands {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

type gmailEnv struct {
	t    *testing.T
	fake *fakeGmail
	srv  *Server
	sess mcpserver.Session
	data string
}

func newGmailEnv(t *testing.T) *gmailEnv {
	fake := newFakeGmail(t)
	data := t.TempDir()
	account := mcpserver.Account{ID: "a1", Values: map[string]string{"email": "janis@example.com", "password": "abcd efgh ijkl mnop"}}
	srv := New(Deps{DataDir: data, Dial: fake.dial, Accounts: func() []mcpserver.Account { return []mcpserver.Account{account} }})
	return &gmailEnv{t: t, fake: fake, srv: srv, sess: mcpserver.Session{ID: "chat1", Workdir: t.TempDir()}, data: data}
}

// call calls a tool and fails the test when it fails, or when it does not
// fail but wantErr.
func (e *gmailEnv) call(tool, args string, wantErr bool) string {
	e.t.Helper()
	r := e.srv.Call(context.Background(), e.sess, tool, json.RawMessage(args))
	text := r.Content[0].Text
	if r.IsError != wantErr {
		e.t.Fatalf("%s %s: error %v, want %v:\n%s", tool, args, r.IsError, wantErr, text)
	}
	return text
}

func must(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Fatalf("no %q in:\n%s", w, text)
		}
	}
}

func TestCheckAccount(t *testing.T) {
	e := newGmailEnv(t)
	c := e.srv.CheckAccount(context.Background(), map[string]string{"email": "janis@example.com", "password": "abcd efgh ijkl mnop"})
	if c.State != mcpserver.AccountValid || !strings.Contains(c.Detail, "2 messages, 1 unread") {
		t.Fatalf("%+v", c)
	}
	c = e.srv.CheckAccount(context.Background(), map[string]string{"email": "janis@example.com", "password": "wrong"})
	if c.State != mcpserver.AccountInvalid {
		t.Fatalf("%+v", c)
	}
	if got := e.srv.AccountLabel(map[string]string{"email": " Janis@Example.com "}); got != "janis@example.com" {
		t.Fatal(got)
	}
}

func TestSearchAndRead(t *testing.T) {
	e := newGmailEnv(t)
	out := e.call(Search, `{"query":"in:inbox"}`, false)
	must(t, out, "2 messages match", "Bitte bestätigen\n  id 1a00000000000002 · thread 1a00000000000002", "INBOX, Antworten?, UNREAD")
	if strings.Index(out, "Bitte bestätigen") > strings.Index(out, "Rechnung November") {
		t.Fatalf("not newest first:\n%s", out)
	}
	must(t, e.call(Search, `{"query":"in:inbox","max":1}`, false), "2 messages match", "the newest 1")
	must(t, e.call(Search, `{"query":"nothing like this"}`, false), "No message")

	out = e.call(Read, `{"id":"1a00000000000002"}`, false)
	must(t, out, "# Bitte bestätigen", "Reply-To: help@shop.example", "Bitte hier (https://shop.example/ok) bestätigen.",
		"authuser=janis@example.com#all/1a00000000000002")
	if slices.Contains(e.fake.msg(0x1a00000000000002).flags, `\Seen`) {
		t.Fatal("reading marked the message read")
	}
	must(t, e.call(Read, `{"id":"1a00000000000001"}`, false), "Hier die Rechnung.", "1. rechnung.pdf (application/pdf, 9 bytes)")
	must(t, e.call(Read, `{"id":"ffff"}`, true), "there is no message ffff")
	must(t, e.call(Read, `{"id":"not hex"}`, true), "is not a message id")
	must(t, e.call(Read, `{"id":"1a00000000000001","extra":1}`, true), `unknown field "extra"`)

	if n := len(e.fake.sent("LOGIN")); n != 1 {
		t.Fatalf("%d sign-ins for one account's calls, want 1 (the connection is kept)", n)
	}
}

func TestThread(t *testing.T) {
	e := newGmailEnv(t)
	for _, id := range []string{"1a00000000000002", "1a00000000000003"} {
		out := e.call(Thread, `{"id":"`+id+`"}`, false)
		must(t, out, "A conversation of 2 messages", "# Bitte bestätigen", "# Re: Bitte bestätigen", "Erledigt.", "SENT")
		if strings.Index(out, "# Bitte") > strings.Index(out, "# Re:") {
			t.Fatalf("not oldest first:\n%s", out)
		}
	}
}

func TestAttachment(t *testing.T) {
	e := newGmailEnv(t)
	out := e.call(Attachment, `{"id":"1a00000000000001","attachment":"1"}`, false)
	path := filepath.Join(e.data, "mcp", "gmail", "chats", "chat1", "1a00000000000001", "rechnung.pdf")
	must(t, out, path)
	if b, err := os.ReadFile(path); err != nil || string(b) != "%PDF-1.4\n" {
		t.Fatalf("%q %v", b, err)
	}
	must(t, e.call(Attachment, `{"id":"1a00000000000001","attachment":"other.pdf"}`, true), "1. rechnung.pdf")
	must(t, e.call(Attachment, `{"id":"1a00000000000002"}`, true), "has no attachments")
	if what := e.srv.End(context.Background(), "chat1"); what == "" {
		t.Fatal("End removed nothing")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the attachment outlived its chat")
	}
}

func TestDraft(t *testing.T) {
	e := newGmailEnv(t)
	if err := os.WriteFile(filepath.Join(e.sess.Workdir, "angebot.txt"), []byte("Angebot"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := e.call(Draft, `{"reply_to":"1a00000000000002","body":"Danke, erledigt.","attachments":["angebot.txt"]}`, false)
	must(t, out, "not sent", "thread 1a00000000000002", "To: <help@shop.example>", "Subject: Re: Bitte bestätigen", "1 attachments")
	appends := e.fake.sent("APPEND")
	if len(appends) != 1 || !strings.Contains(appends[0], fakeDrafts) || !strings.Contains(appends[0], `\Draft`) {
		t.Fatalf("appends %v", appends)
	}
	var d *fakeMsg
	for _, m := range e.fake.msgs {
		if _, ok := m.uid[fakeDrafts]; ok {
			d = m
		}
	}
	must(t, d.raw, "In-Reply-To: <s1@shop.example>", "References: <s1@shop.example>", "From: <janis@example.com>", "filename=angebot.txt", "Content-Type: text/plain; charset=utf-8; name=angebot.txt")

	must(t, e.call(Draft, `{"to":"x@example.com","subject":"Hallo","body":"Text"}`, false), "Subject: Hallo")
	must(t, e.call(Draft, `{"to":"not an address","body":"x"}`, true), "not a list of addresses")
	must(t, e.call(Draft, `{"to":"x@example.com","body":"x","attachments":["missing.txt"]}`, true), "cannot be read")
	if len(e.fake.sent("UID STORE")) != 0 {
		t.Fatal("a draft changed messages")
	}
}

func TestModify(t *testing.T) {
	e := newGmailEnv(t)
	out := e.call(Modify, `{"ids":["1a00000000000001"],"archive":true,"read":false,"starred":true,"add_labels":["Rechnung"]}`, false)
	must(t, out, "Changed 1 messages", "Rechnung", "UNREAD", "STARRED")
	m := e.fake.msg(0x1a00000000000001)
	if slices.Contains(m.labels, `\Inbox`) || !slices.Contains(m.labels, "Rechnung") || slices.Contains(m.flags, `\Seen`) || !slices.Contains(m.flags, `\Flagged`) {
		t.Fatalf("labels %v flags %v", m.labels, m.flags)
	}
	e.call(Modify, `{"ids":["1a00000000000001"],"add_labels":["INBOX"],"read":true}`, false)
	if !slices.Contains(m.labels, `\Inbox`) || !slices.Contains(m.flags, `\Seen`) {
		t.Fatalf("labels %v flags %v", m.labels, m.flags)
	}
	for _, l := range []string{"TRASH", `\Trash`, "SPAM", fakeTrash, "[Gmail]/Gesendet", "DRAFT"} {
		must(t, e.call(Modify, `{"ids":["1a00000000000001"],"add_labels":[`+strconv.Quote(l)+`]}`, true), "cannot be set")
	}
	must(t, e.call(Modify, `{"ids":["1a00000000000001"],"remove_labels":["[Gmail]/Spam"]}`, true), "cannot be taken away")
	must(t, e.call(Modify, `{"ids":["1a00000000000001"]}`, true), "nothing to change")
	must(t, e.call(Modify, `{"ids":["ffff"],"read":true}`, true), "there is no message ffff")
	for _, c := range e.fake.sent("UID STORE") {
		if strings.Contains(c, "Trash") || strings.Contains(c, "Papierkorb") || strings.Contains(c, "Deleted") {
			t.Fatalf("an agent trashed a message: %s", c)
		}
	}
}

func TestLabels(t *testing.T) {
	e := newGmailEnv(t)
	out := e.call(Labels, `{}`, false)
	must(t, out, "the inbox has 2 messages, 1 unread", fakeAll+" (All)", fakeTrash+" (Trash)", "- Antworten?")
	if strings.Contains(out, "- [Gmail]\n") {
		t.Fatalf("a folder that only groups labels is listed:\n%s", out)
	}
}

func TestAccounts(t *testing.T) {
	e := newGmailEnv(t)
	e.srv.deps.Accounts = func() []mcpserver.Account { return nil }
	must(t, e.call(Search, `{"query":"x"}`, true), "no Gmail account is set up")
	e = newGmailEnv(t)
	must(t, e.call(Search, `{"query":"x","account":"other@example.com"}`, true), "there is no Gmail account other@example.com; there is janis@example.com")
	must(t, e.call(Search, `{"query":"in:inbox","account":"Janis@example.com"}`, false), "2 messages")
	e.fake.password = "changed"
	e.srv.idle = map[string]*idleBox{}
	must(t, e.call(Search, `{"query":"x"}`, true), "Gmail refused the app password of janis@example.com")
	must(t, e.call("send", `{}`, true), "Gmail has no tool send")
}

func TestToolsAreDescribed(t *testing.T) {
	s := New(Deps{})
	var names []string
	for _, tool := range s.Tools() {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.Schema["type"] != "object" {
			t.Fatalf("%s is not described", tool.Name)
		}
	}
	if strings.Join(names, " ") != "search read thread labels attachment draft modify" {
		t.Fatal(names)
	}
	if slices.Contains(names, "send") {
		t.Fatal("an agent can send mail")
	}
	if !mcpserver.NameRE.MatchString(s.Name()) {
		t.Fatal(s.Name())
	}
}
