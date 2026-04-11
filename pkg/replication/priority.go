package replication

// AccumulatePriority adds tier.BaseWeight * distanceFactor to the entity's
// running priority score. The dispatcher calls this on ticks where the
// entity is visible but not sent (e.g. below its update divisor), so that
// starvation pressure grows while the entity is skipped. When the next
// send tick arrives, entities sorted by accumulator get the nod first.
//
// distanceFactor is in [0, 1] — typically computed as 1 - (dist/radius)
// by the dispatcher, giving closer entities more priority per skipped tick.
//
// Nil-safe: no-op if p is nil.
func AccumulatePriority(p *EntityPriorityState, tier ReplicationTier, distanceFactor float32) {
	if p == nil {
		return
	}
	p.Accumulator += tier.BaseWeight * distanceFactor
}

// ResetPriority clears the accumulator. Called after the entity has been
// sent so that subsequent skipped ticks start fresh. Does not touch
// LastSentTick or UnchangedTicks — those are managed by the dispatcher's
// dormancy logic.
//
// Nil-safe: no-op if p is nil.
func ResetPriority(p *EntityPriorityState) {
	if p == nil {
		return
	}
	p.Accumulator = 0
}
