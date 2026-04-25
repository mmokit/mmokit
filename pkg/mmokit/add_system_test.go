package mmokit

import "testing"

// fakeSys satisfies System with no-op implementations. Used to exercise
// AddSystem's type plumbing without spinning up a full Process.
type fakeSys struct {
	SystemBase
	name string
}

func (f *fakeSys) Init()             {}
func (f *fakeSys) Update(dt float32) {}

func TestAddSystem_Compiles(t *testing.T) {
	// This test compiles iff AddSystem[T] generic constraints resolve via
	// PT inference (Go infers PT from *T satisfying System). The single-
	// type-arg form is what callers actually use; spell only T here so
	// this test models idiomatic usage.
	_ = AddSystem[fakeSys]
}
