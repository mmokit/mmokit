package universe

// MsgType identifies the kind of inter-node message.
type MsgType uint8

const (
	MsgTransfer        MsgType = 1 // entity transfer payload
	MsgArrivalConfirm  MsgType = 2 // transfer confirmed by destination
	MsgReplica         MsgType = 3 // border entity replication batch
	MsgChat            MsgType = 4 // chat relay
	MsgSpawnTransfer   MsgType = 5 // player spawn on another node
	MsgCrossNodeAction MsgType = 6 // cross-node action request to authoritative node
	MsgActionResult    MsgType = 7 // cross-node action result back to originator
	MsgPlayerAssignment MsgType = 8  // coordinator -> node: player login routed
	MsgProxySummary    MsgType = 9  // lightweight border proxy summary batch
	MsgDetailRequest   MsgType = 10 // request full state for proxy promotion
	MsgDetailResponse  MsgType = 11 // full state response for proxy promotion
	MsgSessionTransfer MsgType = 12 // entity-less session transfer during split
)

// Phase 4+ message types for tiered push replication + co-simulation handoff.
// Use values in the 100+ range so they cannot collide with existing
// constants during the Phase 7 transition, at which point
// MsgReplica/MsgProxySummary/MsgDetailRequest/MsgDetailResponse are
// deleted and the new constants take over.
const (
	MsgBorderFrame    MsgType = 100 // delta frame from one node to a neighbor
	MsgHandoffPrepare MsgType = 101 // begin co-simulation: full snapshot + baselines
	MsgHandoffCommit  MsgType = 102 // authority flip after warmup window
	MsgForwardInput   MsgType = 103 // safety path during single-tick routing overlap
)

// ArrivalConfirmMsg confirms entity arrived on destination node.
type ArrivalConfirmMsg struct {
	NetworkID uint32
	ConnID    uint32 // non-zero for player entities
}

// ChatRelay relays chat messages across nodes.
type ChatRelay struct {
	Username string
	Text     string
}

// SpawnTransfer requests a player spawn on another node.
type SpawnTransfer struct {
	ConnID   uint32
	Username string
}

// PlayerAssignment is sent by the coordinator to a node after successful login.
type PlayerAssignment struct {
	ConnID      uint32
	Username    string
	IsReconnect bool
	Data        any // optional session data from LoginHandler
}

// SessionTransfer carries an entity-less player session during cell splits.
type SessionTransfer struct {
	ConnID   uint32
	Username string
	StateTag string // state name (e.g., "docked", "dead")
	Data     any    // game-specific session data
}

// HandoffPreparePayload begins a co-simulation handoff. The receiver
// creates a read-only shadow entity from TransferBlob, seeds per-client
// baselines from ClientBaselines, and waits for MsgHandoffCommit to
// take authority. Sent on the owner's Border -> Promoted transition.
type HandoffPreparePayload struct {
	NetID           uint32                // entity net ID (matches NetworkID.ID)
	Epoch           uint32                // NEW authority epoch after the commit
	Kind            uint16                // entity kind
	TransferBlob    []byte                // reuses TransferFrame wire format from pkg/universe/transfer.go
	ClientBaselines []ClientBaselineEntry // baseline handover — see §"Client Baseline Continuity" in spec
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

// HandoffCommitPayload flips authority. The old owner increments its
// local epoch and downgrades the entity to a replica at the moment of
// send; the new owner takes the writer role on receive. Fire-and-forget
// — no ack is expected because the reliable channel guarantees delivery
// and the warmup floor + (Epoch, Tick) monotonic stamp make duplicate
// or delayed commit packets harmless.
type HandoffCommitPayload struct {
	NetID      uint32 // entity net ID
	Epoch      uint32 // new-epoch value (the one incremented by Prepare)
	CommitTick uint64 // authoritative commit tick
}

// ForwardInputPayload carries an input frame that arrived at the old
// owner during the single-tick overlap window after a HandoffCommit
// routing update. The old owner forwards the frame to the new owner
// rather than processing it locally. Rare safety path; in practice
// the tick boundary dominates.
type ForwardInputPayload struct {
	ConnID    uint32
	InputBlob []byte
}

// NodeMessage is the envelope for all inter-node communication.
// Transfer and Replicas use []byte for game-agnostic serialization.
type NodeMessage struct {
	Type           MsgType
	FromNodeID     string
	TransferNetID  uint32             // netID of transferred entity (for replica cleanup)
	Transfer       []byte             // game-serialized entity data
	ArrivalConfirm *ArrivalConfirmMsg
	Replicas       [][]byte           // game-serialized replica snapshots
	Chat           *ChatRelay
	Spawn          *SpawnTransfer
	Assignment     *PlayerAssignment  // coordinator -> node player assignment
	Action         *CrossNodeAction   // cross-node action request
	ActionResult   *ActionResult      // cross-node action result
	ProxySummaries [][]byte           // lightweight proxy summaries
	DetailRequest  *DetailRequestMsg  // request full state for proxy promotion
	DetailResponse *DetailResponseMsg // full state response for proxy promotion
	Sessions       []SessionTransfer  // entity-less session transfers during split
	// Phase 4+ fields for tiered push replication + co-simulation handoff.
	BorderFrame    []byte                 // encoded replication.Frame bytes for MsgBorderFrame
	HandoffPrepare *HandoffPreparePayload // for MsgHandoffPrepare
	HandoffCommit  *HandoffCommitPayload  // for MsgHandoffCommit
	ForwardInput   *ForwardInputPayload   // for MsgForwardInput
}
