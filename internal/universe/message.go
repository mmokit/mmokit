package universe

import "github.com/zenion/mmoserver/internal/game"

// MsgType identifies the kind of inter-node message.
type MsgType uint8

const (
	MsgTransfer        MsgType = 1 // entity transfer payload
	MsgArrivalConfirm  MsgType = 2 // transfer confirmed by destination
	MsgReplica         MsgType = 3 // border entity replication batch
	MsgChat            MsgType = 4 // chat relay
	MsgRespawnTransfer MsgType = 5 // player respawn on another node
)

// NodeMessage is the envelope for all inter-node communication.
type NodeMessage struct {
	Type       MsgType
	FromNodeID string
	Transfer       *game.TransferPayload
	ArrivalConfirm *game.ArrivalConfirmMsg
	Replicas       []game.ReplicaSnapshot
	Chat           *game.ChatRelay
	Respawn        *game.RespawnTransfer
}
