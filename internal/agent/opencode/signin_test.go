//go:build linux

package opencode

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// An account is signed in to a provider through OpenCode's own flow on a
// server of that account: the login lands in its own auth.json.
func TestASignInWritesTheAccountsOwnLogin(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := StartSignIn(ctx, r.data, "acct", r.env, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	methods, err := s.Methods(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := methods["fake"]; len(got) != 3 || got[0].Type != "oauth" || got[1].Prompts[0].Key != "workspace" {
		t.Fatalf("methods %+v", methods)
	}
	auth, err := s.Authorize(ctx, "fake", 0, nil)
	if err != nil || auth.Method != "auto" || !strings.Contains(auth.Instructions, "FAKE-1234") {
		t.Fatalf("authorize %+v %v", auth, err)
	}
	if err := s.Callback(ctx, "fake", 0, ""); err != nil {
		t.Fatal(err)
	}
	layout := NewLayout(r.data)
	raw, err := os.ReadFile(layout.AuthFile("acct"))
	if err != nil || !strings.Contains(string(raw), "refresh-fake") {
		t.Fatalf("auth.json %q %v", raw, err)
	}
	if _, err := os.Stat(layout.AuthFile(DefaultAccount)); err == nil {
		t.Fatal("the login landed in another account")
	}
	code, err := s.Authorize(ctx, "fake", 1, map[string]string{"workspace": "acme"})
	if err != nil || code.Method != "code" {
		t.Fatalf("authorize %+v %v", code, err)
	}
	if err := s.Callback(ctx, "fake", 1, "bad"); err == nil {
		t.Fatal("a bad code was taken")
	}
	if err := s.Callback(ctx, "fake", 1, "good"); err != nil {
		t.Fatal(err)
	}
}
