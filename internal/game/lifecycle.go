package game

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func (gw *GameWorld) processDeaths() {
	for _, death := range mmokit.Drain[PlayerDeath](gw.Queue) {
		data := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_PLAYER_DIED), &enginepb.PlayerDiedMsg{
			KillerId: death.KillerNetID,
		})
		if data != nil {
			gw.eng.ConnMgr.SendReliable(death.ConnID, data)
		}

		// Move player from active to dead
		session := gw.Players.ByConnID(death.ConnID)
		if session != nil {
			gw.Players.Transition(session, StateDead)
		}
	}
}

func (gw *GameWorld) processDockCompletions() {
	var completed []*mmokit.PlayerSession
	gw.Players.ForEach(StateDocking, func(s *mmokit.PlayerSession) {
		ds, ok := s.Data.(*DockingState)
		if !ok || ds == nil {
			return
		}
		if ds.Remaining > 0 {
			return
		}
		completed = append(completed, s)
	})

	for _, s := range completed {
		ds := s.Data.(*DockingState)

		if !gw.eng.ECS.Alive(s.Entity) {
			s.Data = nil
			continue
		}

		// Save player state at station position (so undock spawns at station)
		gw.SavePlayerState(s)
		pdata := gw.PlayerDB.GetOrCreate(s.Username)
		pdata.X = ds.StationX
		pdata.Y = ds.StationY
		gw.PlayerDB.MarkDirty(s.Username)

		// Send docked confirmation
		data := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_DOCKED), &gamepb.DockedMsg{})
		if data != nil {
			gw.eng.ConnMgr.SendReliable(s.ConnID, data)
		}

		// Remove entity and move to docked state
		gw.MarkForRemoval(s.Entity)
		s.Entity = ecs.Entity{}
		s.Data = nil
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

	// Bridge.PreTick() is called by the Coordinator's merged hooks after
	// ClearTickState, ensuring inter-node messages survive into systems.
}

// hasStation returns true if this node has a station entity.
func (gw *GameWorld) hasStation() bool {
	filter := ecs.NewFilter1[gamecomp.Station](gw.eng.ECS)
	query := filter.Query()
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
