package mmokit

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestProtocolSchema_DefaultsToInterpolated verifies that a Protocol
// built without an explicit SetClientRenderMode call reports the
// default Interpolated mode on its exported schema. Guards against
// regressions where ClientRenderMode would silently serialize as "".
func TestProtocolSchema_DefaultsToInterpolated(t *testing.T) {
	p := NewProtocol("test")
	ps := p.Schema()
	if ps.ClientRenderMode != ClientRenderInterpolated {
		t.Errorf("default ClientRenderMode on schema = %q, want %q",
			ps.ClientRenderMode, ClientRenderInterpolated)
	}
}

// TestProtocolSchema_SetClientRenderMode_Snap verifies the schema
// round-trips an explicit Snap mode through JSON encode/decode with
// the clientRenderMode field intact — this is what sdkgen consumes.
func TestProtocolSchema_SetClientRenderMode_Snap(t *testing.T) {
	p := NewProtocol("test")
	p.SetClientRenderMode(ClientRenderSnap)

	var buf bytes.Buffer
	if err := p.WriteSchema(&buf); err != nil {
		t.Fatalf("WriteSchema: %v", err)
	}

	var decoded struct {
		ClientRenderMode string `json:"clientRenderMode"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal schema JSON: %v", err)
	}
	if decoded.ClientRenderMode != "snap" {
		t.Errorf("schema JSON clientRenderMode = %q, want %q",
			decoded.ClientRenderMode, "snap")
	}
}

// TestProtocolSchema_SetClientRenderMode_EmptyFallsBackToInterpolated
// verifies that passing an empty string (e.g. a zero-value Config
// field) still produces a valid schema — defaults to Interpolated
// rather than emitting an empty string that clients would treat as
// unknown.
func TestProtocolSchema_SetClientRenderMode_EmptyFallsBackToInterpolated(t *testing.T) {
	p := NewProtocol("test")
	p.SetClientRenderMode("")
	ps := p.Schema()
	if ps.ClientRenderMode != ClientRenderInterpolated {
		t.Errorf("empty SetClientRenderMode → schema = %q, want %q",
			ps.ClientRenderMode, ClientRenderInterpolated)
	}
}
