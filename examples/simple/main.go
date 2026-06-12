package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	process := mmokit.New(mmokit.Config{
		Name:          "simple",
		AnonymousAuth: true,
	})

	mmokit.AddWasmSystem[mmokit.Position](process, "dist/wasmmods/wave.wasm")

	process.AddSystem(mmokit.NewSystem(&FieldSystem{}))

	process.Start()
}
