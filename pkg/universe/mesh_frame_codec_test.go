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
			"handoff_prepare",
			CellMessage{
				Type:       MsgHandoffPrepare,
				FromCellID: "cell_0_0",
				HandoffPrepare: &HandoffPreparePayload{
					NetID:        42,
					Epoch:        3,
					Kind:         7,
					TransferBlob: []byte("hello"),
					ClientBaselines: []ClientBaselineEntry{
						{ConnID: 1, EntityNetID: 42, LastAcked: []byte{9}, LastTick: 100},
					},
					ExpectedTick: 101,
					OldEpoch:     2,
				},
			},
		},
		{
			"handoff_commit",
			CellMessage{
				Type:       MsgHandoffCommit,
				FromCellID: "cell_0_0",
				HandoffCommit: &HandoffCommitPayload{
					NetID:      99,
					Epoch:      5,
					CommitTick: 1234,
				},
			},
		},
		{
			"handoff_cancel",
			CellMessage{
				Type:       MsgHandoffCancel,
				FromCellID: "cell_0_0",
				HandoffCancel: &HandoffCancelPayload{
					NetID: 77,
					Epoch: 4,
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
			"action_result",
			CellMessage{
				Type:       MsgActionResult,
				FromCellID: "cell_0_0",
				ActionResult: &ActionResult{
					Type:        ActionType(7),
					TargetNetID: 10,
					SourceNetID: 20,
					Success:     true,
					Payload:     []byte{0x03},
					SideEffects: []byte{0x04, 0x05},
				},
			},
		},
		{
			"player_assignment_nil_data",
			CellMessage{
				Type:       MsgPlayerAssignment,
				FromCellID: "cell_0_0",
				Assignment: &PlayerAssignment{
					ConnID:      42,
					Username:    "bob",
					IsReconnect: false,
					Data:        nil,
				},
			},
		},
		{
			"player_assignment_bytes_data",
			CellMessage{
				Type:       MsgPlayerAssignment,
				FromCellID: "cell_0_0",
				Assignment: &PlayerAssignment{
					ConnID:      43,
					Username:    "carol",
					IsReconnect: true,
					Data:        []byte{0xDE, 0xAD},
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

	case MsgHandoffPrepare:
		op, gp := orig.HandoffPrepare, got.HandoffPrepare
		if op == nil || gp == nil {
			check("HandoffPrepare nil", op, gp)
			break
		}
		check("HandoffPrepare.NetID", op.NetID, gp.NetID)
		check("HandoffPrepare.Epoch", op.Epoch, gp.Epoch)
		check("HandoffPrepare.Kind", op.Kind, gp.Kind)
		check("HandoffPrepare.TransferBlob", op.TransferBlob, gp.TransferBlob)
		check("HandoffPrepare.ExpectedTick", op.ExpectedTick, gp.ExpectedTick)
		check("HandoffPrepare.OldEpoch", op.OldEpoch, gp.OldEpoch)
		if len(op.ClientBaselines) != len(gp.ClientBaselines) {
			t.Errorf("  HandoffPrepare.ClientBaselines len: %d != %d", len(op.ClientBaselines), len(gp.ClientBaselines))
			ok = false
			break
		}
		for i := range op.ClientBaselines {
			ob, gb := op.ClientBaselines[i], gp.ClientBaselines[i]
			check("ClientBaselines[i].ConnID", ob.ConnID, gb.ConnID)
			check("ClientBaselines[i].EntityNetID", ob.EntityNetID, gb.EntityNetID)
			check("ClientBaselines[i].LastAcked", ob.LastAcked, gb.LastAcked)
			check("ClientBaselines[i].LastTick", ob.LastTick, gb.LastTick)
		}

	case MsgHandoffCommit:
		oc, gc := orig.HandoffCommit, got.HandoffCommit
		if oc == nil || gc == nil {
			check("HandoffCommit nil", oc, gc)
			break
		}
		check("HandoffCommit.NetID", oc.NetID, gc.NetID)
		check("HandoffCommit.Epoch", oc.Epoch, gc.Epoch)
		check("HandoffCommit.CommitTick", oc.CommitTick, gc.CommitTick)

	case MsgHandoffCancel:
		oc, gc := orig.HandoffCancel, got.HandoffCancel
		if oc == nil || gc == nil {
			check("HandoffCancel nil", oc, gc)
			break
		}
		check("HandoffCancel.NetID", oc.NetID, gc.NetID)
		check("HandoffCancel.Epoch", oc.Epoch, gc.Epoch)

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

	case MsgActionResult:
		or, gr := orig.ActionResult, got.ActionResult
		if or == nil || gr == nil {
			check("ActionResult nil", or, gr)
			break
		}
		check("ActionResult.Type", or.Type, gr.Type)
		check("ActionResult.TargetNetID", or.TargetNetID, gr.TargetNetID)
		check("ActionResult.SourceNetID", or.SourceNetID, gr.SourceNetID)
		check("ActionResult.Success", or.Success, gr.Success)
		check("ActionResult.Payload", or.Payload, gr.Payload)
		check("ActionResult.SideEffects", or.SideEffects, gr.SideEffects)

	case MsgPlayerAssignment:
		oa, ga := orig.Assignment, got.Assignment
		if oa == nil || ga == nil {
			check("PlayerAssignment nil", oa, ga)
			break
		}
		check("Assignment.ConnID", oa.ConnID, ga.ConnID)
		check("Assignment.Username", oa.Username, ga.Username)
		check("Assignment.IsReconnect", oa.IsReconnect, ga.IsReconnect)
		// Data is any -> compare as []byte
		var origData, gotData []byte
		if oa.Data != nil {
			origData = oa.Data.([]byte)
		}
		if ga.Data != nil {
			gotData = ga.Data.([]byte)
		}
		check("Assignment.Data", origData, gotData)

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
	_, err := encodeCellMessage(CellMessage{Type: MsgHandoffPrepare, HandoffPrepare: nil}, "cell_1_0")
	if err == nil {
		t.Fatal("expected error for nil HandoffPrepare payload, got nil")
	}
}

func TestEncodePlayerAssignmentNonBytesData(t *testing.T) {
	_, err := encodeCellMessage(CellMessage{
		Type: MsgPlayerAssignment,
		Assignment: &PlayerAssignment{
			ConnID:   1,
			Username: "alice",
			Data:     "not bytes",
		},
	}, "cell_1_0")
	if err == nil {
		t.Fatal("expected error for non-[]byte Data, got nil")
	}
	// Error message should mention Data
	if got := err.Error(); len(got) == 0 {
		t.Error("error message is empty")
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

func TestEncodePlayerAssignmentTypedNilBytesData(t *testing.T) {
	var b []byte // typed nil
	msg := CellMessage{
		Type:       MsgPlayerAssignment,
		FromCellID: "cell_0_0",
		Assignment: &PlayerAssignment{ConnID: 1, Username: "bob", Data: b},
	}
	frame, err := encodeCellMessage(msg, "cell_1_0")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := decodeMeshFrame(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cellMessagesEqual(t, msg, decoded) {
		t.Errorf("round-trip mismatch:\n  orig:    %+v\n  decoded: %+v", msg, decoded)
	}
	// Confirm decoded Data is nil or empty (typed-nil encodes as nil bytes on the
	// wire; proto may decode an absent bytes field as nil or []byte{}).
	if data, _ := decoded.Assignment.Data.([]byte); len(data) != 0 {
		t.Errorf("decoded.Assignment.Data = %v, want nil or empty", decoded.Assignment.Data)
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
