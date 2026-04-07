package universe

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/net"
)

// Node is a self-contained game simulation owning one cell.
type Node struct {
	ID      string
	Cell    CellID
	Engine  *engine.Engine
	World   GameWorld
	Loop    *engine.GameLoop
	Bridge  NodeBridge
	Metrics *metrics.NodeMetrics

	Inbox     chan NodeMessage
	Events    chan net.PlayerEvent
	Neighbors map[string]*Node
	Log       *logger.Logger
}

// Run starts the node's game loop. Blocks until context is cancelled.
func (n *Node) Run(ctx context.Context) {
	n.Log.Log(CatMeshNode, "[%s] node started for cell %s", n.ID, n.Cell)
	n.Loop.Run(ctx)
}

// Shutdown saves all state on this node.
func (n *Node) Shutdown() {
	n.World.Shutdown()
	n.Log.Log(CatMeshNode, "[%s] node shutdown complete", n.ID)
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
		n.Log.Log(CatMeshMsg, "[%s] msg MsgTransfer from=%s netID=%d", n.ID, msg.FromNodeID, msg.TransferNetID)
		// Remove any pre-existing replica or proxy with the same NetworkID
		if msg.TransferNetID != 0 {
			n.World.RemoveReplicaByNetID(msg.TransferNetID)
			n.World.RemoveProxyByNetID(msg.TransferNetID)
		}

		// Pre-create player session so SpawnFromTransfer can wire s.Entity.
		connID, username := PeekTransferPlayer(msg.Transfer)
		if connID != 0 {
			n.Engine.Players.RegisterTransferSession(connID, username)
		}

		netID, spawnConnID, err := n.World.SpawnFromTransfer(msg.Transfer)
		if err != nil {
			return
		}

		// Send arrival confirmation back to source node
		n.Bridge.SendArrivalConfirm(msg.FromNodeID, &ArrivalConfirmMsg{
			NetworkID: netID,
			ConnID:    spawnConnID,
		})

	case MsgReplica:
		if len(msg.Replicas) > 0 {
			n.Log.Log(CatMeshMsg, "[%s] msg MsgReplica from=%s count=%d", n.ID, msg.FromNodeID, len(msg.Replicas))
			n.World.ApplyReplicas(msg.Replicas, msg.FromNodeID)
		}

	case MsgProxySummary:
		if len(msg.ProxySummaries) > 0 {
			n.Log.Log(CatMeshMsg, "[%s] msg MsgProxySummary from=%s count=%d", n.ID, msg.FromNodeID, len(msg.ProxySummaries))
			n.World.ApplyProxySummaries(msg.ProxySummaries, msg.FromNodeID)
		}

	case MsgArrivalConfirm:
		if msg.ArrivalConfirm == nil {
			return
		}
		n.Log.Log(CatMeshMsg, "[%s] msg MsgArrivalConfirm from=%s netID=%d", n.ID, msg.FromNodeID, msg.ArrivalConfirm.NetworkID)
		n.World.RemoveGhostByNetID(msg.ArrivalConfirm.NetworkID)

	case MsgChat:
		if msg.Chat == nil {
			return
		}
		n.Log.Log(CatMeshMsg, "[%s] msg MsgChat from=%s user=%s", n.ID, msg.FromNodeID, msg.Chat.Username)
		n.World.DispatchChat(msg.Chat.Username, msg.Chat.Text)

	case MsgSpawnTransfer:
		if msg.Spawn == nil {
			return
		}
		n.Log.Log(CatMeshMsg, "[%s] msg MsgSpawnTransfer from=%s conn=%d user=%s", n.ID, msg.FromNodeID, msg.Spawn.ConnID, msg.Spawn.Username)
		n.Engine.Players.RegisterPlayer(msg.Spawn.ConnID, msg.Spawn.Username)

	case MsgPlayerAssignment:
		if msg.Assignment == nil {
			return
		}
		n.Log.Log(CatMeshMsg, "[%s] msg MsgPlayerAssignment conn=%d user=%s reconnect=%v",
			n.ID, msg.Assignment.ConnID, msg.Assignment.Username, msg.Assignment.IsReconnect)
		if msg.Assignment.IsReconnect {
			existing := n.Engine.Players.ByUsername(msg.Assignment.Username)
			if existing != nil && existing.State == engine.StateDisconnected {
				existing.ConnID = msg.Assignment.ConnID
				existing.DisconnectTime = time.Time{}
				n.Engine.Players.ReconnectSession(existing)
			} else {
				// Lingering session gone — treat as fresh login
				n.Engine.Players.RegisterPlayer(msg.Assignment.ConnID, msg.Assignment.Username)
			}
		} else {
			n.Engine.Players.RegisterPlayer(msg.Assignment.ConnID, msg.Assignment.Username)
			// Set optional session data from login handler (e.g., skin selection)
			if msg.Assignment.Data != nil {
				if s := n.Engine.Players.ByConnID(msg.Assignment.ConnID); s != nil {
					s.Data = msg.Assignment.Data
				}
			}
		}

	case MsgCrossNodeAction:
		if msg.Action == nil {
			return
		}
		n.Log.Log(CatMeshAction, "[%s] cross-node action from=%s type=%d targetNetID=%d", n.ID, msg.FromNodeID, msg.Action.Type, msg.Action.TargetNetID)
		result := n.World.HandleCrossNodeAction(msg.Action)
		if result != nil {
			n.Bridge.SendActionResult(msg.FromNodeID, result)
		}

	case MsgActionResult:
		if msg.ActionResult == nil {
			return
		}
		n.Log.Log(CatMeshAction, "[%s] action result from=%s type=%d", n.ID, msg.FromNodeID, msg.ActionResult.Type)
		n.World.HandleActionResult(msg.ActionResult)

	case MsgDetailRequest:
		if msg.DetailRequest == nil {
			return
		}
		n.Log.Log(CatMeshMsg, "[%s] msg MsgDetailRequest from=%s count=%d", n.ID, msg.FromNodeID, len(msg.DetailRequest.NetworkIDs))
		resp := n.World.BuildDetailResponse(msg.DetailRequest.NetworkIDs)
		if resp != nil && len(resp.Frames) > 0 {
			n.Bridge.SendDetailResponse(msg.FromNodeID, resp)
		}

	case MsgDetailResponse:
		if msg.DetailResponse == nil {
			return
		}
		n.Log.Log(CatMeshMsg, "[%s] msg MsgDetailResponse from=%s frames=%d", n.ID, msg.FromNodeID, len(msg.DetailResponse.Frames))
		for _, frameBytes := range msg.DetailResponse.Frames {
			frame, err := UnmarshalReplicaFrame(frameBytes)
			if err != nil {
				continue
			}
			n.World.PromoteProxy(frame, msg.FromNodeID)
		}
	}
}
