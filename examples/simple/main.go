package main

import (
	"github.com/zenion/mmokit/pkg/mmokit"
)

func main() {
	process := mmokit.New(mmokit.Config{
		Name:          "simple",
		AnonymousAuth: true,
	})

	process.AddSystem(mmokit.NewSystem(&FieldSystem{}))

	process.Start()
}
