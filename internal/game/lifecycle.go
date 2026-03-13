package game

import (
	"strings"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/netutil"
)

func (gw *GameWorld) onConnect(connID uint32) {
	gw.Log.Log(CatConnect, "player connected: conn=%d (awaiting login)", connID)
	gw.PendingConnections[connID] = true
}

func (gw *GameWorld) onDisconnect(connID uint32) {
	gw.Log.Log(CatConnect, "player disconnected: conn=%d", connID)
	// Save player state before removing entity
	if entity, ok := gw.PlayerEntities[connID]; ok {
		if gw.ECS.Alive(entity) {
			gw.SavePlayerState(connID, entity)
			gw.ECS.RemoveEntity(entity)
		}
		delete(gw.PlayerEntities, connID)
	}
	delete(gw.DeadPlayers, connID)
	delete(gw.DockingPlayers, connID)
	delete(gw.DockedPlayers, connID)
	delete(gw.PendingConnections, connID)
	delete(gw.ConnToUsername, connID)
	gw.updatePlayerCompletions()

	// Notify PlayerSessions (for operation router)
	if gw.PlayerSessions != nil {
		gw.PlayerSessions.Remove(connID)
	}
}

func (gw *GameWorld) processLogins() {
	// Drain input from pending connections looking for login messages
	for connID := range gw.PendingConnections {
		msgs := gw.ConnMgr.DrainInput(connID)
		for _, data := range msgs {
			var evt gamepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				continue
			}
			if gamepb.ClientEventCode(evt.Code) == gamepb.ClientEventCode_CE_LOGIN {
				var login gamepb.LoginMsg
				if err := proto.Unmarshal(evt.Data, &login); err != nil {
					continue
				}
				username := strings.ToLower(login.Username)
				if username == "" {
					continue
				}
				// Reject if username already in use
				if gw.UsernameInUse(username) {
					gw.Log.Log(CatConnect, "login rejected: conn=%d username=%s (already connected)", connID, username)
					rejectData := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_LOGIN_REJECTED), &gamepb.LoginRejectedMsg{
						Reason: "Username already connected",
					})
					if rejectData != nil {
						gw.ConnMgr.SendReliable(connID, rejectData)
					}
					delete(gw.PendingConnections, connID)
					break
				}
				gw.ConnToUsername[connID] = username
				delete(gw.PendingConnections, connID)
				gw.Log.Log(CatConnect, "player logged in: conn=%d username=%s", connID, username)

				// Notify PlayerSessions (for operation router)
				if gw.PlayerSessions != nil {
					gw.PlayerSessions.Set(connID, username)
				}

				gw.SpawnPlayer(connID)
				break
			}
		}
	}

	// Process logins for pending login requests (from PendingLogins map)
	for connID, username := range gw.PendingLogins {
		gw.ConnToUsername[connID] = username

		// Notify PlayerSessions (for operation router)
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Set(connID, username)
		}

		gw.SpawnPlayer(connID)
		delete(gw.PendingLogins, connID)
	}

	gw.updatePlayerCompletions()
}

func (gw *GameWorld) processDeaths() {
	for _, death := range gw.PendingDeaths {
		data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_PLAYER_DIED), &gamepb.PlayerDiedMsg{
			KillerId: death.KillerNetID,
		})
		if data != nil {
			gw.ConnMgr.SendReliable(death.ConnID, data)
		}

		// Move player from active to dead
		delete(gw.PlayerEntities, death.ConnID)
		delete(gw.DockingPlayers, death.ConnID) // cancel docking if killed mid-dock
		gw.DeadPlayers[death.ConnID] = true
	}
}

func (gw *GameWorld) processDockCompletions() {
	for connID, ds := range gw.DockingPlayers {
		if ds.Remaining > 0 {
			continue
		}

		entity, ok := gw.PlayerEntities[connID]
		if !ok || !gw.ECS.Alive(entity) {
			delete(gw.DockingPlayers, connID)
			continue
		}

		// Save player state at station position (so undock spawns at station)
		gw.SavePlayerState(connID, entity)
		if username, ok := gw.ConnToUsername[connID]; ok {
			pdata := gw.PlayerDB.GetOrCreate(username)
			pdata.X = ds.StationX
			pdata.Y = ds.StationY
			gw.PlayerDB.MarkDirty(username)
		}

		// Send docked confirmation
		data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_DOCKED), &gamepb.DockedMsg{})
		if data != nil {
			gw.ConnMgr.SendReliable(connID, data)
		}

		// Remove entity and move to docked state
		gw.MarkForRemoval(entity)
		delete(gw.PlayerEntities, connID)
		delete(gw.DockingPlayers, connID)
		gw.DockedPlayers[connID] = true

		username := gw.ConnToUsername[connID]
		gw.Log.Log(CatDock, "player docked: conn=%d username=%s", connID, username)
	}
}

func (gw *GameWorld) processUndocks() {
	for _, req := range gw.PendingUndockRequests {
		if !gw.DockedPlayers[req.ConnID] {
			continue
		}
		if gw.ConnMgr.Get(req.ConnID) == nil {
			continue
		}

		delete(gw.DockedPlayers, req.ConnID)
		gw.SpawnPlayer(req.ConnID)

		username := gw.ConnToUsername[req.ConnID]
		gw.Log.Log(CatDock, "player undocked: conn=%d username=%s", req.ConnID, username)
	}
	gw.PendingUndockRequests = gw.PendingUndockRequests[:0]
}

func (gw *GameWorld) getNetID(entity ecs.Entity) (uint32, bool) {
	if gw.NetworkIDMap.HasAll(entity) {
		return gw.NetworkIDMap.Get(entity).ID, true
	}
	return 0, false
}

func (gw *GameWorld) postFlush() {
	// Spawn loot crates from deaths that occurred this tick
	for _, drop := range gw.PendingLootDrops {
		gw.SpawnLootCrate(drop.X, drop.Y, drop.Items)
	}
	gw.PendingLootDrops = gw.PendingLootDrops[:0]

	// Process respawn requests (after removals so new entities are clean)
	gw.processRespawns()

	// Process undock requests (after removals so SpawnPlayer is clean)
	gw.processUndocks()
}

func (gw *GameWorld) processRespawns() {
	for _, connID := range gw.PendingRespawns {
		if !gw.DeadPlayers[connID] {
			continue
		}
		delete(gw.DeadPlayers, connID)

		// Verify connection is still alive
		if gw.ConnMgr.Get(connID) == nil {
			continue
		}

		gw.SpawnPlayer(connID)
	}
	gw.PendingRespawns = gw.PendingRespawns[:0]
}

func (gw *GameWorld) clearTickState() {
	gw.PendingDeaths = gw.PendingDeaths[:0]
	gw.PendingTransfers = gw.PendingTransfers[:0]
	gw.PendingBankRequests = gw.PendingBankRequests[:0]
	gw.PendingSellRequests = gw.PendingSellRequests[:0]
	gw.PendingEquipRequests = gw.PendingEquipRequests[:0]
	gw.PendingShopBuys = gw.PendingShopBuys[:0]
	gw.PendingDockRequests = gw.PendingDockRequests[:0]
	gw.PendingUndockRequests = gw.PendingUndockRequests[:0]
	gw.PendingLootItems = gw.PendingLootItems[:0]
	gw.PendingLootAlls = gw.PendingLootAlls[:0]
}

// updatePlayerCompletions refreshes the "players" completion list from connected usernames.
func (gw *GameWorld) updatePlayerCompletions() {
	if gw.console == nil {
		return
	}
	names := make([]string, 0, len(gw.ConnToUsername))
	for _, username := range gw.ConnToUsername {
		names = append(names, username)
	}
	gw.console.SetCompletions("players", names)
}
