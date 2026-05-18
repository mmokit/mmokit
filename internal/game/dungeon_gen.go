package game

import (
	"hash/fnv"
	"math"
	"math/rand/v2"

	"github.com/zenion/mmoserver/pkg/spatial"
)

// dungeonGraph holds the procgen tree of chambers + their adjacency edges.
type dungeonGraph struct {
	chambers []dungeonChamber
	edges    []dungeonEdge // parent → child connectivity
	name     string
	seed     uint64
}

type dungeonChamber struct {
	id      uint16
	center  spatial.Vec2
	radius  float32
	role    ChamberRole
	isEntry bool
	parent  int // index into chambers; -1 for root/entry
}

type dungeonEdge struct {
	a, b int // chamber indices
}

// DungeonSeed derives the cell-stable seed for a dungeon at (cellX,cellY).
func DungeonSeed(cellX, cellY int32) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	buf[0] = byte(cellX)
	buf[1] = byte(cellX >> 8)
	buf[2] = byte(cellX >> 16)
	buf[3] = byte(cellX >> 24)
	buf[4] = byte(cellY)
	buf[5] = byte(cellY >> 8)
	buf[6] = byte(cellY >> 16)
	buf[7] = byte(cellY >> 24)
	h.Write(buf[:])
	h.Write([]byte("dungeon"))
	return h.Sum64()
}

// buildDungeonGraph generates a procgen tree of chambers with deterministic
// roles + a radial-layout placement.
func buildDungeonGraph(cfg *GameConfig, seed uint64) *dungeonGraph {
	rng := rand.New(rand.NewPCG(seed, 0))
	n := cfg.DungeonChamberCountMin + rng.IntN(cfg.DungeonChamberCountMax-cfg.DungeonChamberCountMin+1)

	g := &dungeonGraph{
		chambers: make([]dungeonChamber, n),
		name:     dungeonNameForSeed(seed),
		seed:     seed,
	}

	// Build a random spanning tree: chamber 0 = entry (root), each
	// subsequent chamber attaches to a random existing chamber.
	g.chambers[0] = dungeonChamber{id: 0, isEntry: true, parent: -1}
	for i := 1; i < n; i++ {
		parent := rng.IntN(i)
		g.chambers[i] = dungeonChamber{id: uint16(i), parent: parent}
		g.edges = append(g.edges, dungeonEdge{a: parent, b: i})
	}

	// Pick the leaf farthest from entry (graph distance) as terminal.
	distances := bfsDistances(g, 0)
	terminalIdx := -1
	for i := range g.chambers {
		if i == 0 {
			continue
		}
		if g.isLeaf(i) && (terminalIdx == -1 || distances[i] > distances[terminalIdx]) {
			terminalIdx = i
		}
	}
	if terminalIdx == -1 {
		// n == 1 (only entry); no terminal possible
		return g
	}
	g.chambers[terminalIdx].role = ChamberTerminal

	// Pick 2-3 other leaves as side-boss chambers.
	var leaves []int
	for i := range g.chambers {
		if i == terminalIdx || i == 0 {
			continue
		}
		if g.isLeaf(i) {
			leaves = append(leaves, i)
		}
	}
	rng.Shuffle(len(leaves), func(i, j int) { leaves[i], leaves[j] = leaves[j], leaves[i] })
	sideBossTarget := 2 + rng.IntN(2) // 2 or 3
	if sideBossTarget > len(leaves) {
		sideBossTarget = len(leaves)
	}
	for i := 0; i < sideBossTarget; i++ {
		g.chambers[leaves[i]].role = ChamberSideBoss
	}
	// remaining non-entry/non-terminal/non-side-boss chambers = mob packs (default 0)

	// Per-chamber radius from RNG.
	for i := range g.chambers {
		g.chambers[i].radius = cfg.DungeonChamberRadiusMin +
			rng.Float32()*(cfg.DungeonChamberRadiusMax-cfg.DungeonChamberRadiusMin)
	}

	// Radial layout: entry at the asteroid surface (south-facing),
	// children placed at offset+random direction from their parent.
	layoutGraph(g, cfg, rng)

	return g
}

func (g *dungeonGraph) isLeaf(idx int) bool {
	for _, e := range g.edges {
		if e.a == idx {
			return false
		}
	}
	return true
}

func bfsDistances(g *dungeonGraph, root int) []int {
	d := make([]int, len(g.chambers))
	for i := range d {
		d[i] = -1
	}
	d[root] = 0
	queue := []int{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.edges {
			var other int
			switch {
			case e.a == cur:
				other = e.b
			case e.b == cur:
				other = e.a
			default:
				continue
			}
			if d[other] == -1 {
				d[other] = d[cur] + 1
				queue = append(queue, other)
			}
		}
	}
	return d
}

// layoutGraph places each chamber's center inside the asteroid via a
// random radial walk from the entry chamber.
//
// Each child placement is constrained to lie fully inside the asteroid
// silhouette: dist(origin, center) + chamber.radius + WallThickness must
// be <= DungeonAsteroidRadius. We try up to layoutMaxAngleTries random
// angles per child; if none satisfy the constraint, we fall back to the
// midpoint between the parent and the asteroid center, which is
// guaranteed to be in-bounds whenever the parent itself is in-bounds.
// This trades some visual variety on extreme seeds for the hard
// guarantee that every chamber is reachable on the NavGrid.
func layoutGraph(g *dungeonGraph, cfg *GameConfig, rng *rand.Rand) {
	// Entry chamber on the station-facing (visually-south = world +Y in
	// PixiJS no-flip world container) interior surface, lined up with the
	// primary entrance gap from pickEntranceAngles below.
	margin := cfg.DungeonChamberRadiusMax + cfg.DungeonWallThickness
	g.chambers[0].center = spatial.Vec2{X: 0, Y: cfg.DungeonAsteroidRadius - margin}

	const layoutMaxAngleTries = 16

	visited := make([]bool, len(g.chambers))
	visited[0] = true
	queue := []int{0}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.edges {
			var child int
			switch {
			case e.a == cur:
				child = e.b
			case e.b == cur:
				child = e.a
			default:
				continue
			}
			if visited[child] {
				continue
			}
			visited[child] = true
			// Corridor length scales with asteroid radius so the procgen
			// stays sensible across re-tuning. 15-30% of R puts chambers
			// well inside the silhouette without collapsing to a blob.
			corridorLen := cfg.DungeonAsteroidRadius * (0.15 + rng.Float32()*0.15)
			d := cfg.DungeonChamberRadiusMax + corridorLen
			childRadius := g.chambers[child].radius
			maxDist := cfg.DungeonAsteroidRadius - childRadius - cfg.DungeonWallThickness

			placed := false
			for try := 0; try < layoutMaxAngleTries; try++ {
				angle := rng.Float64() * 2 * math.Pi
				cx := g.chambers[cur].center.X + d*float32(math.Cos(angle))
				cy := g.chambers[cur].center.Y + d*float32(math.Sin(angle))
				if float32(math.Hypot(float64(cx), float64(cy))) <= maxDist {
					g.chambers[child].center = spatial.Vec2{X: cx, Y: cy}
					placed = true
					break
				}
			}
			if !placed {
				// Fallback: midpoint between parent and asteroid center.
				// Always succeeds when the parent is in-bounds, since
				// |midpoint| <= |parent| / 2 < maxDist for any reasonable
				// config (parent itself is constrained the same way and
				// the entry sits inside maxDist by construction).
				g.chambers[child].center = spatial.Vec2{
					X: g.chambers[cur].center.X * 0.5,
					Y: g.chambers[cur].center.Y * 0.5,
				}
			}
			queue = append(queue, child)
		}
	}
}

// wallSpec describes one rect-collider wall to be spawned.
type wallSpec struct {
	Center   spatial.Vec2
	Width    float32
	Height   float32
	Rotation float32 // radians
}

// generateWalls produces the outline walls for the dungeon: a ring of
// segments approximating the asteroid perimeter (with cave-mouth gaps
// for entrances) + per-corridor segments lining each parent→child edge.
//
// Outline-only — interior space outside chambers/corridors is "void"
// and gets no wall colliders. Target total: < 40 walls per dungeon.
func generateWalls(g *dungeonGraph, cfg *GameConfig) []wallSpec {
	var walls []wallSpec

	const ringSegments = 16
	entranceAngles := pickEntranceAngles(g, cfg, ringSegments)
	arcLen := 2 * math.Pi / ringSegments
	// Place perimeter walls a half-thickness inside DungeonAsteroidRadius
	// so the wall surface sits at R and the wall body is fully inside the
	// silhouette outline. Without this, walls float visually outside the
	// jittered asteroid polygon (which inscribes to ~0.88R at minimum).
	wallR := cfg.DungeonAsteroidRadius - cfg.DungeonWallThickness/2
	// Slight chord overlap so adjacent segments meet at the corners
	// without gaps. arcLen*R is the arc length; 2*R*sin(arcLen/2) is the
	// chord length. We use the larger arc-length approximation plus a
	// safety margin so the wall covers the full chord.
	segLen := wallR*float32(arcLen) + cfg.DungeonWallThickness*2
	for s := 0; s < ringSegments; s++ {
		if isEntranceSlot(s, entranceAngles) {
			continue
		}
		angle := float32(arcLen)*float32(s) + float32(arcLen)/2
		cx := wallR * float32(math.Cos(float64(angle)))
		cy := wallR * float32(math.Sin(float64(angle)))
		walls = append(walls, wallSpec{
			Center:   spatial.Vec2{X: cx, Y: cy},
			Width:    segLen,
			Height:   cfg.DungeonWallThickness,
			Rotation: angle + float32(math.Pi/2),
		})
	}

	for _, e := range g.edges {
		a := g.chambers[e.a]
		b := g.chambers[e.b]
		dx := b.center.X - a.center.X
		dy := b.center.Y - a.center.Y
		edgeLen := float32(math.Hypot(float64(dx), float64(dy)))
		if edgeLen < 1 {
			continue
		}
		dirX := dx / edgeLen
		dirY := dy / edgeLen
		pX := -dirY
		pY := dirX
		startGap := a.radius
		endGap := b.radius
		corridorActualLen := edgeLen - startGap - endGap
		if corridorActualLen <= 0 {
			continue
		}
		midX := a.center.X + dirX*(startGap+corridorActualLen/2)
		midY := a.center.Y + dirY*(startGap+corridorActualLen/2)
		rotation := float32(math.Atan2(float64(dirY), float64(dirX)))
		offset := cfg.DungeonCorridorWidth/2 + cfg.DungeonWallThickness/2

		walls = append(walls, wallSpec{
			Center:   spatial.Vec2{X: midX + pX*offset, Y: midY + pY*offset},
			Width:    corridorActualLen,
			Height:   cfg.DungeonWallThickness,
			Rotation: rotation,
		})
		walls = append(walls, wallSpec{
			Center:   spatial.Vec2{X: midX - pX*offset, Y: midY - pY*offset},
			Width:    corridorActualLen,
			Height:   cfg.DungeonWallThickness,
			Rotation: rotation,
		})
	}

	return walls
}

// pickEntranceAngles returns the ring-slot indices that are entrance
// gaps (rest of the ring becomes wall segments). The primary entrance
// is on the visually-south side of the asteroid (slot 4 of 16 ≈ math
// angle 101° ≈ world +Y in PixiJS coords) so it faces the station,
// which sits south of the asteroid in world space. Other entrances
// spread evenly around the ring.
func pickEntranceAngles(_ *dungeonGraph, cfg *GameConfig, ringSegments int) []int {
	base := int(float64(ringSegments) * 0.25) % ringSegments
	out := []int{base}
	for i := 1; i < cfg.DungeonEntranceCount; i++ {
		out = append(out, (base+ringSegments*i/cfg.DungeonEntranceCount)%ringSegments)
	}
	return out
}

func isEntranceSlot(s int, gaps []int) bool {
	for _, g := range gaps {
		if g == s {
			return true
		}
	}
	return false
}

// entranceMaskFor returns the wire-replicated perimeter bitmap for a
// dungeon: bit i is 1 if slot i of the ring is a wall, 0 if it's an
// entrance gap. Derived from pickEntranceAngles so the client renders
// entrance markers at the exact slots the server carved gaps in
// generateWalls. ringSegments must match the constant in generateWalls.
func entranceMaskFor(g *dungeonGraph, cfg *GameConfig, ringSegments int) uint16 {
	entrances := pickEntranceAngles(g, cfg, ringSegments)
	var mask uint16
	for s := 0; s < ringSegments; s++ {
		if !isEntranceSlot(s, entrances) {
			mask |= 1 << s
		}
	}
	return mask
}
