package spatial

import (
	"math"

	"github.com/mlange-42/ark/ecs"
)

// Collision shape types.
const (
	ShapeCircle uint8 = 0
	ShapeRect   uint8 = 1
)

// CellKey identifies a cell in the spatial hash grid.
type CellKey struct {
	X, Y int32
}

// Entry stores an entity with its position, shape, and collision layer.
type Entry struct {
	Entity   ecs.Entity
	X, Y     float32
	Radius   float32 // bounding radius (used for broad-phase and circle shape)
	Width    float32 // rect width (forward axis), 0 for circles
	Height   float32 // rect height (side axis), 0 for circles
	Rotation float32 // entity rotation (needed for OBB narrow-phase)
	Layer    uint8
	Shape    uint8 // ShapeCircle or ShapeRect
}

// Grid is a spatial hash grid for broad-phase collision detection and AoI queries.
type Grid struct {
	cellSize    float32
	invCellSize float32
	cells       map[CellKey][]Entry
}

// NewGrid creates a new spatial hash grid with the given cell size.
func NewGrid(cellSize float32) *Grid {
	return &Grid{
		cellSize:    cellSize,
		invCellSize: 1.0 / cellSize,
		cells:       make(map[CellKey][]Entry, 256),
	}
}

// Clear removes all entries from the grid.
func (g *Grid) Clear() {
	for k := range g.cells {
		g.cells[k] = g.cells[k][:0]
	}
}

// Insert adds an entry to the grid.
func (g *Grid) Insert(entry Entry) {
	key := g.cellKey(entry.X, entry.Y)
	g.cells[key] = append(g.cells[key], entry)
}

// QueryRadius returns all entries within the given radius of (cx, cy).
// Results are appended to the provided slice to avoid allocation.
func (g *Grid) QueryRadius(cx, cy, radius float32, results []Entry) []Entry {
	minX := int32((cx - radius) * g.invCellSize)
	maxX := int32((cx + radius) * g.invCellSize)
	minY := int32((cy - radius) * g.invCellSize)
	maxY := int32((cy + radius) * g.invCellSize)

	r2 := radius * radius

	for x := minX; x <= maxX; x++ {
		for y := minY; y <= maxY; y++ {
			key := CellKey{x, y}
			for _, e := range g.cells[key] {
				dx := e.X - cx
				dy := e.Y - cy
				if dx*dx+dy*dy <= r2 {
					results = append(results, e)
				}
			}
		}
	}
	return results
}

// QueryCollisions finds all pairs of entries that overlap, filtered by the collision matrix.
// Calls the callback for each colliding pair.
func (g *Grid) QueryCollisions(collisionMatrix map[uint8]uint8, fn func(a, b Entry)) {
	offsets := [4]CellKey{{0, 0}, {1, 0}, {0, 1}, {1, 1}}

	for key, cell := range g.cells {
		// Check pairs within the same cell
		for i := 0; i < len(cell); i++ {
			for j := i + 1; j < len(cell); j++ {
				checkCollision(cell[i], cell[j], collisionMatrix, fn)
			}
		}

		// Check against neighboring cells
		for _, off := range offsets[1:] {
			neighbor := CellKey{key.X + off.X, key.Y + off.Y}
			neighborCell := g.cells[neighbor]
			for _, a := range cell {
				for _, b := range neighborCell {
					checkCollision(a, b, collisionMatrix, fn)
				}
			}
		}
	}
}

func checkCollision(a, b Entry, matrix map[uint8]uint8, fn func(a, b Entry)) {
	// Check if these layers should collide
	targetA, okA := matrix[a.Layer]
	targetB, okB := matrix[b.Layer]

	canCollide := (okA && targetA&b.Layer != 0) || (okB && targetB&a.Layer != 0)
	if !canCollide {
		return
	}

	// Broad-phase: bounding circle check
	dx := a.X - b.X
	dy := a.Y - b.Y
	dist2 := dx*dx + dy*dy
	maxDist := a.Radius + b.Radius
	if dist2 > maxDist*maxDist {
		return
	}

	// Narrow-phase: shape-aware check
	if a.Shape == ShapeCircle && b.Shape == ShapeCircle {
		// Already passed broad-phase circle-circle
		fn(a, b)
		return
	}

	if a.Shape == ShapeRect && b.Shape == ShapeCircle {
		if obbCircleCollision(a, b) {
			fn(a, b)
		}
		return
	}

	if a.Shape == ShapeCircle && b.Shape == ShapeRect {
		if obbCircleCollision(b, a) {
			fn(a, b)
		}
		return
	}

	// Both rects: OBB-OBB via SAT
	if obbOBBCollision(a, b) {
		fn(a, b)
	}
}

// obbCircleCollision tests an oriented bounding box against a circle.
func obbCircleCollision(rect, circle Entry) bool {
	// Transform circle center into rect's local space
	cos := float32(math.Cos(float64(-rect.Rotation)))
	sin := float32(math.Sin(float64(-rect.Rotation)))
	dx := circle.X - rect.X
	dy := circle.Y - rect.Y
	localX := dx*cos - dy*sin
	localY := dx*sin + dy*cos

	// Clamp to rect half-extents to find closest point
	halfW := rect.Width / 2
	halfH := rect.Height / 2
	closestX := clamp(localX, -halfW, halfW)
	closestY := clamp(localY, -halfH, halfH)

	// Check distance from closest point to circle center
	cdx := localX - closestX
	cdy := localY - closestY
	return cdx*cdx+cdy*cdy <= circle.Radius*circle.Radius
}

// obbOBBCollision tests two oriented bounding boxes using the Separating Axis Theorem.
func obbOBBCollision(a, b Entry) bool {
	// Get the 4 axes to test (2 per rect)
	cosA := float32(math.Cos(float64(a.Rotation)))
	sinA := float32(math.Sin(float64(a.Rotation)))
	cosB := float32(math.Cos(float64(b.Rotation)))
	sinB := float32(math.Sin(float64(b.Rotation)))

	// Axes for rect A: forward (cosA, sinA) and right (−sinA, cosA)
	// Axes for rect B: forward (cosB, sinB) and right (−sinB, cosB)
	axes := [4][2]float32{
		{cosA, sinA},
		{-sinA, cosA},
		{cosB, sinB},
		{-sinB, cosB},
	}

	// Half-extents
	halfA := [2]float32{a.Width / 2, a.Height / 2}
	halfB := [2]float32{b.Width / 2, b.Height / 2}

	// Centers
	dx := b.X - a.X
	dy := b.Y - a.Y

	for _, axis := range axes {
		// Project center offset onto axis
		dist := abs32(dx*axis[0] + dy*axis[1])

		// Project half-extents of A onto axis
		projA := halfA[0]*abs32(cosA*axis[0]+sinA*axis[1]) +
			halfA[1]*abs32(-sinA*axis[0]+cosA*axis[1])

		// Project half-extents of B onto axis
		projB := halfB[0]*abs32(cosB*axis[0]+sinB*axis[1]) +
			halfB[1]*abs32(-sinB*axis[0]+cosB*axis[1])

		if dist > projA+projB {
			return false // Separating axis found
		}
	}

	return true // No separating axis — collision
}

func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func (g *Grid) cellKey(x, y float32) CellKey {
	return CellKey{
		X: int32(x * g.invCellSize),
		Y: int32(y * g.invCellSize),
	}
}
