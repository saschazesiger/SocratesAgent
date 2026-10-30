package harnesses

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// verboseListing is `opencode models --verbose` as 1.19.4 prints it: an id
// line, then the model as indented JSON, for every model.
const verboseListing = `openai/gpt-5.4
{
  "id": "gpt-5.4",
  "providerID": "openai",
  "name": "GPT-5.4",
  "status": "active",
  "cost": { "input": 1.25, "output": 10, "cache": { "read": 0.125, "write": 0 } },
  "limit": { "context": 400000, "output": 128000 },
  "capabilities": { "reasoning": true, "toolcall": true, "input": { "text": true, "image": true } },
  "variants": { "high": {"reasoningEffort": "high"}, "none": {}, "low": {}, "medium": {}, "xhigh": {} }
}
opencode/big-pickle
{
  "id": "big-pickle",
  "providerID": "opencode",
  "name": "Big Pickle",
  "status": "active",
  "cost": { "input": 0, "output": 0 },
  "limit": { "context": 1048576, "output": 131072 },
  "capabilities": { "reasoning": true, "toolcall": true, "input": { "text": true } },
  "variants": {}
}
xai/grok-imagine-video
{
  "id": "grok-imagine-video",
  "providerID": "xai",
  "name": "Grok Imagine Video",
  "status": "active",
  "capabilities": { "toolcall": false, "input": { "text": true } }
}
openrouter/old/thing
{
  "id": "old/thing",
  "providerID": "openrouter",
  "name": "Old thing",
  "status": "deprecated",
  "capabilities": { "toolcall": true }
}
`

func TestParseOpenCodeVerbose(t *testing.T) {
	list, ok := parseOpenCodeVerbose([]byte(verboseListing))
	if !ok || len(list) != 4 {
		t.Fatalf("parsed %d models (ok=%v), want 4", len(list), ok)
	}
	if list[0].full != "openai/gpt-5.4" || list[0].Name != "GPT-5.4" || list[0].Limit.Context != 400000 {
		t.Fatalf("first model = %+v", list[0])
	}
	if list[3].full != "openrouter/old/thing" {
		t.Fatalf("a model id with a slash = %q", list[3].full)
	}
	// The usage text of a build without the flag is not this format.
	if _, ok := parseOpenCodeVerbose([]byte("opencode models [provider]\n\nlist all available models\n")); ok {
		t.Fatal("usage text was taken for a listing")
	}
}

func TestMergeOpenCode(t *testing.T) {
	one, _ := parseOpenCodeVerbose([]byte(verboseListing))
	// A second account connects a provider the first does not have.
	two, _ := parseOpenCodeVerbose([]byte(`xai/grok-4.7
{"id": "grok-4.7", "providerID": "xai", "name": "Grok 4.7", "cost": {"input": 3, "output": 15},
 "capabilities": {"toolcall": true}, "variants": {"high": {}, "low": {}}}
openai/gpt-5.4
{"id": "gpt-5.4", "providerID": "openai", "name": "GPT-5.4 again", "capabilities": {"toolcall": true}}
`))
	states := []openCodeState{
		{
			Recent:  []openCodeRef{{"openai", "gpt-5.4"}, {"gone", "model"}},
			Variant: map[string]string{"openai/gpt-5.4": "high", "opencode/big-pickle": "default"},
		},
		{
			Recent:   []openCodeRef{{"xai", "grok-4.7"}, {"openai", "gpt-5.4"}},
			Favorite: []openCodeRef{{"xai", "grok-4.7"}},
			Variant:  map[string]string{"openai/gpt-5.4": "low", "xai/grok-4.7": "ultra"},
		},
	}
	cat := mergeOpenCode([][]openCodeModel{one, two}, states, map[string]string{"openai": "OpenAI", "opencode": "OpenCode Zen"})

	var ids []string
	for _, m := range cat.Models {
		ids = append(ids, m.ID)
	}
	// Sorted by provider name, then model name; no tool-less or deprecated
	// model; a model two accounts have is there once.
	if want := []string{"openai/gpt-5.4", "opencode/big-pickle", "xai/grok-4.7"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("models = %v, want %v", ids, want)
	}
	gpt := cat.Models[0]
	if gpt.Label != "GPT-5.4" || gpt.Group != "OpenAI" || !gpt.Default {
		t.Fatalf("gpt = %+v", gpt)
	}
	if want := []string{"none", "low", "medium", "high", "xhigh"}; !reflect.DeepEqual(gpt.Efforts, want) {
		t.Fatalf("efforts = %v, want %v", gpt.Efforts, want)
	}
	// The first account's variant wins; "default" is no variant at all, and
	// one the model does not have is dropped.
	if gpt.DefaultEffort != "high" {
		t.Fatalf("default effort = %q", gpt.DefaultEffort)
	}
	if cat.Models[1].DefaultEffort != "" || cat.Models[2].DefaultEffort != "" {
		t.Fatalf("efforts = %q, %q", cat.Models[1].DefaultEffort, cat.Models[2].DefaultEffort)
	}
	if cat.Models[2].Group != "xai" {
		t.Fatalf("a provider the cache has no name for is its id, got %q", cat.Models[2].Group)
	}
	if want := "400k context · $1.25/$10 per 1M · images"; gpt.Hint != want {
		t.Fatalf("hint = %q, want %q", gpt.Hint, want)
	}
	if want := "1M context · free"; cat.Models[1].Hint != want {
		t.Fatalf("hint = %q, want %q", cat.Models[1].Hint, want)
	}
	if want := []string{"openai/gpt-5.4", "xai/grok-4.7"}; !reflect.DeepEqual(cat.Recent, want) {
		t.Fatalf("recent = %v, want %v", cat.Recent, want)
	}
	if want := []string{"xai/grok-4.7"}; !reflect.DeepEqual(cat.Favorites, want) {
		t.Fatalf("favorites = %v, want %v", cat.Favorites, want)
	}
}

// TestOpenCodeModelsPerAccount runs a fake opencode as two accounts, each
// with its own state folder, and reads both listings and both TUI states.
func TestOpenCodeModelsPerAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for opencode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	script := `#!/bin/sh
[ "$1" = models ] || exit 2
[ "$2" = --verbose ] || exit 2
cat "$LISTING"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	account := func(name, listing, state string) []string {
		home := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(home, "state", "opencode"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "listing"), []byte(listing), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "state", "opencode", "model.json"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
		return []string{"PATH=/usr/bin:/bin", "LISTING=" + filepath.Join(home, "listing"),
			"XDG_STATE_HOME=" + filepath.Join(home, "state"), "XDG_CACHE_HOME=" + filepath.Join(dir, "cache")}
	}
	a := account("a", `a/one
{"id": "one", "providerID": "a", "name": "One", "capabilities": {"toolcall": true}}
`, `{"recent": [{"providerID": "a", "modelID": "one"}], "favorite": [], "variant": {}}`)
	b := account("b", `b/two
{"id": "two", "providerID": "b", "name": "Two", "capabilities": {"toolcall": true}}
`, `{"recent": [], "favorite": [{"providerID": "b", "modelID": "two"}], "variant": {}}`)
	if err := os.MkdirAll(filepath.Join(dir, "cache", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache", "opencode", "models.json"), []byte(`{"a": {"name": "Provider A", "models": {}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cat, err := OpenCode{}.DiscoverModelsIn(context.Background(), bin, [][]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range cat.Models {
		got = append(got, m.ID+"="+m.Group)
	}
	// By the provider's name: "b" before "Provider A".
	if want := "b/two=b,a/one=Provider A"; strings.Join(got, ",") != want {
		t.Fatalf("models = %v, want %s", got, want)
	}
	if cat.Models[0].Default || !cat.Models[1].Default {
		t.Fatalf("the default is the first account's most recent: %+v", cat.Models)
	}
	if !reflect.DeepEqual(cat.Recent, []string{"a/one"}) || !reflect.DeepEqual(cat.Favorites, []string{"b/two"}) {
		t.Fatalf("recent %v, favorites %v", cat.Recent, cat.Favorites)
	}
}

// A context window reads the way models.dev and a person write it: decimal
// thousands and millions, the 2^20 of some models included.
func TestContextWords(t *testing.T) {
	for n, want := range map[int64]string{0: "", 512: "512", 131072: "131k", 400000: "400k", 1048576: "1M", 2000000: "2M", 1500000: "1.5M"} {
		if got := contextWords(n); got != want {
			t.Errorf("contextWords(%d) = %q, want %q", n, got, want)
		}
	}
}
