package universe

import (
	"fmt"
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
		{CellID{1, 1, 1}, 4096, 4096}, // depth 1, size 4096
		{CellID{3, 2, 2}, 6144, 4096}, // depth 2, size 2048
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
		{CellID{0, 0, 0}, "cell_0_0"},
		{CellID{1, 2, 0}, "cell_1_2"},
		{CellID{2, 1, 1}, "cell_d1_2_1"},
		{CellID{3, 5, 2}, "cell_d2_3_5"},
	}

	for _, tt := range tests {
		got := string(tt.cell.MeshID())
		if got != tt.want {
			t.Errorf("CellID%v.MeshID() = %q, want %q", tt.cell, got, tt.want)
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

func TestCellID_LocalBounds(t *testing.T) {
	base := float32(8192)

	tests := []struct {
		name               string
		cell               CellID
		wantMinX, wantMinY float32
		wantMaxX, wantMaxY float32
	}{
		{"depth-0", CellID{0, 0, 0}, 0, 0, 8192, 8192},
		{"depth-0 offset", CellID{1, 0, 0}, 0, 0, 8192, 8192},
		{"depth-1 bottom-left", CellID{0, 0, 1}, 0, 0, 4096, 4096},
		{"depth-1 bottom-right", CellID{1, 0, 1}, 4096, 0, 8192, 4096},
		{"depth-1 top-left", CellID{0, 1, 1}, 0, 4096, 4096, 8192},
		{"depth-1 top-right", CellID{1, 1, 1}, 4096, 4096, 8192, 8192},
		// Children of cell {1,0,0}: sub-cells at depth 1 are {2,0,1},{3,0,1},{2,1,1},{3,1,1}
		// Root of {3,0,1} is {1,0,0}. World bounds of {3,0,1} = [12288, 0, 16384, 4096).
		// Root origin = (8192, 0). Local = [4096, 0, 8192, 4096).
		{"depth-1 in second root", CellID{3, 0, 1}, 4096, 0, 8192, 4096},
		// Depth 2: children of {0,0,1} are {0,0,2},{1,0,2},{0,1,2},{1,1,2}
		// {1,0,2} world bounds = [2048, 0, 4096, 2048). Root = {0,0,0}, origin = (0,0).
		{"depth-2", CellID{1, 0, 2}, 2048, 0, 4096, 2048},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minX, minY, maxX, maxY := tt.cell.LocalBounds(base)
			if minX != tt.wantMinX || minY != tt.wantMinY || maxX != tt.wantMaxX || maxY != tt.wantMaxY {
				t.Errorf("CellID%v.LocalBounds() = (%v,%v,%v,%v), want (%v,%v,%v,%v)",
					tt.cell, minX, minY, maxX, maxY, tt.wantMinX, tt.wantMinY, tt.wantMaxX, tt.wantMaxY)
			}
		})
	}
}

func TestCellDirection(t *testing.T) {
	base := float32(8192)

	tests := []struct {
		name           string
		from, to       CellID
		wantDX, wantDY int32
	}{
		// Same-depth, depth 0
		{"d0 right", CellID{0, 0, 0}, CellID{1, 0, 0}, 1, 0},
		{"d0 left", CellID{1, 0, 0}, CellID{0, 0, 0}, -1, 0},
		{"d0 up", CellID{0, 0, 0}, CellID{0, 1, 0}, 0, 1},
		{"d0 diagonal", CellID{0, 0, 0}, CellID{1, 1, 0}, 1, 1},
		// Siblings within same root cell (the bug case)
		{"sibling right", CellID{0, 0, 1}, CellID{1, 0, 1}, 1, 0},
		{"sibling up", CellID{0, 0, 1}, CellID{0, 1, 1}, 0, 1},
		{"sibling diagonal", CellID{0, 0, 1}, CellID{1, 1, 1}, 1, 1},
		{"sibling left", CellID{1, 0, 1}, CellID{0, 0, 1}, -1, 0},
		// Cross-depth: depth-1 subcell to depth-0 neighbor
		// {1,0,1} bounds [4096,0,8192,4096), {1,0,0} bounds [8192,0,16384,8192)
		{"cross-depth right", CellID{1, 0, 1}, CellID{1, 0, 0}, 1, 0},
		// {0,0,1} bounds [0,0,4096,4096), {1,0,0} bounds [8192,0,16384,8192) — not adjacent but direction still computable
		{"cross-depth far right", CellID{0, 0, 1}, CellID{1, 0, 0}, 1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dx, dy := CellDirection(tt.from, tt.to, base)
			if dx != tt.wantDX || dy != tt.wantDY {
				t.Errorf("CellDirection(%v, %v) = (%d,%d), want (%d,%d)",
					tt.from, tt.to, dx, dy, tt.wantDX, tt.wantDY)
			}
		})
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

func TestMeshCellID_RoundTrip(t *testing.T) {
	tests := []CellID{
		{X: 0, Y: 0, Depth: 0},
		{X: 3, Y: 5, Depth: 0},
		{X: 7, Y: 2, Depth: 1},
		{X: 15, Y: 9, Depth: 3},
	}
	for _, c := range tests {
		mesh := c.MeshID()
		want := ""
		if c.Depth == 0 {
			want = fmt.Sprintf("cell_%d_%d", c.X, c.Y)
		} else {
			want = fmt.Sprintf("cell_d%d_%d_%d", c.Depth, c.X, c.Y)
		}
		if string(mesh) != want {
			t.Errorf("MeshID(%v) = %q; want %q", c, mesh, want)
		}
		back, err := ParseCellID(string(mesh))
		if err != nil {
			t.Errorf("ParseCellID(%q): %v", mesh, err)
			continue
		}
		if back != c {
			t.Errorf("round-trip mismatch: %v -> %q -> %v", c, mesh, back)
		}
	}
}

// TestMeshCellID_TypedDistinctness pins the design: MeshCellID is a
// distinct type from plain string. If someone redefines it as a type
// alias (`type MeshCellID = string`) the explicit conversions in this
// test become no-ops and the bug class returns silently. The
// type-assertion via reflection here would still pass for an alias,
// but the explicit-cast pattern this test exercises only works because
// MeshCellID is a named type.
func TestMeshCellID_TypedDistinctness(t *testing.T) {
	c := CellID{X: 0, Y: 0, Depth: 0}

	var m MeshCellID = c.MeshID()

	// Explicit cast is the only way to assert a plain string is mesh-form.
	asMesh := MeshCellID("cell_0_0")
	if m != asMesh {
		t.Errorf("typed values differ: %q vs %q", m, asMesh)
	}

	// MeshCellID.String() returns the underlying string for fmt printing.
	if m.String() != "cell_0_0" {
		t.Errorf("MeshCellID.String() = %q; want \"cell_0_0\"", m.String())
	}

	// Display form is plain string and produces a different value.
	display := c.String()
	if display == string(m) {
		t.Errorf("display and mesh forms collided: %q", display)
	}
}
