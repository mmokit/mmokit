package game

// Game-specific log categories for debug logging.
// Uses hierarchical "group:sub" naming for organized console output.
const (
	// combat:*
	CatCombatHit     = "combat:hit"     // damage events
	CatCombatKill    = "combat:kill"    // kill events, currency rewards
	CatCombatAbility = "combat:ability" // ability activation and effects

	// economy:*
	CatEconomyBank   = "economy:bank"   // bank deposit/withdraw
	CatEconomyLoot   = "economy:loot"   // loot drops and pickups
	CatEconomyMarket = "economy:market" // marketplace orders and trades
	CatEconomyMining = "economy:mining" // mining extraction and jettison

	// player:*
	CatPlayerConnect = "player:connect" // login/disconnect/reject
	CatPlayerSpawn   = "player:spawn"   // entity spawning
	CatPlayerDock    = "player:dock"    // dock/undock
	CatPlayerEquip   = "player:equip"   // equipment slot changes
	CatPlayerInput   = "player:input"   // input routing

	// world:*
	CatWorldCollision = "world:collision" // physics collisions, terrain bounce
	CatWorldMap       = "world:map"       // spatial/map events
	CatWorldNetwork   = "world:network"   // game-level network frame events
	CatWorldTransfer  = "world:transfer"  // game-level transfer lifecycle
	CatWorldReplica   = "world:replica"   // game-level replica events

	// persist:* — database persistence. Off by default; opt in via `on persist`.
	CatPersistFlush = "persist:flush" // periodic dirty-player flushes (chatty)

	// npcai — NPC AI state-machine transitions, acquire/engage/leash events.
	CatNPCAI = "npcai"

	// poi — POI lifecycle (spawn, roster tracking, clear/cooldown/respawn).
	CatPOI = "poi"
)

// GameCategories lists every game-specific log category.
var GameCategories = []string{
	CatCombatHit, CatCombatKill, CatCombatAbility,
	CatEconomyBank, CatEconomyLoot, CatEconomyMarket, CatEconomyMining,
	CatPlayerConnect, CatPlayerSpawn, CatPlayerDock, CatPlayerEquip, CatPlayerInput,
	CatWorldCollision, CatWorldMap, CatWorldNetwork, CatWorldTransfer, CatWorldReplica,
	CatPersistFlush,
	CatNPCAI,
	CatPOI,
}
