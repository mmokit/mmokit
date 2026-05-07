package chat

import (
	"time"

	"github.com/google/uuid"
)

// tokenBucket is a per-user rate limiter. Capacity tokens fully refill
// over refillDur (so per-token interval = refillDur / capacity). The
// bucket is constructed in newTokenBucket and accessed exclusively
// through Service.rateLimitTake (which holds s.mu.Lock for both lookup
// and take).
type tokenBucket struct {
	tokens    int
	lastFill  time.Time
	capacity  int
	refillDur time.Duration // time to fully refill from 0
}

func newTokenBucket(capacity int, refillDur time.Duration) *tokenBucket {
	return &tokenBucket{
		tokens:    capacity,
		lastFill:  time.Now(),
		capacity:  capacity,
		refillDur: refillDur,
	}
}

// take attempts to consume one token. Returns (true, 0) on success;
// (false, retryAfter) when empty.
func (b *tokenBucket) take(now time.Time) (bool, time.Duration) {
	if b.capacity <= 0 {
		return true, 0
	}
	perToken := b.refillDur / time.Duration(b.capacity)
	if perToken <= 0 {
		perToken = time.Millisecond
	}
	elapsed := now.Sub(b.lastFill)
	gained := int(elapsed / perToken)
	if gained > 0 {
		b.tokens += gained
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.lastFill = b.lastFill.Add(time.Duration(gained) * perToken)
	}
	if b.tokens <= 0 {
		return false, perToken
	}
	b.tokens--
	return true, 0
}

// rateLimitTake is a Service helper: looks up or creates the bucket
// for userID, takes one token, returns (ok, retryAfter).
func (s *Service) rateLimitTake(userID uuid.UUID) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.rateBuckets[userID]
	if !ok {
		b = newTokenBucket(s.opts.UserRateMax, s.opts.UserRateWindow)
		s.rateBuckets[userID] = b
	}
	return b.take(time.Now())
}

// slowModeCheck returns (ok, retryAfter). ok==true when last-send for
// (channelID, userID) is older than channel's slow_mode_seconds (or
// when slow-mode is 0 / disabled). Updates last-send on success.
func (s *Service) slowModeCheck(channelID, userID uuid.UUID) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.channels[channelID]
	if !ok || c.SlowModeSeconds <= 0 {
		return true, 0
	}
	gap := time.Duration(c.SlowModeSeconds) * time.Second
	now := time.Now()
	last, present := s.slowMode[slowModeKey{channelID, userID}]
	if present {
		since := now.Sub(last)
		if since < gap {
			return false, gap - since
		}
	}
	s.slowMode[slowModeKey{channelID, userID}] = now
	return true, 0
}

// muteCheck returns (active, retryAfter). active==true means caller
// is muted (channel-scoped or globally) and may not send. The check
// inspects both the global mute slot (MuteGlobalChannelID) and the
// per-channel slot; either being active blocks the send.
func (s *Service) muteCheck(userID, channelID uuid.UUID) (bool, time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, key := range []muteKey{{userID, MuteGlobalChannelID}, {userID, channelID}} {
		if mu, ok := s.mutes[key]; ok && mu.ExpiresAt.After(now) {
			return true, time.Until(mu.ExpiresAt)
		}
	}
	return false, 0
}

// banCheck returns (active, retryAfter) for channel-scoped bans.
// Looks up s.bans for the (channel, user) key.
func (s *Service) banCheck(userID, channelID uuid.UUID) (bool, time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.bans[banKey{channelID, userID}]; ok && b.BannedUntil.After(time.Now()) {
		return true, time.Until(b.BannedUntil)
	}
	return false, 0
}
