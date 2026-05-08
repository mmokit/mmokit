// Simplest possible mmokit server. A field of entities bobs vertically
// in a traveling sine wave; positions are pushed every tick over the
// engine's WebSocket service to every connected client.
//
// AnonymousAuth=true skips the entire auth/login plumbing — every WS
// upgrade synthesizes a session so OnPlayerJoin fires and the player
// shows up in the engine's PlayerManager. Production games never set
// this; it's a dev/example escape hatch.
//
// Run:   go run ./examples/simple
// Open:  http://localhost:8080
package main

import (
	"embed"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

//go:embed index.html
var webFS embed.FS

func main() {
	mmokit.RegisterEvent[WaveStateMsg]()

	process := mmokit.New(mmokit.Config{
		WebDir:        "embed",
		StaticFS:      webFS,
		AnonymousAuth: true,
		Protocol:      mmokit.NewProtocol("simple"),
	})

	process.AddSystem(mmokit.NewSystem(&FieldSpawnerSystem{}))
	process.AddSystem(mmokit.NewSystem(&SineWaveSystem{}))

	process.Start()
}
