package pathfinding

import (
	"container/heap"
	"math"
)

// AStar runs 8-connected A* with octile heuristic on g, from the cell
// containing start to the cell containing goal. Returns world-space
// waypoints (cell centers) including start and goal. Returns nil if
// the goal cell is unreachable or out of bounds.
func AStar(g *NavGrid, start, goal Vec2) []Vec2 {
	sx, sy, ok := g.WorldToCell(start)
	if !ok || !g.Walkable(sx, sy) {
		return nil
	}
	gx, gy, ok := g.WorldToCell(goal)
	if !ok || !g.Walkable(gx, gy) {
		return nil
	}
	if sx == gx && sy == gy {
		return []Vec2{g.CellToWorld(sx, sy)}
	}

	width := g.Width
	cameFromX := make([]int, width*g.Height)
	cameFromY := make([]int, width*g.Height)
	gScore := make([]float32, width*g.Height)
	for i := range gScore {
		gScore[i] = float32(math.Inf(1))
		cameFromX[i] = -1
	}
	startIdx := sy*width + sx
	gScore[startIdx] = 0

	open := &openHeap{}
	heap.Init(open)
	heap.Push(open, openItem{x: sx, y: sy, f: octile(sx, sy, gx, gy)})

	diagCost := float32(math.Sqrt2)
	cardinal := [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	diag := [4][2]int{{1, 1}, {-1, 1}, {1, -1}, {-1, -1}}

	for open.Len() > 0 {
		cur := heap.Pop(open).(openItem)
		if cur.x == gx && cur.y == gy {
			return reconstruct(g, cameFromX, cameFromY, gx, gy, sx, sy)
		}
		curIdx := cur.y*width + cur.x
		for _, d := range cardinal {
			nx, ny := cur.x+d[0], cur.y+d[1]
			if !g.Walkable(nx, ny) {
				continue
			}
			tentative := gScore[curIdx] + 1
			if tentative < gScore[ny*width+nx] {
				gScore[ny*width+nx] = tentative
				cameFromX[ny*width+nx] = cur.x
				cameFromY[ny*width+nx] = cur.y
				heap.Push(open, openItem{x: nx, y: ny, f: tentative + octile(nx, ny, gx, gy)})
			}
		}
		for _, d := range diag {
			nx, ny := cur.x+d[0], cur.y+d[1]
			if !g.Walkable(nx, ny) {
				continue
			}
			// Don't cut corners through a wall.
			if !g.Walkable(cur.x+d[0], cur.y) || !g.Walkable(cur.x, cur.y+d[1]) {
				continue
			}
			tentative := gScore[curIdx] + diagCost
			if tentative < gScore[ny*width+nx] {
				gScore[ny*width+nx] = tentative
				cameFromX[ny*width+nx] = cur.x
				cameFromY[ny*width+nx] = cur.y
				heap.Push(open, openItem{x: nx, y: ny, f: tentative + octile(nx, ny, gx, gy)})
			}
		}
	}
	return nil
}

func octile(x0, y0, x1, y1 int) float32 {
	dx := abs(x0 - x1)
	dy := abs(y0 - y1)
	if dx > dy {
		return float32(dx-dy) + float32(math.Sqrt2)*float32(dy)
	}
	return float32(dy-dx) + float32(math.Sqrt2)*float32(dx)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func reconstruct(g *NavGrid, cfx, cfy []int, gx, gy, sx, sy int) []Vec2 {
	var rev []Vec2
	cx, cy := gx, gy
	for !(cx == sx && cy == sy) {
		rev = append(rev, g.CellToWorld(cx, cy))
		idx := cy*g.Width + cx
		cx, cy = cfx[idx], cfy[idx]
		if cx == -1 {
			return nil // safety
		}
	}
	rev = append(rev, g.CellToWorld(sx, sy))
	// reverse
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// openHeap implements a min-heap on openItem.f.
type openItem struct {
	x, y int
	f    float32
}
type openHeap []openItem

func (h openHeap) Len() int           { return len(h) }
func (h openHeap) Less(i, j int) bool { return h[i].f < h[j].f }
func (h openHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *openHeap) Push(x any)        { *h = append(*h, x.(openItem)) }
func (h *openHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
