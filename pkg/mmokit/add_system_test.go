package mmokit

import "testing"

// addSysFakeSystem satisfies System with no-op implementations. Used to
// exercise NewSystem's type plumbing without spinning up a full Process.
type addSysFakeSystem struct {
	SystemBase[any]
}

func (f *addSysFakeSystem) Update(dt float32) {}

// addSysFakeWidget covers the case where the type name does NOT end in
// "System" — strip should be a no-op.
type addSysFakeWidget struct {
	SystemBase[any]
}

func (f *addSysFakeWidget) Update(dt float32) {}

func TestNewSystem_StripsSystemSuffix(t *testing.T) {
	def := NewSystem(&addSysFakeSystem{})
	if def.Name != "addSysFake" {
		t.Fatalf("expected name %q (System suffix stripped), got %q", "addSysFake", def.Name)
	}
	got := def.Factory()
	if _, ok := got.(*addSysFakeSystem); !ok {
		t.Fatalf("expected factory to produce *addSysFakeSystem, got %T", got)
	}
}

func TestNewSystem_PreservesNonSystemSuffix(t *testing.T) {
	def := NewSystem(&addSysFakeWidget{})
	if def.Name != "addSysFakeWidget" {
		t.Fatalf("expected name %q (no suffix to strip), got %q", "addSysFakeWidget", def.Name)
	}
}

func TestNewSystem_NamedOverride(t *testing.T) {
	def := NewSystem(&addSysFakeSystem{}).Named("Custom")
	if def.Name != "Custom" {
		t.Fatalf("expected name %q after Named() override, got %q", "Custom", def.Name)
	}
}

func TestNewSystem_FreshInstancePerCall(t *testing.T) {
	def := NewSystem(&addSysFakeSystem{})
	a := def.Factory()
	b := def.Factory()
	if a == b {
		t.Fatal("expected each Factory() call to return a distinct instance")
	}
}

func TestNewSystem_RejectsNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil System input")
		}
	}()
	NewSystem(nil)
}
