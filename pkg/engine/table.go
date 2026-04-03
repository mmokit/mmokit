package engine

import (
	"fmt"
	"strings"
)

// Table builds column-aligned text output for the console.
type Table struct {
	headers []string
	rows    [][]string
	indent  string
}

// NewTable creates a table with the given column headers.
func NewTable(headers ...string) *Table {
	return &Table{
		headers: headers,
		indent:  "  ",
	}
}

// Row appends a row to the table. Each value is formatted with fmt.Sprintf("%v").
func (t *Table) Row(values ...any) {
	row := make([]string, len(values))
	for i, v := range values {
		row[i] = fmt.Sprintf("%v", v)
	}
	t.rows = append(t.rows, row)
}

// String renders the table as column-aligned text with uppercase headers.
func (t *Table) String() string {
	numCols := len(t.headers)
	for _, row := range t.rows {
		if len(row) > numCols {
			numCols = len(row)
		}
	}
	if numCols == 0 {
		return ""
	}

	// Calculate column widths
	widths := make([]int, numCols)
	for i, h := range t.headers {
		if len(h) > widths[i] {
			widths[i] = len(h)
		}
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var b strings.Builder

	// Header line
	if len(t.headers) > 0 {
		b.WriteString(t.indent)
		for i, h := range t.headers {
			if i > 0 {
				b.WriteString("  ")
			}
			fmt.Fprintf(&b, "%-*s", widths[i], strings.ToUpper(h))
		}
		b.WriteByte('\n')
	}

	// Data rows
	for _, row := range t.rows {
		b.WriteString(t.indent)
		for i := 0; i < numCols; i++ {
			if i > 0 {
				b.WriteString("  ")
			}
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			fmt.Fprintf(&b, "%-*s", widths[i], cell)
		}
		b.WriteByte('\n')
	}

	return b.String()
}
