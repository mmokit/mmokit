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
		stage.SpawnPlayer(session,
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
			ViewerName{Name: session.Username},
		)
	})

	mmokit.HandleClient(process, func(player mmokit.Entity, msg *FlyInput) {
		stage := player.Stage()
		if stage == nil {
			return
		}
		h := player.Handle()
		vel := stage.VelocityMap()
		rot := stage.RotationMap()
		if !vel.HasAll(h) || !rot.HasAll(h) {
			return
		}

		// Orientation is authoritative from the client's look angles: this is
		// a spectator camera, not a contested entity, so there is nothing to
		// cheat at and round-tripping it keeps the view responsive.
		orient := mmokit.RotationFromAxisAngle(0, 0, 1, msg.Yaw).
			RotateAxis(1, 0, 0, msg.Pitch)
		*rot.Get(h) = orient

		fwd := clampAxis(msg.Forward)
		strafe := clampAxis(msg.Strafe)
		lift := clampAxis(msg.Lift)

		// Forward and strafe act in the yaw plane; lift is world-vertical so
		// "up" stays up regardless of where the camera is pitched.
		sin, cos := sinCos(msg.Yaw)
		v := vel.Get(h)
		v.X = (fwd*cos - strafe*sin) * FlySpeed
		v.Y = (fwd*sin + strafe*cos) * FlySpeed
		v.Z = lift * FlySpeed
	})
}

// sinCos returns sin and cos of a yaw in radians.
func sinCos(yaw float32) (float32, float32) {
	return float32(math.Sin(float64(yaw))), float32(math.Cos(float64(yaw)))
}
