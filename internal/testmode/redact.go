package testmode

import (
	"encoding/json"
	"sort"
	"strings"
)

// minSecret is the shortest value that is redacted: anything shorter would
// take words out of ordinary text.
const minSecret = 6

// Redactor replaces known secret values with ‹NAME› in everything a run
// stores: records, the evidence it reads, links.
type Redactor struct {
	r *strings.Replacer
}

// NewRedactor redacts the values of secrets; each value is also redacted
// in its parts, split at commas and colons, since a pool member (one entry of
// a list) or its key (user:key) appears on its own.
func NewRedactor(secrets map[string]string) *Redactor {
	type pair struct{ value, name string }
	var pairs []pair
	seen := map[string]bool{}
	add := func(value, name string) {
		value = strings.TrimSpace(value)
		if len(value) < minSecret || seen[value] {
			return
		}
		seen[value] = true
		pairs = append(pairs, pair{value, name})
	}
	for name, value := range secrets {
		add(value, name)
		for _, member := range strings.Split(value, ",") {
			add(member, name)
			for _, part := range strings.Split(member, ":") {
				add(part, name)
			}
		}
	}
	if len(pairs) == 0 {
		return &Redactor{}
	}
	// Longest first, so that a value is replaced whole before a part of it.
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i].value) > len(pairs[j].value) })
	args := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		args = append(args, p.value, "‹"+p.name+"›")
	}
	return &Redactor{r: strings.NewReplacer(args...)}
}

// String redacts s.
func (r *Redactor) String(s string) string {
	if r == nil || r.r == nil || s == "" {
		return s
	}
	return r.r.Replace(s)
}

// JSON redacts raw JSON, keeping it valid: the replacement holds no quote or
// backslash, and a secret that needed escaping in JSON is not found in it.
func (r *Redactor) JSON(raw json.RawMessage) json.RawMessage {
	if r == nil || r.r == nil || len(raw) == 0 {
		return raw
	}
	out := json.RawMessage(r.r.Replace(string(raw)))
	if !json.Valid(out) {
		return raw
	}
	return out
}
