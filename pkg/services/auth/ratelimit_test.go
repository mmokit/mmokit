package auth

import (
	"net/netip"
	"testing"
	"time"
)

func TestRateLimitAllowsUnderThreshold(t *testing.T) {
	rl := NewIPRateLimiter(IPRateLimitConfig{Max: 3, Window: time.Minute, Lockout: time.Minute})
	ip := netip.MustParseAddr("1.2.3.4")
	for i := 0; i < 3; i++ {
		if locked, _ := rl.CheckAndCount(ip, false); locked {
			t.Fatalf("locked at attempt %d", i)
		}
	}
	if locked, _ := rl.CheckAndCount(ip, false); !locked {
		t.Fatal("4th attempt should lock")
	}
}

func TestRateLimitResetOnSuccess(t *testing.T) {
	rl := NewIPRateLimiter(IPRateLimitConfig{Max: 3, Window: time.Minute, Lockout: time.Minute})
	ip := netip.MustParseAddr("5.6.7.8")
	rl.CheckAndCount(ip, false)
	rl.CheckAndCount(ip, false)
	rl.CheckAndCount(ip, true) // success → reset
	for i := 0; i < 3; i++ {
		if locked, _ := rl.CheckAndCount(ip, false); locked {
			t.Fatalf("locked too soon at %d", i)
		}
	}
}

func TestRateLimitWindowExpiry(t *testing.T) {
	rl := NewIPRateLimiter(IPRateLimitConfig{Max: 3, Window: 50 * time.Millisecond, Lockout: time.Minute})
	ip := netip.MustParseAddr("9.9.9.9")
	rl.CheckAndCount(ip, false)
	rl.CheckAndCount(ip, false)
	time.Sleep(60 * time.Millisecond)
	rl.CheckAndCount(ip, false)
	if locked, _ := rl.CheckAndCount(ip, false); locked {
		t.Fatal("window expired counter should not lock")
	}
}
