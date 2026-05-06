package mmokit

import (
	"testing"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
)

// TestProtocolServerEventsBuilder verifies that Protocol.ServerEvents
// builder method registers a ServerEvents registry and merges its schema
// into the protocol's exported schema.
func TestProtocolServerEventsBuilder(t *testing.T) {
	p := NewProtocol("game").
		ServerEvents(func(e *ServerEvents) {
			RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
		})
	schema := p.Schema()
	found := false
	for _, ev := range schema.ServerEvents {
		if ev.Code == uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED) {
			if ev.Name != "playerSpawned" || ev.ProtoName != "enginepb.SpawnedMsg" {
				t.Errorf("wrong server event metadata: %+v", ev)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("SE_PLAYER_SPAWNED not present in schema")
	}
}

// TestProtocolOfAndServerEventsOf verifies that ProtocolOf returns the
// *Protocol stored in Config.Protocol, and that ServerEventsOf returns its
// registry.
func TestProtocolOfAndServerEventsOf(t *testing.T) {
	proto := NewProtocol("test").
		ServerEvents(func(e *ServerEvents) {
			RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
		})
	p := New(Config{Mode: "all", Protocol: proto})

	if got := ProtocolOf(p); got != proto {
		t.Errorf("ProtocolOf = %p, want %p", got, proto)
	}

	events := ServerEventsOf(p)
	if events == nil {
		t.Fatal("ServerEventsOf returned nil for protocol with registry")
	}
	if got := len(events.Schema()); got == 0 {
		t.Errorf("ServerEventsOf returned empty registry; expected SE_PLAYER_SPAWNED registered")
	}
}

// TestProtocolOfNilWhenUnset verifies that ProtocolOf and ServerEventsOf both
// return nil when Config.Protocol is not set.
func TestProtocolOfNilWhenUnset(t *testing.T) {
	p := New(Config{Mode: "all"})
	if got := ProtocolOf(p); got != nil {
		t.Errorf("ProtocolOf = %v, want nil", got)
	}
	if got := ServerEventsOf(p); got != nil {
		t.Errorf("ServerEventsOf = %v, want nil", got)
	}
}

type pTestOpReq struct{ X int32 }
type pTestOpRes struct{ Y int32 }

// TestProtocolSchemaTypedOperations verifies that registered typed-ops surface
// in ProtocolSchema.TypedOperations with correct kind, type IDs, names, and
// reflect-codec field shapes for both request + response.
func TestProtocolSchemaTypedOperations(t *testing.T) {
	t.Cleanup(ResetTypedOpRegistryForTest)
	ResetTypedOpRegistryForTest()

	RegisterOp[pTestOpReq, pTestOpRes](RouteGatewayLocal,
		func(_ *OpContext, req *pTestOpReq) (*pTestOpRes, error) {
			return &pTestOpRes{Y: req.X}, nil
		})

	schema := NewProtocol("test").Schema()
	if len(schema.TypedOperations) != 1 {
		t.Fatalf("TypedOperations: got %d, want 1 (entries=%+v)", len(schema.TypedOperations), schema.TypedOperations)
	}
	op := schema.TypedOperations[0]
	if op.Kind != "gateway-local" {
		t.Errorf("Kind: got %q, want gateway-local", op.Kind)
	}
	if op.RequestTypeName != "mmokit.pTestOpReq" {
		t.Errorf("RequestTypeName: got %q", op.RequestTypeName)
	}
	if op.ResponseTypeName != "mmokit.pTestOpRes" {
		t.Errorf("ResponseTypeName: got %q", op.ResponseTypeName)
	}
	if len(op.RequestFields) != 1 || op.RequestFields[0].Name != "x" || op.RequestFields[0].Encoding != "i32" {
		t.Errorf("RequestFields: got %+v", op.RequestFields)
	}
	if len(op.ResponseFields) != 1 || op.ResponseFields[0].Name != "y" || op.ResponseFields[0].Encoding != "i32" {
		t.Errorf("ResponseFields: got %+v", op.ResponseFields)
	}
}

// TestProtocolSchemaTypedOperationsEmpty verifies the schema TypedOperations
// section is omitted when no ops are registered. Important for zero-diff
// schema dumps before any game has migrated.
func TestProtocolSchemaTypedOperationsEmpty(t *testing.T) {
	t.Cleanup(ResetTypedOpRegistryForTest)
	ResetTypedOpRegistryForTest()

	schema := NewProtocol("test").Schema()
	if len(schema.TypedOperations) != 0 {
		t.Errorf("TypedOperations should be empty, got %+v", schema.TypedOperations)
	}
}
