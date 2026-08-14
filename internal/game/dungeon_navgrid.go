package game

import (
	"math"

	"github.com/zenion/mmokit/pkg/pathfinding"
)

// buildDungeonNavGrid rasterizes the dungeon's walkable area into a
// NavGrid. A cell is walkable iff its center is inside the asteroid
// silhouette (radius DungeonAsteroidRadius around the dungeon's local
// origin) AND not inside any wall rect.
//
// Coordinates are LOCAL to the dungeon — the dungeon entity sits at
// (centerX, centerY) in world space but the NavGrid is built in the
// dungeon's local frame, so all consumers must subtract the dungeon
// center before querying (or, equivalently, do A* in local space and
// convert the resulting waypoints back to world space). For now NPCs
// in the dungeon are spawned at world-space positions, so consumers
// query in world space and the grid origin is also world-space — see
// SpawnDungeonFromGraph for the conversion.
func buildDungeonNavGrid(g *dungeonGraph, walls []wallSpec, cfg *GameConfig) *pathfinding.NavGrid {
	cell := cfg.NavGridCellSize
	r := cfg.DungeonAsteroidRadius
	w := int(math.Ceil(float64(2*r/cell))) + 2
	h := w
	origin := pathfinding.Vec2{X: -r - cell, Y: -r - cell}
	ng := pathfinding.NewNavGrid(origin, cell, w, h)
	for cy := 0; cy < h; cy++ {
		for cx := 0; cx < w; cx++ {
			center := ng.CellToWorld(cx, cy)
			if center.X*center.X+center.Y*center.Y > r*r {
				ng.Block(cx, cy)
				continue
			}
			if cellInsideAnyWall(center, walls) {
				ng.Block(cx, cy)
				continue
			}
		}
	}
	return ng
}

// cellInsideAnyWall reports whether p is inside any wall rect in walls.
func cellInsideAnyWall(p pathfinding.Vec2, walls []wallSpec) bool {
	for _, w := range walls {
		if pointInRect(p, w) {
			return true
		}
	}
	return false
}

// pointInRect tests whether p lies within the oriented rectangle
// described by w (center, width, height, rotation in radians). Rotates
// p into the rect's local frame and tests against the axis-aligned
// half-extents.
func pointInRect(p pathfinding.Vec2, w wallSpec) bool {
	cosA := float32(math.Cos(float64(-w.Rotation)))
	sinA := float32(math.Sin(float64(-w.Rotation)))
	fx := p.X - w.Center.X
	fy := p.Y - w.Center.Y
	lx := cosA*fx - sinA*fy
	ly := sinA*fx + cosA*fy
	return math.Abs(float64(lx)) <= float64(w.Width/2) &&
		math.Abs(float64(ly)) <= float64(w.Height/2)
}
