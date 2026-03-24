package universe

import (
	"github.com/zenion/mmoserver/internal/game"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// Re-export generic message types from pkg/universe.
type (
	MsgType         = pkguniverse.MsgType
	ArrivalConfirmMsg = pkguniverse.ArrivalConfirmMsg
	ChatRelay       = pkguniverse.ChatRelay
	RespawnTransfer = pkguniverse.RespawnTransfer
)

// Re-export message type constants.
const (
	MsgTransfer        = pkguniverse.MsgTransfer
	MsgArrivalConfirm  = pkguniverse.MsgArrivalConfirm
	MsgReplica         = pkguniverse.MsgReplica
	MsgChat            = pkguniverse.MsgChat
	MsgRespawnTransfer = pkguniverse.MsgRespawnTransfer
)

// NodeMessage is the envelope for all inter-node communication.
// Uses generic types from pkg/universe for fields that are not game-specific.
type NodeMessage struct {
	Type       MsgType
	FromNodeID string
	Transfer       *game.TransferPayload  // game-specific transfer data
	ArrivalConfirm *ArrivalConfirmMsg
	Replicas       []game.ReplicaSnapshot // game-specific replica data
	Chat           *ChatRelay
	Respawn        *RespawnTransfer
}
