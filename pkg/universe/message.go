package universe

// MsgType identifies the kind of inter-node message.
type MsgType uint8

const (
	MsgTransfer        MsgType = 1 // entity transfer payload
	MsgArrivalConfirm  MsgType = 2 // transfer confirmed by destination
	MsgReplica         MsgType = 3 // border entity replication batch
	MsgChat            MsgType = 4 // chat relay
	MsgSpawnTransfer MsgType = 5 // player spawn on another node
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
	TransferNetID  uint32            // netID of transferred entity (for replica cleanup)
	Transfer       []byte            // game-serialized entity data
	ArrivalConfirm *ArrivalConfirmMsg
	Replicas       [][]byte          // game-serialized replica snapshots
	Chat           *ChatRelay
	Spawn          *SpawnTransfer
}
