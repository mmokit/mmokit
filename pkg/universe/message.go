package universe

// MsgType identifies the kind of inter-node message.
type MsgType uint8

const (
	MsgTransfer        MsgType = 1 // entity transfer payload
	MsgArrivalConfirm  MsgType = 2 // transfer confirmed by destination
	MsgReplica         MsgType = 3 // border entity replication batch
	MsgChat            MsgType = 4 // chat relay
	MsgRespawnTransfer MsgType = 5 // player respawn on another node
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

// RespawnTransfer requests a player respawn on another node.
type RespawnTransfer struct {
	ConnID   uint32
	Username string
}
