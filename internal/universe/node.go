package universe

import (
	"context"
	"log"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/system"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// Node is a self-contained game simulation owning one sector.
type Node struct {
	ID        string
	Sector    coords.SectorCoord
	Engine    *engine.Engine
	World     *game.GameWorld
	Loop      *engine.GameLoop

	Inbox     chan NodeMessage
	Events    chan net.PlayerEvent
	Neighbors map[string]*Node

	// ReplicaNetIDs maps source network ID → local ECS entity for border replicas.
	ReplicaNetIDs map[uint32]ecs.Entity
}

// NewNode creates a node for the given sector with its own ECS world and systems.
func NewNode(
	sector coords.SectorCoord,
	platformCfg engine.Config,
	gameCfg game.GameConfig,
	connMgr *net.ConnManager,
	playerDB *game.PlayerRepo,
	gameLog *logger.Logger,
) *Node {
	id := SectorID(sector)

	eng := engine.New(platformCfg, connMgr, gameLog)
	grid := spatial.NewGrid(gameCfg.GridCellSize)
	gw := game.NewGameWorld(eng, gameCfg, playerDB, grid, component.SectorCoord{
		SX: sector.SX,
		SY: sector.SY,
	})
	gw.NodeID = id
	game.InitDropTables()

	systems := []engine.System{
		system.NewInputSystem(gw),
		system.NewDockingSystem(gw),
		system.NewTargetLockSystem(gw),
		system.NewShipControlSystem(gw),
		system.NewMiningSystem(gw),
		system.NewEconomySystem(gw),
		system.NewEquipmentSystem(gw),
		system.NewAbilitySystem(gw),
		system.NewStatusEffectSystem(gw),
		system.NewPhysicsSystem(gw),
		system.NewSectorBoundarySystem(gw),
		system.NewLifetimeSystem(gw),
		system.NewSpatialSystem(gw),
		system.NewCollisionSystem(gw),
		system.NewShieldRegenSystem(gw),
		system.NewNetworkSystem(gw),
	}
	sysNames := []string{
		"Input", "Docking", "TargetLock", "ShipControl", "Mining",
		"Economy", "Equipment", "Ability", "StatusEffect", "Physics",
		"SectorBoundary", "Lifetime", "Spatial", "Collision", "ShieldRegen", "Network",
	}

	gameLoop := engine.NewGameLoop(eng, systems, sysNames, gw.Hooks())

	// Per-node events channel — Coordinator fans out ConnManager events here
	events := make(chan net.PlayerEvent, 64)
	gameLoop.SetEventsCh(events)

	return &Node{
		ID:            id,
		Sector:        sector,
		Engine:        eng,
		World:         gw,
		Loop:          gameLoop,
		Inbox:         make(chan NodeMessage, 256),
		Events:        events,
		Neighbors:     make(map[string]*Node),
		ReplicaNetIDs: make(map[uint32]ecs.Entity),
	}
}

// Run starts the node's game loop. Blocks until context is cancelled.
func (n *Node) Run(ctx context.Context) {
	log.Printf("[%s] node started for sector (%d,%d)", n.ID, n.Sector.SX, n.Sector.SY)
	n.Loop.Run(ctx)
}

// Shutdown saves all player state on this node.
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
			// Process ghost and transfer cooldown TTLs
			n.tickGhosts()
			n.tickTransferCooldowns()
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
		p := msg.Transfer
		n.World.Log.Log(game.CatTransfer, "inbox: received transfer netID=%d type=%d from=%s conn=%d username=%s",
			p.NetworkID, p.EntityType, msg.FromNodeID, p.ConnID, p.Username)

		// Remove any existing replica with the same NetworkID (the entity was
		// being replicated across the border before the transfer happened).
		// Use direct ECS removal (not MarkForRemoval) to prevent the netID
		// from appearing in RemovedNetIDs and to avoid duplicate netIDs in
		// the spatial grid during this tick.
		if replicaEntity, ok := n.ReplicaNetIDs[p.NetworkID]; ok {
			if n.World.ECS.Alive(replicaEntity) {
				n.World.ECS.RemoveEntity(replicaEntity)
				n.World.Log.Log(game.CatTransfer, "removed pre-existing replica netID=%d before transfer spawn", p.NetworkID)
			}
			delete(n.ReplicaNetIDs, p.NetworkID)
		}

		entity := n.World.SpawnFromTransfer(p)
		if entity == (ecs.Entity{}) {
			return
		}

		// Send arrival confirmation back to source node
		if n.World.SendArrivalConfirm != nil {
			n.World.SendArrivalConfirm(msg.FromNodeID, &game.ArrivalConfirmMsg{
				NetworkID: p.NetworkID,
				ConnID:    p.ConnID,
			})
		}

	case MsgReplica:
		if len(msg.Replicas) > 0 {
			ApplyReplicas(n, msg.Replicas, msg.FromNodeID)
		}

	case MsgArrivalConfirm:
		if msg.ArrivalConfirm == nil {
			return
		}
		confirm := msg.ArrivalConfirm
		n.World.Log.Log(game.CatTransfer, "inbox: arrival confirmed netID=%d conn=%d from=%s",
			confirm.NetworkID, confirm.ConnID, msg.FromNodeID)

		// Find and remove the ghost entity by NetworkID
		n.removeGhostByNetID(confirm.NetworkID)

	case MsgChat:
		if msg.Chat == nil {
			return
		}
		n.World.Log.Log(game.CatChat, "inbox: relayed chat from=%s <%s> %s",
			msg.FromNodeID, msg.Chat.Username, msg.Chat.Text)
		n.World.PendingChat = append(n.World.PendingChat, &gamepb.ChatMsg{
			Username: msg.Chat.Username,
			Text:     msg.Chat.Text,
		})

	case MsgRespawnTransfer:
		if msg.Respawn == nil {
			return
		}
		r := msg.Respawn
		n.World.Log.Log(game.CatConnect, "inbox: respawn transfer conn=%d username=%s from=%s",
			r.ConnID, r.Username, msg.FromNodeID)
		n.World.ConnToUsername[r.ConnID] = r.Username
		n.World.PendingLogins[r.ConnID] = r.Username
	}
}

// removeGhostByNetID finds a ghost entity with the given network ID and removes it.
func (n *Node) removeGhostByNetID(netID uint32) {
	filter := ecs.NewFilter2[component.NetworkID, component.Ghost](n.World.ECS)
	query := filter.Query()
	for query.Next() {
		nid, _ := query.Get()
		if nid.ID == netID {
			entity := query.Entity()
			query.Close()
			n.World.MarkForRemoval(entity)
			n.World.Log.Log(game.CatTransfer, "ghost removed: netID=%d (arrival confirmed)", netID)
			return
		}
	}
}

// tickGhosts decrements ghost TTLs and removes expired ghosts.
func (n *Node) tickGhosts() {
	filter := ecs.NewFilter1[component.Ghost](n.World.ECS)
	var expired []ecs.Entity
	query := filter.Query()
	for query.Next() {
		ghost := query.Get()
		ghost.TTL--
		if ghost.TTL <= 0 {
			expired = append(expired, query.Entity())
		}
	}
	for _, e := range expired {
		if n.World.ECS.Alive(e) {
			netID := uint32(0)
			if n.World.NetworkIDMap.HasAll(e) {
				netID = n.World.NetworkIDMap.Get(e).ID
			}
			n.World.MarkForRemoval(e)
			n.World.Log.Log(game.CatTransfer, "ghost expired: netID=%d (TTL reached 0)", netID)
		}
	}
}

// tickTransferCooldowns decrements transfer cooldown timers and removes expired ones.
func (n *Node) tickTransferCooldowns() {
	filter := ecs.NewFilter1[component.TransferCooldown](n.World.ECS)
	var expired []ecs.Entity
	query := filter.Query()
	for query.Next() {
		tc := query.Get()
		tc.Remaining--
		if tc.Remaining <= 0 {
			expired = append(expired, query.Entity())
		}
	}
	for _, e := range expired {
		if n.World.ECS.Alive(e) {
			n.World.TransferCooldownMap.Remove(e)
		}
	}
}
