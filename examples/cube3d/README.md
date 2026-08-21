# cube3d

The framework's headless 3D reference process, and the executable form of
[roadmap](../../docs/roadmap.md) §7.5 phase 2's acceptance criterion.

`examples/space` is the 2D regression bed. This is deliberately the smallest
thing that exercises a dimension the reference game does not: entities carry Z,
fall under gravity, tumble on all three axes, replicate through the 3D engine
binding set, cross cell boundaries on their own, and survive a cell split with
their vertical state intact.

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

A field of tumbling cubes over a lit grid, in two roles:

- **Bouncers** fall, hit the ground plane, bounce, and re-launch to their own
  apex forever. They are stationary horizontally. This is the gravity showcase
  — and it is a loop rather than a one-off because a field that simply falls
  has finished being interesting five seconds after it starts.
- **Drifters** hold one height and roam the whole world, turning around at the
  world edge. They leave the cell they were bootstrapped into and are handed
  off to whichever cell owns where they went — and change colour in the browser
  at the moment they do. Before them the only entity that ever crossed a cell
  line was you.

Colour is which cell owns a thing, from your point of view: **red** is you,
**blue** is your own cell, **amber** is another cell replicating across a
border to you, grey is before `DebugInfo` has arrived. That is not the server's
Live/Replica presence, which a client is never told — it is the same
distinction computed from your own vantage point.

If you see a horizontal line and a few dots, something is wrong with the
camera or with gravity's sign — that is exactly what the first version of this
example looked like.

**Click the view first.** The keys do nothing until the pointer is captured;
the on-screen legend dims until it is. Then <kbd>W</kbd>/<kbd>A</kbd>/
<kbd>S</kbd>/<kbd>D</kbd> fly (each works on its own), <kbd>Space</kbd> and
<kbd>Shift</kbd> move world-vertically whatever the camera is pitched at,
<kbd>Esc</kbd> releases the mouse, and <kbd>F3</kbd> toggles the debug overlay.

## The debug overlay

<kbd>F3</kbd>, on by default, in the shape `4node-basic`'s canvas HUD has: link
state, FPS, the newest frame sequence, the interpolation delay and jitter the
playback controller has settled on, frame loss, your netID, the cell you are
in and the node that owns it, your position, the entity count broken down by
class, and the world extent. Bottom right lists every cell with `▸` on yours.

In the scene it also draws each cell's boundary and a label carrying the same
coordinates the console takes — so `cell split 0_0` in the server's terminal
names a rectangle you can see — plus a ring on the ground at your
area-of-interest radius. The ring is the answer to "why did that cube vanish":
replication is by distance from you, and without it drawn the boundary is
invisible.

All of it comes from the `topology` debug grant, which this example gives every
viewer unconditionally (a real game would gate it). It needs no wire change:
`DebugInfo` and `CellChange` were already engine-default server events, and
`NewDebugBroadcaster` only makes them actually get sent.

## What it pins

`TestCube3D_SurvivesCellSplitWithZ` spawns 16 cubes per cell across a 2×2 grid,
each at a distinct height, splits `cell_0_0`, and asserts that every cube
survives **and keeps its exact height**.

The height half is the point. Phase 1 widened the mesh transfer frame to carry
`PosZ` but never populated it, so every cube would have arrived at Z=0 with no
error anywhere — a split that looks perfectly healthy by entity count alone.

It asserts on the *drifters* specifically, because their Z is constant: a
bouncer's height is a function of time and proves nothing about a transfer.
The count is polled to convergence rather than sampled once — a cube in flight
between two cells is live in neither for the tick the handoff takes, and the
census walks cells one at a time. A cube actually lost never converges.

`TestCube3D_GravityMovesTheBouncers` is the other half, and the only test that
can catch it: it watches real cubes fall and come back up. `BounceSystem`'s
arithmetic is unit-tested in isolation, so registering it *before* physics, or
spawning bouncers `MoveWalk` instead of `MoveBallistic`, leaves every other
test green.

## Why the shape is what it is

- **One kind, `Cube`, whose bundle carries only `Spin`.** Position, velocity,
  collider extents and *orientation* all come from the 3D engine binding set.
  A game that attached its own orientation binding here would be refused by
  `BuildReplicators`, because the 3D profile already emits one.
- **Every cube starts airborne.** A cube resting at Z=0 replicates and
  transfers identically in a 2D profile, so the fixture could not tell a
  working 3D pipeline from a broken one.
- **Bouncers are `MoveBallistic`, not `MoveWalk`.** `MoveWalk` clamps to the
  ground plane and zeroes the downward velocity, and the impact speed is the
  one number a bounce is computed from. Ballistic leaves the ground to the
  game, which is what that mode is for.
- **`Bounce` carries no `net:` tag.** It is server-side state no client
  renders. An untagged field on a registered kind component still crosses a
  cell boundary — that is exactly what separates it from `mmokit:"local"` — so
  a cube handed to a neighbour mid-flight keeps its own apex. It costs zero
  wire bytes, though it does rotate the schema fingerprint: that hash gates
  mesh admission as well as client decoding, and for the mesh half the
  component table matters even at zero bytes.
- **Every cube carries a `Bounce`, and a zero `Launch` means "does not
  bounce".** A kind's component set is uniform after a transfer: the
  destination calls `EnsureEntityKindComponents`, which adds a zero value for
  every component the kind declares. `mmokit:"optional"` therefore means "the
  caller may omit it at spawn", not "this entity may lack it". Marking `Bounce`
  optional and omitting it for drifters is how this was first written, and
  within two seconds the eight drifters that had crossed a boundary were
  carrying a `Bounce` nobody spawned — while the ones that had not crossed were
  not. `TestCube3D_EveryCubeCarriesBounce` pins the fix.
- **Drift headings come from the golden angle, not from `rand`.** Every host
  bootstraps its own cells, and a random heading would give the same cube a
  different one on each host in a distributed run.
- **Gravity is −400, not −9.81.** Earth-ish *at this world's scale*: a cube is
  40 units and reads as about a metre, so a unit is ~2.5 cm. At −9.81 a cube
  dropped from 490 units takes ten seconds to land, and what you see is not
  falling, it is drifting.
- **`TumbleSystem` rotates about all three axes.** A static quaternion encodes
  identically every tick, so the delta encoder would never send the `rot` field
  and the 3D orientation path would look healthy while being untested.
- **Built through `mmokit.New`, not `universe.New`.** The facade installs a
  Protocol unconditionally; without one the process's schema fingerprint is 0,
  which the mesh admission reads as "no protocol" and which would silently opt
  this example out of the dimension-agreement gate.
