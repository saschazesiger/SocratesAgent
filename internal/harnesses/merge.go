package harnesses

import "encoding/json"

// deepMerge lays one JSON document over another: an object is merged key by
// key, and anything else - a string, a number, an array - replaces what was
// there. It is what "the admin's raw override wins, but only where it says
// something" means, and it is the same rule for Claude Code's settings file
// and for OpenCode's two generated documents.
func deepMerge(base, over map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for key, value := range over {
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := base[key].(map[string]any); ok {
				base[key] = deepMerge(existing, nested)
				continue
			}
		}
		base[key] = value
	}
	return base
}

// mergeJSON applies a raw JSON object from the dashboard onto a generated
// document. The text was checked where it was saved, so a failure here means
// the stored document has been edited by hand into something unparseable; the
// generated part is used on its own rather than failing the launch, because a
// session that will not start is a worse answer than one setting that did not
// apply.
func mergeJSON(base map[string]any, raw string) map[string]any {
	if raw == "" {
		return base
	}
	var over map[string]any
	if err := json.Unmarshal([]byte(raw), &over); err != nil {
		return base
	}
	return deepMerge(base, over)
}
