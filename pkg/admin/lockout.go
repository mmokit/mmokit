package admin

import (
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/zenion/mmoserver/pkg/services/auth"
)

// Lockout protects the login endpoint from brute force. Wraps the existing
// IPRateLimiter from pkg/services/auth, parameterized for admin defaults.
type Lockout struct {
	rl *auth.IPRateLimiter
}

// NewLockout creates a Lockout that locks an IP for `window` after
// maxAttempts failures within `window`. Defaults: 5 attempts / 15 minutes.
func NewLockout(maxAttempts int, window time.Duration) *Lockout {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &Lockout{rl: auth.NewIPRateLimiter(auth.IPRateLimitConfig{
		Max:     maxAttempts,
		Window:  window,
		Lockout: window, // lock for the same duration as the window
	})}
}

// Check records a login attempt for r.RemoteAddr. Pass success=true on a
// successful login to reset the IP's failure counter. Returns
// (allowed, retryAfter). On allowed=false, the caller must respond 429
// with Retry-After: ceil(retryAfter.Seconds()).
func (l *Lockout) Check(r *http.Request, success bool) (bool, time.Duration) {
	addr := remoteAddr(r)
	locked, retryAfter := l.rl.CheckAndCount(addr, success)
	return !locked, retryAfter
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, _ := netip.ParseAddr(host)
	return addr
}
