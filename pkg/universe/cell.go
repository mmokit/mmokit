package universe

import (
	"context"
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
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

	// OnMessage is an optional hook called for every inbox message before
	// the cell's processMessage handler runs. Set from tests to observe
	// delivered messages without racing against the game loop. Nil in
	// production. Must be set before Run() is called or under external
	// synchronization.
	OnMessage func(CellMessage)

	// runMu guards runCancel / runDone. Run() initializes them on entry,
	// Shutdown() reads + acts on them to cancel the game loop and block
	// until it has actually exited. This is how S7-T10 stops cell
	// goroutines from leaking past a Shutdown call and racing with the
	// next test's setup (coords.SetCellSize, etc.).
	runMu     sync.Mutex
	runCancel context.CancelFunc // nil until Run starts; reset when Run exits
	runDone   chan struct{}      // closed when Run returns
}

// Run starts the cell's game loop. Blocks until context is cancelled OR
// Shutdown() is called on this cell. The ctx argument is wrapped in a
// derived context whose cancel is owned by the cell, so Shutdown() can
// stop the loop without touching the caller's ctx.
func (c *Cell) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.runMu.Lock()
	c.runCancel = cancel
	c.runDone = done
	c.runMu.Unlock()

	defer func() {
		c.runMu.Lock()
		c.runCancel = nil
		c.runMu.Unlock()
		close(done)
	}()

	c.Log.Log(CatMeshCell, "[%s] cell started for cell %s", c.ID, c.Cell)
	c.Loop.Run(ctx)
}

// Shutdown saves all state on this cell. If Run is currently active, it
// cancels the cell's derived game-loop context and BLOCKS until the game
// loop goroutine has exited before saving state and returning. Idempotent:
// safe to call multiple times and safe to call before Run starts.
func (c *Cell) Shutdown() {
	c.runMu.Lock()
	cancel := c.runCancel
	done := c.runDone
	c.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
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
	if c.OnMessage != nil {
		c.OnMessage(msg)
	}
	switch msg.Type {
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

	case MsgCrossCellAction:
		if msg.Action == nil {
			return
		}
		c.Log.Log(CatMeshAction, "[%s] cross-cell action from=%s type=%d targetNetID=%d", c.ID, msg.FromCellID, msg.Action.Type, msg.Action.TargetNetID)
		result := c.World.HandleCrossCellAction(msg.Action)
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

	case MsgHandoffPrepare:
		if msg.HandoffPrepare == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgHandoffPrepare from=%s netID=%d epoch=%d",
			c.ID, msg.FromCellID, msg.HandoffPrepare.NetID, msg.HandoffPrepare.Epoch)

		// Remove any pre-existing replica with the same NetID (the entity
		// was visible via border replication; the shadow replaces it).
		if msg.HandoffPrepare.NetID != 0 {
			c.Base.RemoveReplicaByNetID(msg.HandoffPrepare.NetID)
		}

		// Pre-create player session so SpawnFromTransferCore's
		// onPlayerTransferReceived hook can wire the incoming entity to
		// the session. Without this, the hook's session lookup returns
		// nil, s.Entity is never assigned, and the player loses control
		// on the destination cell.
		//
		// In node mode, we ALSO need to register a VCM session on this
		// host so subsequent ClientInput frames from the gateway route
		// to the freshly transferred player. The frame carries the
		// client's SessionKey (GatewayID + GatewayConnID) from the
		// source's VCM; we register it here to allocate a DESTINATION-
		// local ID, then use that local ID for the engine player
		// session — NOT the source's local ID, which means nothing on
		// this host. SpawnFromTransferCore performs the matching remap
		// when it decodes the blob (idempotent RegisterSession returns
		// the same localID for the same key).
		if srcConnID, gwID, gwConnID, username := PeekTransferPlayer(msg.HandoffPrepare.TransferBlob); srcConnID != 0 {
			localID := srcConnID
			if gwConnID != 0 && c.Base != nil && c.Base.coord != nil && c.Base.coord.vcm != nil {
				key := SessionKey{GatewayID: gwID, ConnID: gwConnID}
				// Pass epoch=0: this is an entity handoff, not a session
				// authority change. Session epoch is owned by sessionRoutes
				// (bumped by Migrate + SessionRegister dispatch). VCM's
				// "never downgrade" logic preserves the real session epoch.
				localID = c.Base.coord.vcm.RegisterSession(key, username, 0, c.ID)
			}
			c.Engine.Players.RegisterTransferSession(localID, username)
		}

		entity, err := c.Base.SpawnShadow(msg.HandoffPrepare)
		if err != nil {
			c.Log.Log(CatMeshMsg, "[%s] shadow spawn failed: netID=%d err=%v",
				c.ID, msg.HandoffPrepare.NetID, err)
			return
		}

		// Set the source cell ID on the shadow.
		shadowMap := ecs.NewMap1[component.Shadow](c.Engine.ECS)
		if shadowMap.HasAll(entity) {
			shadowMap.Get(entity).SourceCellID = msg.FromCellID
		}

	case MsgHandoffCommit:
		if msg.HandoffCommit == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgHandoffCommit from=%s netID=%d epoch=%d",
			c.ID, msg.FromCellID, msg.HandoffCommit.NetID, msg.HandoffCommit.Epoch)

		if !c.Base.PromoteShadow(msg.HandoffCommit.NetID) {
			c.Log.Log(CatMeshMsg,
				"[%s] MsgHandoffCommit: no shadow found for netID=%d (already promoted or out-of-order)",
				c.ID, msg.HandoffCommit.NetID)
			return
		}

	case MsgHandoffCancel:
		if msg.HandoffCancel == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgHandoffCancel from=%s netID=%d",
			c.ID, msg.FromCellID, msg.HandoffCancel.NetID)
		c.Base.RemoveShadowByNetID(msg.HandoffCancel.NetID)

	case MsgForwardInput:
		if msg.ForwardInput == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgForwardInput from=%s conn=%d bytes=%d",
			c.ID, msg.FromCellID, msg.ForwardInput.ConnID, len(msg.ForwardInput.InputBlob))
		// Inject the forwarded input into the local ConnManager's input
		// buffer so it gets processed by the engine's input router on the
		// next tick, as if it had arrived from the player's connection.
		if c.Engine.ConnMgr != nil {
			c.Engine.ConnMgr.InjectInput(msg.ForwardInput.ConnID, msg.ForwardInput.InputBlob)
		}

	case MsgPlayerDisconnected:
		if msg.Disconnect == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgPlayerDisconnected conn=%d reason=%s",
			c.ID, msg.Disconnect.ConnID, msg.Disconnect.Reason)
		// Push a synthetic disconnect event to the cell's Events channel — same
		// path as in-process WebSocket disconnects so the engine's grace-period
		// state machine fires unchanged.
		select {
		case c.Events <- net.PlayerEvent{ConnID: msg.Disconnect.ConnID, Disconnect: true}:
		default:
			c.Log.Log(CatMeshMsg, "[%s] events channel full, dropping MsgPlayerDisconnected conn=%d",
				c.ID, msg.Disconnect.ConnID)
		}
	}
}
