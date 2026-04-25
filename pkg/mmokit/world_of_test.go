package mmokit

import (
	"testing"
)

type fakeSystem struct {
	gw any
}

func (f *fakeSystem) GameWorld() any { return f.gw }

type stubGW struct{ tag string }

func TestWorldOf_ReturnsTypedWorld(t *testing.T) {
	w := &stubGW{tag: "ok"}
	sys := &fakeSystem{gw: w}
	got := WorldOf[*stubGW](sys)
	if got == nil || got.tag != "ok" {
		t.Fatalf("WorldOf returned %+v, want stubGW{tag:\"ok\"}", got)
	}
}

func TestWorldOf_PanicsOnTypeMismatch(t *testing.T) {
	sys := &fakeSystem{gw: 42}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	_ = WorldOf[*stubGW](sys)
}
