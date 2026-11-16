package gmail

import (
	"strings"
	"testing"
	"time"
)

func TestParseResponse(t *testing.T) {
	r, err := parseResponse("* 12 FETCH (UID 7 X-GM-MSGID 1878 X-GM-LABELS (\"\\\\Inbox\" Rechnung \"Antwort benötigt\") FLAGS (\\Seen) BODY[HEADER.FIELDS (FROM SUBJECT)] \x00)",
		[][]byte{[]byte("From: a@b\r\nSubject: hi\r\n\r\n")})
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 12 || r.Kind != "FETCH" {
		t.Fatalf("num %d kind %s", r.Num, r.Kind)
	}
	items := fetchItems(r)
	if uintOf(items["UID"]) != 7 || uintOf(items["X-GM-MSGID"]) != 1878 {
		t.Fatalf("items %v", items)
	}
	if got := strings.Join(asStrings(items["X-GM-LABELS"]), "|"); got != `\Inbox|Rechnung|Antwort benötigt` {
		t.Fatalf("labels %q", got)
	}
	if !strings.Contains(bodyItem(items), "Subject: hi") {
		t.Fatalf("body %q", bodyItem(items))
	}
	r, err = parseResponse(`S3 NO [AUTHENTICATIONFAILED] Invalid credentials (Failure "x`, nil)
	if err != nil || r.Kind != "NO" || !strings.Contains(r.Text, "AUTHENTICATIONFAILED") {
		t.Fatalf("%+v %v", r, err)
	}
	if !authFailed(&imapError{Kind: r.Kind, Text: r.Text}) {
		t.Fatal("not a failed login")
	}
	r, err = parseResponse(`* LIST (\HasNoChildren \All) "/" "[Gmail]/Alle Nachrichten"`, nil)
	if err != nil || asString(r.Values[2]) != "[Gmail]/Alle Nachrichten" || asStrings(r.Values[0])[1] != `\All` {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := parseResponse(`* LIST (\All "/" x`, nil); err == nil {
		t.Fatal("an unclosed list was read")
	}
	if n, at := literalTail([]byte(`* 1 FETCH (BODY[] {120}`)); n != 120 || at != 18 {
		t.Fatalf("literal %d at %d", n, at)
	}
	if n, _ := literalTail([]byte(`* OK done`)); n != -1 {
		t.Fatal("a literal where there is none")
	}
}

func TestQuote(t *testing.T) {
	c := &imapConn{}
	if got := c.str(`a "b" \c`); got != `"a \"b\" \\c"` {
		t.Fatalf("%v", got)
	}
	if _, ok := c.str("Grüsse").(literal); !ok {
		t.Fatal("non-ASCII is quoted without UTF8=ACCEPT")
	}
	c.utf8 = true
	if got := c.str("Grüsse"); got != `"Grüsse"` {
		t.Fatalf("%v", got)
	}
	if _, ok := c.str("a\r\nb").(literal); !ok {
		t.Fatal("a line end is quoted")
	}
}

const multipartMail = "From: =?utf-8?q?J=C3=BCrg?= <jurg@example.com>\r\n" +
	"To: janis@example.com\r\n" +
	"Subject: =?iso-8859-1?q?Gr=FCsse?=\r\n" +
	"Content-Type: multipart/mixed; boundary=\"outer\"\r\n" +
	"\r\n" +
	"--outer\r\n" +
	"Content-Type: multipart/alternative; boundary=\"inner\"\r\n" +
	"\r\n" +
	"--inner\r\n" +
	"Content-Type: text/plain; charset=windows-1252\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Preis: 5 =80, sch=F6n\r\n" +
	"--inner\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>HTML</p>\r\n" +
	"--inner--\r\n" +
	"--outer\r\n" +
	"Content-Type: application/pdf; name=\"rechnung.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"../../rechnung.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"JVBERi0x\r\nLjQK\r\n" +
	"--outer--\r\n"

func TestParseMessage(t *testing.T) {
	m, err := parseMessage([]byte(multipartMail))
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "Preis: 5 €, schön" {
		t.Fatalf("text %q", m.Text)
	}
	if m.body() != m.Text || m.HTML != "<p>HTML</p>" {
		t.Fatalf("body %q html %q", m.body(), m.HTML)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Name != "rechnung.pdf" || string(m.Attachments[0].Data) != "%PDF-1.4\n" {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	if got := decodeHeader(m.Header.Get("Subject")); got != "Grüsse" {
		t.Fatalf("subject %q", got)
	}
	if got := decodeHeader(m.Header.Get("From")); got != "Jürg <jurg@example.com>" {
		t.Fatalf("from %q", got)
	}
	var s summary
	s.headers([]byte(multipartMail))
	if s.Subject != "Grüsse" || s.To != "janis@example.com" {
		t.Fatalf("summary %+v", s)
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><head><style>p{color:red}</style><title>x</title></head><body><!-- hidden -->` +
		`<p>Hallo&nbsp;Janis,</p><p>bitte <a href="https://example.com/confirm?a=1&amp;b=2">bestätigen</a>.</p>` +
		`<ul><li>eins</li><li>zwei</li></ul><script>alert(1)</script><table><tr><td>A</td><td>B</td></tr></table></body></html>`
	want := "Hallo Janis,\n\nbitte bestätigen (https://example.com/confirm?a=1&b=2) .\n\n- eins\n- zwei\n\nA B"
	if got := htmlToText(in); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	m := &message{HTML: in}
	if m.body() != want {
		t.Fatal("an HTML-only message is not read as text")
	}
}

func TestDraftBuild(t *testing.T) {
	d := draft{From: "janis@example.com", To: "Jürg <jurg@example.com>, bob@example.com", Subject: "Re: Grüsse\r\nBcc: evil@example.com",
		Body: "Hallo\nZeile zwei: äöü", InReplyTo: "<a@b>", References: "<z@b> <a@b>",
		Files: []attachment{{Name: "note.txt", Type: "text/plain", Data: []byte("Inhalt")}}}
	raw, err := d.build(time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"From: <janis@example.com>\r\n", "In-Reply-To: <a@b>\r\n", "References: <z@b> <a@b>\r\n",
		"Date: Mon, 16 Nov 2026 10:00:00 +0000\r\n", "Content-Type: multipart/mixed;"} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "\r\nBcc:") {
		t.Fatalf("a header was smuggled in through the subject:\n%s", s)
	}
	m, err := parseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "Hallo\r\nZeile zwei: äöü" {
		t.Fatalf("text %q", m.Text)
	}
	if decodeHeader(m.Header.Get("Subject")) != "Re: Grüsse Bcc: evil@example.com" {
		t.Fatalf("subject %q", decodeHeader(m.Header.Get("Subject")))
	}
	if decodeHeader(m.Header.Get("To")) != "Jürg <jurg@example.com>, <bob@example.com>" {
		t.Fatalf("to %q", decodeHeader(m.Header.Get("To")))
	}
	if len(m.Attachments) != 1 || string(m.Attachments[0].Data) != "Inhalt" || m.Attachments[0].Name != "note.txt" {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	if _, err := (draft{To: "not an address"}).build(time.Now()); err == nil {
		t.Fatal("a wrong address was taken")
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{"../../etc/passwd": "passwd", `C:\x\y.pdf`: "y.pdf", "..": "attachment", "a:b": "a_b"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}
