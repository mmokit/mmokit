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

// Cell is a self-contained game simulation owning one cell.
type Cell struct {
	ID      string
	Cell    CellID
	Engine  *engine.Engine
	World   GameWorld
	Base    *WorldBase // direct access for infrastructure methods
	Loop    *engine.GameLoop
	Bridge  Bridge
	Metrics *metrics.CellMetrics

	Inbox     chan CellMessage
	Events    chan net.PlayerEvent
	Neighbors map[string]*Cell
	Log       *logger.Logger
}

// Run starts the cell's game loop. Blocks until context is cancelled.
func (c *Cell) Run(ctx context.Context) {
	c.Log.Log(CatMeshCell, "[%s] cell started for cell %s", c.ID, c.Cell)
	c.Loop.Run(ctx)
}

// Shutdown saves all state on this cell.
func (c *Cell) Shutdown() {
	c.World.Shutdown()
	c.Log.Log(CatMeshCell, "[%s] cell shutdown complete", c.ID)
}

// DrainInbox processes all pending inter-cell messages.
// Called from the game loop via PreTickFunc.
func (c *Cell) DrainInbox() {
	for {
		select {
		case msg := <-c.Inbox:
			c.processMessage(msg)
		default:
			c.Base.TickGhosts()
			c.Base.TickTransferCooldowns()
			return
		}
	}
}

// processMessage handles a single inter-cell message.
func (c *Cell) processMessage(msg CellMessage) {
	switch msg.Type {
	case MsgTransfer:
		if msg.Transfer == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgTransfer from=%s netID=%d", c.ID, msg.FromCellID, msg.TransferNetID)
		// Remove any pre-existing replica with the same NetworkID
		if msg.TransferNetID != 0 {
			c.Base.RemoveReplicaByNetID(msg.TransferNetID)
		}

		// Pre-create player session so SpawnFromTransfer can wire s.Entity.
		connID, username := PeekTransferPlayer(msg.Transfer)
		if connID != 0 {
			c.Engine.Players.RegisterTransferSession(connID, username)
		}

		netID, spawnConnID, err := c.World.SpawnFromTransfer(msg.Transfer)
		if err != nil {
			return
		}

		// Send arrival confirmation back to source cell
		c.Bridge.SendArrivalConfirm(msg.FromCellID, &ArrivalConfirmMsg{
			NetworkID: netID,
			ConnID:    spawnConnID,
		})

	case MsgArrivalConfirm:
		if msg.ArrivalConfirm == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgArrivalConfirm from=%s netID=%d", c.ID, msg.FromCellID, msg.ArrivalConfirm.NetworkID)
		c.Base.RemoveGhostByNetID(msg.ArrivalConfirm.NetworkID)

	case MsgChat:
		if msg.Chat == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgChat from=%s user=%s", c.ID, msg.FromCellID, msg.Chat.Username)
		c.World.DispatchChat(msg.Chat.Username, msg.Chat.Text)

	case MsgSpawnTransfer:
		if msg.Spawn == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgSpawnTransfer from=%s conn=%d user=%s", c.ID, msg.FromCellID, msg.Spawn.ConnID, msg.Spawn.Username)
		c.Engine.Players.RegisterPlayer(msg.Spawn.ConnID, msg.Spawn.Username)

	case MsgPlayerAssignment:
		if msg.Assignment == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgPlayerAssignment conn=%d user=%s reconnect=%v",
			c.ID, msg.Assignment.ConnID, msg.Assignment.Username, msg.Assignment.IsReconnect)
		if msg.Assignment.IsReconnect {
			existing := c.Engine.Players.ByUsername(msg.Assignment.Username)
			if existing != nil && existing.State == engine.StateDisconnected {
				existing.ConnID = msg.Assignment.ConnID
				existing.DisconnectTime = time.Time{}
				c.Engine.Players.ReconnectSession(existing)
			} else {
				// Lingering session gone — treat as fresh login
				c.Engine.Players.RegisterPlayer(msg.Assignment.ConnID, msg.Assignment.Username)
			}
		} else {
			c.Engine.Players.RegisterPlayer(msg.Assignment.ConnID, msg.Assignment.Username)
			// Set optional session data from login handler (e.g., skin selection)
			if msg.Assignment.Data != nil {
				if s := c.Engine.Players.ByConnID(msg.Assignment.ConnID); s != nil {
					s.Data = msg.Assignment.Data
				}
			}
		}

	case MsgCrossNodeAction:
		if msg.Action == nil {
			return
		}
		c.Log.Log(CatMeshAction, "[%s] cross-cell action from=%s type=%d targetNetID=%d", c.ID, msg.FromCellID, msg.Action.Type, msg.Action.TargetNetID)
		result := c.World.HandleCrossNodeAction(msg.Action)
		if result != nil {
			c.Bridge.SendActionResult(msg.FromCellID, result)
		}

	case MsgActionResult:
		if msg.ActionResult == nil {
			return
		}
		c.Log.Log(CatMeshAction, "[%s] action result from=%s type=%d", c.ID, msg.FromCellID, msg.ActionResult.Type)
		c.World.HandleActionResult(msg.ActionResult)

	case MsgSessionTransfer:
		for _, st := range msg.Sessions {
			c.Log.Log(CatMeshMsg, "[%s] msg MsgSessionTransfer conn=%d user=%s state=%s",
				c.ID, st.ConnID, st.Username, st.StateTag)
			c.Engine.Players.RegisterSessionTransfer(st.ConnID, st.Username, st.StateTag, st.Data)
		}

	case MsgBorderFrame:
		if msg.BorderFrame == nil {
			return
		}
		byteCount := len(msg.BorderFrame)
		frame, err := replication.DecodeFrame(msg.BorderFrame)
		if err != nil {
			c.Log.Log(CatMeshMsg, "[%s] MsgBorderFrame decode error from=%s: %v", c.ID, msg.FromCellID, err)
			return
		}
		if c.Metrics != nil {
			c.Metrics.RecordBorderFrameRecv(byteCount)
		}
		c.Base.ApplyBorderFrame(frame, msg.FromCellID)
	}
}
