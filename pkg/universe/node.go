package universe

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/replication"
)

// Node is a self-contained game simulation owning one cell.
type Node struct {
	ID      string
	Cell    CellID
	Engine  *engine.Engine
	World   GameWorld
	Base    *WorldBase // direct access for infrastructure methods
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
			n.Base.TickGhosts()
			n.Base.TickTransferCooldowns()
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
		// Remove any pre-existing replica with the same NetworkID
		if msg.TransferNetID != 0 {
			n.Base.RemoveReplicaByNetID(msg.TransferNetID)
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

	case MsgArrivalConfirm:
		if msg.ArrivalConfirm == nil {
			return
		}
		n.Log.Log(CatMeshMsg, "[%s] msg MsgArrivalConfirm from=%s netID=%d", n.ID, msg.FromNodeID, msg.ArrivalConfirm.NetworkID)
		n.Base.RemoveGhostByNetID(msg.ArrivalConfirm.NetworkID)

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

	case MsgSessionTransfer:
		for _, st := range msg.Sessions {
			n.Log.Log(CatMeshMsg, "[%s] msg MsgSessionTransfer conn=%d user=%s state=%s",
				n.ID, st.ConnID, st.Username, st.StateTag)
			n.Engine.Players.RegisterSessionTransfer(st.ConnID, st.Username, st.StateTag, st.Data)
		}

	case MsgBorderFrame:
		if msg.BorderFrame == nil {
			return
		}
		byteCount := len(msg.BorderFrame)
		frame, err := replication.DecodeFrame(msg.BorderFrame)
		if err != nil {
			n.Log.Log(CatMeshMsg, "[%s] MsgBorderFrame decode error from=%s: %v", n.ID, msg.FromNodeID, err)
			return
		}
		if n.Metrics != nil {
			n.Metrics.RecordBorderFrameRecv(byteCount)
		}
		n.Base.ApplyBorderFrame(frame, msg.FromNodeID)
	}
}
