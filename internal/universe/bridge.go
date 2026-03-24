package universe

import (
	"log"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// nodeBridge implements game.NodeBridge for multi-node mode.
type nodeBridge struct {
	node  *Node
	coord *Coordinator
}

func (b *nodeBridge) PreTick() {
	b.node.DrainInbox()
}

func (b *nodeBridge) PostSystems() {
	SendReplicas(b.node)
	ExpireReplicas(b.node)
}

func (b *nodeBridge) SectorOwner(sector coords.SectorCoord) string {
	return b.coord.SectorOwner[sector]
}

func (b *nodeBridge) SendTransfer(destNodeID string, payload *game.TransferPayload) {
	if dest, ok := b.coord.Nodes[destNodeID]; ok {
		dest.Inbox <- NodeMessage{
			Type:       pkguniverse.MsgTransfer,
			FromNodeID: b.node.ID,
			Transfer:   payload,
		}
	}
}

func (b *nodeBridge) SendArrivalConfirm(destNodeID string, confirm *pkguniverse.ArrivalConfirmMsg) {
	if dest, ok := b.coord.Nodes[destNodeID]; ok {
		dest.Inbox <- NodeMessage{
			Type:           pkguniverse.MsgArrivalConfirm,
			FromNodeID:     b.node.ID,
			ArrivalConfirm: confirm,
		}
	}
}

func (b *nodeBridge) OnPlayerTransfer(connID uint32, destNodeID string) {
	b.coord.setPlayerNode(connID, destNodeID)
	log.Printf("coordinator: player conn=%d transferred to %s", connID, destNodeID)
}

func (b *nodeBridge) RelayChatToOtherNodes(username, text string) {
	for _, other := range b.coord.Nodes {
		if other.ID == b.node.ID {
			continue
		}
		other.Inbox <- NodeMessage{
			Type:       pkguniverse.MsgChat,
			FromNodeID: b.node.ID,
			Chat:       &pkguniverse.ChatRelay{Username: username, Text: text},
		}
	}
}

func (b *nodeBridge) RequestSpawnOnNode(connID uint32, username string) {
	defaultNode := b.coord.DefaultNode()
	defaultNode.Inbox <- NodeMessage{
		Type:       pkguniverse.MsgSpawnTransfer,
		FromNodeID: b.node.ID,
		Spawn:      &pkguniverse.SpawnTransfer{ConnID: connID, Username: username},
	}
	b.coord.setPlayerNode(connID, defaultNode.ID)
}
