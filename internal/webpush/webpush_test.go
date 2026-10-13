package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := b64.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return raw
}

// The worked example of RFC 8291 Appendix A, byte for byte.
func TestEncryptMatchesTheRFCExample(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	var sub Subscription
	sub.Endpoint = "https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV"
	sub.Keys.P256dh = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	sub.Keys.Auth = "BTBZMqHH6r4Tts7J_aSIgg"
	got, err := encrypt(sub, []byte("When I grow up, I want to be a watermelon"), asPriv, mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("encrypted body\n got %s\nwant %s", b64.EncodeToString(got), want)
	}
}

// browser is the receiving half, written out from RFC 8291 §3.3-3.4 with
// plain HMACs rather than the hkdf package, so the two are independent.
type browser struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) (browser, Subscription) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	var sub Subscription
	sub.Endpoint = "https://push.example.net/x"
	sub.Keys.P256dh = b64.EncodeToString(priv.PublicKey().Bytes())
	sub.Keys.Auth = b64.EncodeToString(auth)
	return browser{priv: priv, auth: auth}, sub
}

func mac(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func (b browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt := body[:16]
	if rs := binary.BigEndian.Uint32(body[16:20]); rs != recordSize {
		t.Fatalf("rs = %d", rs)
	}
	idlen := int(body[20])
	asPublic := body[21 : 21+idlen]
	sealed := body[21+idlen:]
	pub, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := b.priv.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	prkKey := mac(b.auth, shared)
	info := append(append([]byte("WebPush: info\x00"), b.priv.PublicKey().Bytes()...), asPublic...)
	ikm := mac(prkKey, append(info, 1))
	prk := mac(salt, ikm)
	cek := mac(prk, []byte("Content-Encoding: aes128gcm\x00\x01"))[:16]
	nonce := mac(prk, []byte("Content-Encoding: nonce\x00\x01"))[:12]
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatalf("missing the last-record delimiter: %x", plain)
	}
	return plain[:len(plain)-1]
}

func TestEncryptRoundTrip(t *testing.T) {
	b, sub := newBrowser(t)
	payload := []byte(`{"title":"Claude Code · fix tests","body":"Finished"}`)
	body, err := Encrypt(sub, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.decrypt(t, body); !bytes.Equal(got, payload) {
		t.Fatalf("round trip = %q", got)
	}
	// A fresh key and salt every time: two seals of one payload never match.
	again, _ := Encrypt(sub, payload)
	if bytes.Equal(body, again) {
		t.Fatal("two encryptions produced the same bytes")
	}
}

func TestValidateRejectsWhatCannotBeSentTo(t *testing.T) {
	_, good := newBrowser(t)
	if err := good.Validate(); err != nil {
		t.Fatalf("good subscription: %v", err)
	}
	plain := good
	plain.Endpoint = "http://push.example.net/x"
	short := good
	short.Keys.Auth = b64.EncodeToString([]byte("short"))
	bad := good
	bad.Keys.P256dh = b64.EncodeToString(make([]byte, 65))
	for name, sub := range map[string]Subscription{"http": plain, "auth": short, "point": bad} {
		if sub.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Encrypt(good, make([]byte, recordSize)); err == nil {
		t.Error("an oversized payload was accepted")
	}
}

func TestSendSignsEncryptsAndForgetsWhatIsGone(t *testing.T) {
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	b, sub := newBrowser(t)
	var gone bool
	var got *http.Request
	var body []byte
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		if gone {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()
	sub.Endpoint = ts.URL + "/push/abc"

	sender, err := NewSender(keys, "mailto:ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	sender.Client = ts.Client()
	msg := Message{Payload: []byte("hello"), TTL: time.Hour, Urgency: "high", Topic: "s0123"}
	if err := sender.Send(context.Background(), sub, msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	for header, want := range map[string]string{
		"Content-Encoding": "aes128gcm", "TTL": "3600", "Urgency": "high", "Topic": "s0123",
	} {
		if got.Header.Get(header) != want {
			t.Errorf("%s = %q, want %q", header, got.Header.Get(header), want)
		}
	}
	if string(b.decrypt(t, body)) != "hello" {
		t.Error("the push service received something the browser cannot open")
	}

	// The VAPID header: a token for the push service's origin, signed by the
	// key the browser was given.
	auth := got.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "vapid t=") || !strings.HasSuffix(auth, ", k="+keys.Public) {
		t.Fatalf("Authorization = %q", auth)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(auth, "vapid t="), ", k="+keys.Public)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %q", token)
	}
	var claims map[string]any
	json.Unmarshal(mustB64(t, parts[1]), &claims)
	if claims["aud"] != ts.URL || claims["sub"] != "mailto:ops@example.com" {
		t.Errorf("claims = %v", claims)
	}
	if exp, _ := claims["exp"].(float64); int64(exp) <= time.Now().Unix() {
		t.Errorf("exp = %v is not in the future", claims["exp"])
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), mustB64(t, keys.Public))
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	sig := mustB64(t, parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("the token does not verify against the public key")
	}

	gone = true
	if err := sender.Send(context.Background(), sub, msg); !errors.Is(err, ErrGone) {
		t.Fatalf("410 = %v, want ErrGone", err)
	}
}
