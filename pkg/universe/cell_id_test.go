package universe

import (
	"testing"
)

func TestCellID_Size(t *testing.T) {
	base := float32(8192)

	tests := []struct {
		cell CellID
		want float32
	}{
		{CellID{0, 0, 0}, 8192},
		{CellID{0, 0, 1}, 4096},
		{CellID{0, 0, 2}, 2048},
		{CellID{0, 0, 3}, 1024},
	}

	for _, tt := range tests {
		got := tt.cell.Size(base)
		if got != tt.want {
			t.Errorf("CellID%v.Size(%v) = %v, want %v", tt.cell, base, got, tt.want)
		}
	}
}

func TestCellID_WorldOrigin(t *testing.T) {
	base := float32(8192)

	tests := []struct {
		cell  CellID
		wantX float32
		wantY float32
	}{
		{CellID{0, 0, 0}, 0, 0},
		{CellID{1, 0, 0}, 8192, 0},
		{CellID{0, 1, 0}, 0, 8192},
		{CellID{1, 1, 1}, 4096, 4096},   // depth 1, size 4096
		{CellID{3, 2, 2}, 6144, 4096},   // depth 2, size 2048
	}

	for _, tt := range tests {
		x, y := tt.cell.WorldOrigin(base)
		if x != tt.wantX || y != tt.wantY {
			t.Errorf("CellID%v.WorldOrigin(%v) = (%v, %v), want (%v, %v)", tt.cell, base, x, y, tt.wantX, tt.wantY)
		}
	}
}

func TestCellID_WorldBounds(t *testing.T) {
	base := float32(8192)

	c := CellID{1, 1, 1} // depth 1, size 4096, origin (4096, 4096)
	minX, minY, maxX, maxY := c.WorldBounds(base)
	if minX != 4096 || minY != 4096 || maxX != 8192 || maxY != 8192 {
		t.Errorf("WorldBounds = (%v,%v,%v,%v), want (4096,4096,8192,8192)", minX, minY, maxX, maxY)
	}
}

func TestCellID_Children(t *testing.T) {
	parent := CellID{1, 2, 0}
	children := parent.Children()

	expected := [4]CellID{
		{2, 4, 1},
		{3, 4, 1},
		{2, 5, 1},
		{3, 5, 1},
	}

	for i, want := range expected {
		if children[i] != want {
			t.Errorf("Children()[%d] = %v, want %v", i, children[i], want)
		}
	}
}

func TestCellID_Parent(t *testing.T) {
	child := CellID{3, 5, 1}
	parent := child.Parent()
	want := CellID{1, 2, 0}

	if parent != want {
		t.Errorf("Parent() = %v, want %v", parent, want)
	}
}

func TestCellID_ParentPanicsAtDepth0(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for Parent() on depth-0 cell")
		}
	}()

	c := CellID{0, 0, 0}
	c.Parent()
}

func TestCellID_Siblings(t *testing.T) {
	c := CellID{3, 5, 1}
	siblings := c.Siblings()

	// Parent is {1, 2, 0}, children are {2,4,1}, {3,4,1}, {2,5,1}, {3,5,1}
	found := false
	for _, s := range siblings {
		if s == c {
			found = true
		}
	}
	if !found {
		t.Error("Siblings() should include self")
	}
	if len(siblings) != 4 {
		t.Errorf("Siblings() returned %d cells, want 4", len(siblings))
	}
}

func TestCellID_ChildrenParentRoundTrip(t *testing.T) {
	parent := CellID{0, 0, 0}
	children := parent.Children()

	for _, child := range children {
		got := child.Parent()
		if got != parent {
			t.Errorf("child %v.Parent() = %v, want %v", child, got, parent)
		}
	}

	// Depth 2 round trip
	for _, child := range children {
		grandchildren := child.Children()
		for _, gc := range grandchildren {
			if gc.Parent() != child {
				t.Errorf("grandchild %v.Parent() = %v, want %v", gc, gc.Parent(), child)
			}
		}
	}
}

func TestCellID_NodeID(t *testing.T) {
	tests := []struct {
		cell CellID
		want string
	}{
		{CellID{0, 0, 0}, "node_0_0"},
		{CellID{1, 2, 0}, "node_1_2"},
		{CellID{2, 1, 1}, "node_d1_2_1"},
		{CellID{3, 5, 2}, "node_d2_3_5"},
	}

	for _, tt := range tests {
		got := tt.cell.NodeID()
		if got != tt.want {
			t.Errorf("CellID%v.NodeID() = %q, want %q", tt.cell, got, tt.want)
		}
	}
}

func TestCellID_String(t *testing.T) {
	tests := []struct {
		cell CellID
		want string
	}{
		{CellID{0, 0, 0}, "0_0"},
		{CellID{1, 2, 0}, "1_2"},
		{CellID{2, 1, 1}, "d1_2_1"},
		{CellID{3, 5, 2}, "d2_3_5"},
	}

	for _, tt := range tests {
		got := tt.cell.String()
		if got != tt.want {
			t.Errorf("CellID%v.String() = %q, want %q", tt.cell, got, tt.want)
		}
	}
}

func TestParseCellID(t *testing.T) {
	tests := []struct {
		input   string
		want    CellID
		wantErr bool
	}{
		{"0_0", CellID{0, 0, 0}, false},
		{"1_2", CellID{1, 2, 0}, false},
		{"d1_2_1", CellID{2, 1, 1}, false},
		{"d2_3_5", CellID{3, 5, 2}, false},
		{"bad", CellID{}, true},
		{"", CellID{}, true},
	}

	for _, tt := range tests {
		got, err := ParseCellID(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseCellID(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseCellID(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseCellID_RoundTrip(t *testing.T) {
	cells := []CellID{
		{0, 0, 0},
		{1, 2, 0},
		{2, 1, 1},
		{3, 5, 2},
	}

	for _, c := range cells {
		s := c.String()
		parsed, err := ParseCellID(s)
		if err != nil {
			t.Errorf("ParseCellID(%q) error = %v", s, err)
			continue
		}
		if parsed != c {
			t.Errorf("round-trip failed: %v -> %q -> %v", c, s, parsed)
		}
	}
}

func TestAreAdjacent(t *testing.T) {
	base := float32(8192)

	// Same-depth adjacency (depth 0, 2x2 grid)
	if !AreAdjacent(CellID{0, 0, 0}, CellID{1, 0, 0}, base) {
		t.Error("(0,0) and (1,0) should be adjacent")
	}
	if !AreAdjacent(CellID{0, 0, 0}, CellID{0, 1, 0}, base) {
		t.Error("(0,0) and (0,1) should be adjacent")
	}
	if !AreAdjacent(CellID{0, 0, 0}, CellID{1, 1, 0}, base) {
		t.Error("(0,0) and (1,1) should be adjacent (diagonal)")
	}

	// Non-adjacent (depth 0)
	if AreAdjacent(CellID{0, 0, 0}, CellID{2, 0, 0}, base) {
		t.Error("(0,0) and (2,0) should not be adjacent")
	}

	// Self
	if AreAdjacent(CellID{0, 0, 0}, CellID{0, 0, 0}, base) {
		t.Error("cell should not be adjacent to itself")
	}

	// Cross-depth: depth-0 cell (1,0) next to depth-1 sub-cells of (0,0)
	// (0,0,0) splits into: {0,0,1}, {1,0,1}, {0,1,1}, {1,1,1}
	// Cell (1,0,0) has bounds [8192, 0, 16384, 8192]
	// {1,0,1} has bounds [4096, 0, 8192, 4096] — shares east edge with (1,0,0)
	if !AreAdjacent(CellID{1, 0, 1}, CellID{1, 0, 0}, base) {
		t.Error("depth-1 (1,0) and depth-0 (1,0) should be adjacent")
	}
	// {1,1,1} has bounds [4096, 4096, 8192, 8192] — shares east edge with (1,0,0)
	if !AreAdjacent(CellID{1, 1, 1}, CellID{1, 0, 0}, base) {
		t.Error("depth-1 (1,1) and depth-0 (1,0) should be adjacent")
	}
	// {0,0,1} has bounds [0, 0, 4096, 4096] — does NOT touch (1,0,0)
	if AreAdjacent(CellID{0, 0, 1}, CellID{1, 0, 0}, base) {
		t.Error("depth-1 (0,0) and depth-0 (1,0) should NOT be adjacent")
	}
}

func TestAreAdjacent_SiblingCells(t *testing.T) {
	base := float32(8192)

	// All 4 children of (0,0,0) should be adjacent to each other
	children := CellID{0, 0, 0}.Children()
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			if !AreAdjacent(children[i], children[j], base) {
				t.Errorf("siblings %v and %v should be adjacent", children[i], children[j])
			}
		}
	}
}
