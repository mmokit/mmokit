package mmokit

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestProtocolSchema_DefaultsToSnap verifies that a Protocol built
// without an explicit SetClientRenderMode call reports the default
// Snap mode on its exported schema. Guards against regressions where
// ClientRenderMode would silently serialize as "".
func TestProtocolSchema_DefaultsToSnap(t *testing.T) {
	p := NewProtocol("test")
	ps := p.Schema()
	if ps.ClientRenderMode != ClientRenderSnap {
		t.Errorf("default ClientRenderMode on schema = %q, want %q",
			ps.ClientRenderMode, ClientRenderSnap)
	}
}

// TestProtocolSchema_SetClientRenderMode_Interpolated verifies the
// schema round-trips an explicit Interpolated mode through JSON
// encode/decode with the clientRenderMode field intact — this is what
// sdkgen consumes.
func TestProtocolSchema_SetClientRenderMode_Interpolated(t *testing.T) {
	p := NewProtocol("test")
	p.SetClientRenderMode(ClientRenderInterpolated)

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
	if decoded.ClientRenderMode != "interpolated" {
		t.Errorf("schema JSON clientRenderMode = %q, want %q",
			decoded.ClientRenderMode, "interpolated")
	}
}

// TestProtocolSchema_SetClientRenderMode_EmptyFallsBackToSnap verifies
// that passing an empty string (e.g. a zero-value Config field) still
// produces a valid schema — defaults to Snap rather than emitting an
// empty string that clients would treat as unknown.
func TestProtocolSchema_SetClientRenderMode_EmptyFallsBackToSnap(t *testing.T) {
	p := NewProtocol("test")
	p.SetClientRenderMode("")
	ps := p.Schema()
	if ps.ClientRenderMode != ClientRenderSnap {
		t.Errorf("empty SetClientRenderMode → schema = %q, want %q",
			ps.ClientRenderMode, ClientRenderSnap)
	}
}
