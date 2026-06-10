package ui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Table renders simple, aligned columns for CLI output — layout only, no color.
// Unlike text/tabwriter it supports per-column right alignment, which reads better
// for numeric data (container counts, CPU, memory).
type Table struct {
	indent  string
	headers []string
	right   map[int]bool
	rows    [][]string
}

// NewTable creates a table whose rows are prefixed with indent and carry the given
// header labels.
func NewTable(indent string, headers ...string) *Table {
	return &Table{indent: indent, headers: headers, right: map[int]bool{}}
}

// RightAlign marks the given column indexes as right-aligned.
func (t *Table) RightAlign(cols ...int) *Table {
	for _, c := range cols {
		t.right[c] = true
	}
	return t
}

// Row appends a row of cells.
func (t *Table) Row(cells ...string) *Table {
	t.rows = append(t.rows, cells)
	return t
}

// Render writes the header followed by every row, padded to aligned columns.
func (t *Table) Render(w io.Writer) {
	n := len(t.headers)
	for _, r := range t.rows {
		if len(r) > n {
			n = len(r)
		}
	}
	widths := make([]int, n)
	note := func(i int, s string) {
		if i < n {
			if c := utf8.RuneCountInString(s); c > widths[i] {
				widths[i] = c
			}
		}
	}
	for i, h := range t.headers {
		note(i, h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			note(i, c)
		}
	}

	if len(t.headers) > 0 {
		fmt.Fprintln(w, t.format(t.headers, widths))
	}
	for _, r := range t.rows {
		fmt.Fprintln(w, t.format(r, widths))
	}
}

func (t *Table) format(cells []string, widths []int) string {
	var b strings.Builder
	b.WriteString(t.indent)
	for i, width := range widths {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		pad := width - utf8.RuneCountInString(cell)
		if pad < 0 {
			pad = 0
		}
		if t.right[i] {
			b.WriteString(strings.Repeat(" ", pad) + cell)
		} else {
			b.WriteString(cell + strings.Repeat(" ", pad))
		}
		if i < len(widths)-1 {
			b.WriteString("   ") // 3-space gutter between columns
		}
	}
	return strings.TrimRight(b.String(), " ")
}
