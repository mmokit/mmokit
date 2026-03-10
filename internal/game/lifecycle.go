package game

import (
	"strings"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/gameserver/gen/go"
	"github.com/zenion/gameserver/pkg/logger"
)

func (gw *GameWorld) onConnect(connID uint32) {
	gw.Log.Log(logger.CatConnect, "player connected: conn=%d (awaiting login)", connID)
	gw.PendingConnections[connID] = true
}

func (gw *GameWorld) onDisconnect(connID uint32) {
	gw.Log.Log(logger.CatConnect, "player disconnected: conn=%d", connID)
	// Save player state before removing entity
	if entity, ok := gw.PlayerEntities[connID]; ok {
		if gw.ECS.Alive(entity) {
			gw.SavePlayerState(connID, entity)
			if gw.NetworkIDMap.HasAll(entity) {
				gw.RemovedNetIDs = append(gw.RemovedNetIDs, gw.NetworkIDMap.Get(entity).ID)
			}
			gw.ECS.RemoveEntity(entity)
		}
		delete(gw.PlayerEntities, connID)
	}
	delete(gw.DeadPlayers, connID)
	delete(gw.PendingConnections, connID)
	delete(gw.ConnToUsername, connID)
	gw.updatePlayerCompletions()
}

func (gw *GameWorld) processLogins() {
	// Drain input from pending connections looking for login messages
	for connID := range gw.PendingConnections {
		msgs := gw.ConnMgr.DrainInput(connID)
		for _, data := range msgs {
			var msg gamepb.ClientMessage
			if err := proto.Unmarshal(data, &msg); err != nil {
				continue
			}
			if login, ok := msg.Msg.(*gamepb.ClientMessage_Login); ok {
				username := strings.ToLower(login.Login.Username)
				if username == "" {
					continue
				}
				// Reject if username already in use
				if gw.UsernameInUse(username) {
					gw.Log.Log(logger.CatConnect, "login rejected: conn=%d username=%s (already connected)", connID, username)
					reject := &gamepb.ServerMessage{
						Msg: &gamepb.ServerMessage_LoginRejected{
							LoginRejected: &gamepb.LoginRejectedMsg{
								Reason: "Username already connected",
							},
						},
					}
					if data, err := proto.Marshal(reject); err == nil {
						gw.ConnMgr.SendReliable(connID, data)
					}
					delete(gw.PendingConnections, connID)
					break
				}
				gw.ConnToUsername[connID] = username
				delete(gw.PendingConnections, connID)
				gw.Log.Log(logger.CatConnect, "player logged in: conn=%d username=%s", connID, username)
				gw.SpawnPlayer(connID)
				break
			}
		}
	}

	// Process logins for pending login requests (from PendingLogins map)
	for connID, username := range gw.PendingLogins {
		gw.ConnToUsername[connID] = username
		gw.SpawnPlayer(connID)
		delete(gw.PendingLogins, connID)
	}

	gw.updatePlayerCompletions()
}

func (gw *GameWorld) processDeaths() {
	for _, death := range gw.PendingDeaths {
		msg := &gamepb.ServerMessage{
			Msg: &gamepb.ServerMessage_PlayerDied{
				PlayerDied: &gamepb.PlayerDiedMsg{
					KillerId: death.KillerNetID,
				},
			},
		}
		if data, err := proto.Marshal(msg); err == nil {
			gw.ConnMgr.SendReliable(death.ConnID, data)
		}

		// Move player from active to dead
		delete(gw.PlayerEntities, death.ConnID)
		gw.DeadPlayers[death.ConnID] = true
	}
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
		gw.SpawnLootCrate(drop.X, drop.Y, drop.Resources)
	}
	gw.PendingLootDrops = gw.PendingLootDrops[:0]

	// Process respawn requests (after removals so new entities are clean)
	gw.processRespawns()
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
