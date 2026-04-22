package system

import (
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
)

// ReplicaDeadReckoningSystem advances replica, ghost, and shadow entity
// positions each tick using their last-known velocity.
type ReplicaDeadReckoningSystem struct {
	engine.SystemBase
	replicas query.Query[struct {
		Pos *component.Position
		Vel *component.Velocity
		Rep *component.Replica
	}]
	ghosts query.Query[struct {
		Pos   *component.Position
		Vel   *component.Velocity
		Ghost *component.Ghost
	}]
	shadows query.Query[struct {
		Pos    *component.Position
		Vel    *component.Velocity
		Shadow *component.Shadow
	}]
}

func (s *ReplicaDeadReckoningSystem) Init() {
	s.replicas.Init(s, query.IncludeAll())
	s.ghosts.Init(s, query.IncludeAll())
	s.shadows.Init(s, query.IncludeAll())
}

func (s *ReplicaDeadReckoningSystem) Update(dt float32) {
	// Replicas: advance when updated this tick. When a single tick is
	// missed (UpdatedThisTick=false, MissedTicks==1), grace-extrapolate
	// with last-known velocity — cell goroutine tick-phase drift causes
	// one-tick gaps even when the source is healthy, and freezing for
	// those gaps produces a pause+sprint stutter on the client.
	// Two or more consecutive missed ticks means the source is likely
	// silent; freeze then so we don't accumulate drift trails. TTL
	// eventually expires a truly silent replica.
	for _, b := range s.replicas.All() {
		if b.Rep.UpdatedThisTick {
			b.Rep.MissedTicks = 0
			b.Pos.X += b.Vel.X * dt
			b.Pos.Y += b.Vel.Y * dt
			continue
		}
		b.Rep.MissedTicks++
		if b.Rep.MissedTicks <= 1 {
			// Grace extrapolation: one missed tick is normal tick-drift;
			// advance anyway to keep motion smooth across the phase gap.
			b.Pos.X += b.Vel.X * dt
			b.Pos.Y += b.Vel.Y * dt
		}
		// MissedTicks > 1: freeze to avoid drift trails when source is
		// truly silent. TTL eventually expires the replica.
	}

	// Ghosts are mid-transfer entities — always advance unconditionally.
	for _, b := range s.ghosts.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}

	// Shadows: same grace-extrapolation as replicas. During handoff
	// warmup the destination's Shadow absorbs source's border frames
	// (which carry source's tick-N position) then integrates one tick
	// of velocity in the Systems phase. Net result: Shadow at dest
	// PostSystems push reflects source's PREDICTED tick-N+1 position,
	// not tick-N's stale value. The grace window ensures a single
	// missed frame (cell drift) doesn't freeze the shadow and produce
	// the 1-tick freeze + catch-up jump that clients see at authority
	// transfer.
	for _, b := range s.shadows.All() {
		if b.Shadow.UpdatedThisTick {
			b.Shadow.MissedTicks = 0
			b.Pos.X += b.Vel.X * dt
			b.Pos.Y += b.Vel.Y * dt
			continue
		}
		b.Shadow.MissedTicks++
		if b.Shadow.MissedTicks <= 1 {
			// Grace extrapolation: one missed tick is normal tick-drift;
			// advance anyway to keep shadow motion smooth.
			b.Pos.X += b.Vel.X * dt
			b.Pos.Y += b.Vel.Y * dt
		}
		// MissedTicks > 1: freeze to avoid drift trails.
	}
}
