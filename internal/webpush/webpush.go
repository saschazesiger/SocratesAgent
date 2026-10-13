// Package webpush sends Web Push messages: delivered by the browser vendor's
// push service (RFC 8030), signed with a VAPID key (RFC 8292) and encrypted
// for the one browser that subscribed (RFC 8291, aes128gcm from RFC 8188).
//
// It is the standard library and nothing else. The whole protocol is an ECDH
// agreement, two HKDF steps, one AES-GCM seal and an ES256 token, and a
// dependency for that would be a dependency on somebody else's reading of
// three short RFCs.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrGone is a subscription the push service no longer knows: the browser
// unsubscribed, the permission was taken back or the app was uninstalled. It
// will never work again and is to be forgotten.
var ErrGone = errors.New("the push subscription is gone")

// b64 is the encoding every key travels in: URL safe, without padding. A
// browser hands keys over in it and expects the server key in it.
var b64 = base64.RawURLEncoding

// Keys is the server's VAPID key pair as it is kept: the private key as
// PKCS #8 and the public key as the uncompressed point the browser is given
// as its applicationServerKey, both base64url.
type Keys struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}

// GenerateKeys makes a new P-256 key pair.
func GenerateKeys() (Keys, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Keys{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return Keys{}, err
	}
	pub, err := priv.PublicKey.ECDH()
	if err != nil {
		return Keys{}, err
	}
	return Keys{Private: b64.EncodeToString(der), Public: b64.EncodeToString(pub.Bytes())}, nil
}

// Subscription is what PushSubscription.toJSON() gives a page.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Validate checks that a subscription is one this package can send to: an
// https endpoint, a P-256 public key and a 16 byte authentication secret.
func (s Subscription) Validate() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("the endpoint is not an https address")
	}
	if _, err := s.uaPublic(); err != nil {
		return err
	}
	if auth, err := b64.DecodeString(strings.TrimRight(s.Keys.Auth, "=")); err != nil || len(auth) != 16 {
		return errors.New("the auth secret is not 16 bytes")
	}
	return nil
}

func (s Subscription) uaPublic() (*ecdh.PublicKey, error) {
	raw, err := b64.DecodeString(strings.TrimRight(s.Keys.P256dh, "="))
	if err != nil {
		return nil, errors.New("the p256dh key is not base64url")
	}
	pub, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, errors.New("the p256dh key is not a P-256 point")
	}
	return pub, nil
}

// Message is one push.
type Message struct {
	// Payload is what the service worker's push event reads, at most a little
	// under 4 KB.
	Payload []byte
	// TTL is how long the push service keeps a message for a device that is
	// offline. Zero means deliver now or never.
	TTL time.Duration
	// Urgency is "very-low", "low", "normal" or "high"; empty is normal.
	Urgency string
	// Topic, when set, replaces an undelivered message with the same topic, so
	// a phone that was off sees the last word about something rather than all
	// of them. At most 32 characters of the base64url alphabet.
	Topic string
}

// Sender sends messages signed with one key pair.
type Sender struct {
	priv   *ecdsa.PrivateKey
	public string
	// Subject is the VAPID contact, a mailto: or an https: URL. Apple rejects
	// a token without one.
	Subject string
	Client  *http.Client
}

// NewSender reads a stored key pair.
func NewSender(keys Keys, subject string) (*Sender, error) {
	der, err := b64.DecodeString(keys.Private)
	if err != nil {
		return nil, fmt.Errorf("vapid key: %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("vapid key: %w", err)
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || priv.Curve != elliptic.P256() {
		return nil, errors.New("vapid key: not a P-256 key")
	}
	return &Sender{
		priv:    priv,
		public:  keys.Public,
		Subject: subject,
		Client:  &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Send delivers one message to one subscription. It answers ErrGone for a
// subscription the push service has forgotten, and an error carrying the
// service's own words for anything else it refused.
func (s *Sender) Send(ctx context.Context, sub Subscription, msg Message) error {
	if err := sub.Validate(); err != nil {
		return err
	}
	body, err := Encrypt(sub, msg.Payload)
	if err != nil {
		return err
	}
	endpoint, _ := url.Parse(sub.Endpoint)
	token, err := s.token(endpoint.Scheme+"://"+endpoint.Host, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(int(msg.TTL/time.Second)))
	req.Header.Set("Authorization", "vapid t="+token+", k="+s.public)
	if msg.Urgency != "" {
		req.Header.Set("Urgency", msg.Urgency)
	}
	if msg.Topic != "" {
		req.Header.Set("Topic", msg.Topic)
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	case res.StatusCode >= 300:
		text := strings.TrimSpace(string(detail))
		if text == "" {
			text = http.StatusText(res.StatusCode)
		}
		return fmt.Errorf("the push service answered %d: %s", res.StatusCode, text)
	}
	return nil
}

// token is the VAPID JWT for one push service origin (RFC 8292 §2): ES256,
// valid for twelve hours, which is half of the most a service accepts.
func (s *Sender) token(audience string, now time.Time) (string, error) {
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": audience,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": s.Subject,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, sig, err := ecdsa.Sign(rand.Reader, s.priv, digest[:])
	if err != nil {
		return "", err
	}
	// JWS wants the two halves as fixed width big-endian integers, not the
	// ASN.1 a signature is usually wrapped in.
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	sig.FillBytes(raw[32:])
	return signing + "." + b64.EncodeToString(raw), nil
}

// recordSize is the rs field of the one record every message is: large enough
// for any payload a push service accepts.
const recordSize = 4096

// Encrypt seals a payload for one subscription (RFC 8291 §3.4), with a fresh
// ephemeral key and salt.
func Encrypt(sub Subscription, payload []byte) ([]byte, error) {
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(sub, payload, eph, salt)
}

func encrypt(sub Subscription, payload []byte, eph *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	uaPublic, err := sub.uaPublic()
	if err != nil {
		return nil, err
	}
	authSecret, err := b64.DecodeString(strings.TrimRight(sub.Keys.Auth, "="))
	if err != nil {
		return nil, errors.New("the auth secret is not base64url")
	}
	// One record, so the payload plus its delimiter and the tag must fit in it.
	if len(payload)+1+16 > recordSize {
		return nil, errors.New("the payload is too large for a push message")
	}
	shared, err := eph.ECDH(uaPublic)
	if err != nil {
		return nil, err
	}
	asPublic := eph.PublicKey().Bytes()

	// The input keying material binds the agreement to both keys and to the
	// browser's auth secret: HKDF(auth, ecdh, "WebPush: info\0" || ua || as).
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic.Bytes()...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// 0x02 marks the last (and only) record; no padding follows it.
	plain := append(append([]byte{}, payload...), 0x02)

	out := make([]byte, 0, 16+4+1+len(asPublic)+len(plain)+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	return gcm.Seal(out, nonce, plain, nil), nil
}
