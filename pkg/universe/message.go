package universe

import (
	"github.com/zenion/mmoserver/pkg/coords"
)

// MsgType identifies the kind of inter-cell message.
type MsgType uint8

const (
	MsgChat               MsgType = 4   // chat relay
	MsgSpawnTransfer      MsgType = 5   // player spawn on another cell
	MsgCrossCellAction    MsgType = 6   // cross-cell action request to authoritative cell
	MsgActionResult       MsgType = 7   // cross-cell action result back to originator
	MsgPlayerAssignment   MsgType = 8   // coordinator -> cell: player login routed
	MsgSessionTransfer    MsgType = 12  // entity-less session transfer during split
	MsgBorderFrame        MsgType = 100 // delta frame from one cell to a neighbor
	MsgHandoffPrepare     MsgType = 101 // v1 handoff — prepare + spawn shadow on dest
	MsgHandoffCommit      MsgType = 102 // v1 handoff — commit + promote shadow to live
	MsgForwardInput       MsgType = 103 // safety path during single-tick routing overlap
	MsgHandoffCancel      MsgType = 104 // v1 handoff cancel — currently unused; removed in Phase G
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

// PlayerAssignment is sent by the coordinator to a cell after successful login.
type PlayerAssignment struct {
	ConnID        uint32
	Username      string
	IsReconnect   bool
	Data          any // optional session data from LoginHandler
	SpawnLocation coords.Location
}

// SessionTransfer carries an entity-less player session during cell splits.
type SessionTransfer struct {
	ConnID   uint32
	Username string
	StateTag string // state name (e.g., "docked", "dead")
	Data     any    // game-specific session data
}

// HandoffPreparePayload is the v1 same-tick Prepare message: the
// destination creates a shadow entity from TransferBlob. Emitted by
// HandoffDriver.handleCrossing paired with HandoffCommitPayload on
// the same tick. Replaced by the single Handoff message in Phase G
// once the new hard-cut protocol lands.
type HandoffPreparePayload struct {
	NetID           uint32                // entity net ID (matches NetworkID.ID)
	Epoch           uint32                // NEW authority epoch after the commit
	Kind            uint16                // entity kind
	TransferBlob    []byte                // reuses TransferFrame wire format from pkg/universe/transfer.go
	ClientBaselines []ClientBaselineEntry // baseline handover (unused on main path; reserved)
	ExpectedTick    uint64                // predicted commit tick (informational)
	OldEpoch        uint32                // previous epoch — receiver validates the bump
}

// ClientBaselineEntry carries one per-client acknowledged baseline
// during baseline handover. Case A (non-player entity handoff) fills
// this with N entries, one per subscribed client. Case B (player
// handoff) fills this with M entries, one per entity in the player's
// AoI view at handoff time.
type ClientBaselineEntry struct {
	ConnID      uint32 // player connection ID
	EntityNetID uint32 // which entity this baseline is for
	LastAcked   []byte // acknowledged snapshot bytes
	LastTick    uint64 // tick of the last ack
}

// HandoffCommitPayload flips authority. Paired with HandoffPrepare
// on the same tick in the v1 protocol. Replaced by the single Handoff
// message in Phase G.
type HandoffCommitPayload struct {
	NetID      uint32 // entity net ID
	Epoch      uint32 // new-epoch value (the one incremented by Prepare)
	CommitTick uint64 // authoritative commit tick
}

// ForwardInputPayload carries an input frame that arrived at the old
// owner during the single-tick routing overlap window after authority
// flipped. The old owner forwards the frame to the new owner rather
// than processing it locally. Rare safety path.
type ForwardInputPayload struct {
	ConnID    uint32
	InputBlob []byte
}

// HandoffCancelPayload is retained for codec compatibility but has no
// production caller after Phase A2 — the v1 handleCrossing path fires
// Prepare+Commit same tick and cannot cancel. Phase G removes this
// type along with the rest of the three-message protocol.
type HandoffCancelPayload struct {
	NetID uint32 // entity net ID to cancel
	Epoch uint32 // epoch from the original Prepare (for sanity check)
}

// DisconnectPayload carries the disconnect info for MsgPlayerDisconnected.
// Used by the cross-process path to notify a cell that a player has disconnected.
type DisconnectPayload struct {
	ConnID uint32
	Reason string
}

// CellMessage is the envelope for all inter-cell communication.
type CellMessage struct {
	Type           MsgType
	FromCellID     string
	Chat           *ChatRelay
	Spawn          *SpawnTransfer
	Assignment     *PlayerAssignment      // coordinator -> cell player assignment
	Action         *CrossCellAction       // cross-cell action request
	ActionResult   *ActionResult          // cross-cell action result
	Sessions       []SessionTransfer      // entity-less session transfers during split
	BorderFrame    []byte                 // encoded replication.Frame bytes for MsgBorderFrame
	HandoffPrepare *HandoffPreparePayload // for MsgHandoffPrepare
	HandoffCommit  *HandoffCommitPayload  // for MsgHandoffCommit
	ForwardInput   *ForwardInputPayload   // for MsgForwardInput
	HandoffCancel  *HandoffCancelPayload  // for MsgHandoffCancel
	Disconnect     *DisconnectPayload     // for MsgPlayerDisconnected
}
