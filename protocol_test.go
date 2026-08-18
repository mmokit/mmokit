package mmokit

import (
	"testing"
)

type pTestOpReq struct{ X int32 }
type pTestOpRes struct{ Y int32 }

// TestProtocolSchemaTypedOperations verifies that registered typed-ops surface
// in ProtocolSchema.TypedOperations with correct kind, type IDs, names, and
// reflect-codec field shapes for both request + response.
func TestProtocolSchemaTypedOperations(t *testing.T) {
	p := newTestProcess(t)

	RegisterOp[pTestOpReq, pTestOpRes](p, RouteGatewayLocal,
		func(_ *OpContext, req *pTestOpReq) (*pTestOpRes, error) {
			return &pTestOpRes{Y: req.X}, nil
		})

	schema := NewProtocol(p, "test").Schema()
	if len(schema.Operations) != 1 {
		t.Fatalf("TypedOperations: got %d, want 1 (entries=%+v)", len(schema.Operations), schema.Operations)
	}
	op := schema.Operations[0]
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
	p := newTestProcess(t)

	schema := NewProtocol(p, "test").Schema()
	if len(schema.Operations) != 0 {
		t.Errorf("TypedOperations should be empty, got %+v", schema.Operations)
	}
}
