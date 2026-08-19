package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

// interleavedComp declares an `initial` field BEFORE a snapshot field, which is
// the shape that used to misname both of them. examples/space has two real
// components like this — NPCAI (Archetype/State) and POI (Type/Status).
type interleavedComp struct {
	Archetype uint8 `net:"initial,u8"`
	State     uint8 `net:"u8"`
	Tier      uint8 `net:"initial,u8"`
}

// The schema's field names must follow the fields they name.
//
// They used to be carried in a parallel []string on reflectBinding, appended in
// DECLARATION order by buildTaggedFields while schema() consumed it as
// snapshot-fields-then-initial-fields. Those two orders agree only when no
// initial field precedes a snapshot one, so a component like this one had its
// names emitted against the wrong fields — and the generated client decoded the
// per-tick byte into the initial field's name and the entered-payload byte into
// the snapshot field's name. Byte layout was never wrong; only the labels were,
// which is why nothing caught it: every round-trip and byte golden passed.
func TestSchemaFieldNamesFollowTheirFields(t *testing.T) {
	world := ecs.NewWorld()
	m := ecs.NewMap1[interleavedComp](world)
	bs := Component(m).schema()

	byName := map[string]BindingSchemaField{}
	for _, f := range bs.Fields {
		byName[f.Name] = f
	}

	for _, c := range []struct {
		field       string
		wantInitial bool
	}{
		{"archetype", true}, // net:"initial,u8"
		{"state", false},    // net:"u8"
		{"tier", true},      // net:"initial,u8"
	} {
		got, ok := byName[c.field]
		if !ok {
			t.Errorf("%q missing from the schema (fields: %+v)", c.field, bs.Fields)
			continue
		}
		if got.Initial != c.wantInitial {
			t.Errorf("%q: Initial = %v, want %v — the name is attached to the wrong field",
				c.field, got.Initial, c.wantInitial)
		}
	}
}
