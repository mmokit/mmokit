package mmokit

import (
	"fmt"
	"reflect"

	"github.com/zenion/mmoserver/pkg/universe"
)

// AddState registers a per-stage state factory. Each Stage (cell's Stage)
// instantiates one *T at construction time by calling fn. Look up via
// State[T](stage).
func AddState[T any](p *universe.Process, fn func(*universe.Stage) *T) {
	name := reflect.TypeFor[T]().String()
	p.RegisterStateFactory(name, func(base *universe.Stage) any {
		return fn(base)
	})
}

// State returns the typed state previously registered via AddState[T] for this
// stage. Panics if T was not registered (programmer error).
func State[T any](stage *universe.Stage) *T {
	name := reflect.TypeFor[T]().String()
	v, ok := stage.StateByName(name)
	if !ok {
		panic(fmt.Sprintf("mmokit.State: type %s not registered via AddState", name))
	}
	return v.(*T)
}
