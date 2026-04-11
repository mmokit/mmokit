// Package replication is the shared replication primitives library used by
// both the client-facing dispatcher (pkg/system/replication.go) and the
// inter-node border dispatcher (pkg/universe/border_replication.go).
package replication

// Viewer is anything that receives replicated entity frames: a player
// connection, or a neighbor node. The dispatcher is generic over viewer type.
type Viewer interface {
	// ID returns a stable identifier for this viewer. Player connection
	// IDs and neighbor node IDs both occupy the same uint64 namespace;
	// callers must ensure uniqueness across both.
	ID() uint64

	// Position returns world-space coordinates used for tier distance
	// checks. For a neighbor node, this is the midpoint of the shared
	// cell boundary.
	Position() (x, y float32)

	// Tier returns the per-entity-kind replication tier for this viewer.
	// The dispatcher consults this to decide whether to send an entity
	// and at what rate.
	Tier(entityKind uint16) ReplicationTier

	// Baselines returns the per-entity acknowledged-snapshot store for
	// this viewer. May return nil for viewers that don't use delta
	// compression (rare).
	Baselines() *BaselineStore

	// Send delivers a built frame to the viewer. Production in-process
	// implementations may retain the struct directly; network
	// implementations call frame.Encode() inside their own Send.
	Send(frame Frame)
}

// ReplicationTier configures replication behavior for one entity kind
// as seen by one viewer. Defaults apply when a game does not override.
type ReplicationTier struct {
	// Radius is the AoI cutoff. Entities beyond this distance from the
	// viewer are not replicated.
	Radius float32
	// UpdateDivisor: send every Nth tick. 1 = every tick.
	UpdateDivisor int
	// BaseWeight is the per-entity weight for the priority accumulator.
	BaseWeight float32
	// PromoteRadius: inside this distance from the neighbor's owned
	// cell, an entity is promoted to full-rate updates. Defaults to
	// Radius * 0.5.
	PromoteRadius float32
	// PromoteLookahead: ticks of velocity-projection lookahead. If the
	// entity's projected position at tick+PromoteLookahead lies in the
	// neighbor cell, promote now. Defaults to 10.
	PromoteLookahead int
}

// BaselineStore is a per-viewer per-entity acknowledged-snapshot store.
// Real implementation lands in Task 2.2. This stub exists only so the
// Viewer interface signature compiles during Phase 2 scaffolding.
type BaselineStore struct{}

// Frame is a batch of replication updates from one sender to one viewer.
// Real implementation lands in Task 2.3. This stub exists only so the
// Viewer interface signature compiles during Phase 2 scaffolding.
type Frame struct{}
