package tunable

import "testing"

type knobs struct {
	Amplitude float32 `tune:"default=220,min=60,max=420,step=10"`
	Count     int     `tune:"default=3,min=1,max=9"`
	Enabled   bool    `tune:"default=true"`
	private   float32 // untagged + unexported → ignored
	Untagged  float32 // exported but untagged → ignored
}

func TestReflectSourceListAndSet(t *testing.T) {
	k := &knobs{}
	if !HasTunables(k) {
		t.Fatal("knobs should report tunables")
	}
	src := Reflect(k)
	src.(interface{ ApplyDefaults() }).ApplyDefaults()

	if k.Amplitude != 220 || k.Count != 3 || !k.Enabled {
		t.Fatalf("defaults not applied: %+v", k)
	}
	defs := src.Tunables()
	if len(defs) != 3 {
		t.Fatalf("want 3 tunables (private/untagged excluded), got %d: %+v", len(defs), defs)
	}
	if defs[0].Name != "Amplitude" || defs[0].Kind != KindFloat || defs[0].Value != "220" {
		t.Fatalf("bad first def: %+v", defs[0])
	}

	if err := src.Set("Amplitude", "300"); err != nil {
		t.Fatal(err)
	}
	if k.Amplitude != 300 {
		t.Fatalf("set did not write field: %v", k.Amplitude)
	}
	if err := src.Set("Amplitude", "999"); err == nil {
		t.Fatal("out-of-range set should fail")
	}
	if err := src.Set("Nope", "1"); err == nil {
		t.Fatal("unknown field should fail")
	}
}

func TestHasTunablesFalse(t *testing.T) {
	type plain struct{ X float32 }
	if HasTunables(&plain{}) {
		t.Fatal("untagged struct should report no tunables")
	}
}
