package config

import (
	"encoding/json"
	"testing"
)

func TestGitHubSettingsNormalize(t *testing.T) {
	var s Settings
	raw := `{"github":{"app_id":7,"slug":" socrates-x ","owner":"alice","repos":{
		"alice/one":{"setup":"npm ci\r\ncp a b\r\n"},
		"alice/empty":{"setup":"  "},
		"not a repo":{"setup":"x"},
		"../etc":{"setup":"x"}}}}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	g := s.GitHub
	if !g.Connected() || g.Slug != "socrates-x" {
		t.Fatalf("app %+v", g)
	}
	if len(g.Repos) != 1 || g.Repos["alice/one"].Setup != "npm ci\ncp a b\n" {
		t.Fatalf("repos %+v", g.Repos)
	}
	if g.RepoSetup("Alice/One") != "npm ci\ncp a b\n" || g.RepoSetup("alice/two") != "" {
		t.Fatal("RepoSetup does not find the script whatever the case")
	}
	// A fresh document has no App and writes no repositories.
	d := Default()
	d.Normalize()
	if d.GitHub.Connected() || d.GitHub.Repos != nil {
		t.Fatalf("default %+v", d.GitHub)
	}
	for name, want := range map[string]bool{"alice/one": true, "a.b/c_d-e": true, "alice": false, "a/b/c": false, "./x": false, "a/..": false, "a/b c": false} {
		if ValidRepoName(name) != want {
			t.Errorf("ValidRepoName(%q) = %v", name, !want)
		}
	}
}
