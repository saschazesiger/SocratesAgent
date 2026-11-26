package gmail

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP is enough of Gmail's SMTP server to send: AUTH PLAIN with the
// app password, the envelope, DATA. A message it takes goes into the fake
// mailbox under Sent, in the conversation its In-Reply-To names, as Gmail
// files it.
type fakeSMTP struct {
	gmail    *fakeGmail
	password string
	// refuseRcpt is an address RCPT TO refuses.
	refuseRcpt string
	// drop ends the connection after a message's data, without an answer;
	// lose does not file it either (Gmail did not take it).
	drop, lose bool

	mu   sync.Mutex
	sent []sentMail
}

type sentMail struct {
	from string
	rcpt []string
	data string
}

func (f *fakeSMTP) dial(ctx context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	go f.serve(server)
	return client, nil
}

func (f *fakeSMTP) serve(nc net.Conn) {
	defer nc.Close()
	r, w := bufio.NewReader(nc), bufio.NewWriter(nc)
	out := func(line string) {
		w.WriteString(line + "\r\n")
		w.Flush()
	}
	out("220 smtp.gmail.com ESMTP ready")
	authed := false
	var m sentMail
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "EHLO", "HELO":
			out("250-smtp.gmail.com at your service")
			out("250-AUTH LOGIN PLAIN XOAUTH2")
			out("250 8BITMIME")
		case "AUTH":
			mech, resp, _ := strings.Cut(arg, " ")
			raw, _ := base64.StdEncoding.DecodeString(resp)
			parts := strings.Split(string(raw), "\x00")
			if mech != "PLAIN" || len(parts) != 3 || parts[1] != "janis@example.com" || parts[2] != f.password {
				out("535-5.7.8 Username and Password not accepted.")
				out("535 5.7.8 https://support.google.com/mail/?p=BadCredentials")
				continue
			}
			authed = true
			out("235 2.7.0 Accepted")
		case "MAIL":
			if !authed {
				out("530 5.7.0 Authentication Required")
				continue
			}
			from, _, _ := strings.Cut(strings.TrimPrefix(arg, "FROM:<"), ">")
			m = sentMail{from: from}
			out("250 2.1.0 OK")
		case "RCPT":
			to, _, _ := strings.Cut(strings.TrimPrefix(arg, "TO:<"), ">")
			if to == f.refuseRcpt {
				out("553 5.1.3 The recipient address is not a valid RFC 5321 address.")
				continue
			}
			m.rcpt = append(m.rcpt, to)
			out("250 2.1.5 OK")
		case "DATA":
			out("354 Go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			m.data = b.String()
			if f.lose {
				return
			}
			f.mu.Lock()
			f.sent = append(f.sent, m)
			f.mu.Unlock()
			f.file(m.data)
			if f.drop {
				return
			}
			out("250 2.0.0 OK")
		case "QUIT":
			out("221 2.0.0 closing connection")
			return
		default:
			out("502 5.5.1 Unrecognized command.")
		}
	}
}

// file puts a sent message into the fake mailbox, as Gmail does.
func (f *fakeSMTP) file(raw string) {
	g := f.gmail
	g.mu.Lock()
	defer g.mu.Unlock()
	id := uint64(0x1a00000000000200 + len(g.msgs))
	thr := id
	if m := regexp.MustCompile(`In-Reply-To: (\S+)`).FindStringSubmatch(raw); m != nil {
		for _, o := range g.msgs {
			if strings.Contains(o.raw, "Message-ID: "+m[1]) {
				thr = o.thrID
			}
		}
	}
	g.add(id, thr, []string{`\Sent`}, []string{`\Seen`}, raw)
}

func (f *fakeSMTP) mails() []sentMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func newSendEnv(t *testing.T) (*gmailEnv, *fakeSMTP) {
	e := newGmailEnv(t)
	smtp := &fakeSMTP{gmail: e.fake, password: "abcdefghijklmnop"}
	e.srv.deps.DialSMTP = smtp.dial
	return e, smtp
}

func TestSendReply(t *testing.T) {
	e, smtp := newSendEnv(t)
	if err := os.WriteFile(filepath.Join(e.sess.Workdir, "angebot.txt"), []byte("Angebot"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := e.call(Send, `{"reply_to":"1a00000000000002","body":"Danke, erledigt.","bcc":"chef@example.com","attachments":["angebot.txt"]}`, false)
	must(t, out, "Sent from janis@example.com", "thread 1a00000000000002", "To: <help@shop.example>",
		"Bcc: <chef@example.com>", "Subject: Re: Bitte bestätigen", "1 attachments")
	mails := smtp.mails()
	if len(mails) != 1 {
		t.Fatalf("%d mails sent", len(mails))
	}
	m := mails[0]
	if m.from != "janis@example.com" || strings.Join(m.rcpt, " ") != "help@shop.example chef@example.com" {
		t.Fatalf("envelope %q → %v", m.from, m.rcpt)
	}
	must(t, m.data, "In-Reply-To: <s1@shop.example>", "References: <s1@shop.example>", "From: <janis@example.com>",
		"To: <help@shop.example>", "filename=angebot.txt", "Danke, erledigt.")
	if strings.Contains(m.data, "chef@example.com") || strings.Contains(m.data, "Bcc:") {
		t.Fatalf("the blind copy is in the message:\n%s", m.data)
	}
	if len(e.fake.sent("APPEND")) != 0 || len(e.fake.sent("UID STORE")) != 0 {
		t.Fatal("sending wrote into the mailbox")
	}
}

func TestSendNew(t *testing.T) {
	e, smtp := newSendEnv(t)
	out := e.call(Send, `{"to":"Anna Muster <anna@example.com>, bob@example.com","cc":"anna@example.com","subject":"Grüße","body":"Hallo"}`, false)
	must(t, out, "Sent from janis@example.com", "Subject: Grüße")
	m := smtp.mails()[0]
	if strings.Join(m.rcpt, " ") != "anna@example.com bob@example.com" {
		t.Fatalf("recipients %v, want each once", m.rcpt)
	}
	must(t, m.data, `To: "Anna Muster" <anna@example.com>, <bob@example.com>`, "Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=")
}

func TestSendRefused(t *testing.T) {
	e, smtp := newSendEnv(t)
	must(t, e.call(Send, `{"body":"x"}`, true), "no recipient")
	must(t, e.call(Send, `{"to":"x@example.com","body":" "}`, true), "the message is empty")
	must(t, e.call(Send, `{"to":"not an address","body":"x"}`, true), "not a list of addresses")
	many := make([]string, maxRecipients+1)
	for i := range many {
		many[i] = fmt.Sprintf("p%d@example.com", i)
	}
	must(t, e.call(Send, `{"to":"`+strings.Join(many, ",")+`","body":"x"}`, true), "at most 50 recipients")
	must(t, e.call(Send, `{"to":"x@example.com","body":"x","attachments":["missing.txt"]}`, true), "cannot be read")
	smtp.refuseRcpt = "bad@example.com"
	must(t, e.call(Send, `{"to":"x@example.com, bad@example.com","body":"x"}`, true), "refused the recipient bad@example.com", "nothing was sent")
	smtp.password = "changed"
	must(t, e.call(Send, `{"to":"x@example.com","body":"x"}`, true), "Gmail refused to send with the app password of janis@example.com", "nothing was sent")
	if n := len(smtp.mails()); n != 0 {
		t.Fatalf("%d mails sent", n)
	}
}

func TestLongReferencesAreFolded(t *testing.T) {
	var refs []string
	for i := range 40 {
		refs = append(refs, fmt.Sprintf("<message-%02d.with.a.long.id@mail.example.com>", i))
	}
	raw, err := draft{From: "janis@example.com", To: "x@example.com", Body: "x", References: strings.Join(refs, " ")}.build(newGmailEnv(t).srv.now())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(raw), "\r\n") {
		if len(l) > 998 {
			t.Fatalf("a line of %d characters", len(l))
		}
	}
	var s summary
	s.headers(raw)
	if s.References != strings.Join(refs, " ") {
		t.Fatalf("References read back as %q", s.References)
	}
}

func TestSendWithoutAnswer(t *testing.T) {
	// The connection breaks after the data: in Sent, it was sent.
	e, smtp := newSendEnv(t)
	smtp.drop = true
	must(t, e.call(Send, `{"to":"x@example.com","subject":"Hallo","body":"x"}`, false), "Sent from janis@example.com", "To: <x@example.com>")
	// Not in Sent: not known, and the agent is told not to send it again.
	e, smtp = newSendEnv(t)
	smtp.lose = true
	must(t, e.call(Send, `{"to":"x@example.com","body":"x"}`, true), "not known whether the message was sent", "Do not send it again", "rfc822msgid:")
}

func TestLongAddressListsAreFolded(t *testing.T) {
	e, smtp := newSendEnv(t)
	many := make([]string, maxRecipients)
	for i := range many {
		many[i] = fmt.Sprintf("Jürg Müller-Nummer %d <person.number.%d@example.com>", i, i)
	}
	out := e.call(Send, `{"to":"`+strings.Join(many, ", ")+`","body":"x"}`, false)
	if strings.Contains(out, "\r") {
		t.Fatalf("the answer has a folded header:\n%s", out)
	}
	m := smtp.mails()[0]
	if len(m.rcpt) != maxRecipients {
		t.Fatalf("%d recipients", len(m.rcpt))
	}
	for _, l := range strings.Split(m.data, "\r\n") {
		if len(l) > 998 {
			t.Fatalf("a line of %d characters", len(l))
		}
	}
	var s summary
	s.headers([]byte(m.data))
	if !strings.Contains(s.To, "Jürg Müller-Nummer 49 <person.number.49@example.com>") {
		t.Fatalf("To read back as %q", s.To)
	}
}
