package catalog

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// decoder reads the typed values of one front-matter mapping, recording
// each problem against its file and line and returning the zero value, so a
// file with a mistake is still read as far as it goes.
type decoder struct {
	c    *Catalog
	file string
	m    *yamlMap
}

func (d decoder) errorf(key string, format string, a ...any) {
	d.c.errorf(d.file, d.m.Line(key), format, a...)
}

// keys refuses every key not in allowed: a typo is never ignored.
func (d decoder) keys(allowed ...string) {
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	for _, k := range d.m.Keys() {
		if !ok[k] {
			d.errorf(k, "unknown key %q (known: %s)", k, strings.Join(allowed, ", "))
		}
	}
}

func (d decoder) has(key string) bool {
	v, ok := d.m.Get(key)
	return ok && v != nil
}

// str is a scalar value, "" when the key is missing or empty.
func (d decoder) str(key string) string {
	v, _ := d.m.Get(key)
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	d.errorf(key, "%s is a single value, not a %s", key, kind(v))
	return ""
}

// list is a list of scalars; a single scalar is a list of one, an empty
// value none. Duplicates are refused.
func (d decoder) list(key string) []string {
	v, _ := d.m.Get(key)
	out, err := stringList(v)
	if err != "" {
		d.errorf(key, "%s: %s", key, err)
	}
	return out
}

// stringList reads a list of scalars (or one scalar) and explains what is
// wrong with it otherwise.
func stringList(v any) ([]string, string) {
	switch v := v.(type) {
	case nil:
		return nil, ""
	case string:
		return []string{v}, ""
	case []any:
		out := make([]string, 0, len(v))
		seen := map[string]bool{}
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Sprintf("a list of single values, not of a %s", kind(item))
			}
			if seen[s] {
				return nil, fmt.Sprintf("%q is listed twice", s)
			}
			seen[s] = true
			out = append(out, s)
		}
		return out, ""
	}
	return nil, fmt.Sprintf("a list ([a, b]), not a %s", kind(v))
}

// mapping is a nested mapping, nil when the key is missing or empty.
func (d decoder) mapping(key string) *yamlMap {
	v, _ := d.m.Get(key)
	switch v := v.(type) {
	case nil:
		return nil
	case *yamlMap:
		return v
	}
	d.errorf(key, "%s is a mapping (key: value lines below it), not a %s", key, kind(v))
	return nil
}

// sub is a decoder of the mapping under key.
func (d decoder) sub(key string) (decoder, bool) {
	m := d.mapping(key)
	if m == nil {
		return decoder{}, false
	}
	return decoder{c: d.c, file: d.file, m: m}, true
}

// duration is a Go duration (45m, 1h30m) above zero, def when the key is
// missing.
func (d decoder) duration(key string, def time.Duration) time.Duration {
	s := d.str(key)
	if s == "" {
		return def
	}
	v, err := time.ParseDuration(s)
	if err != nil || v <= 0 {
		d.errorf(key, "%s is a duration such as 45m or 1h30m, not %q", key, s)
		return def
	}
	return v
}

// positive is a whole number of at least 1, def when the key is missing.
func (d decoder) positive(key string, def int) int {
	s := d.str(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		d.errorf(key, "%s is a whole number of at least 1, not %q", key, s)
		return def
	}
	return v
}

// oneOf is a value out of choices, def when the key is missing.
func (d decoder) oneOf(key, def string, choices ...string) string {
	s := d.str(key)
	if s == "" {
		return def
	}
	for _, c := range choices {
		if s == c {
			return s
		}
	}
	d.errorf(key, "%s is one of %s, not %q", key, strings.Join(choices, ", "), s)
	return def
}

// paramName is a name a tool gets as SOCRATES_TEST_PARAM_<NAME>.
var paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// params reads a flat mapping of names to single values into dst (keeping
// the order of first appearance in order).
func (d decoder) params(key string, dst map[string]string, order []string) []string {
	m := d.mapping(key)
	if m == nil {
		return order
	}
	for _, k := range m.Keys() {
		v, _ := m.Get(k)
		var s string
		switch v := v.(type) {
		case nil:
		case string:
			s = v
		default:
			d.c.errorf(d.file, m.Line(k), "params: %s is a single value, not a %s", k, kind(v))
			continue
		}
		if msg := envNameProblem(k, paramName); msg != "" {
			d.c.errorf(d.file, m.Line(k), "params: %q %s", k, msg)
			continue
		}
		if _, ok := dst[k]; !ok {
			order = append(order, k)
		}
		dst[k] = s
	}
	return order
}

// envNameProblem explains why name cannot be part of an environment
// variable's name the tools get, or returns "". Codex drops every variable
// whose name holds KEY, SECRET or TOKEN from the commands it runs, so such a
// name would reach the tool under one harness and not under another.
func envNameProblem(name string, valid *regexp.Regexp) string {
	if !valid.MatchString(name) {
		return "is not a valid name: letters, digits and _, not starting with a digit"
	}
	up := strings.ToUpper(name)
	for _, word := range []string{"KEY", "SECRET", "TOKEN"} {
		if strings.Contains(up, word) {
			return "may not hold KEY, SECRET or TOKEN: Codex drops such variables from the commands it runs"
		}
	}
	return ""
}

// kind names a value's YAML shape for an error message.
func kind(v any) string {
	switch v.(type) {
	case *yamlMap:
		return "mapping"
	case []any:
		return "list"
	case nil:
		return "empty value"
	}
	return "single value"
}
