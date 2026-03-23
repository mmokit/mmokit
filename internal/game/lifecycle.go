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
	gw.Players.PendingConnections[connID] = true
}

func (gw *GameWorld) onDisconnect(connID uint32) {
	gw.Log.Log(CatConnect, "player disconnected: conn=%d", connID)
	// Save player state before removing entity
	if entity, ok := gw.Players.Entities[connID]; ok {
		if gw.ECS.Alive(entity) {
			gw.SavePlayerState(connID, entity)
			gw.ECS.RemoveEntity(entity)
		}
	}
	gw.Players.Remove(connID)
	gw.updatePlayerCompletions()

	// Notify PlayerSessions (for operation router)
	if gw.PlayerSessions != nil {
		gw.PlayerSessions.Remove(connID)
	}
}

func (gw *GameWorld) processLogins() {
	// Drain input from pending connections looking for login messages
	for connID := range gw.Players.PendingConnections {
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
				if gw.Players.UsernameInUse(username) {
					gw.Log.Log(CatConnect, "login rejected: conn=%d username=%s (already connected)", connID, username)
					rejectData := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_LOGIN_REJECTED), &gamepb.LoginRejectedMsg{
						Reason: "Username already connected",
					})
					if rejectData != nil {
						gw.ConnMgr.SendReliable(connID, rejectData)
					}
					delete(gw.Players.PendingConnections, connID)
					break
				}
				gw.Players.Usernames[connID] = username
				delete(gw.Players.PendingConnections, connID)
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
	for connID, username := range gw.Players.PendingLogins {
		gw.Players.Usernames[connID] = username

		// Notify PlayerSessions (for operation router)
		if gw.PlayerSessions != nil {
			gw.PlayerSessions.Set(connID, username)
		}

		gw.SpawnPlayer(connID)
		delete(gw.Players.PendingLogins, connID)
	}

	gw.updatePlayerCompletions()
}

func (gw *GameWorld) processDeaths() {
	for _, death := range Drain[PlayerDeath](gw.Queue) {
		data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_PLAYER_DIED), &gamepb.PlayerDiedMsg{
			KillerId: death.KillerNetID,
		})
		if data != nil {
			gw.ConnMgr.SendReliable(death.ConnID, data)
		}

		// Move player from active to dead
		delete(gw.Players.Entities, death.ConnID)
		delete(gw.Players.Docking, death.ConnID) // cancel docking if killed mid-dock
		gw.Players.Dead[death.ConnID] = true
	}
}

func (gw *GameWorld) processDockCompletions() {
	for connID, ds := range gw.Players.Docking {
		if ds.Remaining > 0 {
			continue
		}

		entity, ok := gw.Players.Entities[connID]
		if !ok || !gw.ECS.Alive(entity) {
			delete(gw.Players.Docking, connID)
			continue
		}

		// Save player state at station position (so undock spawns at station)
		gw.SavePlayerState(connID, entity)
		if username, ok := gw.Players.Usernames[connID]; ok {
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
		delete(gw.Players.Entities, connID)
		delete(gw.Players.Docking, connID)
		gw.Players.Docked[connID] = true

		username := gw.Players.Usernames[connID]
		gw.Log.Log(CatDock, "player docked: conn=%d username=%s", connID, username)
	}
}

func (gw *GameWorld) processUndocks() {
	for _, req := range Drain[PendingUndockRequest](gw.Queue) {
		if !gw.Players.Docked[req.ConnID] {
			continue
		}
		if gw.ConnMgr.Get(req.ConnID) == nil {
			continue
		}

		delete(gw.Players.Docked, req.ConnID)
		gw.SpawnPlayer(req.ConnID)

		username := gw.Players.Usernames[req.ConnID]
		gw.Log.Log(CatDock, "player undocked: conn=%d username=%s", req.ConnID, username)
	}
}

func (gw *GameWorld) getNetID(entity ecs.Entity) (uint32, bool) {
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
	for _, drop := range Drain[PendingLootDrop](gw.Queue) {
		gw.SpawnLootCrate(drop.X, drop.Y, drop.Items)
	}

	// Process respawn requests (after removals so new entities are clean)
	gw.processRespawns()

	// Process undock requests (after removals so SpawnPlayer is clean)
	gw.processUndocks()
}

func (gw *GameWorld) processRespawns() {
	for _, req := range Drain[PendingRespawn](gw.Queue) {
		connID := req.ConnID
		if !gw.Players.Dead[connID] {
			continue
		}
		delete(gw.Players.Dead, connID)

		// Verify connection is still alive
		if gw.ConnMgr.Get(connID) == nil {
			continue
		}

		// In multi-node mode, players always respawn at the station on sector (0,0).
		// If this node is not the station node, transfer the respawn there.
		if gw.Sector.SX != 0 || gw.Sector.SY != 0 {
			username := gw.Players.Usernames[connID]
			gw.Log.Log(CatConnect, "respawn transfer: conn=%d username=%s -> station node", connID, username)
			gw.Bridge.RespawnTransfer(connID, username)
			// Clean up player from this node
			delete(gw.Players.Usernames, connID)
			delete(gw.Players.Entities, connID)
			continue
		}

		gw.SpawnPlayer(connID)
	}
}

func (gw *GameWorld) clearTickState() {
	// Drain inter-node inbox before clearing tick state
	gw.Bridge.PreTick()

	gw.Queue.ClearAll()
}

// updatePlayerCompletions refreshes the "players" completion list from connected usernames.
func (gw *GameWorld) updatePlayerCompletions() {
	if gw.console == nil {
		return
	}
	names := make([]string, 0, len(gw.Players.Usernames))
	for _, username := range gw.Players.Usernames {
		names = append(names, username)
	}
	gw.console.SetCompletions("players", names)
}
