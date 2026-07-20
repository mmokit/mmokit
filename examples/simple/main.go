package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	process := mmokit.New(mmokit.Config{
		Name:          "simple",
		AnonymousAuth: true,
	})

	process.AddSystem(mmokit.NewSystem(&FieldSystem{}))

	process.Start()
}
