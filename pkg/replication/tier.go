package replication

// DefaultPromoteRadius returns the effective PromoteRadius for a tier,
// substituting a default of Radius*0.5 when the caller did not set an
// explicit value. Used by the dispatcher when deciding whether a
// border-tier entity should escalate to full-rate updates.
func DefaultPromoteRadius(t ReplicationTier) float32 {
	if t.PromoteRadius > 0 {
		return t.PromoteRadius
	}
	return t.Radius * 0.5
}

// DefaultPromoteLookahead returns the effective PromoteLookahead for a
// tier, substituting a default of 10 ticks when the caller did not set
// an explicit value. The dispatcher projects the entity's position
// forward by this many ticks when deciding whether to promote on
// trajectory alone.
func DefaultPromoteLookahead(t ReplicationTier) int {
	if t.PromoteLookahead > 0 {
		return t.PromoteLookahead
	}
	return 10
}

// InsideRadius reports whether point (px, py) is within t.Radius of
// (cx, cy). Uses squared distance to avoid a sqrt. The check is
// inclusive: a point at exactly the radius is considered inside.
func InsideRadius(t ReplicationTier, cx, cy, px, py float32) bool {
	dx := px - cx
	dy := py - cy
	return dx*dx+dy*dy <= t.Radius*t.Radius
}

// SkipThisTick reports whether the dispatcher should skip this entity
// this tick given the tier's UpdateDivisor. Divisors 0 and 1 are both
// treated as "send every tick" (no skipping). For larger divisors,
// the entity is sent on ticks where tick % divisor == 0.
func SkipThisTick(t ReplicationTier, tick uint64) bool {
	d := t.UpdateDivisor
	if d <= 1 {
		return false
	}
	return tick%uint64(d) != 0
}
