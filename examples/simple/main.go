package main

import (
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func main() {
	process := mmokit.New(mmokit.Config{
		Name:          "simple",
		AnonymousAuth: true,
	})

	process.AddSystem(mmokit.NewSystem(&FieldSystem{}))

	process.Start()
}
