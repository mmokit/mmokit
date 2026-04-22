package universe

import (
	"fmt"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/coords"
)

// locationToProto converts a coords.Location to its meshpb wire form.
// Returns nil when the input is zero-valued so the proto omits an
// empty message on the wire.
func locationToProto(loc coords.Location) *meshpb.Location {
	if loc.IsZero() {
		return nil
	}
	return &meshpb.Location{
		X:      loc.X,
		Y:      loc.Y,
		Facing: loc.Facing,
		Tag:    loc.Tag,
	}
}

// protoToLocation is the inverse. Returns a zero-value coords.Location
// when the proto is nil, matching the "no preference" sentinel convention.
func protoToLocation(pb *meshpb.Location) coords.Location {
	if pb == nil {
		return coords.Location{}
	}
	return coords.Location{
		X:      pb.X,
		Y:      pb.Y,
		Facing: pb.Facing,
		Tag:    pb.Tag,
	}
}

// encodeCellMessage converts a CellMessage into a MeshFrame ready to send
// over a MeshData gRPC stream. destCellID populates MeshFrame.DestCellId —
// the sender knows the routing target from the SendX(destCellID, ...) call
// site, and the receiver needs it to look up the target cell before
// dispatching on the oneof variant.
//
// Returns an error for MsgTypes that have no wire mapping (should never
// happen for messages the Bridge interface dispatches, but we fail loud
// rather than silently drop).
//
// PlayerAssignment.Data and SessionTransfer.Data constraints:
// Both fields are `any` in Go but `bytes` on the wire. The encoder accepts
// only nil or []byte values. Any other concrete type is an error — game-
// specific session data must be serialized to []byte before crossing host
// boundaries. The decoder always sets Data to the raw []byte from the wire.
//
// Multi-entry MsgSessionTransfer (len(Sessions) > 1) is rejected. The proto
// carries one SessionTransfer per MeshFrame, and cell splits don't cross
// hosts in S3. Future S7 work should handle the multi-entry case by emitting
// multiple frames.
func encodeCellMessage(msg CellMessage, destCellID string) (*meshpb.MeshFrame, error) {
	frame := &meshpb.MeshFrame{DestCellId: destCellID}

	switch msg.Type {
	case MsgBorderFrame:
		frame.Msg = &meshpb.MeshFrame_BorderFrame{
			BorderFrame: &meshpb.BorderFrame{
				FromCellId: msg.FromCellID,
				Data:       msg.BorderFrame,
			},
		}

	case MsgHandoffPrepare:
		if msg.HandoffPrepare == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgHandoffPrepare payload is nil")
		}
		p := msg.HandoffPrepare
		baselines := make([]*meshpb.ClientBaseline, len(p.ClientBaselines))
		for i, b := range p.ClientBaselines {
			baselines[i] = &meshpb.ClientBaseline{
				ConnId:      b.ConnID,
				EntityNetId: b.EntityNetID,
				LastAcked:   b.LastAcked,
				LastTick:    b.LastTick,
			}
		}
		frame.Msg = &meshpb.MeshFrame_HandoffPrepare{
			HandoffPrepare: &meshpb.HandoffPrepare{
				FromCellId:   msg.FromCellID,
				NetId:        p.NetID,
				Epoch:        p.Epoch,
				Kind:         uint32(p.Kind),
				TransferBlob: p.TransferBlob,
				Baselines:    baselines,
				ExpectedTick: p.ExpectedTick,
				OldEpoch:     p.OldEpoch,
			},
		}

	case MsgHandoffCommit:
		if msg.HandoffCommit == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgHandoffCommit payload is nil")
		}
		p := msg.HandoffCommit
		frame.Msg = &meshpb.MeshFrame_HandoffCommit{
			HandoffCommit: &meshpb.HandoffCommit{
				FromCellId: msg.FromCellID,
				NetId:      p.NetID,
				Epoch:      p.Epoch,
				CommitTick: p.CommitTick,
			},
		}

	case MsgHandoffCancel:
		if msg.HandoffCancel == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgHandoffCancel payload is nil")
		}
		p := msg.HandoffCancel
		frame.Msg = &meshpb.MeshFrame_HandoffCancel{
			HandoffCancel: &meshpb.HandoffCancel{
				FromCellId: msg.FromCellID,
				NetId:      p.NetID,
				Epoch:      p.Epoch,
			},
		}

	case MsgForwardInput:
		if msg.ForwardInput == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgForwardInput payload is nil")
		}
		p := msg.ForwardInput
		frame.Msg = &meshpb.MeshFrame_ForwardInput{
			ForwardInput: &meshpb.ForwardInput{
				FromCellId: msg.FromCellID,
				ConnId:     p.ConnID,
				InputBlob:  p.InputBlob,
			},
		}

	case MsgChat:
		if msg.Chat == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgChat payload is nil")
		}
		frame.Msg = &meshpb.MeshFrame_ChatRelay{
			ChatRelay: &meshpb.ChatRelay{
				FromCellId: msg.FromCellID,
				Username:   msg.Chat.Username,
				Text:       msg.Chat.Text,
			},
		}

	case MsgCrossCellAction:
		if msg.Action == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgCrossCellAction payload is nil")
		}
		a := msg.Action
		frame.Msg = &meshpb.MeshFrame_CrossAction{
			CrossAction: &meshpb.CrossCellAction{
				FromCellId:   msg.FromCellID,
				ActionType:   uint32(a.Type),
				TargetNetId:  a.TargetNetID,
				SourceNetId:  a.SourceNetID,
				SourceCellId: a.SourceCellID,
				Payload:      a.Payload,
			},
		}

	case MsgActionResult:
		if msg.ActionResult == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgActionResult payload is nil")
		}
		r := msg.ActionResult
		frame.Msg = &meshpb.MeshFrame_ActionResult{
			ActionResult: &meshpb.ActionResult{
				FromCellId:  msg.FromCellID,
				ActionType:  uint32(r.Type),
				TargetNetId: r.TargetNetID,
				SourceNetId: r.SourceNetID,
				Success:     r.Success,
				Payload:     r.Payload,
				SideEffects: r.SideEffects,
			},
		}

	case MsgPlayerAssignment:
		if msg.Assignment == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgPlayerAssignment payload is nil")
		}
		a := msg.Assignment
		dataBytes, err := encodeAnyToBytes("PlayerAssignment.Data", a.Data)
		if err != nil {
			return nil, err
		}
		frame.Msg = &meshpb.MeshFrame_PlayerAssignment{
			PlayerAssignment: &meshpb.PlayerAssignment{
				FromCellId:    msg.FromCellID,
				ConnId:        a.ConnID,
				Username:      a.Username,
				IsReconnect:   a.IsReconnect,
				Data:          dataBytes,
				SpawnLocation: locationToProto(a.SpawnLocation),
			},
		}

	case MsgSessionTransfer:
		if len(msg.Sessions) > 1 {
			return nil, fmt.Errorf("encodeCellMessage: MsgSessionTransfer with len(Sessions)=%d > 1 is not supported in S3; cell splits don't cross hosts yet — future S7 work should emit one MeshFrame per session entry", len(msg.Sessions))
		}
		if len(msg.Sessions) == 0 {
			return nil, fmt.Errorf("encodeCellMessage: MsgSessionTransfer with empty Sessions slice")
		}
		s := msg.Sessions[0]
		dataBytes, err := encodeAnyToBytes("SessionTransfer.Data", s.Data)
		if err != nil {
			return nil, err
		}
		frame.Msg = &meshpb.MeshFrame_SessionTransfer{
			SessionTransfer: &meshpb.SessionTransfer{
				FromCellId: msg.FromCellID,
				ConnId:     s.ConnID,
				Username:   s.Username,
				StateTag:   s.StateTag,
				Data:       dataBytes,
			},
		}

	case MsgSpawnTransfer:
		if msg.Spawn == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgSpawnTransfer payload is nil")
		}
		frame.Msg = &meshpb.MeshFrame_SpawnTransfer{
			SpawnTransfer: &meshpb.SpawnTransfer{
				FromCellId:    msg.FromCellID,
				ConnId:        msg.Spawn.ConnID,
				Username:      msg.Spawn.Username,
				SpawnLocation: locationToProto(msg.Spawn.SpawnLocation),
			},
		}

	default:
		return nil, fmt.Errorf("encodeCellMessage: unsupported MsgType %d", msg.Type)
	}

	return frame, nil
}

// decodeMeshFrame is the inverse of encodeCellMessage — reads the top-level
// dest_cell_id (available on the returned CellMessage only if the caller
// stores it; in practice the caller already knows because
// HostNetwork.routeInboundFrame looked it up first) and dispatches on the
// oneof payload variant.
//
// Data fields on PlayerAssignment and SessionTransfer are returned as []byte
// directly from the wire — the caller is responsible for deserializing them
// into the appropriate game-specific type.
func decodeMeshFrame(frame *meshpb.MeshFrame) (CellMessage, error) {
	if frame == nil {
		return CellMessage{}, fmt.Errorf("decodeMeshFrame: frame is nil")
	}
	if frame.Msg == nil {
		return CellMessage{}, fmt.Errorf("decodeMeshFrame: MeshFrame has no oneof payload (dest=%s)", frame.DestCellId)
	}

	switch p := frame.Msg.(type) {
	case *meshpb.MeshFrame_BorderFrame:
		if p.BorderFrame == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: BorderFrame payload is nil")
		}
		return CellMessage{
			Type:        MsgBorderFrame,
			FromCellID:  p.BorderFrame.FromCellId,
			BorderFrame: p.BorderFrame.Data,
		}, nil

	case *meshpb.MeshFrame_HandoffPrepare:
		if p.HandoffPrepare == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: HandoffPrepare payload is nil")
		}
		hp := p.HandoffPrepare
		baselines := make([]ClientBaselineEntry, len(hp.Baselines))
		for i, b := range hp.Baselines {
			baselines[i] = ClientBaselineEntry{
				ConnID:      b.ConnId,
				EntityNetID: b.EntityNetId,
				LastAcked:   b.LastAcked,
				LastTick:    b.LastTick,
			}
		}
		return CellMessage{
			Type:       MsgHandoffPrepare,
			FromCellID: hp.FromCellId,
			HandoffPrepare: &HandoffPreparePayload{
				NetID:           hp.NetId,
				Epoch:           hp.Epoch,
				Kind:            uint16(hp.Kind),
				TransferBlob:    hp.TransferBlob,
				ClientBaselines: baselines,
				ExpectedTick:    hp.ExpectedTick,
				OldEpoch:        hp.OldEpoch,
			},
		}, nil

	case *meshpb.MeshFrame_HandoffCommit:
		if p.HandoffCommit == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: HandoffCommit payload is nil")
		}
		hc := p.HandoffCommit
		return CellMessage{
			Type:       MsgHandoffCommit,
			FromCellID: hc.FromCellId,
			HandoffCommit: &HandoffCommitPayload{
				NetID:      hc.NetId,
				Epoch:      hc.Epoch,
				CommitTick: hc.CommitTick,
			},
		}, nil

	case *meshpb.MeshFrame_HandoffCancel:
		if p.HandoffCancel == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: HandoffCancel payload is nil")
		}
		hc := p.HandoffCancel
		return CellMessage{
			Type:       MsgHandoffCancel,
			FromCellID: hc.FromCellId,
			HandoffCancel: &HandoffCancelPayload{
				NetID: hc.NetId,
				Epoch: hc.Epoch,
			},
		}, nil

	case *meshpb.MeshFrame_ForwardInput:
		if p.ForwardInput == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: ForwardInput payload is nil")
		}
		fi := p.ForwardInput
		return CellMessage{
			Type:       MsgForwardInput,
			FromCellID: fi.FromCellId,
			ForwardInput: &ForwardInputPayload{
				ConnID:    fi.ConnId,
				InputBlob: fi.InputBlob,
			},
		}, nil

	case *meshpb.MeshFrame_ChatRelay:
		if p.ChatRelay == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: ChatRelay payload is nil")
		}
		cr := p.ChatRelay
		return CellMessage{
			Type:       MsgChat,
			FromCellID: cr.FromCellId,
			Chat: &ChatRelay{
				Username: cr.Username,
				Text:     cr.Text,
			},
		}, nil

	case *meshpb.MeshFrame_CrossAction:
		if p.CrossAction == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: CrossAction payload is nil")
		}
		ca := p.CrossAction
		return CellMessage{
			Type:       MsgCrossCellAction,
			FromCellID: ca.FromCellId,
			Action: &CrossCellAction{
				Type:         ActionType(ca.ActionType),
				TargetNetID:  ca.TargetNetId,
				SourceNetID:  ca.SourceNetId,
				SourceCellID: ca.SourceCellId,
				Payload:      ca.Payload,
			},
		}, nil

	case *meshpb.MeshFrame_ActionResult:
		if p.ActionResult == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: ActionResult payload is nil")
		}
		ar := p.ActionResult
		return CellMessage{
			Type:       MsgActionResult,
			FromCellID: ar.FromCellId,
			ActionResult: &ActionResult{
				Type:        ActionType(ar.ActionType),
				TargetNetID: ar.TargetNetId,
				SourceNetID: ar.SourceNetId,
				Success:     ar.Success,
				Payload:     ar.Payload,
				SideEffects: ar.SideEffects,
			},
		}, nil

	case *meshpb.MeshFrame_PlayerAssignment:
		if p.PlayerAssignment == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: PlayerAssignment payload is nil")
		}
		pa := p.PlayerAssignment
		return CellMessage{
			Type:       MsgPlayerAssignment,
			FromCellID: pa.FromCellId,
			Assignment: &PlayerAssignment{
				ConnID:        pa.ConnId,
				Username:      pa.Username,
				IsReconnect:   pa.IsReconnect,
				Data:          pa.Data, // []byte — caller deserializes
				SpawnLocation: protoToLocation(pa.SpawnLocation),
			},
		}, nil

	case *meshpb.MeshFrame_SessionTransfer:
		if p.SessionTransfer == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: SessionTransfer payload is nil")
		}
		st := p.SessionTransfer
		return CellMessage{
			Type:       MsgSessionTransfer,
			FromCellID: st.FromCellId,
			Sessions: []SessionTransfer{
				{
					ConnID:   st.ConnId,
					Username: st.Username,
					StateTag: st.StateTag,
					Data:     st.Data, // []byte — caller deserializes
				},
			},
		}, nil

	case *meshpb.MeshFrame_SpawnTransfer:
		if p.SpawnTransfer == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: SpawnTransfer payload is nil")
		}
		sp := p.SpawnTransfer
		return CellMessage{
			Type:       MsgSpawnTransfer,
			FromCellID: sp.FromCellId,
			Spawn: &SpawnTransfer{
				ConnID:        sp.ConnId,
				Username:      sp.Username,
				SpawnLocation: protoToLocation(sp.SpawnLocation),
			},
		}, nil

	default:
		return CellMessage{}, fmt.Errorf("decodeMeshFrame: unknown oneof variant %T", frame.Msg)
	}
}

// encodeAnyToBytes coerces an `any` field to []byte for the wire.
// Accepts nil (returns nil) and []byte values only. Any other type is an error.
// A typed-nil []byte wrapped in an any interface (e.g. `var b []byte; Data = b`)
// fails the v == nil guard but succeeds the type assertion and encodes as nil
// bytes on the wire — same net result as a true nil.
func encodeAnyToBytes(fieldName string, v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("encodeCellMessage: %s must be nil or []byte before crossing host boundaries, got %T", fieldName, v)
	}
	return b, nil
}
