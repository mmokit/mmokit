package universe

import (
	"log"

	"github.com/zenion/mmoserver/pkg/coords"
)

// nodeBridge implements NodeBridge for multi-node mode.
type nodeBridge struct {
	node  *Node
	coord *Coordinator
}

func (b *nodeBridge) PreTick() {
	b.node.DrainInbox()
}

func (b *nodeBridge) PostSystems() {
	b.sendReplicas()
	b.node.World.ExpireReplicas()
}

func (b *nodeBridge) SectorOwner(sector coords.SectorCoord) string {
	return b.coord.SectorOwner[sector]
}

func (b *nodeBridge) SendTransfer(destNodeID string, data []byte, netID uint32) {
	if dest, ok := b.coord.Nodes[destNodeID]; ok {
		dest.Inbox <- NodeMessage{
			Type:          MsgTransfer,
			FromNodeID:    b.node.ID,
			TransferNetID: netID,
			Transfer:      data,
		}
	}
}

func (b *nodeBridge) SendArrivalConfirm(destNodeID string, confirm *ArrivalConfirmMsg) {
	if dest, ok := b.coord.Nodes[destNodeID]; ok {
		dest.Inbox <- NodeMessage{
			Type:           MsgArrivalConfirm,
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
			Type:       MsgChat,
			FromNodeID: b.node.ID,
			Chat:       &ChatRelay{Username: username, Text: text},
		}
	}
}

func (b *nodeBridge) RequestSpawnOnNode(connID uint32, username string) {
	defaultNode := b.coord.DefaultNode()
	defaultNode.Inbox <- NodeMessage{
		Type:       MsgSpawnTransfer,
		FromNodeID: b.node.ID,
		Spawn:      &SpawnTransfer{ConnID: connID, Username: username},
	}
	b.coord.setPlayerNode(connID, defaultNode.ID)
}

// sendReplicas scans border entities and sends replica snapshots to neighboring nodes.
func (b *nodeBridge) sendReplicas() {
	// Build neighbor info map
	neighbors := make(map[string]NeighborInfo, len(b.node.Neighbors))
	for nID, neighbor := range b.node.Neighbors {
		neighbors[nID] = NeighborInfo{
			NodeID: nID,
			DX:     neighbor.Sector.SX - b.node.Sector.SX,
			DY:     neighbor.Sector.SY - b.node.Sector.SY,
		}
	}

	snapsByNeighbor := b.node.World.ScanBorderEntities(neighbors)
	for neighborID, snaps := range snapsByNeighbor {
		if neighbor, ok := b.node.Neighbors[neighborID]; ok {
			neighbor.Inbox <- NodeMessage{
				Type:       MsgReplica,
				FromNodeID: b.node.ID,
				Replicas:   snaps,
			}
		}
	}
}
