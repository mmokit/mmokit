package game

// Game-specific log categories for debug logging.
const (
	CatCombat    = "combat"
	CatKill      = "kill"
	CatMining    = "mining"
	CatConnect   = "connect"
	CatSpawn     = "spawn"
	CatCollision = "collision"
	CatInput     = "input"
	CatEconomy   = "economy"
	CatChat      = "chat"
	CatEquip     = "equip"
	CatDock      = "dock"
	CatLoot      = "loot"
	CatMarket    = "market"
	CatNetwork   = "network"
	CatMap       = "map"
	CatTransfer  = "transfer"
	CatReplica   = "replica"
)

// GameCategories lists every game-specific log category.
var GameCategories = []string{
	CatCombat,
	CatKill,
	CatMining,
	CatConnect,
	CatSpawn,
	CatCollision,
	CatInput,
	CatEconomy,
	CatChat,
	CatEquip,
	CatDock,
	CatLoot,
	CatMarket,
	CatNetwork,
	CatMap,
	CatTransfer,
	CatReplica,
}
