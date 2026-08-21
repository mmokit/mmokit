package main

import (
	"math"
	"testing"
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
