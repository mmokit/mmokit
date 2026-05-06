package universe

import (
	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/coords"
)

// MsgType identifies the kind of inter-cell message.
type MsgType uint8

const (
	MsgChat               MsgType = 4   // chat relay
	MsgSpawnTransfer      MsgType = 5   // player spawn on another cell
	MsgCrossCellAction    MsgType = 6   // cross-cell action request to authoritative cell
	MsgPlayerAssignment   MsgType = 8   // coordinator -> cell: player login routed
	MsgSessionTransfer    MsgType = 12  // entity-less session transfer during split
	MsgBorderFrame        MsgType = 100 // delta frame from one cell to a neighbor
	MsgHandoff            MsgType = 101 // single-message hard-cut handoff (Phase G)
	MsgForwardInput       MsgType = 103 // safety path during single-tick routing overlap
	MsgPlayerDisconnected MsgType = 107 // cross-process player disconnect notification
)

// ChatRelay relays chat messages across cells.
type ChatRelay struct {
	Username string
	Text     string
}

// SpawnTransfer requests a player spawn on another cell.
type SpawnTransfer struct {
	ConnID        uint32
	Username      string
	SpawnLocation coords.Location
}

// PlayerAssignment is sent by the gateway to a cell after successful auth.
// UserID is the canonical identity from auth_users; SessionToken is the
// opaque session token bound to that login.
type PlayerAssignment struct {
	ConnID        uint32
	UserID        uuid.UUID
	Username      string
	SessionToken  string
	IsReconnect   bool
	SpawnLocation coords.Location
}

// SessionTransfer carries an entity-less player session during cell splits.
type SessionTransfer struct {
	ConnID   uint32
	Username string
	StateTag string // state name (e.g., "docked", "dead")
	Data     any    // game-specific session data
}

// HandoffPayload is the single authority-transfer message. Mirrors the
// meshpb.Handoff proto for in-process and cross-host dispatch.
//
// CommitTick is the cluster-clock tick number at which the destination
// becomes authoritative. Source demotes at end-of-tick (CommitTick-1);
// destination promotes at start-of-tick CommitTick. A single-tick slip
// on delivery is absorbed by client render-lag.
//
// TransferBlob is populated only when the destination does not already
// have a border-replica for NetID — the fast-mover / cross-boundary
// spawn case. When nil, destination promotes its existing Replica to
// Live at CommitTick.
//
// ConnID is 0 for non-player entities; non-zero when a player entity
// crosses the boundary so the destination can register the player's
// session on arrival.
type HandoffPayload struct {
	NetID        uint32
	Epoch        uint32
	CommitTick   uint64
	TransferBlob []byte // optional; empty when dest has replica
	ConnID       uint32 // 0 for non-player
}

// ForwardInputPayload carries an input frame that arrived at the old
// owner during the single-tick routing overlap window after authority
// flipped. The old owner forwards the frame to the new owner rather
// than processing it locally. Rare safety path.
type ForwardInputPayload struct {
	ConnID    uint32
	InputBlob []byte
}

// DisconnectPayload carries the disconnect info for MsgPlayerDisconnected.
// Used by the cross-process path to notify a cell that a player has disconnected.
type DisconnectPayload struct {
	ConnID uint32
	Reason string
}

// CellMessage is the envelope for all inter-cell communication.
type CellMessage struct {
	Type         MsgType
	FromCellID   MeshCellID
	Chat         *ChatRelay
	Spawn        *SpawnTransfer
	Assignment   *PlayerAssignment    // coordinator -> cell player assignment
	Action       *CrossCellAction     // cross-cell action request
	Sessions     []SessionTransfer    // entity-less session transfers during split
	BorderFrame  []byte               // encoded replication.Frame bytes for MsgBorderFrame
	Handoff      *HandoffPayload      // for MsgHandoff
	ForwardInput *ForwardInputPayload // for MsgForwardInput
	Disconnect   *DisconnectPayload   // for MsgPlayerDisconnected
}
