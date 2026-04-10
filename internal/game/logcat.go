package game

// Game-specific log categories for debug logging.
// Uses hierarchical "group:sub" naming for organized console output.
const (
	// combat:*
	CatCombatHit     = "combat:hit"     // damage events
	CatCombatKill    = "combat:kill"    // kill events, currency rewards
	CatCombatLock    = "combat:lock"    // target lock acquire/break
	CatCombatAbility = "combat:ability" // ability activation and effects

	// economy:*
	CatEconomyBank   = "economy:bank"   // bank deposit/withdraw
	CatEconomyLoot   = "economy:loot"   // loot drops and pickups
	CatEconomyMarket = "economy:market" // marketplace orders and trades
	CatEconomyMining = "economy:mining" // mining extraction and jettison
	CatEconomyShop   = "economy:shop"   // buy/sell at stations

	// player:*
	CatPlayerConnect = "player:connect" // login/disconnect/reject
	CatPlayerSpawn   = "player:spawn"   // entity spawning
	CatPlayerDock    = "player:dock"    // dock/undock
	CatPlayerEquip   = "player:equip"   // equipment slot changes
	CatPlayerChat    = "player:chat"    // chat messages and relay
	CatPlayerInput   = "player:input"   // input routing

	// world:*
	CatWorldCollision = "world:collision" // physics collisions, terrain bounce
	CatWorldMap       = "world:map"       // spatial/map events
	CatWorldNetwork   = "world:network"   // game-level network frame events
	CatWorldTransfer  = "world:transfer"  // game-level transfer lifecycle
	CatWorldReplica   = "world:replica"   // game-level replica events

	// persist:* — database persistence. Off by default; opt in via `on persist`.
	CatPersistFlush = "persist:flush" // periodic dirty-player flushes (chatty)
)

// GameCategories lists every game-specific log category.
var GameCategories = []string{
	CatCombatHit, CatCombatKill, CatCombatLock, CatCombatAbility,
	CatEconomyBank, CatEconomyLoot, CatEconomyMarket, CatEconomyMining, CatEconomyShop,
	CatPlayerConnect, CatPlayerSpawn, CatPlayerDock, CatPlayerEquip, CatPlayerChat, CatPlayerInput,
	CatWorldCollision, CatWorldMap, CatWorldNetwork, CatWorldTransfer, CatWorldReplica,
	CatPersistFlush,
}
