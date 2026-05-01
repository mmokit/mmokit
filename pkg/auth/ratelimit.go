package auth

import (
	"net/netip"
	"sync"
	"time"
)

type IPRateLimitConfig struct {
	Max     int           // max failures per Window before lockout
	Window  time.Duration // sliding window
	Lockout time.Duration // how long the IP is locked when Max is exceeded
}

type ipBucket struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
}

// IPRateLimiter is the in-memory per-IP rate limiter described in §9 of
// the spec. Successful auth resets the counter; failures accrue.
type IPRateLimiter struct {
	cfg     IPRateLimitConfig
	mu      sync.Mutex
	buckets map[netip.Addr]*ipBucket
	now     func() time.Time
}

func NewIPRateLimiter(cfg IPRateLimitConfig) *IPRateLimiter {
	return &IPRateLimiter{cfg: cfg, buckets: map[netip.Addr]*ipBucket{}, now: time.Now}
}

// CheckAndCount records an auth attempt outcome for ip. Returns
// (locked, retryAfter). When locked is true, retryAfter > 0 and the
// caller should reject the operation with RATE_LIMITED. Successful
// attempts reset the bucket.
func (rl *IPRateLimiter) CheckAndCount(ip netip.Addr, success bool) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	b, ok := rl.buckets[ip]
	if !ok {
		b = &ipBucket{}
		rl.buckets[ip] = b
	}

	// Already locked?
	if !b.lockedUntil.IsZero() && b.lockedUntil.After(now) {
		return true, b.lockedUntil.Sub(now)
	}

	if success {
		delete(rl.buckets, ip)
		return false, 0
	}

	// Window expired?
	if !b.windowStart.IsZero() && now.Sub(b.windowStart) > rl.cfg.Window {
		b.failures = 0
		b.windowStart = time.Time{}
		b.lockedUntil = time.Time{}
	}

	if b.failures == 0 {
		b.windowStart = now
	}
	b.failures++
	if b.failures > rl.cfg.Max {
		b.lockedUntil = now.Add(rl.cfg.Lockout)
		return true, rl.cfg.Lockout
	}
	return false, 0
}

// Sweep evicts buckets idle longer than maxIdle. Called periodically
// from the service's reaper goroutine.
func (rl *IPRateLimiter) Sweep(maxIdle time.Duration) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	n := 0
	for ip, b := range rl.buckets {
		latest := b.windowStart
		if b.lockedUntil.After(latest) {
			latest = b.lockedUntil
		}
		if now.Sub(latest) > maxIdle {
			delete(rl.buckets, ip)
			n++
		}
	}
	return n
}
