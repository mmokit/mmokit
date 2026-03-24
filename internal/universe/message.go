package universe

import (
	"github.com/zenion/mmoserver/internal/game"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// NodeMessage is the envelope for all inter-node communication.
// Uses generic types from pkg/universe for fields that are not game-specific.
type NodeMessage struct {
	Type           pkguniverse.MsgType
	FromNodeID     string
	Transfer       *game.TransferPayload          // game-specific transfer data
	ArrivalConfirm *pkguniverse.ArrivalConfirmMsg
	Replicas       []game.ReplicaSnapshot         // game-specific replica data
	Chat           *pkguniverse.ChatRelay
	Spawn          *pkguniverse.SpawnTransfer
}
