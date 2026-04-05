package universe

import "fmt"

// CellID uniquely identifies a cell at any quadtree depth in the server mesh.
// Depth 0 is the original grid. Each split doubles the coordinate space and
// produces 4 sub-cells at Depth+1.
//
// Cell size at depth D = BaseCellSize / 2^D.
// Coordinates scale with depth: splitting {X, Y, D} produces
// {2X, 2Y, D+1}, {2X+1, 2Y, D+1}, {2X, 2Y+1, D+1}, {2X+1, 2Y+1, D+1}.
type CellID struct {
	X, Y  int32
	Depth uint8
}

// Size returns the cell's side length at this depth.
func (c CellID) Size(baseCellSize float32) float32 {
	return baseCellSize / float32(uint32(1)<<c.Depth)
}

// WorldOrigin returns the world-space origin (min corner) of this cell.
func (c CellID) WorldOrigin(baseCellSize float32) (float32, float32) {
	size := c.Size(baseCellSize)
	return float32(c.X) * size, float32(c.Y) * size
}

// WorldBounds returns the world-space bounding box (minX, minY, maxX, maxY).
func (c CellID) WorldBounds(baseCellSize float32) (minX, minY, maxX, maxY float32) {
	size := c.Size(baseCellSize)
	minX = float32(c.X) * size
	minY = float32(c.Y) * size
	maxX = minX + size
	maxY = minY + size
	return
}

// Children returns the 4 sub-cells produced by splitting this cell.
func (c CellID) Children() [4]CellID {
	d := c.Depth + 1
	x2, y2 := c.X*2, c.Y*2
	return [4]CellID{
		{x2, y2, d},         // bottom-left
		{x2 + 1, y2, d},     // bottom-right
		{x2, y2 + 1, d},     // top-left
		{x2 + 1, y2 + 1, d}, // top-right
	}
}

// Parent returns the parent cell at Depth-1. Panics if Depth == 0.
func (c CellID) Parent() CellID {
	if c.Depth == 0 {
		panic("CellID.Parent called on depth-0 cell")
	}
	return CellID{X: c.X / 2, Y: c.Y / 2, Depth: c.Depth - 1}
}

// Siblings returns all 4 children of this cell's parent (including this cell).
// Panics if Depth == 0.
func (c CellID) Siblings() [4]CellID {
	return c.Parent().Children()
}

// NodeID returns a string identifier for the node that owns this cell.
// Format: "node_X_Y" at depth 0, "node_dN_X_Y" at depth N > 0.
func (c CellID) NodeID() string {
	if c.Depth == 0 {
		return fmt.Sprintf("node_%d_%d", c.X, c.Y)
	}
	return fmt.Sprintf("node_d%d_%d_%d", c.Depth, c.X, c.Y)
}

// String returns a human-readable cell identifier for console display.
// Format: "X_Y" at depth 0, "dN_X_Y" at depth N > 0.
func (c CellID) String() string {
	if c.Depth == 0 {
		return fmt.Sprintf("%d_%d", c.X, c.Y)
	}
	return fmt.Sprintf("d%d_%d_%d", c.Depth, c.X, c.Y)
}

// ParseCellID parses a cell ID string ("X_Y" or "dN_X_Y") as produced by String().
func ParseCellID(s string) (CellID, error) {
	var c CellID

	// Try "dN_X_Y" format first
	n, err := fmt.Sscanf(s, "d%d_%d_%d", &c.Depth, &c.X, &c.Y)
	if err == nil && n == 3 {
		return c, nil
	}

	// Try "X_Y" format (depth 0)
	n, err = fmt.Sscanf(s, "%d_%d", &c.X, &c.Y)
	if err == nil && n == 2 {
		c.Depth = 0
		return c, nil
	}

	return CellID{}, fmt.Errorf("invalid cell ID %q: expected X_Y or dN_X_Y", s)
}

// AreAdjacent returns true if two cells are neighbors (share an edge or corner).
// Works across different depths by comparing world-space bounds.
func AreAdjacent(a, b CellID, baseCellSize float32) bool {
	if a == b {
		return false
	}

	aMinX, aMinY, aMaxX, aMaxY := a.WorldBounds(baseCellSize)
	bMinX, bMinY, bMaxX, bMaxY := b.WorldBounds(baseCellSize)

	// Two rectangles are adjacent if they touch (share boundary) but don't overlap interior.
	// They touch if the gap between them is zero in both axes.
	// Gap in X: max(aMinX - bMaxX, bMinX - aMaxX) <= 0 means they overlap or touch in X.
	// Gap in Y: same logic.
	// They are adjacent (not overlapping) if they touch on at least one axis.

	gapX := max32(aMinX-bMaxX, bMinX-aMaxX)
	gapY := max32(aMinY-bMaxY, bMinY-aMaxY)

	// Must touch or overlap in both dimensions (gap <= 0),
	// and must not have interior overlap (at least one gap == 0 for adjacency,
	// or for corner-touching, both gaps == 0).
	// Actually, for 8-connectivity (including diagonal/corner neighbors):
	// cells are neighbors if gapX <= 0 AND gapY <= 0.
	// But we want "touching" not "overlapping interior", so:
	// they share boundary if gapX <= 0 && gapY <= 0 (they touch or overlap in both dims).
	// Since cells in a quadtree don't overlap, gapX <= 0 && gapY <= 0 is sufficient.
	return gapX <= 0 && gapY <= 0
}

// FromCellCoordDepth0 creates a depth-0 CellID from x, y coordinates.
func FromCellCoordDepth0(x, y int32) CellID {
	return CellID{X: x, Y: y, Depth: 0}
}

// LocalBounds returns the cell's bounds in base-cell-local coordinates —
// i.e., relative to the depth-0 ancestor's origin. For depth-0 cells this
// is [0, baseCellSize) x [0, baseCellSize). For deeper cells the range is
// narrower and offset within the root cell.
func (c CellID) LocalBounds(baseCellSize float32) (minX, minY, maxX, maxY float32) {
	if c.Depth == 0 {
		return 0, 0, baseCellSize, baseCellSize
	}
	wMinX, wMinY, wMaxX, wMaxY := c.WorldBounds(baseCellSize)
	root := c
	for root.Depth > 0 {
		root = root.Parent()
	}
	rootOX := float32(root.X) * baseCellSize
	rootOY := float32(root.Y) * baseCellSize
	return wMinX - rootOX, wMinY - rootOY, wMaxX - rootOX, wMaxY - rootOY
}

// CellDirection returns the spatial direction from cell `from` to cell `to`
// as a unit vector with components in {-1, 0, 1}. Works across any depth mix
// by comparing world-space bounds (edge adjacency).
func CellDirection(from, to CellID, baseCellSize float32) (dx, dy int32) {
	aMinX, aMinY, aMaxX, aMaxY := from.WorldBounds(baseCellSize)
	bMinX, bMinY, bMaxX, bMaxY := to.WorldBounds(baseCellSize)
	if bMinX >= aMaxX {
		dx = 1
	} else if bMaxX <= aMinX {
		dx = -1
	}
	if bMinY >= aMaxY {
		dy = 1
	} else if bMaxY <= aMinY {
		dy = -1
	}
	return
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
