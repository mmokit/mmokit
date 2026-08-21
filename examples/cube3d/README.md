# cube3d

The framework's headless 3D reference process, and the executable form of
[roadmap](../../docs/roadmap.md) §7.5 phase 2's acceptance criterion.

`examples/space` is the 2D regression bed. This is deliberately the smallest
thing that exercises a dimension the reference game does not: entities carry Z,
fall under gravity, tumble on all three axes, replicate through the 3D engine
binding set, and survive a cell split with their vertical state intact.

## Running it

```bash
go run ./examples/cube3d
```

No database, no frontend, no client — and that is a property worth keeping,
because it means the 3D acceptance test runs anywhere `go test` runs:

```bash
go test ./examples/cube3d/
```

## What it pins

`TestCube3D_SurvivesCellSplitWithZ` spawns 16 cubes per cell across a 2×2 grid,
each at a distinct height, splits `cell_0_0`, and asserts that every cube
survives **and keeps its height**.

The height half is the point. Phase 1 widened the mesh transfer frame to carry
`PosZ` but never populated it, so every cube would have arrived at Z=0 with no
error anywhere — a split that looks perfectly healthy by entity count alone.
Reverting that fix turns this test red with `57 cubes aloft, want 64`.

## Why the shape is what it is

- **One kind, `Cube`, whose bundle carries only `Spin`.** Position, velocity,
  collider extents and *orientation* all come from the 3D engine binding set.
  A game that attached its own orientation binding here would be refused by
  `BuildReplicators`, because the 3D profile already emits one.
- **Every cube starts airborne.** A cube resting at Z=0 replicates and
  transfers identically in a 2D profile, so the fixture could not tell a
  working 3D pipeline from a broken one.
- **`TumbleSystem` rotates about all three axes.** A static quaternion encodes
  identically every tick, so the delta encoder would never send the `rot` field
  and the 3D orientation path would look healthy while being untested.
- **Built through `mmokit.New`, not `universe.New`.** The facade installs a
  Protocol unconditionally; without one the process's schema fingerprint is 0,
  which the mesh admission reads as "no protocol" and which would silently opt
  this example out of the dimension-agreement gate.
