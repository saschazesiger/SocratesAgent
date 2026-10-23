// Package costs reads what every conversation of Claude Code, Codex and
// OpenCode on this machine cost, hour by hour and model by model, from the
// files the CLIs keep themselves. The server keeps the result in the store's
// cost ledger, and the ledger is what reaches the bucket.
//
// The numbers are API-equivalent estimates: the published per-token prices
// applied to the tokens the transcripts record, which is also what Claude
// Code's own /cost and the session header show. A subscription is billed
// differently; the estimate is what the same work would have cost on the API.
package costs

import (
	"regexp"
	"strings"
)

// claudePrice is a model's price in USD per million tokens. A cache write
// costs 1.25 times the input price for the five minute TTL and twice for the
// hour; Claude Code writes the hour.
type claudePrice struct{ input, output, cacheRead float64 }

// claudePrices are Anthropic's first-party API rates. A model that is not
// here is counted in tokens and marked unpriced; the cost Claude Code itself
// records when it exits still reaches the ledger (see claude.go).
var claudePrices = map[string]claudePrice{
	"claude-fable-5-1":  {10, 50, 0.25},
	"claude-mythos-5-1": {10, 50, 0.25},
	"claude-fable-5":    {10, 50, 1},
	"claude-mythos-5":   {10, 50, 1},
	"claude-opus-5-5":   {4, 20, 0.20},
	"claude-opus-5":     {5, 25, 0.50},
	"claude-opus-4-8":   {5, 25, 0.50},
	"claude-opus-4-7":   {5, 25, 0.50},
	"claude-opus-4-6":   {5, 25, 0.50},
	"claude-opus-4-5":   {5, 25, 0.50},
	"claude-opus-4-1":   {15, 75, 1.50},
	"claude-opus-4":     {15, 75, 1.50},
	"claude-sonnet-5-5": {2, 10, 0.20},
	"claude-sonnet-5":   {2, 10, 0.20},
	"claude-sonnet-4-6": {3, 15, 0.30},
	"claude-sonnet-4-5": {3, 15, 0.30},
	"claude-sonnet-4":   {3, 15, 0.30},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01},
	"claude-haiku-4-5":  {1, 5, 0.10},
	"claude-3-5-haiku":  {0.80, 4, 0.08},
}

// fastModels are the models whose fast mode costs twice the standard rate.
var fastModels = map[string]bool{"claude-opus-5-5": true, "claude-opus-5": true}

// webSearchUSD is the price of one server-side web search.
const webSearchUSD = 0.01

// haikuLongPrompt is where Claude Haiku 5.5's long prompt rate begins, and
// haikuLongFactor what it multiplies by ($0.50/$2.50 instead of $0.10/$0.50).
const (
	haikuLongPrompt = 100_000
	haikuLongFactor = 5
)

var (
	dateSuffix    = regexp.MustCompile(`-\d{8}$`)
	versionSuffix = regexp.MustCompile(`-v\d+(:\d+)?$`)
)

// claudeModel is the model id a price is looked up by: a provider prefix
// (us.anthropic.…), a version or context suffix and a date are dropped.
func claudeModel(model string) string {
	i := strings.Index(model, "claude-")
	if i < 0 {
		return ""
	}
	m := model[i:]
	if j := strings.IndexAny(m, "@["); j >= 0 {
		m = m[:j]
	}
	m = versionSuffix.ReplaceAllString(m, "")
	return dateSuffix.ReplaceAllString(m, "")
}

// claudeTokens is the usage of one Claude request.
type claudeTokens struct {
	input, output, cacheRead, write5m, write1h, webSearches int64
	fast                                                    bool
}

// claudeCost prices one request. ok is false for a model without a price.
func claudeCost(model string, t claudeTokens) (float64, bool) {
	id := claudeModel(model)
	p, ok := claudePrices[id]
	if !ok {
		return 0, false
	}
	cost := float64(t.input)*p.input + float64(t.output)*p.output + float64(t.cacheRead)*p.cacheRead +
		float64(t.write5m)*p.input*1.25 + float64(t.write1h)*p.input*2
	cost /= 1e6
	if id == "claude-haiku-5-5" && t.input+t.cacheRead+t.write5m+t.write1h > haikuLongPrompt {
		cost *= haikuLongFactor
	}
	if t.fast && fastModels[id] {
		cost *= 2
	}
	return cost + float64(t.webSearches)*webSearchUSD, true
}

// TokenPrice is a Codex model's price in USD per million tokens.
type TokenPrice struct{ Input, Cached, CacheWrite, Output float64 }

// CodexPrices are the published ChatGPT Work/Codex rates; aliases keep old
// sessions useful while unknown future models are counted unpriced.
var CodexPrices = map[string]TokenPrice{
	"gpt-6-astra": {10, 1, 12.5, 50},
	// The GPT-6 Sol and Luna rates are from third-party price lists of
	// 2026-10 that agree on input and output; the cached rate is the usual
	// tenth of the input. gpt-6-sol is the name 6.1 Sol was first served under.
	"gpt-6.1-sol":   {2, .2, 2.5, 10},
	"gpt-6-sol":     {2, .2, 2.5, 10},
	"gpt-6-luna":    {.1, .01, .125, .5},
	"gpt-5.6-sol":   {4, .4, 5, 20},
	"gpt-5.6":       {4, .4, 5, 20},
	"gpt-5.6-terra": {2, .2, 2.5, 12},
	"gpt-5.6-luna":  {.2, .02, .25, 1.2},
	"gpt-5.5":       {5, .5, 6.25, 30},
	"gpt-5.4":       {2.5, .25, 3.125, 15},
	"gpt-5.4-mini":  {.75, .075, .9375, 4.5},
	"gpt-5.3-codex": {1.75, .175, 2.1875, 14},
}

// CodexCost prices Codex usage. input counts the cached and cache-written
// tokens too, the way Codex reports it.
func CodexCost(model string, input, cached, cacheWrite, output float64) (float64, bool) {
	price, ok := CodexPrices[model]
	if !ok {
		return 0, false
	}
	plain := max(0, input-cached-cacheWrite)
	return (plain*price.Input + cached*price.Cached + cacheWrite*price.CacheWrite + output*price.Output) / 1e6, true
}
