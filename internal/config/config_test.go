package config

import "testing"

func TestNormalizeFillsDefaults(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.OpenRouter.BaseURL != DefaultOpenRouterBaseURL {
		t.Errorf("base url = %q", s.OpenRouter.BaseURL)
	}
	if s.Agent.MaxIterations != DefaultMaxIterations {
		t.Errorf("max iterations = %d", s.Agent.MaxIterations)
	}
	if len(s.Backends) != 3 {
		t.Errorf("expected the three default agents, got %d", len(s.Backends))
	}
	if s.Voice.TTSProvider != "browser" || s.Voice.STTProvider != "openrouter" {
		t.Errorf("voice defaults = %#v", s.Voice)
	}
}

func TestNormalizeSanitisesBackends(t *testing.T) {
	s := Settings{Backends: []Backend{{Name: "My Agent!", Kind: "nonsense", Approval: "maybe", Sandbox: "wild"}}}
	s.Normalize()
	b := s.Backends[0]
	if b.ID != "my-agent" {
		t.Errorf("id = %q", b.ID)
	}
	if b.Kind != KindCustom {
		t.Errorf("kind = %q", b.Kind)
	}
	if b.Approval != "auto" {
		t.Errorf("approval = %q", b.Approval)
	}
	if b.Sandbox != "workspace-write" {
		t.Errorf("sandbox = %q", b.Sandbox)
	}
	if b.TimeoutSeconds <= 0 {
		t.Errorf("timeout = %d", b.TimeoutSeconds)
	}
}

func TestNormalizeTrimsBaseURL(t *testing.T) {
	s := Settings{}
	s.OpenRouter.BaseURL = "https://example.com/v1/"
	s.Normalize()
	if s.OpenRouter.BaseURL != "https://example.com/v1" {
		t.Errorf("base url = %q", s.OpenRouter.BaseURL)
	}
}

func TestEnabledBackends(t *testing.T) {
	s := Default()
	s.Backends[0].Enabled = false
	enabled := s.EnabledBackends()
	for _, b := range enabled {
		if !b.Enabled {
			t.Fatalf("disabled backend leaked: %#v", b)
		}
	}
	if _, ok := s.Backend("claude"); !ok {
		t.Error("Backend() should find disabled agents too")
	}
	if _, ok := s.Backend("ghost"); ok {
		t.Error("unknown id should not resolve")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Claude Code":  "claude-code",
		"  Weird__ID ": "weird-id",
		"???":          "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
