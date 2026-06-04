// Simplest possible mmokit server. A field of entities bobs vertically
// in a traveling sine wave; positions are pushed every tick over the
// engine's WebSocket service to every connected client.
//
// AnonymousAuth=true skips the entire auth/login plumbing — every WS
// upgrade synthesizes a session so OnPlayerJoin fires and the player
// shows up in the engine's PlayerManager. Production games never set
// this; it's a dev/example escape hatch.
//
// The admin dashboard is left enabled (the engine default), so this demo
// needs a Postgres to back it. Use the justfile — `just run` brings up
// Postgres, ensures the mmo_simple database exists, and passes
// --postgres-url for you.
//
// Run:   just run        (from examples/simple/)
// Open:  http://localhost:5174            game client (run `just web-serve`)
//
//	http://localhost:9101/admin/     admin dashboard (login admin / admin)
package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	process := mmokit.New(mmokit.Config{
		Name:          "simple",
		AnonymousAuth: true,
	})

	// The field's MOTION is a hot-swappable wasm module — it runs before the
	// broadcaster below so each frame carries this tick's positions. Edit
	// wasmmods/wave/main.go, `just wasm-build`, then `wasm swap wave` to morph
	// the motion of the whole field live.
	mmokit.AddWasmSystem[mmokit.Position](process, "dist/wasmmods/wave.wasm")

	// Native scaffold: spawns the field + broadcasts positions to clients.
	process.AddSystem(mmokit.NewSystem(&FieldSystem{}))

	process.Start()
}
