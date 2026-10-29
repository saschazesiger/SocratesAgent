package catalog

import (
	"fmt"
	"strings"
)

// The front matter of a catalog file is a strict subset of YAML, parsed here
// rather than by a library: the binary takes only MIT, BSD and ISC code, and
// the subset is all a catalog needs (TEST-MODE.md, "The catalog"). What it
// reads: block mappings and sequences with spaces for indentation, flow
// sequences and mappings ([a, b], {except: [a]}, nested), plain, single- and
// double-quoted scalars, comments and empty values. What YAML has beyond that
// (anchors, aliases, tags, block scalars, several documents, tabs for
// indentation, a key twice) is an error with its line, never a silent guess.
//
// Every scalar comes back as a string (no numbers, booleans or nulls: the
// catalog decides what a value means), an empty value as nil, a sequence as
// []any and a mapping as *yamlMap, which keeps the order its keys were
// written in (a dimension's values are its columns, in that order).

// yamlMap is a mapping with its keys in the order they were written and the
// line each key is on, so a problem with a value can name its line.
type yamlMap struct {
	keys  []string
	vals  map[string]any
	lines map[string]int
	line  int // the line the mapping starts on
}

func newYAMLMap(line int) *yamlMap {
	return &yamlMap{vals: map[string]any{}, lines: map[string]int{}, line: line}
}

// Keys returns the keys in the order they were written.
func (m *yamlMap) Keys() []string { return m.keys }

// Get returns the value of key and whether the mapping has it.
func (m *yamlMap) Get(key string) (any, bool) {
	v, ok := m.vals[key]
	return v, ok
}

// Line is the line of key, or the mapping's own line when it lacks the key.
func (m *yamlMap) Line(key string) int {
	if n, ok := m.lines[key]; ok {
		return n
	}
	return m.line
}

// Len is the number of keys.
func (m *yamlMap) Len() int { return len(m.keys) }

// set adds key, refusing a key the mapping already has: YAML would keep the
// last one, which hides the first.
func (m *yamlMap) set(key string, v any, line int) error {
	if _, dup := m.vals[key]; dup {
		return &yamlError{line, fmt.Sprintf("%q is given twice (first on line %d)", key, m.lines[key])}
	}
	m.keys = append(m.keys, key)
	m.vals[key] = v
	m.lines[key] = line
	return nil
}

// yamlError is a front-matter problem on one line of the file.
type yamlError struct {
	Line int
	Msg  string
}

func (e *yamlError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// splitFrontMatter separates a file's front matter from its body. The front
// matter is the text between a first line `---` and the next `---` line; a
// file that does not start with `---` has none and is all body. frontLine and
// bodyLine are the file's line numbers of the first line of each, so the
// parsers below can report the file's own lines.
func splitFrontMatter(src string) (front string, frontLine int, body string, bodyLine int, err error) {
	src = strings.TrimPrefix(src, "\uFEFF")
	lines := strings.SplitAfter(src, "\n")
	if len(lines) == 0 || !isFence(lines[0]) {
		return "", 0, src, 1, nil
	}
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			return strings.Join(lines[1:i], ""), 2, strings.Join(lines[i+1:], ""), i + 2, nil
		}
	}
	return "", 0, "", 0, &yamlError{1, "the front matter is not closed: a line --- must end it"}
}

func isFence(line string) bool {
	return strings.TrimRight(line, " \t\r\n") == "---"
}

// yamlLine is one line that holds something: its indentation, and its text
// without the indentation, a trailing comment and trailing blanks.
type yamlLine struct {
	n      int
	indent int
	text   string
}

type yamlParser struct {
	lines []yamlLine
	pos   int
}

// parseYAML parses front matter into a mapping; firstLine is the file's line
// number of src's first line. Empty front matter is an empty mapping.
func parseYAML(src string, firstLine int) (*yamlMap, error) {
	var p yamlParser
	for i, raw := range strings.Split(src, "\n") {
		n := firstLine + i
		raw = strings.TrimRight(raw, "\r")
		j := 0
		for j < len(raw) && raw[j] == ' ' {
			j++
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if raw[j] == '\t' {
			return nil, &yamlError{n, "a tab in the indentation: indent with spaces"}
		}
		text := strings.TrimRight(stripComment(raw[j:]), " \t")
		if text == "" {
			continue
		}
		if j == 0 && (text == "---" || text == "...") {
			return nil, &yamlError{n, "only one document: " + text + " is not supported here"}
		}
		p.lines = append(p.lines, yamlLine{n: n, indent: j, text: text})
	}
	if len(p.lines) == 0 {
		return newYAMLMap(firstLine), nil
	}
	first := p.lines[0]
	if isSeqItem(first.text) || first.text[0] == '[' || first.text[0] == '{' {
		return nil, &yamlError{first.n, "the front matter is a list of `key: value` lines"}
	}
	v, err := p.block(first.indent)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		return nil, &yamlError{p.lines[p.pos].n, "unexpected indentation"}
	}
	return v.(*yamlMap), nil
}

// stripComment drops a comment: a # at the start or after a blank, outside
// quotes. A quote opens a quoted scalar only where one can start (after a
// key's colon, a list dash or a flow bracket or comma), so the apostrophe of
// a plain "the cell's locale" is just a letter.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case quote == '\'':
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		case (c == '"' || c == '\'') && quoteOpens(s, i):
			quote = c
		}
	}
	return s
}

func quoteOpens(s string, i int) bool {
	j := i - 1
	if j >= 0 && s[j] != ' ' && s[j] != '\t' && !strings.ContainsRune("[{,", rune(s[j])) {
		return false
	}
	for j >= 0 && (s[j] == ' ' || s[j] == '\t') {
		j--
	}
	return j < 0 || strings.ContainsRune(":-[{,", rune(s[j]))
}

func isSeqItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

// isMapEntry reports whether text is a `key: value` (or `key:`) line.
func isMapEntry(text string) bool {
	if text == "" || text[0] == '[' || text[0] == '{' {
		return false
	}
	if text[0] == '"' || text[0] == '\'' {
		_, n, err := parseQuoted(text)
		return err == nil && strings.HasPrefix(text[n:], ":") && (len(text) == n+1 || text[n+1] == ' ')
	}
	return keyColon(text) >= 0
}

// keyColon is the index of the colon that ends a plain key: the first one
// followed by a blank or the end of the line, or -1.
func keyColon(text string) int {
	for i := 0; i < len(text); i++ {
		if text[i] == ':' && (i+1 == len(text) || text[i+1] == ' ') {
			return i
		}
	}
	return -1
}

// block parses the mapping or sequence whose lines start at p.pos, indented
// by indent.
func (p *yamlParser) block(indent int) (any, error) {
	if isSeqItem(p.lines[p.pos].text) {
		return p.sequence(indent)
	}
	return p.mapping(indent)
}

func (p *yamlParser) mapping(indent int) (*yamlMap, error) {
	m := newYAMLMap(p.lines[p.pos].n)
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, &yamlError{l.n, "unexpected indentation"}
		}
		if isSeqItem(l.text) {
			return nil, &yamlError{l.n, "a list item where a `key: value` line was expected"}
		}
		key, rest, err := splitKey(l)
		if err != nil {
			return nil, err
		}
		p.pos++
		v, err := p.value(l, indent, rest, true)
		if err != nil {
			return nil, err
		}
		if err := m.set(key, v, l.n); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// splitKey splits a `key: value` line into its key and the value's text.
func splitKey(l yamlLine) (key, rest string, err error) {
	t := l.text
	if t[0] == '"' || t[0] == '\'' {
		k, n, err := parseQuoted(t)
		if err != nil {
			return "", "", &yamlError{l.n, err.Error()}
		}
		after := t[n:]
		if !strings.HasPrefix(after, ":") || (len(after) > 1 && after[1] != ' ') {
			return "", "", &yamlError{l.n, "expected `key: value`"}
		}
		return k, strings.TrimSpace(after[1:]), nil
	}
	if msg := badStart(t); msg != "" {
		return "", "", &yamlError{l.n, msg}
	}
	i := keyColon(t)
	if i < 0 {
		return "", "", &yamlError{l.n, "expected `key: value`"}
	}
	key = strings.TrimSpace(t[:i])
	if key == "" {
		return "", "", &yamlError{l.n, "a key is missing before the colon"}
	}
	return key, strings.TrimSpace(t[i+1:]), nil
}

// value parses what follows a key's colon or a list dash: rest on the line,
// or, when rest is empty, the block indented below it (for a key also a list
// at the key's own indentation, as YAML allows), or nil.
func (p *yamlParser) value(l yamlLine, indent int, rest string, key bool) (any, error) {
	if rest != "" {
		return p.inline(l, indent, rest)
	}
	if p.pos < len(p.lines) {
		next := p.lines[p.pos]
		if next.indent > indent {
			return p.block(next.indent)
		}
		if key && next.indent == indent && isSeqItem(next.text) {
			return p.sequence(indent)
		}
	}
	return nil, nil
}

func (p *yamlParser) sequence(indent int) ([]any, error) {
	items := []any{}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent || (l.indent == indent && !isSeqItem(l.text)) {
			break
		}
		if l.indent > indent {
			return nil, &yamlError{l.n, "unexpected indentation"}
		}
		content := strings.TrimLeft(l.text[1:], " ")
		var v any
		var err error
		switch {
		case content == "":
			p.pos++
			v, err = p.value(l, indent, "", false)
		case isSeqItem(content) || isMapEntry(content):
			// The item's content starts a block of its own at its column:
			// `- name: a` continued by `  label: b`.
			col := indent + len(l.text) - len(content)
			p.lines[p.pos] = yamlLine{n: l.n, indent: col, text: content}
			v, err = p.block(col)
		default:
			p.pos++
			v, err = p.inline(l, indent, content)
		}
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, nil
}

// inline parses a value written on the line itself: a flow collection (which
// may go on over the following, deeper indented lines until its brackets
// close), a quoted scalar or a plain one.
func (p *yamlParser) inline(l yamlLine, indent int, text string) (any, error) {
	switch text[0] {
	case '[', '{':
		for flowOpen(text) && p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
			text += " " + p.lines[p.pos].text
			p.pos++
		}
		f := flowParser{s: text, line: l.n}
		v, err := f.value()
		if err != nil {
			return nil, err
		}
		f.skip()
		if f.i < len(f.s) {
			return nil, &yamlError{l.n, fmt.Sprintf("unexpected %q after the closing bracket", f.s[f.i:])}
		}
		return v, nil
	case '"', '\'':
		s, n, err := parseQuoted(text)
		if err != nil {
			return nil, &yamlError{l.n, err.Error()}
		}
		if strings.TrimSpace(text[n:]) != "" {
			return nil, &yamlError{l.n, "unexpected text after the closing quote"}
		}
		return s, nil
	}
	if msg := badStart(text); msg != "" {
		return nil, &yamlError{l.n, msg}
	}
	if isSeqItem(text) {
		return nil, &yamlError{l.n, "a list item goes on a line of its own"}
	}
	if keyColon(text) >= 0 {
		return nil, &yamlError{l.n, "a value with \": \" in it must be quoted"}
	}
	return text, nil
}

// badStart explains why a plain scalar or key cannot start as text does: the
// character stands for YAML that this subset does not read.
func badStart(text string) string {
	switch text[0] {
	case '&', '*':
		return "anchors and aliases (& and *) are not supported; quote a value that starts with one"
	case '!':
		return "tags (!) are not supported; quote a value that starts with !"
	case '|', '>':
		return "block scalars (| and >) are not supported: write the value on one line, quoted if needed"
	case '%', '@', '`':
		return fmt.Sprintf("a value cannot start with %c; quote it", text[0])
	case '?':
		if text == "?" || strings.HasPrefix(text, "? ") {
			return "complex keys (?) are not supported"
		}
	case ']', '}', ',':
		return fmt.Sprintf("unexpected %c; quote a value that starts with it", text[0])
	}
	return ""
}

// flowOpen reports whether a flow collection's brackets are still open at
// the end of s.
func flowOpen(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		case '"', '\'':
			if _, n, err := parseQuoted(s[i:]); err == nil {
				i += n - 1
			}
		}
	}
	return depth > 0
}

// parseQuoted reads the quoted scalar at the start of s and returns it with
// the number of bytes it took. Double quotes take the escapes \" \\ \n \t;
// single quotes take a doubled quote for one and nothing else.
func parseQuoted(s string) (string, int, error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'' && c == '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), i + 1, nil
		case q == '"' && c == '"':
			return b.String(), i + 1, nil
		case q == '"' && c == '\\':
			if i+1 == len(s) {
				return "", 0, fmt.Errorf("the quoted value is not closed")
			}
			i++
			switch s[i] {
			case '"', '\\', '/':
				b.WriteByte(s[i])
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", 0, fmt.Errorf("the escape \\%c is not supported (only \\\" \\\\ \\n \\t)", s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, fmt.Errorf("the quoted value is not closed (a quoted value stays on one line)")
}

// flowParser reads a flow collection: [a, b] and {k: v}, nested.
type flowParser struct {
	s    string
	i    int
	line int
}

func (f *flowParser) fail(format string, a ...any) error {
	return &yamlError{f.line, fmt.Sprintf(format, a...)}
}

func (f *flowParser) skip() {
	for f.i < len(f.s) && f.s[f.i] == ' ' {
		f.i++
	}
}

func (f *flowParser) value() (any, error) {
	f.skip()
	if f.i == len(f.s) {
		return nil, f.fail("a bracket is not closed")
	}
	switch c := f.s[f.i]; c {
	case '[':
		return f.sequence()
	case '{':
		return f.mapping()
	case '"', '\'':
		s, n, err := parseQuoted(f.s[f.i:])
		if err != nil {
			return nil, f.fail("%v", err)
		}
		f.i += n
		return s, nil
	}
	return f.plain(false)
}

// plain reads a plain scalar up to a flow indicator, or, for a key, up to
// its colon. An empty one is nil.
func (f *flowParser) plain(key bool) (any, error) {
	start := f.i
	for f.i < len(f.s) {
		c := f.s[f.i]
		if c == ',' || c == '[' || c == ']' || c == '{' || c == '}' {
			break
		}
		if c == ':' && (f.i+1 == len(f.s) || strings.ContainsRune(" ,[]{}", rune(f.s[f.i+1]))) {
			if key {
				break
			}
			return nil, f.fail("a value with \": \" in it must be quoted")
		}
		f.i++
	}
	text := strings.TrimSpace(f.s[start:f.i])
	if text == "" {
		return nil, nil
	}
	if msg := badStart(text); msg != "" {
		return nil, f.fail("%s", msg)
	}
	return text, nil
}

func (f *flowParser) sequence() ([]any, error) {
	f.i++ // [
	items := []any{}
	for {
		f.skip()
		if f.i == len(f.s) {
			return nil, f.fail("a [ is not closed")
		}
		if f.s[f.i] == ']' {
			f.i++
			return items, nil
		}
		v, err := f.value()
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, f.fail("an empty item in a [list]")
		}
		items = append(items, v)
		if err := f.separator(']'); err != nil {
			return nil, err
		}
	}
}

func (f *flowParser) mapping() (*yamlMap, error) {
	f.i++ // {
	m := newYAMLMap(f.line)
	for {
		f.skip()
		if f.i == len(f.s) {
			return nil, f.fail("a { is not closed")
		}
		if f.s[f.i] == '}' {
			f.i++
			return m, nil
		}
		var key string
		if c := f.s[f.i]; c == '"' || c == '\'' {
			k, n, err := parseQuoted(f.s[f.i:])
			if err != nil {
				return nil, f.fail("%v", err)
			}
			f.i += n
			key = k
		} else {
			k, err := f.plain(true)
			if err != nil {
				return nil, err
			}
			if k == nil {
				return nil, f.fail("a key is missing in a {mapping}")
			}
			key = k.(string)
		}
		f.skip()
		if f.i == len(f.s) || f.s[f.i] != ':' {
			return nil, f.fail("expected a colon after %q in a {mapping}", key)
		}
		f.i++
		f.skip()
		var v any
		if f.i < len(f.s) && f.s[f.i] != ',' && f.s[f.i] != '}' {
			var err error
			if v, err = f.value(); err != nil {
				return nil, err
			}
		}
		if err := m.set(key, v, f.line); err != nil {
			return nil, err
		}
		if err := f.separator('}'); err != nil {
			return nil, err
		}
	}
}

// separator consumes the comma after an item, or leaves the closing bracket
// for the caller.
func (f *flowParser) separator(closing byte) error {
	f.skip()
	if f.i == len(f.s) {
		return f.fail("a bracket is not closed: %c is missing", closing)
	}
	switch f.s[f.i] {
	case ',':
		f.i++
		return nil
	case closing:
		return nil
	}
	return f.fail("expected a comma or %c, found %q", closing, f.s[f.i:])
}
