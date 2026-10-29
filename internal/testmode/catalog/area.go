package catalog

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// idSegment is one part of a test's id (a folder or the file's name without
// .md): the id is in cell keys (id@device.locale), URLs and variables.
var idSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// dimensionName is a dimension's name: a front-matter key of its tests and
// part of SOCRATES_TEST_DIM_<NAME>.
var dimensionName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// valueID is a dimension value's id; values are joined by "." in a cell's
// key, so an id holds none.
var valueID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// testKeys are the front-matter keys of a test; the area's dimension names
// come on top and may not be one of them.
var testKeys = []string{"title", "priority", "tags", "tools", "exclusive", "params", "timeout", "previous"}

// exclusiveName is a name of shared test data in exclusive.
var exclusiveName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// loadArea reads a top-level folder: its AREA.md and its tests.
func (c *Catalog) loadArea(id string) {
	if !idSegment.MatchString(id) {
		c.errorf(id+"/", 0, "a folder's name is part of its tests' ids: letters, digits, -, _ and .")
		return
	}
	if fi, err := os.Lstat(filepath.Join(c.Dir, id, AreaFile)); err != nil || !fi.Mode().IsRegular() {
		c.errorf(id+"/", 0, "a folder of the catalog needs an %s (or it is not an area and its tests would be lost)", AreaFile)
		return
	}
	a := &Area{ID: id, Params: map[string]string{}}
	rel := id + "/" + AreaFile
	front, body, _, _ := c.readFile(rel)
	a.lines = front.lines
	d := decoder{c: c, file: rel, m: front}
	d.keys("title", "tools", "columns", "dimensions", "params", "evidence", "mcp", "timeout")
	a.Title = d.str("title")
	if a.Title == "" {
		a.Title = id
	}
	a.Instructions = strings.TrimSpace(body)
	a.Tools = d.list("tools")
	a.ParamOrder = d.params("params", a.Params, nil)
	a.Evidence = d.oneOf("evidence", EvidenceScreenshot, EvidenceScreenshot, EvidenceAny, EvidenceNone)
	a.MCP = d.list("mcp")
	a.Timeout = d.duration("timeout", DefaultTimeout)
	c.loadDimensions(a, d)

	a.Columns = d.str("columns")
	switch {
	case a.Columns == "" && len(a.Dimensions) > 0:
		a.Columns = a.Dimensions[0].Name
	case a.Columns != "" && a.dimension(a.Columns) == nil:
		d.errorf("columns", "columns names a dimension of the area, and %q is none", a.Columns)
		a.Columns = ""
		if len(a.Dimensions) > 0 {
			a.Columns = a.Dimensions[0].Name
		}
	}

	c.Areas = append(c.Areas, a)
	c.areas[id] = a
	c.loadAreaTests(a)
}

// dimension returns the area's dimension called name, or nil.
func (a *Area) dimension(name string) *Dimension {
	for _, d := range a.Dimensions {
		if d.Name == name {
			return d
		}
	}
	return nil
}

func (c *Catalog) loadDimensions(a *Area, d decoder) {
	dims := d.mapping("dimensions")
	if dims == nil {
		return
	}
	rotates := 0
	for _, name := range dims.Keys() {
		line := dims.Line(name)
		if !dimensionName.MatchString(name) {
			c.errorf(d.file, line, "dimensions: %q is not a dimension's name: lower-case letters, digits and _, starting with a letter", name)
			continue
		}
		if msg := envNameProblem(name, dimensionName); msg != "" {
			c.errorf(d.file, line, "dimensions: %q %s", name, msg)
			continue
		}
		if contains(testKeys, name) {
			c.errorf(d.file, line, "dimensions: %q is a key of every test and cannot name a dimension", name)
			continue
		}
		sub := decoder{c: c, file: d.file, m: newYAMLMap(line)}
		if v, _ := dims.Get(name); v != nil {
			m, ok := v.(*yamlMap)
			if !ok {
				c.errorf(d.file, line, "dimensions: %s is a mapping with values (and extra, default)", name)
				continue
			}
			sub.m = m
		}
		sub.keys("values", "extra", "default")
		dim := &Dimension{Name: name}
		dim.Values = sub.values("values", name, nil)
		dim.Extra = sub.values("extra", name, dim.Values)
		if len(dim.Values) == 0 {
			sub.errorf("values", "dimensions: %s has no values (id: label lines below values)", name)
			continue
		}
		dim.Default = Selection{Kind: SelectAll}
		if v, ok := sub.m.Get("default"); ok && v != nil {
			sel, msg := parseSelection(v, dim)
			if msg != "" {
				sub.errorf("default", "dimensions: %s.default: %s", name, msg)
			} else {
				dim.Default = sel
			}
		}
		if dim.Default.Kind == SelectRotate {
			rotates++
			if rotates == 2 {
				sub.errorf("default", "dimensions: only one dimension may rotate")
				dim.Default = Selection{Kind: SelectAll}
			}
		}
		a.Dimensions = append(a.Dimensions, dim)
	}
}

// values reads a dimension's ordered id: label mapping (or a list of ids,
// each its own label). An id may not be one of taken.
func (d decoder) values(key, dim string, taken []Value) []Value {
	v, _ := d.m.Get(key)
	var out []Value
	add := func(id, label string, line int) {
		switch {
		case !valueID.MatchString(id):
			d.c.errorf(d.file, line, "dimensions: %s: %q is not a value id (letters, digits, - and _)", dim, id)
		case id == SelectAll || id == SelectRotate || id == SelectExcept:
			d.c.errorf(d.file, line, "dimensions: %s: %q is a word of the selections and cannot be a value", dim, id)
		case hasValue(taken, id) || hasValue(out, id):
			d.c.errorf(d.file, line, "dimensions: %s: %q is defined twice", dim, id)
		default:
			if label == "" {
				label = id
			}
			out = append(out, Value{ID: id, Label: label})
		}
	}
	switch v := v.(type) {
	case nil:
	case *yamlMap:
		for _, id := range v.Keys() {
			label, ok := v.vals[id].(string)
			if !ok && v.vals[id] != nil {
				d.c.errorf(d.file, v.Line(id), "dimensions: %s: the label of %s is a single value", dim, id)
				continue
			}
			add(id, label, v.Line(id))
		}
	case []any:
		ids, msg := stringList(v)
		if msg != "" {
			d.errorf(key, "dimensions: %s.%s: %s", dim, key, msg)
		}
		for _, id := range ids {
			add(id, "", d.m.Line(key))
		}
	default:
		d.errorf(key, "dimensions: %s.%s is a mapping of id: label lines", dim, key)
	}
	return out
}

func hasValue(vs []Value, id string) bool {
	for _, v := range vs {
		if v.ID == id {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// parseSelection reads a dimension's selection: all, rotate, a list of ids
// (one id alone is a list of one) or {except: [ids]}. It explains what is
// wrong, or returns "".
func parseSelection(v any, dim *Dimension) (Selection, string) {
	switch v := v.(type) {
	case string:
		if v == SelectAll || v == SelectRotate {
			return Selection{Kind: v}, ""
		}
	case *yamlMap:
		ex, ok := v.Get(SelectExcept)
		if !ok || v.Len() != 1 {
			return Selection{}, "all, rotate, a list of values or {except: [values]}"
		}
		ids, msg := stringList(ex)
		if msg != "" {
			return Selection{}, "except: " + msg
		}
		if len(ids) == 0 {
			return Selection{}, "except names no value"
		}
		for _, id := range ids {
			if !hasValue(dim.Values, id) && !hasValue(dim.Extra, id) {
				return Selection{}, unknownValue(dim, id)
			}
		}
		sel := Selection{Kind: SelectExcept, IDs: ids}
		if len(dim.selected(sel)) == 0 {
			return Selection{}, "except leaves no value"
		}
		return sel, ""
	}
	ids, msg := stringList(v)
	if msg != "" {
		return Selection{}, "all, rotate, a list of values or {except: [values]}"
	}
	if len(ids) == 0 {
		return Selection{}, "selects no value"
	}
	for _, id := range ids {
		if !hasValue(dim.Values, id) && !hasValue(dim.Extra, id) {
			return Selection{}, unknownValue(dim, id)
		}
	}
	return Selection{Kind: SelectList, IDs: ids}, ""
}

func unknownValue(dim *Dimension, id string) string {
	var ids []string
	for _, v := range append(append([]Value{}, dim.Values...), dim.Extra...) {
		ids = append(ids, v.ID)
	}
	return "\"" + id + "\" is not a value of " + dim.Name + " (" + strings.Join(ids, ", ") + ")"
}

// loadAreaTests reads every test below the area's folder: each *.md but
// AREA.md and README.md, hidden files and folders and symbolic links left
// out. The tests are sorted by id, which is what rotation counts by.
func (c *Catalog) loadAreaTests(a *Area) {
	base := filepath.Join(c.Dir, a.ID)
	_ = filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
		relOS, _ := filepath.Rel(c.Dir, p)
		rel := filepath.ToSlash(relOS)
		if err != nil {
			c.errorf(rel, 0, "%v", err)
			return nil
		}
		name := e.Name()
		if p == base {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if e.Type()&fs.ModeSymlink != 0 {
			c.warnf(rel, 0, "a symbolic link is not part of the catalog (it is not copied into a run)")
			return nil
		}
		if e.IsDir() {
			if !idSegment.MatchString(name) {
				c.errorf(rel+"/", 0, "a folder's name is part of its tests' ids: letters, digits, -, _ and .")
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".md") || name == readme || !e.Type().IsRegular() {
			return nil
		}
		if name == AreaFile {
			if path.Dir(rel) != a.ID {
				c.errorf(rel, 0, "only the area's own folder holds an %s; a sub-folder only groups tests", AreaFile)
			}
			return nil
		}
		if !idSegment.MatchString(strings.TrimSuffix(name, ".md")) {
			c.errorf(rel, 0, "a test's file name is part of its id: letters, digits, -, _ and .")
			return nil
		}
		a.Tests = append(a.Tests, c.loadTest(a, rel))
		return nil
	})
	sort.Slice(a.Tests, func(i, j int) bool { return a.Tests[i].ID < a.Tests[j].ID })
	for i, t := range a.Tests {
		t.Index = i
		c.tests[t.ID] = t
	}
}

func (c *Catalog) loadTest(a *Area, rel string) *Test {
	t := &Test{
		ID:     strings.TrimSuffix(rel, ".md"),
		Area:   a.ID,
		File:   rel,
		Select: map[string]Selection{},
		Params: map[string]string{},
	}
	front, body, bodyLine, _ := c.readFile(rel)
	t.lines = front.lines
	d := decoder{c: c, file: rel, m: front}
	allowed := append([]string{}, testKeys...)
	for _, dim := range a.Dimensions {
		allowed = append(allowed, dim.Name)
	}
	d.keys(allowed...)

	t.Title = strings.TrimSpace(d.str("title"))
	if t.Title == "" {
		c.errorf(rel, front.Line("title"), "no title: the front matter names the test with title:")
	}
	t.explicitPriority = d.has("priority")
	t.Priority = d.oneOf("priority", P1, P0, P1, P2)
	t.Tags = d.list("tags")
	t.Tools = d.list("tools")
	t.Exclusive = d.list("exclusive")
	for _, x := range t.Exclusive {
		if !exclusiveName.MatchString(x) {
			d.errorf("exclusive", "exclusive: %q is not a name (lower-case letters, digits, -, _ and .)", x)
		}
	}
	for _, k := range a.ParamOrder {
		t.Params[k] = a.Params[k]
	}
	d.params("params", t.Params, nil)
	t.Timeout = d.duration("timeout", a.Timeout)
	t.Previous = d.str("previous")
	if t.Previous == t.ID {
		d.errorf("previous", "previous names the test itself")
	}

	var rotating []string // the test's own first, then the area's defaults
	for _, dim := range a.Dimensions {
		sel := dim.Default
		_, own := front.Get(dim.Name)
		if own {
			s, msg := parseSelection(front.vals[dim.Name], dim)
			if msg != "" {
				d.errorf(dim.Name, "%s: %s", dim.Name, msg)
			} else {
				sel = s
			}
		}
		if sel.Kind == SelectRotate {
			if own {
				rotating = append([]string{dim.Name}, rotating...)
			} else {
				rotating = append(rotating, dim.Name)
			}
		}
		t.Select[dim.Name] = sel
	}
	if len(rotating) > 1 {
		// Only a test's own selection can add a second rotation (the area
		// refuses two defaults): it keeps its own, the others run all.
		d.errorf(rotating[0], "%s: only one dimension may rotate, and %s rotates too (give it a list)", rotating[0], strings.Join(rotating[1:], ", "))
		for _, name := range rotating[1:] {
			t.Select[name] = Selection{Kind: SelectAll}
		}
	}

	steps, context, problems := parseBody(body, bodyLine)
	t.Steps, t.Context = steps, context
	for _, p := range problems {
		if p.warning {
			c.warnf(rel, p.line, "%s", p.msg)
		} else {
			c.errorf(rel, p.line, "%s", p.msg)
		}
	}
	return t
}
