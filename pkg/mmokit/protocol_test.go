package mmokit

import (
	"bytes"
	"encoding/json"
	"testing"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
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

func testLoginHandler(connID uint32, msgs [][]byte) (string, any, error) {
	return "testuser", nil, nil
}

// TestProtocolOfAndServerEventsOf verifies that ProtocolOf returns the
// *Protocol stored in Config.Protocol, and that ServerEventsOf returns its
// registry.
func TestProtocolOfAndServerEventsOf(t *testing.T) {
	proto := NewProtocol("test").
		ServerEvents(func(e *ServerEvents) {
			RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
		})
	p := New(Config{Mode: "all", LoginHandler: testLoginHandler, Protocol: proto})

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
	p := New(Config{Mode: "all", LoginHandler: testLoginHandler})
	if got := ProtocolOf(p); got != nil {
		t.Errorf("ProtocolOf = %v, want nil", got)
	}
	if got := ServerEventsOf(p); got != nil {
		t.Errorf("ServerEventsOf = %v, want nil", got)
	}
}
