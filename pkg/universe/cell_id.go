package universe

import "fmt"

// Cell IDs have two string forms:
//
//   - MeshCellID ("cell_X_Y" / "cell_dN_X_Y") — wire/internal form. Used
//     as keys in Process.Cells, Host.OwnedCells, and on the MeshControl
//     wire. Returned by (CellID).MeshID(). Distinct Go type so the
//     compiler refuses to mix it with plain strings.
//
//   - Display form ("X_Y" / "dN_X_Y") — human-readable form. Used in
//     console output, log messages, and command result fields. Returned
//     by (CellID).String(). Plain string type because display strings
//     are emitted to humans, not re-parsed by code.
//
// ParseCellID accepts both forms and returns a structured CellID. It is
// the canonical way to convert any cell-ID string (proto field, user
// input, log line) back into a CellID value.
//
// Convention: any function accepting a cell ID by string MUST take
// MeshCellID. Functions accepting raw user input take plain string and
// parse via ParseCellID at the boundary.
//
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

// MeshCellID is the wire/internal string form of a CellID.
//
// Format: "cell_X_Y" at depth 0, "cell_dN_X_Y" at depth N > 0. This is the
// form used as keys in Process.Cells, Host.OwnedCells, RemoteHost.OwnedCells,
// and on the wire in proto fields like meshpb.CellAssign.CellId.
//
// MeshCellID is a distinct type from plain string so the compiler refuses
// to mix it with display-form strings (CellID.String() — "X_Y") or with
// arbitrary user input. Convert at the boundary: ParseCellID(s) returns a
// structured CellID; (CellID).MeshID() returns a typed MeshCellID; an
// explicit MeshCellID(s) cast is the only way to assert that a plain
// string is mesh-form.
type MeshCellID string

// String makes MeshCellID satisfy fmt.Stringer so it prints cleanly via
// %s/%v formatting verbs. The underlying value is already a string; this
// is a documentation aid, not a converter.
func (m MeshCellID) String() string { return string(m) }

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
		panic(fmt.Sprintf("CellID.Parent called on depth-0 cell %s", c))
	}
	return CellID{X: c.X / 2, Y: c.Y / 2, Depth: c.Depth - 1}
}

// Siblings returns all 4 children of this cell's parent (including this cell).
// Panics if Depth == 0.
func (c CellID) Siblings() [4]CellID {
	return c.Parent().Children()
}

// Neighbors returns the 8 Moore-neighborhood neighbors of this cell at the
// same depth (the cells immediately surrounding it: N, NE, E, SE, S, SW, W, NW).
// The neighborhood is computed in the cell's own coordinate space at its depth;
// neighbors at different depths are not considered. Used by the rendezvous
// locality bias to keep adjacent cells co-located on the same host.
func (c CellID) Neighbors() [8]CellID {
	return [8]CellID{
		{X: c.X - 1, Y: c.Y - 1, Depth: c.Depth},
		{X: c.X, Y: c.Y - 1, Depth: c.Depth},
		{X: c.X + 1, Y: c.Y - 1, Depth: c.Depth},
		{X: c.X - 1, Y: c.Y, Depth: c.Depth},
		{X: c.X + 1, Y: c.Y, Depth: c.Depth},
		{X: c.X - 1, Y: c.Y + 1, Depth: c.Depth},
		{X: c.X, Y: c.Y + 1, Depth: c.Depth},
		{X: c.X + 1, Y: c.Y + 1, Depth: c.Depth},
	}
}

// MeshID returns the wire-format identifier for this cell — a typed
// MeshCellID used as a key in Process.Cells / Host.OwnedCells and on the
// MeshControl wire. Format: "cell_X_Y" at depth 0, "cell_dN_X_Y" at depth
// N > 0.
func (c CellID) MeshID() MeshCellID {
	if c.Depth == 0 {
		return MeshCellID(fmt.Sprintf("cell_%d_%d", c.X, c.Y))
	}
	return MeshCellID(fmt.Sprintf("cell_d%d_%d_%d", c.Depth, c.X, c.Y))
}

// String returns a human-readable cell identifier for console display.
// Format: "X_Y" at depth 0, "dN_X_Y" at depth N > 0.
func (c CellID) String() string {
	if c.Depth == 0 {
		return fmt.Sprintf("%d_%d", c.X, c.Y)
	}
	return fmt.Sprintf("d%d_%d_%d", c.Depth, c.X, c.Y)
}

// ParseCellID parses a cell ID string in any of the four canonical
// formats produced elsewhere in the package:
//
//	"X_Y"         — String(), depth 0
//	"dN_X_Y"      — String(), depth N > 0
//	"cell_X_Y"    — MeshID() / MeshCellID, depth 0 (the wire format used
//	                by MeshControl CellAssign / CellRelease messages and
//	                by Process.Cells map keys)
//	"cell_dN_X_Y" — MeshID() / MeshCellID, depth N > 0
//
// Accepting both formats makes ParseCellID a true inverse of both
// CellID.String() and CellID.MeshID(), which removes a footgun where
// the assignment engine produces "cell_0_0" via MeshCellID and the
// host side tries to parse it back — that used to silently drop every
// CellAssign message.
func ParseCellID(s string) (CellID, error) {
	var c CellID

	// Try "cell_dN_X_Y" format (MeshID, depth > 0)
	n, err := fmt.Sscanf(s, "cell_d%d_%d_%d", &c.Depth, &c.X, &c.Y)
	if err == nil && n == 3 {
		return c, nil
	}

	// Try "cell_X_Y" format (MeshID, depth 0)
	n, err = fmt.Sscanf(s, "cell_%d_%d", &c.X, &c.Y)
	if err == nil && n == 2 {
		c.Depth = 0
		return c, nil
	}

	// Try "dN_X_Y" format (String, depth > 0)
	n, err = fmt.Sscanf(s, "d%d_%d_%d", &c.Depth, &c.X, &c.Y)
	if err == nil && n == 3 {
		return c, nil
	}

	// Try "X_Y" format (String, depth 0)
	n, err = fmt.Sscanf(s, "%d_%d", &c.X, &c.Y)
	if err == nil && n == 2 {
		c.Depth = 0
		return c, nil
	}

	return CellID{}, fmt.Errorf("invalid cell ID %q: expected X_Y, dN_X_Y, cell_X_Y, or cell_dN_X_Y", s)
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
