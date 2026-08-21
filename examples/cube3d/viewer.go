package main

import (
	"math"

	"github.com/mmokit/mmokit"
)

// KindViewer is the player's own entity.
//
// A dedicated kind rather than a reused Cube: the schema dump and the admin
// UI both read better when the thing a client controls is named, and it costs
// one entity kind. Its bundle is empty of game components — position,
// velocity, collider and orientation all come from the 3D engine binding set,
// which is the whole point of the example.
const KindViewer uint8 = 2

// ViewerName labels a viewer so the browser can show who is who.
//
// It exists partly because RegisterKind refuses a bundle with no registrable
// fields, and partly because it is the one initial-only field in cube3d's
// schema — which means the 3D layout exercises the initial-payload path
// alongside the fixed one, rather than leaving it untested in this profile.
type ViewerName struct {
	Name string `net:"initial"`
}

// ViewerBundle is the viewer's game state. Position, velocity, collider and
// orientation are framework-owned and rejected as bundle fields.
type ViewerBundle struct {
	Name *ViewerName
}

// FlyInput is the client's desired motion, in the viewer's own frame.
//
// Axis inputs rather than a target position, because this is a 6DOF fly
// control and there is no ground to click on. Each field is -1, 0 or +1 as
// sent; the server clamps rather than trusting it.
//
// Wire layout (reflect codec): four float32s, little-endian, in declaration
// order. Yaw and pitch are absolute look angles in radians rather than deltas,
// so a dropped datagram cannot accumulate into a permanently rotated camera.
type FlyInput struct {
	Forward float32
	Strafe  float32
	Lift    float32
	Yaw     float32
	Pitch   float32
}

// FlySpeed is how fast a viewer moves, in world units per second.
const FlySpeed = 220

// ViewerSpawnZ is how high above the ground plane a viewer starts.
//
// Not zero, which is what SpawnPlayer gives it: the cubes settle onto z=0 and
// are 20 units tall, so a camera at z=0 sits at their mid-height and sees the
// scene edge-on — a horizontal band and the grid as a line. Starting above the
// tallest cube's rest height means the first frame looks like a 3D scene
// rather than a horizon.
const ViewerSpawnZ = 260

// FlyVelocity is the world-space velocity a fly input asks for.
//
// A pure function so it can be tested without a live connection. The bug it
// replaces was invisible precisely because the maths was buried in a handler
// nothing could call.
//
// Forward and strafe act in the YAW PLANE only; lift is world-vertical, so
// "up" stays up no matter where the camera is pitched. That is what makes a
// fly camera feel predictable rather than tumbling.
func FlyVelocity(msg *FlyInput) mmokit.Velocity {
	fwd := clampAxis(msg.Forward)
	strafe := clampAxis(msg.Strafe)
	lift := clampAxis(msg.Lift)

	// Right is forward x up. Facing +X with up +Z, that is -Y — so strafing
	// right SUBTRACTS from Y. The obvious-looking sign pair (+strafe*cos on
	// Y) inverts it and makes D move you left, which is what this shipped
	// with until a unit test on this function caught it.
	sin, cos := sinCos(msg.Yaw)
	return mmokit.Velocity{
		X: (fwd*cos + strafe*sin) * FlySpeed,
		Y: (fwd*sin - strafe*cos) * FlySpeed,
		Z: lift * FlySpeed,
	}
}

// FlyRotation is the orientation a fly input asks for: yaw about Z, then
// pitch about the camera's own X.
func FlyRotation(msg *FlyInput) mmokit.Rotation {
	return mmokit.RotationFromAxisAngle(0, 0, 1, msg.Yaw).
		RotateAxis(1, 0, 0, msg.Pitch)
}

// clampAxis keeps a hostile client from flying at arbitrary speed. The
// framework does not validate game input for the game.
func clampAxis(v float32) float32 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	if v != v { // NaN
		return 0
	}
	return v
}

// registerViewer wires the viewer kind, its spawn, and its input handler.
func registerViewer(process *mmokit.Process) {
	mmokit.RegisterKind[ViewerBundle](process, KindViewer, "Viewer")

	process.OnPlayerJoin(func(session *mmokit.PlayerSession, stage *mmokit.Stage) {
		viewer := stage.SpawnPlayer(session,
			mmokit.EntityKind{Type: KindViewer},
			mmokit.Collider{
				Shape:  mmokit.ShapeSphere,
				Radius: 12,
				Width:  24,
				Height: 24,
				Depth:  24,
			},
			// MoveFly: the viewer flies, so gravity must not act on it even
			// though the process configures gravity for the cubes.
			mmokit.Motion{Mode: mmokit.MoveFly},
			// Explicit, because Stage.Spawn does not add one. Without it the
			// 3D engine binding set emits identity for this entity forever
			// and the viewer never appears to turn.
			mmokit.RotationIdentity(),
			ViewerName{Name: session.Username},
		)

		// SpawnPlayer positions from the session's 2D spawn location, so Z is
		// 0. Lift the viewer after the fact — there is no 3D spawn resolver,
		// and adding one for a camera would be a wire change for no gain.
		if h := viewer.Handle(); stage.PositionMap().HasAll(h) {
			stage.PositionMap().Get(h).Z = ViewerSpawnZ
		}
	})

	mmokit.HandleClient(process, func(player mmokit.Entity, msg *FlyInput) {
		stage := player.Stage()
		if stage == nil {
			return
		}
		h := player.Handle()

		// Velocity only. Rotation is written separately BELOW and its absence
		// must never block movement — requiring both here is what made every
		// input a no-op: Stage.Spawn auto-adds a zero Velocity but does NOT
		// add a Rotation, so `rot.HasAll(h)` was false for the viewer and the
		// handler returned before touching anything.
		vel := stage.VelocityMap()
		if !vel.HasAll(h) {
			return
		}
		*vel.Get(h) = FlyVelocity(msg)

		// Orientation is authoritative from the client's look angles: this is
		// a spectator camera, not a contested entity, so there is nothing to
		// cheat at and round-tripping it keeps the view responsive.
		if rot := stage.RotationMap(); rot.HasAll(h) {
			*rot.Get(h) = FlyRotation(msg)
		}
	})
}

// sinCos returns sin and cos of a yaw in radians.
func sinCos(yaw float32) (float32, float32) {
	return float32(math.Sin(float64(yaw))), float32(math.Cos(float64(yaw)))
}
