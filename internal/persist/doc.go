// Package persist defines the space-game persistence interfaces.
//
// Distinct from pkg/persist (the engine persistence layer). The engine
// owns identity tables (engine.players, engine.admin_operators) via
// pkg/persist; this package owns space-game tables (space.player_state,
// space.market_orders, space.market_trades, space.config).
//
// Implementations live in internal/persist/postgres.
package persist
