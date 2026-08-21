# cube3d

The framework's headless 3D reference process, and the executable form of
[roadmap](../../docs/roadmap.md) §7.5 phase 2's acceptance criterion.

`examples/space` is the 2D regression bed. This is deliberately the smallest
thing that exercises a dimension the reference game does not: entities carry Z,
fall under gravity, tumble on all three axes, replicate through the 3D engine
binding set, and survive a cell split with their vertical state intact.

## Running it

```bash
cd examples/cube3d && just dev
```

Vite serves the browser client on <http://localhost:5173> with HMR; the Go
backend is on `:8080` and metrics on `:9101`. The server runs in the
foreground so its console is usable — type `cell split 0_0` there and watch
the split from the browser. `just run` builds the client instead and serves it
on `:5174`.

**The server needs a TTY.** A non-headless process starts an interactive
console, so backgrounded or piped its stdin hits EOF and it shuts down
immediately. Pass `--headless` when you want it without a console:
`just dev --headless`.

With a server running, `just verify` probes connection setup — a matching
schema fingerprint upgrades (426), a stale one is refused before the upgrade
(409).

**No database.** Every recipe here keeps it that way, which is also why the
acceptance test runs anywhere `go test` runs:

```bash
go test ./examples/cube3d/
```

## What you should see

A lattice of tumbling cubes hanging in the air between z=0 and roughly z=460,
half of them falling to rest on the ground grid and half hovering. Your own
viewer is red and starts about 260 units up, pitched down at the field.

If you see a horizontal line and a few dots, something is wrong with the
camera or with gravity's sign — that is exactly what the first version of this
example looked like.

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
