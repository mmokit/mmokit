package main

import (
	"strings"
	"testing"
)

func sampleEntitySchema() ProtocolSchema {
	return ProtocolSchema{
		Game: "demo",
		Entities: []EntitySchema{
			{
				Kind: 0,
				Name: "Ship",
				Bindings: []BindingSchema{
					{Type: "Position", Fields: []BindingSchemaField{
						{Name: "x", Encoding: "f32", Size: 4},
						{Name: "y", Encoding: "f32", Size: 4},
					}},
					{Type: "Velocity", Fields: []BindingSchemaField{
						{Name: "vx", Encoding: "qvel", Size: 2, Scale: 0.01},
					}},
					{Type: "Name", Fields: []BindingSchemaField{
						{Name: "shipName", Encoding: "string", Initial: true},
					}},
				},
				VarTail: &VarTailSchema{
					Name:     "statusEffects",
					ItemSize: 5,
					ItemFields: []BindingSchemaField{
						{Name: "kind", Encoding: "u8", Size: 1},
						{Name: "stacks", Encoding: "f32", Size: 4},
					},
				},
			},
			{Kind: 1, Name: "Asteroid", Bindings: []BindingSchema{
				{Type: "Position", Fields: []BindingSchemaField{
					{Name: "x", Encoding: "f32", Size: 4},
				}},
			}},
		},
	}
}

func TestCsharpBackend_EntityType(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genEntityType(sampleEntitySchema())
	for _, want := range []string{
		"namespace Mmokit.Sdk",
		"public enum EntityType : byte",
		"Ship = 0,",
		"Asteroid = 1,",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genEntityType missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_Entities(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genEntities(sampleEntitySchema())
	for _, want := range []string{
		"public abstract class EntityBase",
		"public uint NetID;",
		"public sealed class ShipEntity : EntityBase",
		"public float x;",         // f32 → float
		"public float vx;",        // qvel → float
		"public string shipName;", // string + initial field present
		"public List<ShipStatusEffectsItem> statusEffects = new();",
		"public sealed class ShipStatusEffectsItem",
		"public byte kind;",       // u8 → byte
		"public sealed class AsteroidEntity : EntityBase",
		"public sealed class DeltaWorldUpdate",
		"public List<EntityBase> Entered = new();",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genEntities missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_OutputFiles_SkipsWhenNoEntities(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(ProtocolSchema{})
	if len(files) != 0 {
		t.Fatalf("expected no files for empty schema, got %d", len(files))
	}
	files = csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	for _, name := range []string{"EntityType.cs", "Entities.cs"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("expected %s in OutputFiles", name)
		}
	}
}

func TestCsharpBackend_CoreFiles(t *testing.T) {
	core := csharpBackend{coreDir: "x/core"}.CoreFiles()
	if len(core) != 5 {
		t.Fatalf("CoreFiles len = %d, want 5", len(core))
	}
	// Dst basenames are what the SDK compiles; Src is threaded from coreDir.
	if core[0].Dst != "DeltaDecoderCore.cs" || core[0].Src != "x/core/DeltaDecoderCore.cs" {
		t.Fatalf("CoreFiles[0] = %+v", core[0])
	}
}
