package main

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/component"

	"github.com/mlange-42/ark/ecs"
)

func TestDriftVelocity_IsAlwaysFullSpeed(t *testing.T) {
	for i := range CubesPerCell {
		vx, vy := DriftVelocity(i)
		speed := math.Hypot(float64(vx), float64(vy))
		if math.Abs(speed-DriftSpeed) > 1e-3 {
			t.Errorf("cube %d: |v| = %.4f, want %v", i, speed, DriftSpeed)
		}
	}
}

// TestDriftVelocity_SpreadsDirections guards the reason the golden angle is
// there at all. An angle step that divides 2π evenly — π/2, say — gives every
// fourth cube the same heading, and the field marches in four columns.
func TestDriftVelocity_SpreadsDirections(t *testing.T) {
	seen := make(map[int]int)
	for i := range CubesPerCell {
		vx, vy := DriftVelocity(i)
		// Bucket into 30° sectors; with 16 cubes over 12 sectors no sector
		// should hold more than two.
		sector := int(math.Floor((math.Atan2(float64(vy), float64(vx)) + math.Pi) / (math.Pi / 6)))
		seen[sector]++
	}
	for sector, n := range seen {
		if n > 2 {
			t.Errorf("sector %d holds %d cubes — headings are clustering", sector, n)
		}
	}
	if len(seen) < 8 {
		t.Errorf("only %d of 12 heading sectors used", len(seen))
	}
}

// TestDriftVelocity_IsDeterministic is a distributed-mode requirement, not a
// style preference: every host bootstraps its own cells, and a rand-derived
// heading would give the same cube a different one on each host.
func TestDriftVelocity_IsDeterministic(t *testing.T) {
	for i := range CubesPerCell {
		x1, y1 := DriftVelocity(i)
		x2, y2 := DriftVelocity(i)
		if x1 != x2 || y1 != y2 {
			t.Fatalf("cube %d: DriftVelocity is not a pure function", i)
		}
	}
}

func TestReflectAxis(t *testing.T) {
	const min, max = 40, 1960

	cases := []struct {
		name    string
		p, v    float32
		want    float32
		comment string
	}{
		{name: "inside, moving up", p: 1000, v: 55, want: 55},
		{name: "inside, moving down", p: 1000, v: -55, want: -55},
		{name: "at low edge, outbound", p: 40, v: -55, want: 55},
		{name: "past low edge, outbound", p: 12, v: -55, want: 55},
		{name: "at high edge, outbound", p: 1960, v: 55, want: -55},
		{name: "past high edge, outbound", p: 1990, v: 55, want: -55},
		// The inbound cases are what stops a cube from vibrating at the
		// wall: for the tick or two it spends outside the margin after a
		// turn, its velocity already points back inward and must be left
		// alone.
		{name: "past low edge, already inbound", p: 12, v: 55, want: 55},
		{name: "past high edge, already inbound", p: 1990, v: -55, want: -55},
		{name: "stationary at the edge", p: 40, v: 0, want: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReflectAxis(c.p, c.v, min, max); got != c.want {
				t.Errorf("ReflectAxis(%v, %v) = %v, want %v", c.p, c.v, got, c.want)
			}
		})
	}
}

// TestReflectAxis_MarginClearsTheFrameworkClamp is the assertion that keeps
// drifters moving forever. BoundarySystem clamps an entity that leaves the
// world and ZEROES the velocity component that took it there — so a cube that
// ever reaches the world edge does not bounce, it parks against the wall. The
// turn has to happen with a full tick of travel still to spare.
func TestReflectAxis_MarginClearsTheFrameworkClamp(t *testing.T) {
	const frameworkEdgeMargin = 5 // pkg/universe.edgeMargin
	perTick := float32(DriftSpeed) / TickRate
	if DriftMargin <= perTick+frameworkEdgeMargin {
		t.Fatalf("DriftMargin %v does not clear one tick of travel (%v) plus the framework's edge margin (%v)",
			float32(DriftMargin), perTick, float32(frameworkEdgeMargin))
	}
}

// TestDrift_StaysInsideTheWorld walks the reflect rule forward for a simulated
// minute per cube and asserts nothing escapes. The reflect-at-the-margin rule
// is only correct if the margin is wide enough for the speed; this is what
// couples the two constants together.
func TestDrift_StaysInsideTheWorld(t *testing.T) {
	const dt = 1.0 / TickRate
	for i := range CubesPerCell {
		if IsBouncer(i) {
			continue
		}
		lx, ly := cubeXY(i)
		// Start in the far corner cell, the worst case for the high edge.
		x := float32(CellSize) + lx
		y := float32(CellSize) + ly
		vx, vy := DriftVelocity(i)
		for tick := range TickRate * 60 {
			vx = ReflectAxis(x, vx, DriftMargin, WorldSizeX-DriftMargin)
			vy = ReflectAxis(y, vy, DriftMargin, WorldSizeY-DriftMargin)
			x += vx * dt
			y += vy * dt
			if x < 0 || x > WorldSizeX || y < 0 || y > WorldSizeY {
				t.Fatalf("cube %d escaped the world at tick %d: (%v, %v)", i, tick, x, y)
			}
		}
	}
}

// TestDrift_CrossesACellLine is the point of the whole system. If a drifting
// cube never leaves the cell it was bootstrapped into, nothing in this example
// exercises entity handoff and the browser's blue/amber distinction never
// changes for anything but the viewer.
func TestDrift_CrossesACellLine(t *testing.T) {
	const dt = 1.0 / TickRate
	crossed := 0
	for i := range CubesPerCell {
		if IsBouncer(i) {
			continue
		}
		lx, ly := cubeXY(i)
		x, y := lx, ly
		vx, vy := DriftVelocity(i)
		startCX, startCY := int(x/CellSize), int(y/CellSize)
		for range TickRate * 30 {
			vx = ReflectAxis(x, vx, DriftMargin, WorldSizeX-DriftMargin)
			vy = ReflectAxis(y, vy, DriftMargin, WorldSizeY-DriftMargin)
			x += vx * dt
			y += vy * dt
			if int(x/CellSize) != startCX || int(y/CellSize) != startCY {
				crossed++
				break
			}
		}
	}
	if crossed == 0 {
		t.Fatal("no drifting cube left its cell within 30 seconds — handoff is never exercised")
	}
}

// TestCube3D_DriftersCrossCellBoundaries is the drift feature's acceptance
// criterion: a cube leaves the cell it was bootstrapped into and arrives,
// intact, in the cell that owns where it went.
//
// Nothing else here asserts that. ReflectAxis is a pure function tested
// against arithmetic; the split test proves cubes survive a topology change
// the OPERATOR triggered. This is the only test covering the handoff an
// entity triggers by itself, which is the one thing a viewer watching this
// example is meant to see happen without touching anything.
func TestCube3D_DriftersCrossCellBoundaries(t *testing.T) {
	process, stop := runProcess(t)
	defer stop()

	start := cubeCells(t, process)
	if len(start) == 0 {
		t.Fatal("no cubes exist")
	}

	// The lattice is inset 100 units from the cell edge, so the earliest a
	// well-aimed cube can reach a boundary is under two seconds. The deadline
	// is generous because the headings are fixed and some cubes start by
	// moving inward.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		now := cubeCells(t, process)
		for id, at := range now {
			was, ok := start[id]
			if !ok || was.cell == at.cell {
				continue
			}
			// It crossed. It must also still be at the height it left with:
			// a drifter's Z never changes, so a cube that arrives at a
			// different one lost it in transit — the exact silent failure
			// this example exists to catch, in the path the split test does
			// not cover.
			if at.z != was.z {
				t.Fatalf("cube %d crossed %s -> %s and arrived at Z=%v, want %v",
					id, was.cell, at.cell, at.z, was.z)
			}
			return
		}
	}
	t.Fatal("no cube changed cells in 20 seconds — entity handoff is never exercised")
}

// cubeWhere is where one cube is: which cell holds it, and at what height.
type cubeWhere struct {
	cell string
	z    float32
}

// cubeCells maps every live cube's NetworkID to where it currently is.
func cubeCells(t *testing.T, process *mmokit.Process) map[uint32]cubeWhere {
	t.Helper()

	var ids []mmokit.MeshCellID
	process.Control.AllOwnedCells(func(cellKey, _ string) bool {
		ids = append(ids, mmokit.MeshCellID(cellKey))
		return true
	})

	out := make(map[uint32]cubeWhere)
	for _, id := range ids {
		cell := process.CellByID(id)
		if cell == nil || cell.Stage == nil || cell.Engine == nil {
			continue
		}
		stage := cell.Stage
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := cell.Engine.RunOnLoop(ctx, func() error {
			w := stage.ECSWorld()
			posMap := ecs.NewMap1[component.Position](w)
			netMap := ecs.NewMap1[component.NetworkID](w)
			bounceMap := ecs.NewMap1[Bounce](w)
			spinMap := ecs.NewMap1[Spin](w)
			filter := ecs.NewFilter1[component.Position](w).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			defer q.Close()
			for q.Next() {
				e := q.Entity()
				// Drifters only: a bouncer's height changes every tick, so
				// including one would make the Z comparison below fail for
				// a reason that has nothing to do with a transfer. Selected
				// by a zero Launch rather than by the component's absence,
				// because every cube carries one.
				if !spinMap.HasAll(e) || !netMap.HasAll(e) {
					continue
				}
				if bounceMap.HasAll(e) && bounceMap.Get(e).Launch > 0 {
					continue
				}
				out[netMap.Get(e).ID] = cubeWhere{cell: string(id), z: posMap.Get(e).Z}
			}
			return nil
		})
		cancel()
		if err != nil {
			t.Fatalf("cell scan on %s: %v", id, err)
		}
	}
	return out
}
