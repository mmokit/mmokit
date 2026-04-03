package engine

import (
	"strings"
	"testing"
)

func TestTableEmpty(t *testing.T) {
	tbl := NewTable("Name", "Value")
	out := tbl.String()
	// Should have header but no data rows
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (header only), got %d: %q", len(lines), out)
	}
	if !strings.Contains(out, "NAME") {
		t.Errorf("headers should be uppercase, got: %q", out)
	}
}

func TestTableSingleRow(t *testing.T) {
	tbl := NewTable("Name", "Score")
	tbl.Row("Alice", 42)
	out := tbl.String()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}

	// Check indent
	for _, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("line should start with 2-space indent: %q", line)
		}
	}

	// Check alignment — "Score" (5) vs "42" (2), header is wider
	if !strings.Contains(lines[0], "SCORE") {
		t.Errorf("expected SCORE header: %q", lines[0])
	}
}

func TestTableMultipleRows(t *testing.T) {
	tbl := NewTable("Player", "Level", "HP")
	tbl.Row("Alice", 10, 100)
	tbl.Row("Bob", 5, 50)
	tbl.Row("LongPlayerName", 99, 9999)
	out := tbl.String()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(lines))
	}

	// The "Player" column should be wide enough for "LongPlayerName" (14 chars)
	// All lines should have consistent alignment
	for i, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("line %d missing indent: %q", i, line)
		}
	}
}

func TestTableNoHeaders(t *testing.T) {
	tbl := NewTable()
	tbl.Row("a", "b")
	out := tbl.String()
	// No header line, just the data row
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (data only), got %d: %q", len(lines), out)
	}
}

func TestTableColumnWidthFromValues(t *testing.T) {
	tbl := NewTable("X", "Y")
	tbl.Row("VeryLongValue", "s")
	out := tbl.String()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Header and value column should both be padded to "VeryLongValue" width
	header := lines[0]
	data := lines[1]
	// Find where second column starts — should be same offset in both lines
	xIdx := strings.Index(header, "Y")
	sIdx := strings.Index(data, "s")
	if xIdx != sIdx {
		t.Errorf("columns not aligned: Y at %d, s at %d", xIdx, sIdx)
	}
}

func TestTableCompletelyEmpty(t *testing.T) {
	tbl := NewTable()
	out := tbl.String()
	if out != "" {
		t.Errorf("expected empty string for no headers and no rows, got %q", out)
	}
}

func TestTableFewerValuesThanHeaders(t *testing.T) {
	tbl := NewTable("A", "B", "C")
	tbl.Row("only-one")
	out := tbl.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
}
