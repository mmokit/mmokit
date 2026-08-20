package game

import (
	"context"
	"log"
	"math/rand/v2"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
)

// Hooks returns the engine lifecycle hooks wired to this game world.
// OnConnect and OnDisconnect are handled by PlayerManager; login processing is engine-internal.
func (gw *GameWorld) Hooks() mmokit.Hooks {
	return mmokit.Hooks{
		PreFlush: func(float32) {
			gw.processDockCompletions()
		},
		PostFlush:      gw.postFlush,
		ClearTickState: gw.clearTickState,
		PostTick:       gw.postTick,
	}
}

// wireStageCallbacks installs the per-cell transfer-receive hooks on this
// GameWorld's stage. Called from NewGameWorld; once world.Init() was
// removed from the framework lifecycle (spec 2026-05-08-stage-on-systembase),
// this is the canonical place to register stage callbacks — there's no
// post-construction phase that fires for cell states.
//
// Without this, gw.FinishTransferSpawn never runs on real cross-cell
// handoffs and config-only fields on `mmokit:"local"` components stay
// zero after every transfer.
func (gw *GameWorld) wireStageCallbacks() {
	gw.stage.SetOnTransferReceived(func(entity mmokit.EntityHandle, frame *mmokit.TransferFrame) {
		gw.FinishTransferSpawn(entity, frame)
	})

	gw.stage.SetOnPlayerTransferReceived(func(entity mmokit.EntityHandle, frame *mmokit.TransferFrame) {
		if s := gw.eng.Players.ByConnID(frame.ConnID); s != nil {
			gw.WireTransferPlayer(entity, s)
		}
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Set(frame.ConnID, frame.Username)
		}

		// Topology-transparent protocol: no CellChange event is sent. The
		// destination cell's ReplicationSystem will set the
		// FRAME_FLAG_FRESH_SNAPSHOT bit on its first frame to this conn,
		// causing the client's decoder to reset baselines and repopulate
		// from the frame's Entered list — exactly like Valve Source's
		// cl_fullupdate or Gaffer's "encoded relative to initial state"
		// pattern. Clients never learn about cells, authority transfers,
		// or server boundaries.
		mmokit.SendEvent(gw.stage, frame.ConnID, &MapData{
			Stations: gw.CollectStationMapData(),
		})
		// Topology / debug overlay is pushed reactively by the
		// mmokit.NewDebugBroadcaster system (added in GameSetup) to any
		// player whose DebugFlags carry the topology bit. No explicit
		// per-connect send needed.
	})
}

// Shutdown saves all connected players and flushes dirty data.
// Call after the game loop has stopped.
func (gw *GameWorld) Shutdown() {
	gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
		if gw.stage.ECSWorld().Alive(s.Entity) {
			gw.SavePlayerState(s)
		}
	})
	n, err := gw.PlayerDB.FlushDirty(context.Background())
	if err != nil {
		log.Printf("shutdown: flush error: %v", err)
	}
	log.Printf("shutdown: saved %d players", n)
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
	// Auto-respawn timer: every dead session whose timer has elapsed
	// gets a deferred executeRespawnFor closure. The client's Respawn
	// input is dropped by the typed-input dispatcher when the player
	// entity is dead, so this is the only path.
	if len(gw.autoRespawnAt) > 0 {
		now := gw.eng.Tick
		for connID, deadline := range gw.autoRespawnAt {
			if now >= deadline {
				cid := connID
				gw.stage.Commands().Defer(func() {
					gw.executeRespawnFor(cid)
				})
				delete(gw.autoRespawnAt, connID)
			}
		}
	}

	if gw.flushTicks > 0 && gw.eng.Tick%gw.flushTicks == 0 {
		gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
			if gw.stage.ECSWorld().Alive(s.Entity) {
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

func (gw *GameWorld) processDockCompletions() {
	var completed []*mmokit.PlayerSession
	gw.Players.ForEach(StateDocking, func(s *mmokit.PlayerSession) {
		ds := gw.dockingStates[s.Username]
		if ds == nil {
			return
		}
		if ds.Remaining > 0 {
			return
		}
		completed = append(completed, s)
	})

	for _, s := range completed {
		ds := gw.dockingStates[s.Username]

		entity := mmokit.EntityFromECS(gw.stage, s.Entity)
		if !entity.Alive() {
			delete(gw.dockingStates, s.Username)
			continue
		}

		// Save player state (copies entity Inventory/Equipment into pdata so
		// the bank UI can manipulate pdata.Cargo while docked). Position is
		// also saved at station coords so an offline-recovered session
		// respawns at the station.
		gw.SavePlayerState(s)
		pdata := gw.PlayerDB.Bind(s)
		pdata.X = ds.StationX
		pdata.Y = ds.StationY
		gw.PlayerDB.MarkDirtyByUserID(pdata.UserID)

		// Park the entity at station center, zero out motion, and mark
		// Dormant. Dormant excludes the entity from AoI broadcasts (other
		// pilots see the ship vanish into the station) AND from border
		// scans, while keeping it in the spatial grid AND keeping the
		// session as a viewer in PlayerViewerSource. The viewer-ness is
		// what keeps WorldUpdateMsg flowing — tick counter and other
		// ships' AoI deltas continue to reach the docked player so
		// they "see" the world from the station hangar window.
		if pos := mmokit.Get[mmokit.Position](entity); pos != nil {
			pos.X = ds.StationX
			pos.Y = ds.StationY
		}
		if vel := mmokit.Get[mmokit.Velocity](entity); vel != nil {
			vel.X = 0
			vel.Y = 0
		}
		if mt := mmokit.Get[mmokit.MoveTarget](entity); mt != nil {
			mt.Active = false
		}
		if !mmokit.Has[mmokit.Dormant](entity) {
			mmokit.Set(entity, mmokit.Dormant{})
		}

		// Notify the client AFTER server-side state is fully consistent.
		mmokit.SendEvent(gw.stage, s.ConnID, &Docked{})

		delete(gw.dockingStates, s.Username)
		gw.Players.Transition(s, StateDocked)

		gw.eng.Log.Log(CatPlayerDock, "player docked: conn=%d username=%s", s.ConnID, s.Username)
	}
}

// startUndockingFor processes a single Undock request. Dispatched via
// Commands.Defer from the Undock input handler; runs at the next per-system
// flush boundary with the ECS world unlocked.
func (gw *GameWorld) startUndockingFor(connID uint32) {
	s := gw.Players.ByConnID(connID)
	if s == nil || s.State != StateDocked {
		return
	}

	// Wake the entity in place: remove Dormant so AoI broadcasts pick it
	// up again (the ship "reappears" to other pilots), nudge position
	// off station center so the player isn't stuck inside the
	// station's collider, and sync pdata.Cargo back into the entity's
	// Inventory (bank deposits/withdrawals while docked mutate pdata,
	// not the entity directly).
	entity := mmokit.EntityFromECS(gw.stage, s.Entity)
	if !entity.Alive() {
		gw.eng.Log.Log(CatPlayerDock, "undock skipped: entity gone for conn=%d username=%s — falling back to spawn", connID, s.Username)
		gw.Players.Transition(s, mmokit.StateActive)
		return
	}
	// Drop Dormant via Commands; the engine flushes between systems on
	// the next tick so the entity is reachable to AoI broadcasts from
	// that point on, preserving the prior semantics.
	mmokit.RemoveComponent[mmokit.Dormant](gw.stage.Commands(), entity)

	// Sync pdata.Cargo (which the bank UI mutates while docked) back
	// into the entity's Inventory so the in-space ship reflects what
	// the player did at the station.
	pdata := gw.PlayerDB.Bind(s)
	if inv := mmokit.Get[gamecomp.Inventory](entity); inv != nil {
		inv.Items = make(map[uint32]int32, len(pdata.Cargo))
		for id, qty := range pdata.Cargo {
			if qty > 0 {
				inv.Items[id] = qty
			}
		}
	}

	// Reposition slightly off the station center so the ship undocks
	// "next to" the station rather than embedded in it. Same jitter
	// the new-player spawn uses (~17 unit ring).
	if pos := mmokit.Get[mmokit.Position](entity); pos != nil {
		// Pull the saved station coords as the anchor.
		pos.X = pdata.X + (rand.Float32()-0.5)*16.7
		pos.Y = pdata.Y + (rand.Float32()-0.5)*16.7
	}
	if vel := mmokit.Get[mmokit.Velocity](entity); vel != nil {
		vel.X = 0
		vel.Y = 0
	}

	gw.Players.Transition(s, mmokit.StateActive)

	gw.eng.Log.Log(CatPlayerDock, "player undocked: conn=%d username=%s", s.ConnID, s.Username)
}

func (gw *GameWorld) GetNetID(entity mmokit.EntityHandle) (uint32, bool) {
	e := mmokit.EntityFromECS(gw.stage, entity)
	// Ghost and Replica removals are silent — don't generate kill notifications
	if mmokit.Has[mmokit.Ghost](e) || mmokit.Has[mmokit.Replica](e) {
		return 0, false
	}
	if id := e.NetID(); id != 0 {
		return id, true
	}
	return 0, false
}

func (gw *GameWorld) postFlush() {
	// Loot drops: scheduled via Commands.Defer from verb_death.go.
	// Respawn requests: scheduled via Commands.Defer from the Respawn
	// input handler and from the auto-respawn timer in postTick.
	// Undock requests: scheduled via Commands.Defer from the Undock
	// input handler.
	// All of the above fire at the next per-system flush boundary —
	// no drain here.
}

// executeRespawnFor processes a single respawn request. Dispatched via
// Commands.Defer from the Respawn input handler and from the auto-respawn
// timer in postTick; runs at the next per-system flush boundary with the
// ECS world unlocked.
func (gw *GameWorld) executeRespawnFor(connID uint32) {
	s := gw.Players.ByConnID(connID)
	if s == nil || s.State != StateDead {
		return
	}

	// In multi-node mode, players always respawn at the station cell.
	// If this node doesn't have a station, transfer the respawn there.
	if !gw.hasStation() {
		gw.eng.Log.Log(CatPlayerConnect, "respawn transfer: conn=%d username=%s -> station node", connID, s.Username)
		gw.stage.Bridge().RequestRespawn(connID, s.Username)
		// Clean up player from this node.
		gw.Players.Transition(s, mmokit.StateTransferring)
		gw.Players.Remove(s)
		return
	}

	gw.Players.Transition(s, mmokit.StateActive)
}

func (gw *GameWorld) clearTickState() {
	// No game-specific per-tick reset remaining — every Pending* queue
	// has been migrated to stage.Commands().Defer. Bridge.PreTick() is
	// called by the Process's merged hooks after ClearTickState,
	// ensuring inter-node messages survive into systems.
}

// hasStation returns true if this node has a station entity.
func (gw *GameWorld) hasStation() bool {
	return mmokit.Any[gamecomp.Station](gw.stage)
}

// updatePlayerCompletions refreshes the "players" completion list from connected usernames.
func (gw *GameWorld) updatePlayerCompletions() {
	if gw.console == nil {
		return
	}
	var names []string
	gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
		names = append(names, s.Username)
	})
	gw.Players.ForEach(StateDocking, func(s *mmokit.PlayerSession) {
		names = append(names, s.Username)
	})
	gw.Players.ForEach(StateDocked, func(s *mmokit.PlayerSession) {
		names = append(names, s.Username)
	})
	gw.Players.ForEach(StateDead, func(s *mmokit.PlayerSession) {
		names = append(names, s.Username)
	})
	gw.console.SetCompletions("players", names)
}
