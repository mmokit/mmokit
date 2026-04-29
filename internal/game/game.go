package game

import (
	"context"
	"log"
	"time"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Package-level custom player states.
var (
	StateDead    mmokit.PlayerState
	StateDocking mmokit.PlayerState
	StateDocked  mmokit.PlayerState
)

// NewGameWorld creates a new game world backed by the given Stage.
// The cfg pointer is shared across all GameWorlds in the coordinator so that
// runtime `config set` mutations propagate to every node at once.
func NewGameWorld(base *mmokit.Stage, cfg *GameConfig, playerDB *PlayerRepo, cell mmokit.CellCoord, fromSplit bool) *GameWorld {
	eng := base.Engine()
	item.Init()
	ecsWorld := eng.ECS

	gw := &GameWorld{
		Stage:     base,
		eng:           eng,
		Spatial:       base.SpatialGrid(),
		Config:        cfg,
		Queue:         mmokit.NewTickQueue(),
		NetIDToEntity: make(map[uint32]ecs.Entity),
		PlayerDB:      playerDB,
		SideEffects:   &mmokit.SideEffectCollector{},
	}
	gw.Players = eng.Players

	// Register custom player states
	StateDead = gw.Players.RegisterState("dead")
	StateDocking = gw.Players.RegisterState("docking")
	StateDocked = gw.Players.RegisterState("docked")
	// removeFromWorld saves and removes the player's ECS entity.
	// Used by transitions where the player permanently leaves the world.
	// If the entity has a Ghost component (transfer in progress), skip removal —
	// the ghost lingers for visual continuity until the replica arrives.
	ghostCheck := ecs.NewMap1[mmokit.Ghost](ecsWorld)
	removeFromWorld := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		if gw.eng.ECS.Alive(s.Entity) {
			if ghostCheck.HasAll(s.Entity) {
				// Transfer ghost — don't remove, let TTL expire.
				// The ghost lingers for visual continuity until the replica arrives.
				s.Entity = ecs.Entity{}
				if gw.PlayerSessions != nil {
					gw.PlayerSessions.Remove(s.ConnID)
				}
				gw.updatePlayerCompletions()
				return
			}
			gw.SavePlayerState(s)
			gw.Spatial.Deregister(s.Entity)
			gw.eng.ECS.RemoveEntity(s.Entity)
		}
		s.Entity = ecs.Entity{}
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Remove(s.ConnID)
		}
		gw.updatePlayerCompletions()
	}

	// disconnectKeepEntity preserves the entity during grace period so the
	// player can reconnect to the same ship. Entity cleanup happens in
	// StateDisconnected.OnExit when the grace period expires.
	disconnectKeepEntity := func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
		gw.SavePlayerState(s)
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Remove(s.ConnID)
		}
	}

	gw.Players.SetGracePeriod(time.Duration(cfg.DisconnectGracePeriod * float32(time.Second)))
	gw.Players.AddTransitions([]mmokit.StateTransition{
		{From: mmokit.StateActive, To: StateDocking},                                           // entity persists
		{From: mmokit.StateActive, To: StateDead, Action: removeFromWorld},                     // entity removed
		{From: mmokit.StateActive, To: mmokit.StateTransferring, Action: removeFromWorld},      // entity removed
		{From: mmokit.StateActive, To: mmokit.StateDisconnected, Action: disconnectKeepEntity}, // entity persists for reconnect
		{From: StateDead, To: mmokit.StateActive},                                              // respawn
		{From: StateDead, To: mmokit.StateDisconnected},                                        // disconnect while dead
		{From: mmokit.StateDisconnected, To: StateDead},                                        // reconnect resumes dead state
		{From: StateDocking, To: StateDocked},
		{From: StateDocking, To: StateDead, Action: removeFromWorld},
		{From: StateDocked, To: mmokit.StateActive},
		{From: StateDocked, To: mmokit.StateDisconnected, Action: disconnectKeepEntity},
	})

	// StateActive spawn / reconnect hook is installed via coord.OnPlayerJoin
	// in factory.registerPlayerJoin. The Process-level API is canonical;
	// registering directly on gw.Players.OnState(StateActive) here would be
	// silently overwritten by the universe-side callback wired in createNode.

	// When grace period expires (or session is removed while Disconnected),
	// clean up the entity that was kept alive for potential reconnection.
	// On reconnect, ConnID is restored before Transition — preserve the entity.
	gw.Players.OnState(mmokit.StateDisconnected, mmokit.StateCallbacks{
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.ConnID != 0 {
				// Reconnecting — keep entity alive for reuse in StateActive.OnEnter
				gw.updatePlayerCompletions()
				return
			}
			// Grace period expired — clean up
			if gw.eng.ECS.Alive(s.Entity) {
				gw.SavePlayerState(s)
				gw.Spatial.Deregister(s.Entity)
				gw.eng.ECS.RemoveEntity(s.Entity)
			}
			s.Entity = ecs.Entity{}
			gw.updatePlayerCompletions()
		},
	})

	gw.RootCell = cell
	gw.flushTicks = uint32(gw.Config.PersistFlushInterval * float32(eng.Config.TickRate))
	gw.FullRefreshInterval = uint32(eng.Config.TickRate)

	// Component mappers (must be created before initEntityKinds which uses them)
	gw.C = NewComponents(ecsWorld)

	// Initialize entity kinds (transfer replication + component auto-fill) and
	// entity registry (admin commands)
	gw.Registry = mmokit.NewEntityRegistry()
	gw.initEntityKinds()

	// Spawn initial content for this cell (skip for split-created worlds —
	// entities arrive via transfer from the parent cell)
	if !fromSplit {
		gw.spawnAsteroids()
		if cell == cfg.StationCell {
			gw.SpawnStation()
		}
	}

	return gw
}

// Hooks returns the engine lifecycle hooks wired to this game world.
// OnConnect and OnDisconnect are handled by PlayerManager; login processing is engine-internal.
func (gw *GameWorld) Hooks() mmokit.Hooks {
	return mmokit.Hooks{
		PreFlush: func() {
			gw.processDeaths()
			gw.processDockCompletions()
		},
		PostFlush:      gw.postFlush,
		ClearTickState: gw.clearTickState,
		PostTick:       gw.postTick,
	}
}

// Init is called by the Process after all nodes are created and bridges are wired.
// It sets up replication, transfer hooks, and post-spawn callbacks.
func (gw *GameWorld) Init() {
	gw.SetOnTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
		gw.FinishTransferSpawn(entity, frame)
	})

	gw.SetOnPlayerTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
		if s := gw.eng.Players.ByConnID(frame.ConnID); s != nil {
			gw.WireTransferPlayer(entity, s)
		}
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Set(frame.ConnID, frame.Username)
		}

		// Topology-transparent protocol: no SE_CELL_CHANGE is sent. The
		// destination cell's ReplicationSystem will set the
		// FRAME_FLAG_FRESH_SNAPSHOT bit on its first frame to this conn,
		// causing the client's decoder to reset baselines and repopulate
		// from the frame's Entered list — exactly like Valve Source's
		// cl_fullupdate or Gaffer's "encoded relative to initial state"
		// pattern. Clients never learn about cells, authority transfers,
		// or server boundaries.
		gw.ServerEvents().Send(gw.eng.ConnMgr, frame.ConnID, uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
			Stations: gw.CollectStationMapData(),
		})
		// Topology / debug overlay is pushed reactively by the
		// mmokit.NewDebugBroadcaster system (added in GameSetup) to any
		// player whose DebugFlags carry the topology bit. No explicit
		// per-connect send needed.
	})

	// OnPostSpawn is no longer needed for topology — see comment above.
	gw.OnPostSpawn = nil
}

// postTick runs after each tick — periodic saves.
// Bridge.PostSystems() is called by the Process's merged hooks.
//
// Snapshots every active player's live ECS state (position, cell, cargo,
// equipment) into the PlayerRepo on each flush tick so an ungraceful crash
// loses at most PersistFlushInterval seconds of gameplay. Without this,
// SavePlayerState is only called on state transitions (disconnect, death,
// dock, transfer, shutdown), so normal gameplay leaves positions stale in
// the DB until the next transition. StateDocked sessions have no live
// entity and their inventory/currency mutations already MarkDirty directly,
// so they piggyback on FlushDirty without needing iteration here.
func (gw *GameWorld) postTick() {
	if gw.flushTicks > 0 && gw.eng.Tick%gw.flushTicks == 0 {
		gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
			if gw.eng.ECS.Alive(s.Entity) {
				gw.SavePlayerState(s)
			}
		})
		n, err := gw.PlayerDB.FlushDirty(context.Background())
		if err != nil {
			gw.eng.Log.Log(CatPersistFlush, "flush error: %v", err)
		}
		if n > 0 {
			gw.eng.Log.Log(CatPersistFlush, "flushed %d dirty players", n)
		}
	}
}

// Shutdown saves all connected players and flushes dirty data.
// Call after the game loop has stopped.
func (gw *GameWorld) Shutdown() {
	gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
		if gw.eng.ECS.Alive(s.Entity) {
			gw.SavePlayerState(s)
		}
	})
	n, err := gw.PlayerDB.FlushDirty(context.Background())
	if err != nil {
		log.Printf("shutdown: flush error: %v", err)
	}
	log.Printf("shutdown: saved %d players", n)
}

// DispatchChat handles a chat message relayed from another node.
func (gw *GameWorld) DispatchChat(username, text string) {
	gw.eng.Log.Log(CatPlayerChat, "inbox: relayed chat <%s> %s", username, text)
	mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{
		Username: username,
		Text:     text,
	})
}

// HandleCrossCellAction processes a cross-cell action on the target node.
func (gw *GameWorld) HandleCrossCellAction(action *mmokit.CrossCellAction) *mmokit.ActionResult {
	var result *mmokit.ActionResult

	switch action.Type {
	case ActionDamage:
		dmg, err := UnmarshalDamageAction(action.Payload)
		if err != nil {
			gw.eng.Log.Log(CatCombatAbility, "cross-cell damage: bad payload from node=%s: %v", action.SourceCellID, err)
			return nil
		}

		target, ok := gw.NetIDToEntity[action.TargetNetID]
		if !ok || !gw.eng.ECS.Alive(target) {
			gw.eng.Log.Log(CatCombatAbility, "cross-cell damage: target netID=%d not found (from node=%s)",
				action.TargetNetID, action.SourceCellID)
			return nil
		}

		damage := dmg.Damage
		if dmg.BonusDamage > 0 && gw.C.Shield.HasAll(target) {
			shield := gw.C.Shield.Get(target)
			if shield.Current <= 0 {
				damage += dmg.BonusDamage
			}
		}

		dealt := gw.ApplyDamage(target, damage, action.SourceNetID)
		gw.eng.Log.Log(CatCombatAbility, "cross-cell damage: src=%d -> target=%d dmg=%.1f dealt=%.1f (from node=%s)",
			action.SourceNetID, action.TargetNetID, damage, dealt, action.SourceCellID)

		dead := false
		if gw.C.Health.HasAll(target) {
			dead = gw.C.Health.Get(target).Current <= 0
		}

		result = &mmokit.ActionResult{
			Type:        ActionDamage,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
			Payload: MarshalDamageResult(&DamageResult{
				DamageDealt: dealt,
				TargetDead:  dead,
				Slot:        dmg.Slot,
				AbilityType: dmg.AbilityType,
			}),
		}

	case ActionStatusEffect:
		se, err := UnmarshalStatusEffectAction(action.Payload)
		if err != nil {
			gw.eng.Log.Log(CatCombatAbility, "cross-cell status effect: bad payload from node=%s: %v", action.SourceCellID, err)
			return nil
		}

		target, ok := gw.NetIDToEntity[action.TargetNetID]
		if !ok || !gw.eng.ECS.Alive(target) {
			gw.eng.Log.Log(CatCombatAbility, "cross-cell status effect: target netID=%d not found (from node=%s)",
				action.TargetNetID, action.SourceCellID)
			return nil
		}

		if gw.C.StatusEffects.HasAll(target) {
			effects := gw.C.StatusEffects.Get(target)
			effects.Add(gamecomp.StatusEffect{
				Type:     gamecomp.StatusType(se.EffectType),
				Duration: se.Duration,
				Value:    se.Value,
			})
			gw.eng.Log.Log(CatCombatAbility, "cross-cell status effect: src=%d -> target=%d type=%d dur=%.1f val=%.1f (from node=%s)",
				action.SourceNetID, action.TargetNetID, se.EffectType, se.Duration, se.Value, action.SourceCellID)
		}

		result = &mmokit.ActionResult{
			Type:        ActionStatusEffect,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
		}

	case ActionMining:
		mining, err := UnmarshalMiningAction(action.Payload)
		if err != nil {
			gw.eng.Log.Log(CatEconomyMining, "cross-cell mining: bad payload from node=%s: %v", action.SourceCellID, err)
			return nil
		}

		target, ok := gw.NetIDToEntity[action.TargetNetID]
		if !ok || !gw.eng.ECS.Alive(target) {
			gw.eng.Log.Log(CatEconomyMining, "cross-cell mining: target netID=%d not found (from node=%s)",
				action.TargetNetID, action.SourceCellID)
			return nil
		}

		if !gw.C.Minable.HasAll(target) {
			gw.eng.Log.Log(CatEconomyMining, "cross-cell mining: target netID=%d not minable (from node=%s)",
				action.TargetNetID, action.SourceCellID)
			return nil
		}

		minable := gw.C.Minable.Get(target)
		extracted := mining.Amount
		if extracted > minable.Remaining {
			extracted = minable.Remaining
		}
		minable.Remaining -= extracted

		depleted := minable.Remaining <= 0
		if depleted {
			gw.MarkForRemoval(target)
			gw.eng.Log.Log(CatEconomyMining, "cross-cell mining: asteroid netID=%d depleted (from node=%s)",
				action.TargetNetID, action.SourceCellID)
		} else {
			gw.eng.Log.Log(CatEconomyMining, "cross-cell mining: src=%d -> target=%d extracted=%.1f remaining=%.1f (from node=%s)",
				action.SourceNetID, action.TargetNetID, extracted, minable.Remaining, action.SourceCellID)
		}

		result = &mmokit.ActionResult{
			Type:        ActionMining,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
			Payload:     MarshalMiningResult(&MiningResult{Extracted: extracted, Depleted: depleted}),
		}

	default:
		gw.eng.Log.Log(CatCombatAbility, "cross-cell action: unknown type=%d from node=%s", action.Type, action.SourceCellID)
		return nil
	}

	if result != nil {
		if sideEffects := gw.SideEffects.Drain(); len(sideEffects) > 0 {
			result.SideEffects = mmokit.MarshalSideEffects(sideEffects)
		}
	}

	return result
}

// HandleActionResult processes the result of a cross-cell action on the source node.
func (gw *GameWorld) HandleActionResult(result *mmokit.ActionResult) {
	if len(result.SideEffects) > 0 {
		effects, err := mmokit.UnmarshalSideEffects(result.SideEffects)
		if err != nil {
			gw.eng.Log.Log(CatCombatAbility, "cross-cell side effects: bad data: %v", err)
		} else {
			gw.sideEffectRegistry.Dispatch(result.SourceNetID, effects)
		}
	}

	switch result.Type {
	case ActionDamage:
		dmgResult, err := UnmarshalDamageResult(result.Payload)
		if err != nil {
			gw.eng.Log.Log(CatCombatAbility, "cross-cell damage result: bad payload: %v", err)
			return
		}

		mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
			Slot:        uint32(dmgResult.Slot),
			CasterId:    result.SourceNetID,
			TargetId:    result.TargetNetID,
			DamageDealt: dmgResult.DamageDealt,
			Success:     true,
			AbilityType: uint32(dmgResult.AbilityType),
		})

		if dmgResult.TargetDead {
			replicaNetIDs := gw.ReplicaNetIDs()
			if replicaEntity, ok := replicaNetIDs[result.TargetNetID]; ok {
				if gw.eng.ECS.Alive(replicaEntity) {
					gw.eng.ECS.RemoveEntity(replicaEntity)
				}
				delete(replicaNetIDs, result.TargetNetID)
			}
			gw.eng.RemovedNetIDs = append(gw.eng.RemovedNetIDs, result.TargetNetID)
		}

		gw.eng.Log.Log(CatCombatAbility, "cross-cell damage result: src=%d -> target=%d dealt=%.1f dead=%v",
			result.SourceNetID, result.TargetNetID, dmgResult.DamageDealt, dmgResult.TargetDead)

	case ActionStatusEffect:
		gw.eng.Log.Log(CatCombatAbility, "cross-cell status effect result: src=%d -> target=%d success=%v",
			result.SourceNetID, result.TargetNetID, result.Success)

	case ActionMining:
		miningResult, err := UnmarshalMiningResult(result.Payload)
		if err != nil {
			gw.eng.Log.Log(CatEconomyMining, "cross-cell mining result: bad payload: %v", err)
			return
		}

		if miningResult.Depleted {
			replicaNetIDs := gw.ReplicaNetIDs()
			if replicaEntity, ok := replicaNetIDs[result.TargetNetID]; ok {
				if gw.eng.ECS.Alive(replicaEntity) {
					gw.eng.ECS.RemoveEntity(replicaEntity)
				}
				delete(replicaNetIDs, result.TargetNetID)
			}
			gw.eng.RemovedNetIDs = append(gw.eng.RemovedNetIDs, result.TargetNetID)
		}

		gw.eng.Log.Log(CatEconomyMining, "cross-cell mining result: target=%d extracted=%.1f depleted=%v",
			result.TargetNetID, miningResult.Extracted, miningResult.Depleted)
	}
}

// UnwrapGameWorld extracts the underlying *GameWorld from a mmokit.GameWorld.
func UnwrapGameWorld(w mmokit.GameWorld) *GameWorld {
	return w.(*GameWorld)
}
