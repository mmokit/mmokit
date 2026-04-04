package main

// PlayerName stores a player's display name (replicated to other nodes).
type PlayerName struct {
	Name string `net:"initial"`
}

// DebugInfo holds per-entity game-specific debug state replicated to clients.
type DebugInfo struct {
	AoIRadius float32 `net:"f32"` // server's current AoI radius (for debug overlay)
}
