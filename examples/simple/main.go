// Simplest possible mmokit server. One entity oscillates left and right.
// Use the interactive console to inspect state: "entity list", "perf".
package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	process := mmokit.New(mmokit.Config{
		OnInit: func(stage *mmokit.Stage) {
			stage.SpawnEntity(mmokit.Position{X: 0, Y: 0})
		},
	})

	process.AddSystem(mmokit.NewSystem(&OscillateSystem{}))

	process.Start()
}
