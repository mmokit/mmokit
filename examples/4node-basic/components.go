package main

// PlayerName stores a player's display name (replicated to other nodes).
type PlayerName struct {
	Name string `net:"initial"`
}

// DebugInfo holds per-entity debug/visualization state replicated to clients.
// This demonstrates extending AutoReplicator with game-specific attributes.
type DebugInfo struct {
	State     uint8 `net:"auto"` // 0=local, 1=replica, 2=ghost
	OwnerNode uint8 `net:"auto"` // root node index (cellY * gridW + cellX)
}

const (
	EntityLocal   uint8 = 0
	EntityReplica uint8 = 1
	EntityGhost   uint8 = 2
)
