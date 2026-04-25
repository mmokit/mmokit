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
	// This test compiles iff AddSystem[T, PT] generic constraints resolve.
	// We don't actually call AddSystem on a *Process here (that requires
	// non-trivial setup) — but the type-level check is the primary risk:
	// whether *T satisfies System for an arbitrary T. The reference here
	// pins it.
	_ = AddSystem[fakeSys, *fakeSys]
}
