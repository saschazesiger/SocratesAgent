package catalog

import (
	"fmt"
	"strings"
	"testing"
)

// dump writes a parsed value in one line, keys in their order, so a table
// can compare it: {k: "v", l: ["a" "b"], e: nil}.
func dump(v any) string {
	switch v := v.(type) {
	case nil:
		return "nil"
	case string:
		return fmt.Sprintf("%q", v)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = dump(item)
		}
		return "[" + strings.Join(parts, " ") + "]"
	case *yamlMap:
		parts := make([]string, 0, v.Len())
		for _, k := range v.Keys() {
			val, _ := v.Get(k)
			parts = append(parts, k+": "+dump(val))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprintf("?%T", v)
}

// Every construct of the subset reads as YAML would read it, every scalar
// as a string and every mapping in the order its keys were written.
func TestFrontMatterReadsTheSubset(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"empty", "", `{}`},
		{"only comments", "# a\n\n  # b\n", `{}`},
		{"plain scalars", "title: The header leads\npriority: P0\nn: 1\nb: true", `{title: "The header leads", priority: "P0", n: "1", b: "true"}`},
		{"order is kept", "z: 1\na: 2\nm: 3", `{z: "1", a: "2", m: "3"}`},
		{"empty value", "a:\nb: x", `{a: nil, b: "x"}`},
		{"trailing comment", "a: b # c\nd: e#f", `{a: "b", d: "e#f"}`},
		{"apostrophe in a plain value", "a: the cell's locale # c", `{a: "the cell's locale"}`},
		{"colon without blank", "url: https://beta.browser.lol/x", `{url: "https://beta.browser.lol/x"}`},
		{"double quoted", `a: "x \"y\" \\ z\n# not a comment"`, `{a: "x \"y\" \\ z\n# not a comment"}`},
		{"single quoted", `a: 'it''s # here'`, `{a: "it's # here"}`},
		{"quoted key", `"a b": c`, `{a b: "c"}`},
		{"quoted with colon", `a: "k: v"`, `{a: "k: v"}`},
		{"nested mapping", "a:\n  b: c\n  d:\n    e: f\ng: h", `{a: {b: "c", d: {e: "f"}}, g: "h"}`},
		{"any consistent indentation", "a:\n    b: c\n    d: e", `{a: {b: "c", d: "e"}}`},
		{"block sequence", "a:\n  - x\n  - y z\nb: c", `{a: ["x" "y z"], b: "c"}`},
		{"sequence at the key's indentation", "a:\n- x\n- y\nb: c", `{a: ["x" "y"], b: "c"}`},
		{"sequence of mappings", "a:\n  - name: x\n    label: X\n  - name: y", `{a: [{name: "x", label: "X"} {name: "y"}]}`},
		{"sequence of sequences", "a:\n  - - x\n    - y\n  - z", `{a: [["x" "y"] "z"]}`},
		{"empty item and nested block", "a:\n  -\n    b: c\n  -", `{a: [{b: "c"} nil]}`},
		{"quoted item", `a: ["x, y", 'z']`, `{a: ["x, y" "z"]}`},
		{"flow sequence", "a: [a, b, \"c d\"]", `{a: ["a" "b" "c d"]}`},
		{"empty flow sequence", "a: []", `{a: []}`},
		{"trailing comma", "a: [a, b, ]", `{a: ["a" "b"]}`},
		{"flow mapping", "a: {except: [a, b], k: v}", `{a: {except: ["a" "b"], k: "v"}}`},
		{"nested flow", "a: [{x: [1, {y: z}]}, []]", `{a: [{x: ["1" {y: "z"}]} []]}`},
		{"flow empty value", "a: {k: , l: }", `{a: {k: nil, l: nil}}`},
		{"flow with url", "a: {u: https://x.y/z}", `{a: {u: "https://x.y/z"}}`},
		{"flow over lines", "a: [x,\n  y,\n  z]\nb: c", `{a: ["x" "y" "z"], b: "c"}`},
		{"flow mapping of labels", "values: {en: English, de: German}", `{values: {en: "English", de: "German"}}`},
		{"unicode", "iphone: iPhone 15 · Safari", `{iphone: "iPhone 15 · Safari"}`},
		{"secret names", "names: [A, B?, C?]", `{names: ["A" "B?" "C?"]}`},
		{"crlf", "a: b\r\nc: d\r\n", `{a: "b", c: "d"}`},
		{"tab inside a value", "a: x\ty", `{a: "x\ty"}`},
	} {
		m, err := parseYAML(tc.src, 1)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := dump(m); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// What the subset does not read is an error on its line, never a guess.
func TestFrontMatterRefusesWhatItDoesNotRead(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		line      int
		want      string
	}{
		{"anchor", "a: &x b", 1, "anchors"},
		{"alias", "a: b\nc: *x", 2, "anchors"},
		{"tag", "a: !!str b", 1, "tags"},
		{"literal block", "a: |\n  x", 1, "block scalars"},
		{"folded block", "a: >\n  x", 1, "block scalars"},
		{"second document", "a: b\n---\nc: d", 2, "one document"},
		{"document end", "a: b\n...", 2, "one document"},
		{"directive", "a: %YAML", 1, "cannot start with %"},
		{"tab indentation", "a:\n\tb: c", 2, "tab"},
		{"tab after spaces", "a:\n  \tb: c", 2, "tab"},
		{"duplicate key", "a: b\nc: d\na: e", 3, `"a" is given twice (first on line 1)`},
		{"duplicate in flow", "a: {b: 1, b: 2}", 1, "given twice"},
		{"duplicate nested", "a:\n  b: 1\n  b: 2", 3, "given twice"},
		{"no colon", "a: b\njust text", 2, "expected `key: value`"},
		{"colon without blank", "a:b", 1, "expected `key: value`"},
		{"bad indentation", "a:\n    b: c\n  d: e", 3, "unexpected indentation"},
		{"over-indented", "a: b\n  c: d", 2, "unexpected indentation"},
		{"top-level list", "- a\n- b", 1, "key: value"},
		{"list among keys", "a: b\n- c", 2, "list item"},
		{"unclosed bracket", "a: [x, y", 1, "not closed"},
		{"unclosed quote", `a: "x`, 1, "not closed"},
		{"text after quote", `a: "x" y`, 1, "after the closing quote"},
		{"text after bracket", "a: [x] y", 1, "after the closing bracket"},
		{"bad escape", `a: "\q"`, 1, `\q`},
		{"colon in a plain value", "a: b: c", 1, "must be quoted"},
		{"colon in a flow value", "a: [b: c]", 1, "must be quoted"},
		{"empty flow item", "a: [x, , y]", 1, "empty item"},
		{"flow key without colon", "a: {b}", 1, "expected a colon"},
		{"missing comma", "a: [x y, [z] w]", 1, "comma"},
		{"complex key", "? a\n: b", 1, "complex keys"},
		{"dash value", "a: - b", 1, "line of its own"},
		{"empty key", ": b", 1, "key is missing"},
	} {
		_, err := parseYAML(tc.src, 1)
		ye, ok := err.(*yamlError)
		if !ok {
			t.Errorf("%s: %v, want a yamlError", tc.name, err)
			continue
		}
		if ye.Line != tc.line || !strings.Contains(ye.Msg, tc.want) {
			t.Errorf("%s: line %d %q, want line %d with %q", tc.name, ye.Line, ye.Msg, tc.line, tc.want)
		}
	}
}

// The front matter is the text between a first line --- and the next one,
// with the file's line numbers; a file without it is all body.
func TestFrontMatterIsSplitOffTheBody(t *testing.T) {
	front, fl, body, bl, err := splitFrontMatter("---\ntitle: x\n---\nbody\n")
	if err != nil || front != "title: x\n" || fl != 2 || body != "body\n" || bl != 4 {
		t.Fatalf("got %q %d %q %d %v", front, fl, body, bl, err)
	}
	front, _, body, bl, err = splitFrontMatter("\uFEFF---\r\n---  \r\nbody")
	if err != nil || front != "" || body != "body" || bl != 3 {
		t.Fatalf("bom/crlf: got %q %q %d %v", front, body, bl, err)
	}
	front, _, body, bl, err = splitFrontMatter("# Title\n---\nx")
	if err != nil || front != "" || body != "# Title\n---\nx" || bl != 1 {
		t.Fatalf("none: got %q %q %d %v", front, body, bl, err)
	}
	if _, _, _, _, err := splitFrontMatter("---\ntitle: x\n"); err == nil || !strings.Contains(err.Error(), "not closed") {
		t.Fatalf("unclosed: %v", err)
	}
	// The line numbers of a problem are the file's own.
	front, fl, _, _, _ = splitFrontMatter("---\na: b\nc: &d\n---\n")
	if _, err := parseYAML(front, fl); err == nil || err.(*yamlError).Line != 3 {
		t.Fatalf("line: %v", err)
	}
}

// A comment starts at a # after a blank outside quotes, and a quote opens
// only where a quoted value can start.
func TestCommentsAreStrippedOutsideQuotes(t *testing.T) {
	for in, want := range map[string]string{
		"a: b # c":            "a: b ",
		"# all":               "",
		"a: b#c":              "a: b#c",
		`a: "b # c" # d`:      `a: "b # c" `,
		`a: 'b # c' # d`:      `a: 'b # c' `,
		`a: 'it''s # x' # d`:  `a: 'it''s # x' `,
		"a: it's # c":         "a: it's ",
		`a: [x, "y # z"] # c`: `a: [x, "y # z"] `,
		`- "a\" # b" # c`:     `- "a\" # b" `,
	} {
		if got := stripComment(in); got != want {
			t.Errorf("stripComment(%q) = %q, want %q", in, got, want)
		}
	}
}
