package tunectl

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/engine"
)

type tuneNativeSys struct {
	engine.SystemBase
	Gain float32 `tune:"default=2,min=0,max=10"`
}

func (s *tuneNativeSys) Update(dt float32) {}

func TestTunableSourceForNativeAndRegistry(t *testing.T) {
	sys := &tuneNativeSys{}
	src, ok := SourceFor(sys)
	if !ok {
		t.Fatal("native tagged system should resolve to a Source")
	}
	if err := src.Set("Gain", "5"); err != nil {
		t.Fatal(err)
	}
	if sys.Gain != 5 {
		t.Fatalf("set did not write field: %v", sys.Gain)
	}

	reg := newRegistry()
	reg.harvest("Field", src.Tunables())
	reg.setValue("Field", "Gain", "7")
	if v, _ := reg.value("Field", "Gain"); v != "7" {
		t.Fatalf("registry value = %q want 7", v)
	}
	defs := reg.defs("Field")
	if len(defs) != 1 || defs[0].Value != "7" {
		t.Fatalf("registry defs wrong: %+v", defs)
	}
}

type tuneNoTagsSys struct{ engine.SystemBase }

func (s *tuneNoTagsSys) Update(dt float32) {}

func TestTunableSourceForNone(t *testing.T) {
	if _, ok := SourceFor(&tuneNoTagsSys{}); ok {
		t.Fatal("a system with no tune-tagged fields must report no tunables")
	}
}

type tuneDefaultSys struct {
	engine.SystemBase
	Gain float32 `tune:"default=5,min=0,max=10"`
}

func (s *tuneDefaultSys) Update(dt float32) {}

// SyncSource must apply the tag default to a native system whose field starts
// at the Go zero value, and seed the registry with that default (not 0).
func TestSyncSourceAppliesNativeDefaults(t *testing.T) {
	sys := &tuneDefaultSys{} // Gain == 0 (default NOT yet applied)
	reg := newRegistry()
	src, ok := SourceFor(sys)
	if !ok {
		t.Fatal("tagged system should resolve")
	}
	SyncSource(reg, "Defaulted", src)

	if sys.Gain != 5 {
		t.Fatalf("tag default not applied to live field: Gain=%v want 5", sys.Gain)
	}
	if v, _ := reg.value("Defaulted", "Gain"); v != "5" {
		t.Fatalf("registry baseline = %q want 5", v)
	}

	// A subsequent operator change then a re-sync of a fresh instance inherits it.
	reg.setValue("Defaulted", "Gain", "8")
	fresh := &tuneDefaultSys{}
	freshSrc, _ := SourceFor(fresh)
	SyncSource(reg, "Defaulted", freshSrc)
	if fresh.Gain != 8 {
		t.Fatalf("fresh instance did not inherit intended value: Gain=%v want 8", fresh.Gain)
	}
}
