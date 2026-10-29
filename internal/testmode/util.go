package testmode

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// clip cuts s to n bytes on a rune boundary, with an ellipsis.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}
