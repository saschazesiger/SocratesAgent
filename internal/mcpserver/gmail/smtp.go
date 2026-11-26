package gmail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// Sending goes over Gmail's SMTP server with the same app password as IMAP.
// Gmail files what it sends in Sent itself, in the conversation its
// In-Reply-To and References name.

// gmailSMTP is Gmail's SMTP server (TLS from the start).
const gmailSMTP = "smtp.gmail.com:465"

// smtpRefused is the SMTP server refusing the app password.
type smtpRefused struct{ err error }

func (e *smtpRefused) Error() string { return "Gmail refused the sign-in to send: " + e.err.Error() }
func (e *smtpRefused) Unwrap() error { return e.err }

// plainAuth is SMTP AUTH PLAIN. smtp.PlainAuth refuses a connection it did
// not see TLS on; this one is only used on a connection that is TLS from
// its start (or a test's).
type plainAuth struct{ user, password string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.password), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("Gmail asked for more than the app password")
	}
	return nil, nil
}

func (s *Server) dialSMTP(ctx context.Context) (net.Conn, error) {
	if s.deps.DialSMTP != nil {
		return s.deps.DialSMTP(ctx)
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 20 * time.Second}, Config: &tls.Config{ServerName: "smtp.gmail.com"}}
	return d.DialContext(ctx, "tcp", gmailSMTP)
}

// sendMail signs in to Gmail's SMTP server and sends one message from the
// account to the recipients (the envelope; Bcc is not in msg).
func (s *Server) sendMail(ctx context.Context, email, password string, rcpt []string, msg []byte) error {
	nc, err := s.dialSMTP(ctx)
	if err != nil {
		return fmt.Errorf("Gmail's mail server could not be reached: %w", err)
	}
	if d, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(d)
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()
	c, err := smtp.NewClient(nc, "smtp.gmail.com")
	if err != nil {
		nc.Close()
		return fmt.Errorf("Gmail's mail server did not answer: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("AUTH"); !ok {
		return errors.New("Gmail's mail server offers no sign-in")
	}
	if err := c.Auth(plainAuth{user: email, password: strings.ReplaceAll(password, " ", "")}); err != nil {
		var te *textproto.Error
		if errors.As(err, &te) && (te.Code == 535 || te.Code == 534) {
			return &smtpRefused{err}
		}
		return fmt.Errorf("Gmail refused the sign-in to send: %w", err)
	}
	if err := c.Mail(email); err != nil {
		return fmt.Errorf("Gmail refused the sender %s: %w", email, err)
	}
	for _, r := range rcpt {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("Gmail refused the recipient %s, and nothing was sent: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("Gmail refused the message, and nothing was sent: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("the message could not be handed to Gmail, and was not sent: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("Gmail refused the message, and nothing was sent: %w", err)
	}
	// It is sent once DATA is taken; a failed QUIT changes nothing.
	_ = c.Quit()
	return nil
}
