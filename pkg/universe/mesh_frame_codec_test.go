package universe

import (
	"bytes"
	"testing"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
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
					ConnID:    55,
					InputBlob: []byte{0xAB, 0xCD},
				},
			},
		},
		{
			"chat",
			CellMessage{
				Type:       MsgChat,
				FromCellID: "cell_0_0",
				Chat: &ChatRelay{
					Username: "alice",
					Text:     "hello world",
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
					ConnID:      42,
					Username:    "bob",
					IsReconnect: false,
				},
			},
		},
		{
			"player_assignment_reconnect",
			CellMessage{
				Type:       MsgPlayerAssignment,
				FromCellID: "cell_0_0",
				Assignment: &PlayerAssignment{
					ConnID:      43,
					Username:    "carol",
					IsReconnect: true,
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
						ConnID:   11,
						Username: "dave",
						StateTag: "docked",
						Data:     []byte{0xFF},
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
						ConnID:   12,
						Username: "eve",
						StateTag: "dead",
						Data:     nil,
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
					ConnID:   33,
					Username: "frank",
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
		check("ForwardInput.ConnID", of.ConnID, gf.ConnID)
		check("ForwardInput.InputBlob", of.InputBlob, gf.InputBlob)

	case MsgChat:
		oc, gc := orig.Chat, got.Chat
		if oc == nil || gc == nil {
			check("Chat nil", oc, gc)
			break
		}
		check("Chat.Username", oc.Username, gc.Username)
		check("Chat.Text", oc.Text, gc.Text)

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

	case MsgSessionTransfer:
		if len(orig.Sessions) != len(got.Sessions) {
			t.Errorf("  Sessions len: %d != %d", len(orig.Sessions), len(got.Sessions))
			ok = false
			break
		}
		for i := range orig.Sessions {
			os, gs := orig.Sessions[i], got.Sessions[i]
			check("Sessions[i].ConnID", os.ConnID, gs.ConnID)
			check("Sessions[i].Username", os.Username, gs.Username)
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
