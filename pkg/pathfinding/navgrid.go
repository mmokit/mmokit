package pathfinding

// Vec2 mirrors spatial.Vec2; kept package-local so pathfinding has no
// dependency on pkg/spatial. Callers convert at the boundary.
type Vec2 struct {
	X, Y float32
}

// NavGrid is a rasterized walkability bitmap. Origin is the world-space
// position of cell (0,0)'s lower-left corner. Walkable cells default to
// true; call Block(x,y) to mark a cell as obstructed.
type NavGrid struct {
	Origin   Vec2
	CellSize float32
	Width    int
	Height   int
	walkable []bool
}

// NewNavGrid allocates a grid with every cell defaulted to walkable.
func NewNavGrid(origin Vec2, cellSize float32, width, height int) *NavGrid {
	g := &NavGrid{
		Origin:   origin,
		CellSize: cellSize,
		Width:    width,
		Height:   height,
		walkable: make([]bool, width*height),
	}
	for i := range g.walkable {
		g.walkable[i] = true
	}
	return g
}

// CellToWorld returns the center of cell (x,y) in world space.
func (g *NavGrid) CellToWorld(x, y int) Vec2 {
	return Vec2{
		X: g.Origin.X + (float32(x)+0.5)*g.CellSize,
		Y: g.Origin.Y + (float32(y)+0.5)*g.CellSize,
	}
}

// WorldToCell returns the integer cell containing the given world-space
// point. ok=false if the point falls outside the grid.
func (g *NavGrid) WorldToCell(p Vec2) (int, int, bool) {
	dx := p.X - g.Origin.X
	dy := p.Y - g.Origin.Y
	if dx < 0 || dy < 0 {
		return 0, 0, false
	}
	cx := int(dx / g.CellSize)
	cy := int(dy / g.CellSize)
	if cx >= g.Width || cy >= g.Height {
		return 0, 0, false
	}
	return cx, cy, true
}

// Walkable reports whether cell (x,y) is traversable.
func (g *NavGrid) Walkable(x, y int) bool {
	if x < 0 || y < 0 || x >= g.Width || y >= g.Height {
		return false
	}
	return g.walkable[y*g.Width+x]
}

// Block marks cell (x,y) as obstructed.
func (g *NavGrid) Block(x, y int) {
	if x < 0 || y < 0 || x >= g.Width || y >= g.Height {
		return
	}
	g.walkable[y*g.Width+x] = false
}
