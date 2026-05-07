package chat

import "time"

// tokenBucket is a placeholder for the per-user rate-limiter implemented
// in Phase 9. The Service constructs a map[uuid.UUID]*tokenBucket so the
// type must exist now, but no methods are required until handleSend
// starts gating on it.
type tokenBucket struct {
	tokens    int
	lastFill  time.Time
	capacity  int
	refillDur time.Duration
}
