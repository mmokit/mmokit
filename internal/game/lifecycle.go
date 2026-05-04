package game

import (
	"math/rand/v2"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

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

		if !gw.eng.ECS.Alive(s.Entity) {
			delete(gw.dockingStates, s.Username)
			continue
		}

		// Save player state (copies entity Inventory/Equipment into pdata so
		// the bank UI can manipulate pdata.Cargo while docked). Position is
		// also saved at station coords so an offline-recovered session
		// respawns at the station.
		gw.SavePlayerState(s)
		pdata := gw.PlayerDB.GetOrCreate(s.Username)
		pdata.X = ds.StationX
		pdata.Y = ds.StationY
		gw.PlayerDB.MarkDirty(s.Username)

		// Park the entity at station center, zero out motion, and mark
		// Dormant. Dormant excludes the entity from AoI broadcasts (other
		// pilots see the ship vanish into the station) AND from border
		// scans, while keeping it in the spatial grid AND keeping the
		// session as a viewer in PlayerViewerSource. The viewer-ness is
		// what keeps WorldUpdateMsg flowing — tick counter, chat, and
		// other ships' AoI deltas continue to reach the docked player so
		// they "see" the world from the station hangar window.
		entity := s.Entity
		if gw.C.Position.HasAll(entity) {
			pos := gw.C.Position.Get(entity)
			pos.X = ds.StationX
			pos.Y = ds.StationY
		}
		if gw.C.Velocity.HasAll(entity) {
			vel := gw.C.Velocity.Get(entity)
			vel.X = 0
			vel.Y = 0
		}
		if gw.C.MoveTarget.HasAll(entity) {
			gw.C.MoveTarget.Get(entity).Active = false
		}
		if !gw.C.Dormant.HasAll(entity) {
			gw.C.Dormant.Add(entity, &mmokit.Dormant{})
		}

		// Notify the client AFTER server-side state is fully consistent.
		gw.ServerEvents().Send(gw.eng.ConnMgr, s.ConnID, uint32(gamepb.GameServerEventCode_GSE_DOCKED), &gamepb.DockedMsg{})

		delete(gw.dockingStates, s.Username)
		gw.Players.Transition(s, StateDocked)

		gw.eng.Log.Log(CatPlayerDock, "player docked: conn=%d username=%s", s.ConnID, s.Username)
	}
}

func (gw *GameWorld) processUndocks() {
	for _, req := range mmokit.Drain[PendingUndockRequest](gw.Queue) {
		s := gw.Players.ByConnID(req.ConnID)
		if s == nil || s.State != StateDocked {
			continue
		}

		// Wake the entity in place: remove Dormant so AoI broadcasts pick it
		// up again (the ship "reappears" to other pilots), nudge position
		// off station center so the player isn't stuck inside the
		// station's collider, and sync pdata.Cargo back into the entity's
		// Inventory (bank deposits/withdrawals while docked mutate pdata,
		// not the entity directly).
		entity := s.Entity
		if !gw.eng.ECS.Alive(entity) {
			gw.eng.Log.Log(CatPlayerDock, "undock skipped: entity gone for conn=%d username=%s — falling back to spawn", req.ConnID, s.Username)
			gw.Players.Transition(s, mmokit.StateActive)
			continue
		}
		if gw.C.Dormant.HasAll(entity) {
			gw.C.Dormant.Remove(entity)
		}

		// Sync pdata.Cargo (which the bank UI mutates while docked) back
		// into the entity's Inventory so the in-space ship reflects what
		// the player did at the station.
		pdata := gw.PlayerDB.GetOrCreate(s.Username)
		if gw.C.Inventory.HasAll(entity) {
			inv := gw.C.Inventory.Get(entity)
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
		if gw.C.Position.HasAll(entity) {
			pos := gw.C.Position.Get(entity)
			// Pull the saved station coords as the anchor.
			pos.X = pdata.X + (rand.Float32()-0.5)*16.7
			pos.Y = pdata.Y + (rand.Float32()-0.5)*16.7
		}
		if gw.C.Velocity.HasAll(entity) {
			vel := gw.C.Velocity.Get(entity)
			vel.X = 0
			vel.Y = 0
		}

		gw.Players.Transition(s, mmokit.StateActive)

		gw.eng.Log.Log(CatPlayerDock, "player undocked: conn=%d username=%s", s.ConnID, s.Username)
	}
}

func (gw *GameWorld) GetNetID(entity ecs.Entity) (uint32, bool) {
	// Ghost and Replica removals are silent — don't generate kill notifications
	if gw.C.Ghost.HasAll(entity) || gw.C.Replica.HasAll(entity) {
		return 0, false
	}
	if gw.C.NetworkID.HasAll(entity) {
		return gw.C.NetworkID.Get(entity).ID, true
	}
	return 0, false
}

func (gw *GameWorld) postFlush() {
	// Spawn loot crates from deaths that occurred this tick
	for _, drop := range mmokit.Drain[PendingLootDrop](gw.Queue) {
		gw.SpawnLootCrate(drop.X, drop.Y, drop.Items)
	}

	// Process respawn requests (after removals so new entities are clean)
	gw.processRespawns()

	// Process undock requests (after removals so SpawnPlayer is clean)
	gw.processUndocks()
}

func (gw *GameWorld) processRespawns() {
	for _, req := range mmokit.Drain[PendingRespawn](gw.Queue) {
		connID := req.ConnID
		s := gw.Players.ByConnID(connID)
		if s == nil || s.State != StateDead {
			continue
		}

		// In multi-node mode, players always respawn at the station cell.
		// If this node doesn't have a station, transfer the respawn there.
		if !gw.hasStation() {
			gw.eng.Log.Log(CatPlayerConnect, "respawn transfer: conn=%d username=%s -> station node", connID, s.Username)
			gw.Bridge().RequestRespawn(connID, s.Username)
			// Clean up player from this node
			gw.Players.Transition(s, mmokit.StateTransferring)
			gw.Players.Remove(s)
			continue
		}

		gw.Players.Transition(s, mmokit.StateActive)
	}
}

func (gw *GameWorld) clearTickState() {
	gw.Queue.ClearAll()

	// Bridge.PreTick() is called by the Process's merged hooks after
	// ClearTickState, ensuring inter-node messages survive into systems.
}

// hasStation returns true if this node has a station entity.
//
// MUST close the query in all paths — ark v0.7.1 holds a world write-lock
// for the duration of an open query, and a leaked lock causes the next
// write-side operation (e.g. ECS.RemoveEntity in any later removal path)
// to panic with "cannot modify a locked world". A previous version returned
// query.Next() directly without closing, which leaked the lock forever
// any time the filter matched.
func (gw *GameWorld) hasStation() bool {
	filter := ecs.NewFilter1[gamecomp.Station](gw.eng.ECS)
	query := filter.Query()
	defer query.Close()
	return query.Next()
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
