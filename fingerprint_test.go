package mmokit

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/system"
)

// baseSchema is shaped like a real one: an entity whose engine bindings emit
// two world coordinates as f32, a quantized velocity, and a game component.
func baseSchema() ProtocolSchema {
	return ProtocolSchema{
		Game:      "test",
		Dimension: "2d",
		Entities: []system.EntitySchema{{
			Kind: 0,
			Name: "Ship",
			Bindings: []system.BindingSchema{
				{
					Type: "engine_bindings",
					Fields: []system.BindingSchemaField{
						{Name: "worldX", Encoding: "f32", Size: 4},
						{Name: "worldY", Encoding: "f32", Size: 4},
						{Name: "velX", Encoding: "qvel", Size: 2, Scale: 2000},
					},
				},
				{
					Type:       "component",
					StructName: "HealthComp",
					Fields:     []system.BindingSchemaField{{Name: "hp", Encoding: "f32", Size: 4}},
				},
			},
			Layout: []int{4, 4, 2, 4},
		}},
		ServerEventTypes: []ServerEventTypeSchema{{
			Name:   "game.PlayerDied",
			TypeID: 0xDEAD,
			Fields: []BroadcastFieldSchema{{Name: "netID", Encoding: "u32", Size: 4}},
		}},
	}
}

// THE MOTIVATING FAILURE. A 3D engine binding set emits three world
// coordinates where a 2D one emits two. Type IDs cannot see that — one
// component type set across profiles means a 2D and a 3D component.Position
// hash identically — so if the fingerprint cannot see it either, this whole
// unit is theatre.
func TestFingerprintRotatesOnA3DShapedChange(t *testing.T) {
	twoD := baseSchema()

	threeD := baseSchema()
	threeD.Dimension = "3d"
	b := threeD.Entities[0].Bindings[0]
	b.Fields = append([]system.BindingSchemaField{
		b.Fields[0], b.Fields[1],
		{Name: "worldZ", Encoding: "f32", Size: 4},
	}, b.Fields[2:]...)
	threeD.Entities[0].Bindings[0] = b
	threeD.Entities[0].Layout = []int{4, 4, 4, 2, 4}

	if SchemaFingerprint(twoD) == SchemaFingerprint(threeD) {
		t.Fatal("a third world coordinate did not rotate the fingerprint — " +
			"a 2D client would be admitted to a 3D server and mis-decode every snapshot")
	}
}

// The two exclusions, and the criterion behind them: cmd/sdkgen drops
// structName at its JSON boundary and never reads a binding's Type, so neither
// can change a generated SDK or a wire byte. Rotating on them would reject
// every deployed client over a Go-side rename that owed no regeneration.
func TestFingerprintIgnoresWhatNoGeneratorConsumes(t *testing.T) {
	base := baseSchema()
	want := SchemaFingerprint(base)

	renamedStruct := baseSchema()
	renamedStruct.Entities[0].Bindings[1].StructName = "VitalityComponent"
	if got := SchemaFingerprint(renamedStruct); got != want {
		t.Errorf("renaming a Go struct rotated the fingerprint (%08x -> %08x); "+
			"sdkgen never decodes structName, so no client changed", want, got)
	}

	relabelled := baseSchema()
	relabelled.Entities[0].Bindings[0].Type = "engine_bindings_v2"
	if got := SchemaFingerprint(relabelled); got != want {
		t.Errorf("relabelling a binding Type rotated the fingerprint (%08x -> %08x); "+
			"no sdkgen site reads it", want, got)
	}
}

// Field names ARE generator output — sdkgen emits them as TypeScript property
// names — so a rename genuinely breaks app code against an unregenerated
// client and must rotate.
func TestFingerprintRotatesOnThingsGeneratorsConsume(t *testing.T) {
	base := SchemaFingerprint(baseSchema())

	for _, c := range []struct {
		name   string
		mutate func(*ProtocolSchema)
	}{
		{"field rename", func(p *ProtocolSchema) {
			p.Entities[0].Bindings[0].Fields[0].Name = "posX"
		}},
		{"encoding change", func(p *ProtocolSchema) {
			p.Entities[0].Bindings[0].Fields[0].Encoding = "f64"
		}},
		{"size change", func(p *ProtocolSchema) {
			p.Entities[0].Bindings[0].Fields[0].Size = 8
		}},
		{"quantization scale", func(p *ProtocolSchema) {
			// Structural: a client decoding qvel at the wrong scale gets
			// silently wrong values rather than a decode error.
			p.Entities[0].Bindings[0].Fields[2].Scale = 4000
		}},
		{"layout", func(p *ProtocolSchema) {
			p.Entities[0].Layout = []int{4, 4, 2, 8}
		}},
		{"entity kind", func(p *ProtocolSchema) { p.Entities[0].Kind = 7 }},
		{"entity name", func(p *ProtocolSchema) { p.Entities[0].Name = "Frigate" }},
		{"server event type ID", func(p *ProtocolSchema) { p.ServerEventTypes[0].TypeID = 0xBEEF }},
		{"server event field", func(p *ProtocolSchema) {
			p.ServerEventTypes[0].Fields[0].Encoding = "u64"
		}},
		{"dimension", func(p *ProtocolSchema) { p.Dimension = "3d" }},
		{"game name", func(p *ProtocolSchema) { p.Game = "other" }},
	} {
		s := baseSchema()
		c.mutate(&s)
		if got := SchemaFingerprint(s); got == base {
			t.Errorf("%s did not rotate the fingerprint", c.name)
		}
	}
}

// The fingerprint is a field of the document it hashes, so the projection
// zeroes it. Without that, populating the field would change the value that
// was supposed to go in it.
func TestFingerprintIsStableAgainstItsOwnField(t *testing.T) {
	s := baseSchema()
	bare := SchemaFingerprint(s)
	s.Fingerprint = FormatSchemaFingerprint(bare)
	if got := SchemaFingerprint(s); got != bare {
		t.Fatalf("fingerprint changed once written into the schema: %08x -> %08x", bare, got)
	}
}

func TestFingerprintIsDeterministicAndNonZero(t *testing.T) {
	first := SchemaFingerprint(baseSchema())
	for range 8 {
		if got := SchemaFingerprint(baseSchema()); got != first {
			t.Fatalf("fingerprint is not deterministic: %08x then %08x", first, got)
		}
	}
	// Zero means "no schema installed"; a real one must never collide with it.
	if SchemaFingerprint(ProtocolSchema{}) == 0 {
		t.Error("the empty schema hashed to 0, which is reserved for 'no protocol'")
	}
}

// The projection must not mutate the caller's schema — Schema() hands us the
// document it is about to serialize.
func TestFingerprintDoesNotMutateItsInput(t *testing.T) {
	s := baseSchema()
	s.Fingerprint = "aaaaaaaa"
	SchemaFingerprint(s)

	if s.Fingerprint != "aaaaaaaa" {
		t.Error("SchemaFingerprint cleared the caller's Fingerprint field")
	}
	if got := s.Entities[0].Bindings[1].StructName; got != "HealthComp" {
		t.Errorf("SchemaFingerprint cleared the caller's StructName: %q", got)
	}
	if got := s.Entities[0].Bindings[0].Type; got != "engine_bindings" {
		t.Errorf("SchemaFingerprint cleared the caller's binding Type: %q", got)
	}
}
