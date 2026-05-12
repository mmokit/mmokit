package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

// TestCommands_Defer_ExecutesInSubmitOrder verifies that Defer closures
// run in the order they were submitted when flush() is invoked.
func TestCommands_Defer_ExecutesInSubmitOrder(t *testing.T) {
	c := &Commands{}
	var got []int
	c.Defer(func() { got = append(got, 1) })
	c.Defer(func() { got = append(got, 2) })
	c.Defer(func() { got = append(got, 3) })

	c.flush()

	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("Defer order = %v, want [1 2 3]", got)
	}
}

// TestCommands_Flush_ClearsQueue verifies that ops are not re-applied
// on a second flush — the buffer resets after draining.
func TestCommands_Flush_ClearsQueue(t *testing.T) {
	c := &Commands{}
	calls := 0
	c.Defer(func() { calls++ })

	c.flush()
	c.flush()

	if calls != 1 {
		t.Fatalf("Defer called %d times across two flushes, want 1", calls)
	}
}

// TestCommands_Despawn_NoopOnDeadHandle verifies that Despawning an
// already-removed entity is a silent no-op (covers the AddComponent-after-
// Despawn-in-same-batch and the cross-cell-handoff-then-defer cases).
func TestCommands_Despawn_NoopOnDeadHandle(t *testing.T) {
	w := ecs.NewWorld()
	type tag struct{}
	mapper := ecs.NewMap1[tag](w)
	h := mapper.NewEntity(&tag{})
	w.RemoveEntity(h)

	c := &Commands{world: w}
	c.Despawn(h) // queue against an already-dead handle
	// Should not panic on flush.
	c.flush()
}
