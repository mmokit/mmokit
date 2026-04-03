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
	MsgProxySummary    MsgType = 8 // lightweight border proxy summary batch
	MsgDetailRequest   MsgType = 9 // request full state for proxy promotion
	MsgDetailResponse  MsgType = 10 // full state response for proxy promotion
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
	Action         *CrossNodeAction   // cross-node action request
	ActionResult   *ActionResult      // cross-node action result
	ProxySummaries [][]byte           // lightweight proxy summaries
	DetailRequest  *DetailRequestMsg  // request full state for proxy promotion
	DetailResponse *DetailResponseMsg // full state response for proxy promotion
}
