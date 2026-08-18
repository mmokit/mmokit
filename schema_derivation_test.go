package mmokit

import (
	"reflect"
	"testing"
)

// The protocol schema must not depend on which roles a process runs.
//
// It used to: AssembleFromProcess found the entity section by iterating
// Process.Cells and reading EntityKindDefs off the first cell that had any, so
// a process owning no cells — every gateway, and any pure coordinator —
// reported "entities": null. Measured on examples/space before this changed, a
// --mode=gateway dump carried 0 of 12 entities, 0 of 6 broadcasts and 5 of 12
// operations. A gateway is the process a client connects to, so a contract
// derived there and a contract derived on a host were different documents.
//
// This asserts the property directly: the kinds derived from a constructed
// schema stage equal the kinds realized on a real cell's stage.
func TestSchemaStageDerivesTheSameKindsAsACell(t *testing.T) {
	mmo := newTestProcess(t)
	RegisterKind[kindRegTestBundle](mmo, 100, "TestKind")
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	cell := firstCell(mmo)
	if cell == nil {
		t.Fatal("expected at least one cell")
	}
	fromCell := cell.Stage.EntityKindDefs()
	fromSchemaStage := mmo.NewSchemaStage().EntityKindDefs()

	if len(fromSchemaStage) != len(fromCell) {
		t.Fatalf("schema stage realized %d kinds, cell has %d", len(fromSchemaStage), len(fromCell))
	}
	for kind, cellDef := range fromCell {
		stageDef, ok := fromSchemaStage[kind]
		if !ok {
			t.Errorf("kind %d realized on the cell but not on the schema stage", kind)
			continue
		}
		if stageDef.Name != cellDef.Name {
			t.Errorf("kind %d: schema stage name %q, cell name %q", kind, stageDef.Name, cellDef.Name)
		}
	}
}

// The schema is assembled by Build, not by the --dump-schema path, so what a
// running server reports equals what generated its SDK. Before this, Schema()
// returned "entities": null at runtime on every process, which made a
// runtime-computed fingerprint structurally incapable of matching the dumped
// one an SDK is built from.
func TestProtocolSchemaIsPopulatedAfterBuild(t *testing.T) {
	mmo := newTestProcess(t)
	RegisterKind[kindRegTestBundle](mmo, 100, "TestKind")
	proto := NewProtocol(mmo, "test")
	mmo.SetProtocol(proto)
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	schema := proto.Schema()
	if len(schema.Entities) == 0 {
		t.Fatal("Schema().Entities is empty after Build — the derivation did not run")
	}
	var found bool
	for _, e := range schema.Entities {
		if e.Kind == 100 && e.Name == "TestKind" {
			found = true
		}
	}
	if !found {
		t.Errorf("registered kind 100 (TestKind) absent from Schema().Entities: %+v", schema.Entities)
	}
}

// Assembly is idempotent. Build calls it, and a fingerprint recomputed later
// must be the same value — EntityName appends, so a second pass used to
// duplicate every name.
func TestAssembleFromProcessIsIdempotent(t *testing.T) {
	mmo := newTestProcess(t)
	RegisterKind[kindRegTestBundle](mmo, 100, "TestKind")
	proto := NewProtocol(mmo, "test")
	mmo.SetProtocol(proto)
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	first := proto.Schema()
	proto.AssembleFromProcess(mmo)
	second := proto.Schema()

	if !reflect.DeepEqual(first, second) {
		t.Errorf("schema changed on reassembly:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}
