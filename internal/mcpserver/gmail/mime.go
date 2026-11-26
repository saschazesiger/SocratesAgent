package gmail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Reading a message (MIME, RFC 2045 ff.) into its text and attachments, and
// writing a draft. Only the standard library: the charsets mail is written
// in beside UTF-8 are nearly all Latin-1 and Windows-1252, which are read
// here; any other is read as UTF-8 with what does not fit replaced.

// attachment is a part of a message that is a file.
type attachment struct {
	Name string
	Type string
	// Inline is whether the message shows it in its text (an image in the
	// HTML), not as a file below it.
	Inline bool
	Data   []byte
}

// message is a message read: its text (the plain part, or the HTML one as
// text) and its files.
type message struct {
	Header      mail.Header
	Text        string
	HTML        string
	Attachments []attachment
}

// body is the message's text: the plain part, or the HTML one made text.
func (m *message) body() string {
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	return htmlToText(m.HTML)
}

var wordDecoder = &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(decodeCharset(charset, b)), nil
}}

// decodeHeader is a header's value with its encoded words (=?utf-8?q?…?=)
// decoded.
func decodeHeader(v string) string {
	v = strings.TrimSpace(v)
	if d, err := wordDecoder.DecodeHeader(v); err == nil {
		v = d
	}
	return strings.ToValidUTF8(v, "�")
}

// cp1252 is what Windows-1252 has at 0x80–0x9F, where Latin-1 has control
// characters.
var cp1252 = [32]rune{
	'€', '�', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '�', 'Ž', '�',
	'�', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '�', 'ž', 'Ÿ',
}

// decodeCharset is text in a charset as UTF-8.
func decodeCharset(charset string, b []byte) string {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(charset), `"`)) {
	case "iso-8859-1", "latin1", "latin-1", "iso8859-1", "l1", "cp819", "iso-8859-15", "latin9", "windows-1252", "cp1252", "x-cp1252":
		if utf8.Valid(b) && !bytes.ContainsFunc(b, func(r rune) bool { return r >= 0x80 }) {
			return string(b)
		}
		var sb strings.Builder
		for _, c := range b {
			if c >= 0x80 && c < 0xA0 {
				sb.WriteRune(cp1252[c-0x80])
			} else {
				sb.WriteRune(rune(c))
			}
		}
		return sb.String()
	}
	return strings.ToValidUTF8(string(b), "�")
}

// maxDepth is how deep parts nest before the rest is passed over.
const maxDepth = 12

// parseMessage reads a message as it was received.
func parseMessage(raw []byte) (*message, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("the message could not be read: %w", err)
	}
	m := &message{Header: msg.Header}
	m.walk(textproto.MIMEHeader(msg.Header), msg.Body, 0)
	return m, nil
}

func (m *message) walk(h textproto.MIMEHeader, body io.Reader, depth int) {
	ctype, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if errors.Is(err, mime.ErrInvalidMediaParameter) && ctype != "" {
		// A parameter Go will not read (an unquoted boundary with = in
		// it): keep the type and take the boundary and charset as written.
		params = looseParams(h.Get("Content-Type"))
	} else if err != nil || ctype == "" {
		ctype, params = "text/plain", map[string]string{"charset": "utf-8"}
	}
	if strings.HasPrefix(ctype, "multipart/") && depth < maxDepth {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return
			}
			m.walk(p.Header, p, depth+1)
		}
	}
	data, err := io.ReadAll(transferDecoder(h.Get("Content-Transfer-Encoding"), body))
	if err != nil && len(data) == 0 {
		return
	}
	disp, dparams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	name := decodeHeader(dparams["filename"])
	if name == "" {
		name = decodeHeader(params["name"])
	}
	isText := ctype == "text/plain" || ctype == "text/html"
	if isText && disp != "attachment" && name == "" {
		text := decodeCharset(params["charset"], data)
		if ctype == "text/plain" && m.Text == "" {
			m.Text = text
			return
		}
		if ctype == "text/html" && m.HTML == "" {
			m.HTML = text
			return
		}
	}
	if name == "" {
		switch {
		case ctype == "message/rfc822":
			name = "message.eml"
		default:
			ext := ".bin"
			if exts, _ := mime.ExtensionsByType(ctype); len(exts) > 0 {
				ext = exts[0]
			}
			name = "part" + ext
		}
	}
	m.Attachments = append(m.Attachments, attachment{Name: filepath.Base(strings.ReplaceAll(name, `\`, "/")), Type: ctype,
		Inline: disp == "inline" || (disp == "" && h.Get("Content-Id") != ""), Data: data})
}

var reParam = regexp.MustCompile(`(?i);\s*(boundary|charset|name)\s*=\s*("([^"]*)"|[^;\s]+)`)

// looseParams reads the parameters of a Content-Type that the mime
// package refuses.
func looseParams(v string) map[string]string {
	out := map[string]string{}
	for _, m := range reParam.FindAllStringSubmatch(v, -1) {
		val := m[2]
		if m[3] != "" || strings.HasPrefix(val, `"`) {
			val = m[3]
		}
		out[strings.ToLower(m[1])] = val
	}
	return out
}

// transferDecoder decodes a part's Content-Transfer-Encoding.
func transferDecoder(enc string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &base64Clean{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// base64Clean drops what is not base64 (spaces, line ends, junk a mailer
// added), which the decoder would stop at.
type base64Clean struct{ r io.Reader }

func (b *base64Clean) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	j := 0
	for _, c := range p[:n] {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
			p[j] = c
			j++
		}
	}
	if j == 0 && n > 0 && err == nil {
		return b.Read(p)
	}
	return j, err
}

var (
	reDrop    = regexp.MustCompile(`(?is)<(script|style|head|title)\b[^>]*>.*?</(script|style|head|title)\s*>`)
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reLink    = regexp.MustCompile(`(?is)<a\b[^>]*?\bhref\s*=\s*["']([^"']*)["'][^>]*>(.*?)</a\s*>`)
	reBreak   = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|tr|h[1-6]|ul|ol|table|blockquote|pre)\s*>|<(p|div|tr|table|h[1-6])\b[^>]*>`)
	reItem    = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	reCell    = regexp.MustCompile(`(?i)</t[dh]\s*>`)
	reTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces  = regexp.MustCompile(`[ \t\f\v\x{00A0}]+`)
	reBlank   = regexp.MustCompile(`\n{3,}`)
)

// htmlToText is an HTML mail as text: its words, a line per block, and
// every link's address after its words, so links in it can be followed.
func htmlToText(s string) string {
	if s == "" {
		return ""
	}
	s = reComment.ReplaceAllString(s, "")
	s = reDrop.ReplaceAllString(s, "")
	s = reLink.ReplaceAllStringFunc(s, func(a string) string {
		m := reLink.FindStringSubmatch(a)
		href, words := html.UnescapeString(strings.TrimSpace(m[1])), strings.TrimSpace(reTag.ReplaceAllString(m[2], " "))
		words = strings.Join(strings.Fields(html.UnescapeString(words)), " ")
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "mailto:") || href == words {
			return " " + words + " "
		}
		if words == "" {
			return " " + href + " "
		}
		return " " + words + " (" + href + ") "
	})
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	s = reBreak.ReplaceAllString(s, "\n")
	s = reItem.ReplaceAllString(s, "\n- ")
	s = reCell.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "‌", "")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	s = reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(s)
}

// draft is a message to write into Drafts or to send.
type draft struct {
	From, To, Cc, Bcc, Subject, Body string
	InReplyTo, References            string
	Files                            []attachment
	// MessageID is its Message-ID; one is made when "".
	MessageID string
	// NoBcc leaves the Bcc header out: a message that is sent names its
	// blind copies only in the envelope.
	NoBcc bool
}

// formatAddresses checks a list of addresses and writes it as a header
// takes it, names encoded.
func formatAddresses(list string) (string, error) {
	list = strings.TrimSpace(list)
	if list == "" {
		return "", nil
	}
	addrs, err := mail.ParseAddressList(list)
	if err != nil {
		return "", fmt.Errorf("%q is not a list of addresses: %v", list, err)
	}
	out := make([]string, len(addrs))
	n := 0
	for i, a := range addrs {
		out[i] = a.String()
		n += len(out[i]) + 2
	}
	// A long list goes one address a line: a line stays under 998
	// characters.
	if n > 900 {
		return strings.Join(out, ",\r\n "), nil
	}
	return strings.Join(out, ", "), nil
}

func headerSafe(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

// newMessageID is a new Message-ID in the sender's domain.
func newMessageID(from string) string {
	var id [12]byte
	_, _ = rand.Read(id[:])
	domain := "socrates.local"
	if _, after, ok := strings.Cut(from, "@"); ok {
		domain = strings.Trim(after, "> ")
	}
	return "<socrates." + hex.EncodeToString(id[:]) + "@" + domain + ">"
}

// build writes the draft as a message (RFC 5322): plain text in UTF-8,
// quoted-printable, with its files after it.
func (d draft) build(now time.Time) ([]byte, error) {
	var b bytes.Buffer
	hdr := func(k, v string) {
		if v != "" {
			b.WriteString(k + ": " + v + "\r\n")
		}
	}
	from, err := formatAddresses(d.From)
	if err != nil {
		return nil, err
	}
	hdr("From", from)
	for _, f := range []struct{ k, v string }{{"To", d.To}, {"Cc", d.Cc}, {"Bcc", d.Bcc}} {
		v, err := formatAddresses(f.v)
		if err != nil {
			return nil, err
		}
		if f.k != "Bcc" || !d.NoBcc {
			hdr(f.k, v)
		}
	}
	hdr("Subject", mime.QEncoding.Encode("utf-8", headerSafe(d.Subject)))
	hdr("Date", now.Format(time.RFC1123Z))
	if d.MessageID == "" {
		d.MessageID = newMessageID(d.From)
	}
	hdr("Message-ID", headerSafe(d.MessageID))
	hdr("In-Reply-To", headerSafe(d.InReplyTo))
	// One id a line: a long conversation's References stays under a line's
	// 998 characters.
	hdr("References", strings.Join(strings.Fields(headerSafe(d.References)), "\r\n "))
	hdr("MIME-Version", "1.0")
	body := strings.ReplaceAll(strings.ReplaceAll(d.Body, "\r\n", "\n"), "\n", "\r\n")
	writeText := func(w io.Writer) error {
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(body)); err != nil {
			return err
		}
		return qp.Close()
	}
	if len(d.Files) == 0 {
		hdr("Content-Type", "text/plain; charset=utf-8")
		hdr("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeText(&b); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	mw := multipart.NewWriter(&b)
	hdr("Content-Type", `multipart/mixed; boundary="`+mw.Boundary()+`"`)
	b.WriteString("\r\n")
	tw, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"}})
	if err != nil {
		return nil, err
	}
	if err := writeText(tw); err != nil {
		return nil, err
	}
	for _, f := range d.Files {
		ctype, params, err := mime.ParseMediaType(f.Type)
		if err != nil {
			ctype, params = "application/octet-stream", map[string]string{}
		}
		name := headerSafe(f.Name)
		params["name"] = name
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {mime.FormatMediaType(ctype, params)},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": name})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, err
		}
		enc := base64.StdEncoding.EncodeToString(f.Data)
		for len(enc) > 76 {
			io.WriteString(pw, enc[:76]+"\r\n")
			enc = enc[76:]
		}
		io.WriteString(pw, enc+"\r\n")
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
