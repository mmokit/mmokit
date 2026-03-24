package universe

import (
	"context"
	"log"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// Node is a self-contained game simulation owning one sector.
type Node struct {
	ID        string
	Sector    coords.SectorCoord
	Engine    *engine.Engine
	World     GameWorld
	Loop      *engine.GameLoop
	Bridge    NodeBridge

	Inbox     chan NodeMessage
	Events    chan net.PlayerEvent
	Neighbors map[string]*Node
	Log       *logger.Logger
}

// Run starts the node's game loop. Blocks until context is cancelled.
func (n *Node) Run(ctx context.Context) {
	log.Printf("[%s] node started for sector (%d,%d)", n.ID, n.Sector.SX, n.Sector.SY)
	n.Loop.Run(ctx)
}

// Shutdown saves all state on this node.
func (n *Node) Shutdown() {
	n.World.Shutdown()
	log.Printf("[%s] node shutdown complete", n.ID)
}

// DrainInbox processes all pending inter-node messages.
// Called from the game loop via PreTickFunc.
func (n *Node) DrainInbox() {
	for {
		select {
		case msg := <-n.Inbox:
			n.processMessage(msg)
		default:
			n.World.TickGhosts()
			n.World.TickTransferCooldowns()
			return
		}
	}
}

// processMessage handles a single inter-node message.
func (n *Node) processMessage(msg NodeMessage) {
	switch msg.Type {
	case MsgTransfer:
		if msg.Transfer == nil {
			return
		}
		// Remove any pre-existing replica with the same NetworkID
		if msg.TransferNetID != 0 {
			n.World.RemoveReplicaByNetID(msg.TransferNetID)
		}

		netID, connID, err := n.World.SpawnFromTransfer(msg.Transfer)
		if err != nil {
			return
		}

		// Send arrival confirmation back to source node
		n.Bridge.SendArrivalConfirm(msg.FromNodeID, &ArrivalConfirmMsg{
			NetworkID: netID,
			ConnID:    connID,
		})

	case MsgReplica:
		if len(msg.Replicas) > 0 {
			n.World.ApplyReplicas(msg.Replicas, msg.FromNodeID)
		}

	case MsgArrivalConfirm:
		if msg.ArrivalConfirm == nil {
			return
		}
		n.World.RemoveGhostByNetID(msg.ArrivalConfirm.NetworkID)

	case MsgChat:
		if msg.Chat == nil {
			return
		}
		n.World.DispatchChat(msg.Chat.Username, msg.Chat.Text)

	case MsgSpawnTransfer:
		if msg.Spawn == nil {
			return
		}
		n.World.RegisterPendingLogin(msg.Spawn.ConnID, msg.Spawn.Username)

	case MsgCrossNodeAction:
		if msg.Action == nil {
			return
		}
		result := n.World.HandleCrossNodeAction(msg.Action)
		if result != nil {
			n.Bridge.SendActionResult(msg.FromNodeID, result)
		}

	case MsgActionResult:
		if msg.ActionResult == nil {
			return
		}
		n.World.HandleActionResult(msg.ActionResult)
	}
}
