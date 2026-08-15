package game

import (
	"testing"

	"github.com/mmokit/mmokit"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := mmokit.NewEntityRegistry()
	r.Register(mmokit.EntityDef{Name: "ship", EntityType: 1, Spawnable: true})

	def, ok := r.Get("ship")
	if !ok {
		t.Fatal("expected to find 'ship'")
	}
	if def.Name != "ship" {
		t.Errorf("got name %q, want 'ship'", def.Name)
	}
	if def.EntityType != 1 {
		t.Errorf("got type %d, want 1", def.EntityType)
	}
}

func TestRegistry_GetUnknown(t *testing.T) {
	r := mmokit.NewEntityRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected ok=false for unknown name")
	}
}

func TestRegistry_ByType(t *testing.T) {
	r := mmokit.NewEntityRegistry()
	r.Register(mmokit.EntityDef{Name: "asteroid", EntityType: 5})

	def := r.ByType(5)
	if def.Name != "asteroid" {
		t.Errorf("got name %q, want 'asteroid'", def.Name)
	}
}

func TestRegistry_ByTypePanicsOnUnregistered(t *testing.T) {
	r := mmokit.NewEntityRegistry()

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic for unregistered type")
		}
	}()

	r.ByType(99)
}

func TestRegistry_AllReturnsRegistrationOrder(t *testing.T) {
	r := mmokit.NewEntityRegistry()
	r.Register(mmokit.EntityDef{Name: "alpha", EntityType: 1})
	r.Register(mmokit.EntityDef{Name: "beta", EntityType: 2})
	r.Register(mmokit.EntityDef{Name: "gamma", EntityType: 3})

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 defs, got %d", len(all))
	}
	want := []string{"alpha", "beta", "gamma"}
	for i, name := range want {
		if all[i].Name != name {
			t.Errorf("index %d: got %q, want %q", i, all[i].Name, name)
		}
	}
}

func TestRegistry_SpawnableNames(t *testing.T) {
	r := mmokit.NewEntityRegistry()
	r.Register(mmokit.EntityDef{Name: "ship", EntityType: 1, Spawnable: true})
	r.Register(mmokit.EntityDef{Name: "station", EntityType: 2, Spawnable: false})
	r.Register(mmokit.EntityDef{Name: "asteroid", EntityType: 3, Spawnable: true})

	names := r.SpawnableNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 spawnable names, got %d", len(names))
	}
	if names[0] != "ship" || names[1] != "asteroid" {
		t.Errorf("got %v, want [ship asteroid]", names)
	}
}
