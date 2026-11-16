package gmail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A Gmail account over IMAP: Gmail shows every label as a mailbox, and one
// of them (special-use \All, "[Gmail]/All Mail" in English, named in the
// account's language) holds every message but spam and trash. Searching,
// reading and labelling all happen there; a message is known by Gmail's
// X-GM-MSGID and its conversation by X-GM-THRID, the same ids the web app
// and the Gmail API show in hex.

// dialFunc opens a connection to the IMAP server.
type dialFunc func(ctx context.Context) (net.Conn, error)

// gmailIMAP is Gmail's IMAP server.
const gmailIMAP = "imap.gmail.com:993"

// box is a mailbox (a label) of the account.
type box struct {
	Name string
	// Special is its special use (RFC 6154): \All, \Drafts, \Sent, \Trash,
	// \Junk, \Flagged, \Important; "" for none.
	Special string
	// NoSelect is whether it only groups labels below it.
	NoSelect bool
}

// mailbox is a signed-in account.
type mailbox struct {
	c        *imapConn
	email    string
	boxes    []box
	selected string
	writable bool
}

// summary is what a list of messages shows of one.
type summary struct {
	UID                   uint32
	MsgID, ThrID          uint64
	Labels, Flags         []string
	Date                  time.Time
	From, To, Cc, Subject string
	MessageID, References string
	ReplyTo               string
	// RawFrom, RawTo and RawReplyTo are the headers as written (encoded
	// words and all), which addresses are read from: a decoded name may
	// have a comma in it.
	RawFrom, RawTo, RawReplyTo string
	Size                       int
}

// fetched is a message fetched: its summary and, when asked for, the whole
// message as it was received.
type fetched struct {
	summary
	Raw []byte
}

// openMailbox signs in to an account and reads its labels.
func openMailbox(ctx context.Context, dial dialFunc, email, password string) (*mailbox, error) {
	nc, err := dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("Gmail could not be reached: %w", err)
	}
	c, err := newConn(ctx, nc)
	if err != nil {
		return nil, err
	}
	m := &mailbox{c: c, email: email}
	ok := false
	defer func() {
		if !ok {
			c.close()
		}
	}()
	if _, err := c.run(ctx, "LOGIN", c.str(email), c.str(strings.ReplaceAll(password, " ", ""))); err != nil {
		var refused *imapError
		if errors.As(err, &refused) && refused.Kind == "NO" {
			return nil, &loginRefused{refused}
		}
		return nil, err
	}
	rs, err := c.run(ctx, "CAPABILITY")
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if r.Kind == "CAPABILITY" {
			for _, v := range r.Values {
				c.caps[strings.ToUpper(asString(v))] = true
			}
		}
	}
	if !c.caps["X-GM-EXT-1"] {
		return nil, errors.New("the mail server is not Gmail's (it has no X-GM-EXT-1)")
	}
	if c.caps["UTF8=ACCEPT"] {
		if _, err := c.run(ctx, "ENABLE", "UTF8=ACCEPT"); err == nil {
			c.utf8 = true
		}
	}
	if err := m.list(ctx); err != nil {
		return nil, err
	}
	if m.special(`\All`) == "" {
		return nil, errors.New(`Gmail shows no "All Mail" over IMAP: switch it on in Gmail's settings, Labels, "Show in IMAP"`)
	}
	ok = true
	return m, nil
}

func (m *mailbox) close() { m.c.close() }

// list reads the account's mailboxes.
func (m *mailbox) list(ctx context.Context) error {
	rs, err := m.c.run(ctx, "LIST", `""`, `"*"`)
	if err != nil {
		return err
	}
	m.boxes = nil
	for _, r := range rs {
		if r.Kind != "LIST" || len(r.Values) < 3 {
			continue
		}
		b := box{Name: asString(r.Values[2])}
		for _, a := range asStrings(r.Values[0]) {
			switch strings.ToLower(a) {
			case `\all`, `\drafts`, `\sent`, `\trash`, `\junk`, `\flagged`, `\important`:
				b.Special = `\` + strings.ToUpper(a[1:2]) + strings.ToLower(a[2:])
			case `\noselect`, `\nonexistent`:
				b.NoSelect = true
			}
		}
		m.boxes = append(m.boxes, b)
	}
	return nil
}

// special is the name of the mailbox of a special use, "" for none.
func (m *mailbox) special(use string) string {
	for _, b := range m.boxes {
		if b.Special == use {
			return b.Name
		}
	}
	return ""
}

// selectBox opens a mailbox, read-only unless write.
func (m *mailbox) selectBox(ctx context.Context, name string, write bool) error {
	if m.selected == name && (m.writable || !write) {
		return nil
	}
	cmd := "EXAMINE"
	if write {
		cmd = "SELECT"
	}
	m.selected = ""
	if _, err := m.c.run(ctx, cmd, m.c.str(name)); err != nil {
		return err
	}
	m.selected, m.writable = name, write
	return nil
}

// search is the UIDs in All Mail that a Gmail search finds, oldest first.
func (m *mailbox) search(ctx context.Context, query string) ([]uint32, error) {
	if err := m.selectBox(ctx, m.special(`\All`), false); err != nil {
		return nil, err
	}
	parts := []any{"UID SEARCH"}
	arg := m.c.str(query)
	if _, isLit := arg.(literal); isLit && !m.c.utf8 {
		parts = append(parts, "CHARSET UTF-8")
	}
	parts = append(parts, "X-GM-RAW", arg)
	return m.searchUIDs(ctx, parts...)
}

// uidsOf is the UIDs in a mailbox (All Mail when "") of a message
// (X-GM-MSGID) or a conversation (X-GM-THRID).
func (m *mailbox) uidsOf(ctx context.Context, boxName, key string, id uint64) ([]uint32, error) {
	if boxName == "" {
		boxName = m.special(`\All`)
	}
	if err := m.selectBox(ctx, boxName, false); err != nil {
		return nil, err
	}
	return m.searchUIDs(ctx, "UID SEARCH", key, strconv.FormatUint(id, 10))
}

func (m *mailbox) searchUIDs(ctx context.Context, parts ...any) ([]uint32, error) {
	rs, err := m.c.run(ctx, parts...)
	if err != nil {
		return nil, err
	}
	var out []uint32
	for _, r := range rs {
		if r.Kind != "SEARCH" {
			continue
		}
		for _, v := range r.Values {
			if n, err := strconv.ParseUint(asString(v), 10, 32); err == nil {
				out = append(out, uint32(n))
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// fetch reads messages of the selected mailbox, in the order of uids; full
// reads each whole message, else only the headers a list shows.
func (m *mailbox) fetch(ctx context.Context, uids []uint32, full bool) ([]fetched, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	body := "BODY.PEEK[HEADER.FIELDS (DATE FROM TO CC SUBJECT MESSAGE-ID REFERENCES REPLY-TO)]"
	if full {
		body = "BODY.PEEK[]"
	}
	rs, err := m.c.run(ctx, "UID FETCH", uidSet(uids),
		"(UID X-GM-MSGID X-GM-THRID X-GM-LABELS FLAGS INTERNALDATE RFC822.SIZE "+body+")")
	if err != nil {
		return nil, err
	}
	byUID := map[uint32]fetched{}
	for _, r := range rs {
		if r.Kind != "FETCH" {
			continue
		}
		items := fetchItems(r)
		uid := uint32(uintOf(items["UID"]))
		if uid == 0 {
			continue
		}
		f := byUID[uid]
		f.UID = uid
		if v, ok := items["X-GM-MSGID"]; ok {
			f.MsgID = uintOf(v)
		}
		if v, ok := items["X-GM-THRID"]; ok {
			f.ThrID = uintOf(v)
		}
		if v, ok := items["X-GM-LABELS"]; ok {
			f.Labels = asStrings(v)
		}
		if v, ok := items["FLAGS"]; ok {
			f.Flags = asStrings(v)
		}
		if v, ok := items["RFC822.SIZE"]; ok {
			f.Size = int(uintOf(v))
		}
		if v, ok := items["INTERNALDATE"]; ok && f.Date.IsZero() {
			if t, err := time.Parse("2-Jan-2006 15:04:05 -0700", strings.TrimSpace(asString(v))); err == nil {
				f.Date = t
			}
		}
		if raw := bodyItem(items); raw != "" {
			if full {
				f.Raw = []byte(raw)
			}
			f.headers([]byte(raw))
		}
		byUID[uid] = f
	}
	out := make([]fetched, 0, len(uids))
	for _, u := range uids {
		if f, ok := byUID[u]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// headers fills in the summary from a message's header.
func (s *summary) headers(raw []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		raw = raw[:i+4]
	} else if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		raw = raw[:i+2]
	} else {
		raw = append(slices.Clone(raw), "\r\n\r\n"...)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return
	}
	h := msg.Header
	if t, err := mail.ParseDate(h.Get("Date")); err == nil {
		s.Date = t
	}
	s.RawFrom, s.RawTo, s.RawReplyTo = h.Get("From"), h.Get("To"), h.Get("Reply-To")
	s.From = decodeHeader(h.Get("From"))
	s.To = decodeHeader(h.Get("To"))
	s.Cc = decodeHeader(h.Get("Cc"))
	s.ReplyTo = decodeHeader(h.Get("Reply-To"))
	s.Subject = decodeHeader(h.Get("Subject"))
	s.MessageID = strings.TrimSpace(h.Get("Message-Id"))
	s.References = strings.Join(strings.Fields(h.Get("References")), " ")
}

// systemFlag is a flag or system label, which goes as an atom (\Seen).
var systemFlag = regexp.MustCompile(`^\\[A-Za-z]+$`)

// store changes labels or flags of messages in All Mail: item is
// +X-GM-LABELS, -X-GM-LABELS, +FLAGS or -FLAGS.
func (m *mailbox) store(ctx context.Context, uids []uint32, item string, values []string) error {
	if len(uids) == 0 || len(values) == 0 {
		return nil
	}
	if err := m.selectBox(ctx, m.special(`\All`), true); err != nil {
		return err
	}
	var list []string
	for _, v := range values {
		if systemFlag.MatchString(v) {
			list = append(list, v)
			continue
		}
		arg, ok := m.c.str(v).(string)
		if !ok {
			return fmt.Errorf("the label %q cannot be written over IMAP", v)
		}
		list = append(list, arg)
	}
	_, err := m.c.run(ctx, "UID STORE", uidSet(uids), item+".SILENT", "("+strings.Join(list, " ")+")")
	return err
}

// appendDraft puts a message into Drafts and answers its Gmail ids.
func (m *mailbox) appendDraft(ctx context.Context, msg []byte) (msgID, thrID uint64, err error) {
	drafts := m.special(`\Drafts`)
	if drafts == "" {
		return 0, 0, errors.New("Gmail shows no Drafts over IMAP")
	}
	rs, err := m.c.run(ctx, "APPEND", m.c.str(drafts), `(\Draft \Seen)`, literal(msg))
	if err != nil {
		return 0, 0, err
	}
	// The tagged OK carries [APPENDUID <validity> <uid>] (UIDPLUS).
	var uid uint64
	if last := rs[len(rs)-1]; last.Kind == "OK" {
		if f := strings.Fields(strings.Trim(strings.SplitN(last.Text, "]", 2)[0], "[")); len(f) == 3 && strings.EqualFold(f[0], "APPENDUID") {
			uid, _ = strconv.ParseUint(f[2], 10, 32)
		}
	}
	if uid == 0 {
		return 0, 0, nil
	}
	if err := m.selectBox(ctx, drafts, false); err != nil {
		return 0, 0, nil
	}
	got, err := m.fetch(ctx, []uint32{uint32(uid)}, false)
	if err != nil || len(got) == 0 {
		return 0, 0, nil
	}
	return got[0].MsgID, got[0].ThrID, nil
}

// inboxStatus is how many messages the inbox has, and unread.
func (m *mailbox) inboxStatus(ctx context.Context) (total, unread int, err error) {
	rs, err := m.c.run(ctx, "STATUS", "INBOX", "(MESSAGES UNSEEN)")
	if err != nil {
		return 0, 0, err
	}
	for _, r := range rs {
		if r.Kind != "STATUS" || len(r.Values) < 2 {
			continue
		}
		l, _ := r.Values[1].([]any)
		for i := 0; i+1 < len(l); i += 2 {
			switch strings.ToUpper(asString(l[i])) {
			case "MESSAGES":
				total = int(uintOf(l[i+1]))
			case "UNSEEN":
				unread = int(uintOf(l[i+1]))
			}
		}
	}
	return total, unread, nil
}
