package universe

import (
	"fmt"

	"github.com/google/uuid"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	"github.com/mmokit/mmokit/pkg/coords"
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
func encodeCellMessage(msg CellMessage, destCellID MeshCellID) (*meshpb.MeshFrame, error) {
	frame := &meshpb.MeshFrame{DestCellId: string(destCellID)}

	switch msg.Type {
	case MsgBorderFrame:
		frame.Msg = &meshpb.MeshFrame_BorderFrame{
			BorderFrame: &meshpb.BorderFrame{
				FromCellId: string(msg.FromCellID),
				Data:       msg.BorderFrame,
			},
		}

	case MsgHandoff:
		if msg.Handoff == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgHandoff payload is nil")
		}
		p := msg.Handoff
		frame.Msg = &meshpb.MeshFrame_Handoff{
			Handoff: &meshpb.Handoff{
				FromCellId:   string(msg.FromCellID),
				NetId:        p.NetID,
				Epoch:        p.Epoch,
				CommitTick:   p.CommitTick,
				TransferBlob: p.TransferBlob,
				ConnId:       p.ConnID,
			},
		}

	case MsgHandoffAccepted:
		if msg.HandoffAck == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgHandoffAccepted payload is nil")
		}
		// Deliberately tunnelled through the existing CrossCellAction
		// variant under the engine-reserved ActionHandoffAccepted opcode
		// rather than given its own MeshFrame oneof field. See
		// handoff_ack.go for why: the repo's never-reserve proto convention
		// makes any meshpb edit a lockstep cluster redeploy. Do not
		// "fix" this into a new proto message.
		a := msg.HandoffAck
		frame.Msg = &meshpb.MeshFrame_CrossAction{
			CrossAction: &meshpb.CrossCellAction{
				FromCellId:   string(msg.FromCellID),
				ActionType:   uint32(ActionHandoffAccepted),
				TargetNetId:  a.NetID,
				SourceCellId: string(msg.FromCellID),
				Payload:      encodeHandoffAck(a),
			},
		}

	case MsgForwardInput:
		if msg.ForwardInput == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgForwardInput payload is nil")
		}
		p := msg.ForwardInput
		frame.Msg = &meshpb.MeshFrame_ForwardInput{
			ForwardInput: &meshpb.ForwardInput{
				FromCellId:   string(msg.FromCellID),
				ConnId:       p.ConnID,
				InputBlob:    p.InputBlob,
				GatewayId:    p.GatewayID,
				SessionEpoch: p.SessionEpoch,
			},
		}

	case MsgCrossCellAction:
		if msg.Action == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgCrossCellAction payload is nil")
		}
		a := msg.Action
		frame.Msg = &meshpb.MeshFrame_CrossAction{
			CrossAction: &meshpb.CrossCellAction{
				FromCellId:   string(msg.FromCellID),
				ActionType:   uint32(a.Type),
				TargetNetId:  a.TargetNetID,
				SourceNetId:  a.SourceNetID,
				SourceCellId: a.SourceCellID,
				Payload:      a.Payload,
			},
		}

	case MsgPlayerAssignment:
		if msg.Assignment == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgPlayerAssignment payload is nil")
		}
		a := msg.Assignment
		frame.Msg = &meshpb.MeshFrame_PlayerAssignment{
			PlayerAssignment: &meshpb.PlayerAssignment{
				FromCellId:       string(msg.FromCellID),
				ConnId:           a.ConnID,
				UserId:           a.UserID.String(),
				Username:         a.Username,
				SessionToken:     a.SessionToken,
				IsReconnect:      a.IsReconnect,
				StreamGeneration: a.StreamGeneration,
				SpawnLocation:    locationToProto(a.SpawnLocation),
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
				FromCellId:       string(msg.FromCellID),
				ConnId:           s.ConnID,
				GatewayId:        s.GatewayID,
				GatewayConnId:    s.GatewayConnID,
				SessionEpoch:     s.SessionEpoch,
				Username:         s.Username,
				StateTag:         s.StateTag,
				Data:             dataBytes,
				UserId:           s.UserID.String(),
				StreamGeneration: s.StreamGeneration,
			},
		}

	case MsgSpawnTransfer:
		if msg.Spawn == nil {
			return nil, fmt.Errorf("encodeCellMessage: MsgSpawnTransfer payload is nil")
		}
		frame.Msg = &meshpb.MeshFrame_SpawnTransfer{
			SpawnTransfer: &meshpb.SpawnTransfer{
				FromCellId:       string(msg.FromCellID),
				ConnId:           msg.Spawn.ConnID,
				Username:         msg.Spawn.Username,
				SpawnLocation:    locationToProto(msg.Spawn.SpawnLocation),
				StreamGeneration: msg.Spawn.StreamGeneration,
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
			FromCellID:  MeshCellID(p.BorderFrame.FromCellId),
			BorderFrame: p.BorderFrame.Data,
		}, nil

	case *meshpb.MeshFrame_Handoff:
		if p.Handoff == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: Handoff payload is nil")
		}
		h := p.Handoff
		return CellMessage{
			Type:       MsgHandoff,
			FromCellID: MeshCellID(h.FromCellId),
			Handoff: &HandoffPayload{
				NetID:        h.NetId,
				Epoch:        h.Epoch,
				CommitTick:   h.CommitTick,
				TransferBlob: h.TransferBlob,
				ConnID:       h.ConnId,
			},
		}, nil

	case *meshpb.MeshFrame_ForwardInput:
		if p.ForwardInput == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: ForwardInput payload is nil")
		}
		fi := p.ForwardInput
		return CellMessage{
			Type:       MsgForwardInput,
			FromCellID: MeshCellID(fi.FromCellId),
			ForwardInput: &ForwardInputPayload{
				GatewayID:    fi.GatewayId,
				ConnID:       fi.ConnId,
				SessionEpoch: fi.SessionEpoch,
				InputBlob:    fi.InputBlob,
			},
		}, nil

	case *meshpb.MeshFrame_CrossAction:
		if p.CrossAction == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: CrossAction payload is nil")
		}
		ca := p.CrossAction
		// ActionHandoffAccepted is a wire carrier, not a real cross-cell
		// action: intercept it here so it becomes a typed MsgHandoffAccepted
		// and never reaches Stage.HandleEngineAction. See handoff_ack.go.
		if ca.ActionType == uint32(ActionHandoffAccepted) {
			ack, ok := decodeHandoffAck(ca.TargetNetId, ca.Payload)
			if !ok {
				return CellMessage{}, fmt.Errorf(
					"decodeMeshFrame: malformed HandoffAccepted payload (%d bytes)", len(ca.Payload))
			}
			return CellMessage{
				Type:       MsgHandoffAccepted,
				FromCellID: MeshCellID(ca.FromCellId),
				HandoffAck: ack,
			}, nil
		}
		return CellMessage{
			Type:       MsgCrossCellAction,
			FromCellID: MeshCellID(ca.FromCellId),
			Action: &CrossCellAction{
				Type:         ActionType(ca.ActionType),
				TargetNetID:  ca.TargetNetId,
				SourceNetID:  ca.SourceNetId,
				SourceCellID: ca.SourceCellId,
				Payload:      ca.Payload,
			},
		}, nil

	case *meshpb.MeshFrame_PlayerAssignment:
		if p.PlayerAssignment == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: PlayerAssignment payload is nil")
		}
		pa := p.PlayerAssignment
		var userID uuid.UUID
		if pa.UserId != "" {
			if u, err := uuid.Parse(pa.UserId); err == nil {
				userID = u
			}
		}
		return CellMessage{
			Type:       MsgPlayerAssignment,
			FromCellID: MeshCellID(pa.FromCellId),
			Assignment: &PlayerAssignment{
				ConnID:           pa.ConnId,
				UserID:           userID,
				Username:         pa.Username,
				SessionToken:     pa.SessionToken,
				IsReconnect:      pa.IsReconnect,
				StreamGeneration: pa.StreamGeneration,
				SpawnLocation:    protoToLocation(pa.SpawnLocation),
			},
		}, nil

	case *meshpb.MeshFrame_SessionTransfer:
		if p.SessionTransfer == nil {
			return CellMessage{}, fmt.Errorf("decodeMeshFrame: SessionTransfer payload is nil")
		}
		st := p.SessionTransfer
		var userID uuid.UUID
		if st.UserId != "" {
			if u, err := uuid.Parse(st.UserId); err == nil {
				userID = u
			}
		}
		return CellMessage{
			Type:       MsgSessionTransfer,
			FromCellID: MeshCellID(st.FromCellId),
			Sessions: []SessionTransfer{
				{
					ConnID:           st.ConnId,
					GatewayID:        st.GatewayId,
					GatewayConnID:    st.GatewayConnId,
					SessionEpoch:     st.SessionEpoch,
					UserID:           userID,
					Username:         st.Username,
					StreamGeneration: st.StreamGeneration,
					StateTag:         st.StateTag,
					Data:             st.Data, // []byte — caller deserializes
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
			FromCellID: MeshCellID(sp.FromCellId),
			Spawn: &SpawnTransfer{
				ConnID:           sp.ConnId,
				Username:         sp.Username,
				StreamGeneration: sp.StreamGeneration,
				SpawnLocation:    protoToLocation(sp.SpawnLocation),
			},
		}, nil

	case *meshpb.MeshFrame_ServiceEvent:
		// ServiceEvent is handled inline by routeInboundFrame and never
		// falls through to decodeMeshFrame. Reaching this branch is a
		// programmer bug, not a wire-level error.
		return CellMessage{}, fmt.Errorf("decodeMeshFrame: ServiceEvent must be handled inline by routeInboundFrame")

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
