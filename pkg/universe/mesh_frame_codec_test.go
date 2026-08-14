package universe

import (
	"bytes"
	"testing"

	"github.com/google/uuid"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
)

func TestMeshFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  CellMessage
	}{
		{
			"border",
			CellMessage{
				Type:        MsgBorderFrame,
				FromCellID:  "cell_0_0",
				BorderFrame: []byte{1, 2, 3},
			},
		},
		{
			"handoff",
			CellMessage{
				Type:       MsgHandoff,
				FromCellID: "cell_0_0",
				Handoff: &HandoffPayload{
					NetID:        42,
					Epoch:        3,
					CommitTick:   101,
					TransferBlob: []byte("hello"),
					ConnID:       55,
				},
			},
		},
		{
			"handoff_no_blob",
			CellMessage{
				Type:       MsgHandoff,
				FromCellID: "cell_0_0",
				Handoff: &HandoffPayload{
					NetID:      99,
					Epoch:      5,
					CommitTick: 1234,
				},
			},
		},
		{
			"forward_input",
			CellMessage{
				Type:       MsgForwardInput,
				FromCellID: "cell_0_0",
				ForwardInput: &ForwardInputPayload{
					GatewayID:    "gateway-a",
					ConnID:       55,
					SessionEpoch: 9,
					InputBlob:    []byte{0xAB, 0xCD},
				},
			},
		},
		{
			"cross_node_action",
			CellMessage{
				Type:       MsgCrossCellAction,
				FromCellID: "cell_0_0",
				Action: &CrossCellAction{
					Type:         ActionType(7),
					TargetNetID:  10,
					SourceNetID:  20,
					SourceCellID: "node-1",
					Payload:      []byte{0x01, 0x02},
				},
			},
		},
		{
			"player_assignment_basic",
			CellMessage{
				Type:       MsgPlayerAssignment,
				FromCellID: "cell_0_0",
				Assignment: &PlayerAssignment{
					ConnID:           42,
					Username:         "bob",
					IsReconnect:      false,
					StreamGeneration: 17,
				},
			},
		},
		{
			"player_assignment_reconnect",
			CellMessage{
				Type:       MsgPlayerAssignment,
				FromCellID: "cell_0_0",
				Assignment: &PlayerAssignment{
					ConnID:           43,
					Username:         "carol",
					IsReconnect:      true,
					StreamGeneration: 0,
				},
			},
		},
		{
			"session_transfer",
			CellMessage{
				Type:       MsgSessionTransfer,
				FromCellID: "cell_0_0",
				Sessions: []SessionTransfer{
					{
						ConnID:           11,
						GatewayID:        "gateway-a",
						GatewayConnID:    991,
						SessionEpoch:     37,
						UserID:           uuid.MustParse("d096079e-e90b-4ae9-b3eb-0cbd9f5f4f1b"),
						Username:         "dave",
						StreamGeneration: 23,
						StateTag:         "docked",
						Data:             []byte{0xFF},
					},
				},
			},
		},
		{
			"session_transfer_nil_data",
			CellMessage{
				Type:       MsgSessionTransfer,
				FromCellID: "cell_0_0",
				Sessions: []SessionTransfer{
					{
						ConnID:           12,
						Username:         "eve",
						StreamGeneration: 0,
						StateTag:         "dead",
						Data:             nil,
					},
				},
			},
		},
		{
			"spawn_transfer",
			CellMessage{
				Type:       MsgSpawnTransfer,
				FromCellID: "cell_0_0",
				Spawn: &SpawnTransfer{
					ConnID:           33,
					Username:         "frank",
					StreamGeneration: ^uint32(0),
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := encodeCellMessage(tc.msg, "cell_1_0")
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if frame.DestCellId != "cell_1_0" {
				t.Errorf("DestCellId = %q, want cell_1_0", frame.DestCellId)
			}

			decoded, err := decodeMeshFrame(frame)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if !cellMessagesEqual(t, tc.msg, decoded) {
				t.Errorf("round-trip mismatch:\n  orig:    %+v\n  decoded: %+v", tc.msg, decoded)
			}
		})
	}
}

func TestPlayerAssignmentWireSeparatesRouteEpochFromStreamGeneration(t *testing.T) {
	frame, err := encodeCellMessage(CellMessage{
		Type:       MsgPlayerAssignment,
		FromCellID: "cell_0_0",
		Assignment: &PlayerAssignment{
			ConnID:           42,
			StreamGeneration: 29,
		},
	}, "cell_1_0")
	if err != nil {
		t.Fatalf("encodeCellMessage: %v", err)
	}

	assignment := frame.GetPlayerAssignment()
	if assignment == nil {
		t.Fatal("encoded PlayerAssignment is nil")
	}
	if assignment.Epoch != 0 {
		t.Fatalf("route Epoch = %d, want 0 for embedded CellMessage", assignment.Epoch)
	}
	if assignment.StreamGeneration != 29 {
		t.Fatalf("StreamGeneration = %d, want 29", assignment.StreamGeneration)
	}

	// A host route fence can differ from the replication stream generation.
	// Decoding the cell payload must never truncate or reinterpret that epoch.
	assignment.Epoch = ^uint64(0)
	assignment.StreamGeneration = ^uint32(0)
	decoded, err := decodeMeshFrame(frame)
	if err != nil {
		t.Fatalf("decodeMeshFrame: %v", err)
	}
	if decoded.Assignment == nil {
		t.Fatal("decoded PlayerAssignment is nil")
	}
	if decoded.Assignment.StreamGeneration != ^uint32(0) {
		t.Fatalf("decoded StreamGeneration = %d, want %d", decoded.Assignment.StreamGeneration, uint32(^uint32(0)))
	}
}

// cellMessagesEqual compares the fields the codec actually touches.
// PlayerAssignment.Data and SessionTransfer.Data are `any` but the codec
// guarantees they are []byte after decode, so we cast and compare.
func cellMessagesEqual(t *testing.T, orig, got CellMessage) bool {
	t.Helper()
	ok := true

	check := func(field string, a, b any) {
		t.Helper()
		// byte slice comparison
		ab, aIsBytes := a.([]byte)
		bb, bIsBytes := b.([]byte)
		if aIsBytes || bIsBytes {
			if !bytes.Equal(ab, bb) {
				t.Errorf("  %s: %v != %v", field, a, b)
				ok = false
			}
			return
		}
		if a != b {
			t.Errorf("  %s: %v != %v", field, a, b)
			ok = false
		}
	}

	check("Type", orig.Type, got.Type)
	check("FromCellID", orig.FromCellID, got.FromCellID)

	switch orig.Type {
	case MsgBorderFrame:
		check("BorderFrame", orig.BorderFrame, got.BorderFrame)

	case MsgHandoff:
		oh, gh := orig.Handoff, got.Handoff
		if oh == nil || gh == nil {
			check("Handoff nil", oh, gh)
			break
		}
		check("Handoff.NetID", oh.NetID, gh.NetID)
		check("Handoff.Epoch", oh.Epoch, gh.Epoch)
		check("Handoff.CommitTick", oh.CommitTick, gh.CommitTick)
		check("Handoff.TransferBlob", oh.TransferBlob, gh.TransferBlob)
		check("Handoff.ConnID", oh.ConnID, gh.ConnID)

	case MsgForwardInput:
		of, gf := orig.ForwardInput, got.ForwardInput
		if of == nil || gf == nil {
			check("ForwardInput nil", of, gf)
			break
		}
		check("ForwardInput.GatewayID", of.GatewayID, gf.GatewayID)
		check("ForwardInput.ConnID", of.ConnID, gf.ConnID)
		check("ForwardInput.SessionEpoch", of.SessionEpoch, gf.SessionEpoch)
		check("ForwardInput.InputBlob", of.InputBlob, gf.InputBlob)

	case MsgCrossCellAction:
		oa, ga := orig.Action, got.Action
		if oa == nil || ga == nil {
			check("CrossCellAction nil", oa, ga)
			break
		}
		check("Action.Type", oa.Type, ga.Type)
		check("Action.TargetNetID", oa.TargetNetID, ga.TargetNetID)
		check("Action.SourceNetID", oa.SourceNetID, ga.SourceNetID)
		check("Action.SourceCellID", oa.SourceCellID, ga.SourceCellID)
		check("Action.Payload", oa.Payload, ga.Payload)

	case MsgPlayerAssignment:
		oa, ga := orig.Assignment, got.Assignment
		if oa == nil || ga == nil {
			check("PlayerAssignment nil", oa, ga)
			break
		}
		check("Assignment.ConnID", oa.ConnID, ga.ConnID)
		check("Assignment.Username", oa.Username, ga.Username)
		check("Assignment.IsReconnect", oa.IsReconnect, ga.IsReconnect)
		check("Assignment.StreamGeneration", oa.StreamGeneration, ga.StreamGeneration)

	case MsgSessionTransfer:
		if len(orig.Sessions) != len(got.Sessions) {
			t.Errorf("  Sessions len: %d != %d", len(orig.Sessions), len(got.Sessions))
			ok = false
			break
		}
		for i := range orig.Sessions {
			os, gs := orig.Sessions[i], got.Sessions[i]
			check("Sessions[i].ConnID", os.ConnID, gs.ConnID)
			check("Sessions[i].GatewayID", os.GatewayID, gs.GatewayID)
			check("Sessions[i].GatewayConnID", os.GatewayConnID, gs.GatewayConnID)
			check("Sessions[i].SessionEpoch", os.SessionEpoch, gs.SessionEpoch)
			check("Sessions[i].UserID", os.UserID, gs.UserID)
			check("Sessions[i].Username", os.Username, gs.Username)
			check("Sessions[i].StreamGeneration", os.StreamGeneration, gs.StreamGeneration)
			check("Sessions[i].StateTag", os.StateTag, gs.StateTag)
			// Data is any -> compare as []byte
			var origData, gotData []byte
			if os.Data != nil {
				origData = os.Data.([]byte)
			}
			if gs.Data != nil {
				gotData = gs.Data.([]byte)
			}
			check("Sessions[i].Data", origData, gotData)
		}

	case MsgSpawnTransfer:
		os, gs := orig.Spawn, got.Spawn
		if os == nil || gs == nil {
			check("SpawnTransfer nil", os, gs)
			break
		}
		check("Spawn.ConnID", os.ConnID, gs.ConnID)
		check("Spawn.Username", os.Username, gs.Username)
		check("Spawn.StreamGeneration", os.StreamGeneration, gs.StreamGeneration)
	}

	return ok
}

func TestEncodeUnsupportedType(t *testing.T) {
	_, err := encodeCellMessage(CellMessage{Type: MsgType(99)}, "cell_1_0")
	if err == nil {
		t.Fatal("expected error for unsupported MsgType, got nil")
	}
}

func TestEncodeNilPayload(t *testing.T) {
	_, err := encodeCellMessage(CellMessage{Type: MsgHandoff, Handoff: nil}, "cell_1_0")
	if err == nil {
		t.Fatal("expected error for nil Handoff payload, got nil")
	}
}

func TestDecodeMeshFrameNilMsg(t *testing.T) {
	_, err := decodeMeshFrame(&meshpb.MeshFrame{DestCellId: "cell_0_0"})
	if err == nil {
		t.Fatal("expected error for nil Msg, got nil")
	}
	if got := err.Error(); !contains(got, "no oneof payload") {
		t.Errorf("error %q does not contain \"no oneof payload\"", got)
	}
}

// contains is a local helper to avoid importing strings in the test file.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

func TestEncodeSessionTransferMultipleRejected(t *testing.T) {
	_, err := encodeCellMessage(CellMessage{
		Type:       MsgSessionTransfer,
		FromCellID: "cell_0_0",
		Sessions: []SessionTransfer{
			{ConnID: 1, Username: "alice"},
			{ConnID: 2, Username: "bob"},
		},
	}, "cell_1_0")
	if err == nil {
		t.Fatal("expected error for multi-entry Sessions, got nil")
	}
}

// TestHandoffAcceptedCarrierRoundTrip pins the deliberate wire choice: a
// MsgHandoffAccepted travels inside the existing CrossCellAction oneof under
// ActionHandoffAccepted rather than a new meshpb message, and must decode back
// into a typed CellMessage — never into a CrossCellAction that would reach
// Stage.HandleEngineAction. See handoff_ack.go for why.
func TestHandoffAcceptedCarrierRoundTrip(t *testing.T) {
	msg := CellMessage{
		Type:       MsgHandoffAccepted,
		FromCellID: "cell_1_0",
		HandoffAck: &HandoffAckPayload{NetID: 4242, Epoch: 9, CommitTick: 123456789},
	}

	frame, err := encodeCellMessage(msg, "cell_0_0")
	if err != nil {
		t.Fatalf("encodeCellMessage: %v", err)
	}
	ca, ok := frame.Msg.(*meshpb.MeshFrame_CrossAction)
	if !ok {
		t.Fatalf("encoded oneof = %T, want *meshpb.MeshFrame_CrossAction", frame.Msg)
	}
	if got := ca.CrossAction.ActionType; got != uint32(ActionHandoffAccepted) {
		t.Fatalf("ActionType = %d, want %d", got, ActionHandoffAccepted)
	}
	if got := ca.CrossAction.TargetNetId; got != 4242 {
		t.Fatalf("TargetNetId = %d, want 4242", got)
	}

	got, err := decodeMeshFrame(frame)
	if err != nil {
		t.Fatalf("decodeMeshFrame: %v", err)
	}
	if got.Type != MsgHandoffAccepted {
		t.Fatalf("decoded Type = %v, want MsgHandoffAccepted", got.Type)
	}
	if got.Action != nil {
		t.Fatal("decoded a CrossCellAction; the carrier must be intercepted, not surfaced")
	}
	if got.FromCellID != "cell_1_0" {
		t.Fatalf("FromCellID = %q, want cell_1_0", got.FromCellID)
	}
	if got.HandoffAck == nil {
		t.Fatal("HandoffAck is nil")
	}
	if *got.HandoffAck != *msg.HandoffAck {
		t.Fatalf("HandoffAck = %+v, want %+v", *got.HandoffAck, *msg.HandoffAck)
	}
}

// TestHandoffAcceptedMalformedPayloadRejected asserts a truncated carrier
// payload fails at the codec boundary rather than arming a demote against a
// garbage commit tick.
func TestHandoffAcceptedMalformedPayloadRejected(t *testing.T) {
	frame := &meshpb.MeshFrame{
		DestCellId: "cell_0_0",
		Msg: &meshpb.MeshFrame_CrossAction{
			CrossAction: &meshpb.CrossCellAction{
				FromCellId:  "cell_1_0",
				ActionType:  uint32(ActionHandoffAccepted),
				TargetNetId: 42,
				Payload:     []byte{1, 2, 3},
			},
		},
	}
	if _, err := decodeMeshFrame(frame); err == nil {
		t.Fatal("decodeMeshFrame accepted a malformed HandoffAccepted payload")
	}
}

// TestOrdinaryCrossCellActionStillDecodes is the regression guard for the
// carrier interception: ActionTypedMessage (100) and game action types must
// still decode as MsgCrossCellAction.
func TestOrdinaryCrossCellActionStillDecodes(t *testing.T) {
	frame := &meshpb.MeshFrame{
		DestCellId: "cell_0_0",
		Msg: &meshpb.MeshFrame_CrossAction{
			CrossAction: &meshpb.CrossCellAction{
				FromCellId:  "cell_1_0",
				ActionType:  uint32(ActionTypedMessage),
				TargetNetId: 42,
				Payload:     []byte{1, 2, 3},
			},
		},
	}
	got, err := decodeMeshFrame(frame)
	if err != nil {
		t.Fatalf("decodeMeshFrame: %v", err)
	}
	if got.Type != MsgCrossCellAction {
		t.Fatalf("Type = %v, want MsgCrossCellAction", got.Type)
	}
	if got.Action == nil || got.Action.Type != ActionTypedMessage {
		t.Fatalf("Action = %+v, want ActionTypedMessage", got.Action)
	}
	if !bytes.Equal(got.Action.Payload, []byte{1, 2, 3}) {
		t.Fatalf("Payload = %v, want [1 2 3]", got.Action.Payload)
	}
}

// TestEncodeNilHandoffAckPayload asserts the encoder rejects a
// MsgHandoffAccepted with no payload rather than emitting a zero-value ack
// that would fail the source's fence in a confusing way.
func TestEncodeNilHandoffAckPayload(t *testing.T) {
	if _, err := encodeCellMessage(CellMessage{Type: MsgHandoffAccepted, FromCellID: "cell_1_0"}, "cell_0_0"); err == nil {
		t.Fatal("encodeCellMessage accepted a nil HandoffAck payload")
	}
}
