package catalog

import (
	"sort"
	"strings"
)

// Cell is a test on one value of each of its area's dimensions: one agent
// session per attempt. Key is the values' ids joined by "." in the area's
// dimension order ("pixel.es"), "" for an area without dimensions.
type Cell struct {
	Test *Test
	Dims []DimValue
	Key  string
}

// ID is the cell's id: CellID of its test and key.
func (c Cell) ID() string { return CellID(c.Test.ID, c.Key) }

// DimValue is a cell's value of one dimension.
type DimValue struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

// CellID is a cell's id: the test's id, then @ and the key when there is one
// (web-browser/header-navigation@pixel.es). History is keyed by it.
func CellID(testID, key string) string {
	if key == "" {
		return testID
	}
	return testID + "@" + key
}

// priorityRank orders the priorities, P0 first; one the catalog refused
// counts as the default.
func priorityRank(p string) int {
	switch p {
	case P0:
		return 0
	case P2:
		return 2
	}
	return 1
}

// Cells returns every cell of the catalog in the order a run takes them: by
// priority (P0 first), then by area, then by test (both by id), then in the
// order of the dimensions' values.
func (c *Catalog) Cells() []Cell {
	var cells []Cell
	for _, a := range c.Areas {
		for _, t := range a.Tests {
			cells = append(cells, c.CellsOf(t)...)
		}
	}
	sort.SliceStable(cells, func(i, j int) bool {
		return priorityRank(cells[i].Test.Priority) < priorityRank(cells[j].Test.Priority)
	})
	return cells
}

// CellsOf returns the cells of one test, in the order of its area's
// dimension values (the first dimension outermost). A rotated dimension
// takes one value per combination of the others: the value at (test index +
// combination's position) mod the number of the dimension's values, so each
// value meets each other value across the area's tests.
func (c *Catalog) CellsOf(t *Test) []Cell {
	a := c.areas[t.Area]
	if a == nil {
		return nil
	}
	rotated := -1
	axes := make([][]Value, len(a.Dimensions))
	for i, d := range a.Dimensions {
		sel, ok := t.Select[d.Name]
		if !ok {
			sel = d.Default
		}
		if sel.Kind == SelectRotate && rotated < 0 {
			rotated = i
			continue
		}
		if sel.Kind == SelectRotate {
			sel = Selection{Kind: SelectAll}
		}
		axes[i] = d.selected(sel)
		if len(axes[i]) == 0 {
			return nil
		}
	}
	var cells []Cell
	combo := make([]Value, len(a.Dimensions))
	position := 0
	var walk func(i int)
	walk = func(i int) {
		if i == len(a.Dimensions) {
			if rotated >= 0 {
				vals := a.Dimensions[rotated].Values
				combo[rotated] = vals[(t.Index+position)%len(vals)]
			}
			position++
			cell := Cell{Test: t, Dims: make([]DimValue, len(combo))}
			ids := make([]string, len(combo))
			for k, v := range combo {
				cell.Dims[k] = DimValue{Name: a.Dimensions[k].Name, ID: v.ID, Label: v.Label}
				ids[k] = v.ID
			}
			cell.Key = strings.Join(ids, ".")
			cells = append(cells, cell)
			return
		}
		if i == rotated {
			walk(i + 1)
			return
		}
		for _, v := range axes[i] {
			combo[i] = v
			walk(i + 1)
		}
	}
	walk(0)
	return cells
}

// selected returns the values a selection other than rotate runs on, in the
// dimension's order: Values, then Extra (which only a list names).
func (d *Dimension) selected(sel Selection) []Value {
	switch sel.Kind {
	case SelectList:
		var out []Value
		for _, v := range append(append([]Value{}, d.Values...), d.Extra...) {
			if contains(sel.IDs, v.ID) {
				out = append(out, v)
			}
		}
		return out
	case SelectExcept:
		var out []Value
		for _, v := range d.Values {
			if !contains(sel.IDs, v.ID) {
				out = append(out, v)
			}
		}
		return out
	}
	return d.Values
}
