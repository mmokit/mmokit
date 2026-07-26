package universe

import (
	"github.com/mlange-42/ark/ecs"
)

// Commands is a per-stage deferred ECS mutation buffer. The engine
// flushes it after every system's Update so ops queued in System N
// are visible to System N+1 in the same tick. Game code should never
// call flush() directly — only the engine loop does that.
//
// Operations are applied in submit order. Each op checks
// ecs.World.Alive on its target handle and silently no-ops if the
// entity is gone, so AddComponent-after-Despawn within the same
// batch is safe.
type Commands struct {
	world *ecs.World
	stage *Stage // nil during initial construction; set after Stage wires it
	ops   []func()
}

// Despawn queues authoritative destruction of the entity at next flush.
// The engine records its network ID and runs spatial/index cleanup before
// removing it, while preserving the command buffer's system-N to system-N+1
// visibility contract. Subsequent ops on this handle within the same batch
// become no-ops.
func (c *Commands) Despawn(h ecs.Entity) {
	c.ops = append(c.ops, func() {
		if c.stage != nil && c.stage.eng != nil {
			c.stage.eng.RemoveEntityNow(h)
			return
		}
		// Stand-alone Commands values are useful in focused buffer tests. A
		// production Commands buffer is always stage-backed and therefore
		// takes the authoritative engine path above.
		if c.world != nil && c.world.Alive(h) {
			c.world.RemoveEntity(h)
		}
	})
}

// Defer schedules an arbitrary closure to run during next flush. Use
// for game-action logic that doesn't reduce to a single ECS primitive
// (spawning a loot crate with inventory setup, starting a docking
// sequence, cross-cell respawn routing).
//
// Closures may call Commands ops on this same buffer, but those ops
// land in the NEXT system's flush, not the current one. Single-pass
// per flush; no convergence loop.
func (c *Commands) Defer(fn func()) {
	c.ops = append(c.ops, fn)
}

// AddOp is the public-but-internal hook for cross-package generic
// ops in mmokit. Game code should use mmokit.AddComponent /
// mmokit.RemoveComponent instead of calling this directly.
func (c *Commands) AddOp(op func()) {
	c.ops = append(c.ops, op)
}

// Flush applies all queued ops in submit order and clears the buffer.
// The engine game loop is the authoritative production caller (it
// invokes Flush after every system's Update via the AfterSystem
// hook). Tests that bypass the loop (calling sys.Update directly)
// also call Flush manually — use stage.TickOne(sys, dt) to wrap that
// pattern cleanly.
//
// Game code should never call this directly. Mutations are queued
// via Despawn / Defer (or mmokit.AddComponent / mmokit.RemoveComponent)
// and applied automatically.
func (c *Commands) Flush() {
	// Iterate by index against the snapshotted length. Ops that the
	// running closures queue (nested AddComponent / RemoveComponent /
	// Despawn / Defer) land at indices >= n and are NOT executed in
	// this flush — they survive to the next one. Matches the "single-
	// pass per flush; no convergence loop" contract documented on
	// Defer above.
	n := len(c.ops)
	for i := range n {
		c.ops[i]()
	}
	// Compact: shift any nested-queued ops down to the front and
	// truncate. Preserves the backing array so steady-state ticks
	// don't allocate.
	if extra := len(c.ops) - n; extra > 0 {
		copy(c.ops, c.ops[n:])
		c.ops = c.ops[:extra]
	} else {
		c.ops = c.ops[:0]
	}
}
