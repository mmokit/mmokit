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
//        http://localhost:9101/admin/     admin dashboard (login admin / admin)
package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	cfg := mmokit.Config{
		Name:          "simple",
		AnonymousAuth: true,
	}

	process := mmokit.New(cfg)

	process.AddSystem(mmokit.NewSystem(&SineWaveSystem{}))

	process.Start()
}
