package engine

import "time"

// defaultTickPeriodMs is the period substituted when Config.TickRate is unset
// or nonsensical. It is 20 Hz, the rate every in-tree configuration uses.
const defaultTickPeriodMs uint64 = 50

// maxTickRate is the highest schedulable rate. Above it the period would round
// to zero milliseconds, which time.NewTicker panics on and which would make
// ClusterClock.ClusterTick divide by zero.
const maxTickRate = 1000

// tickSchedule is the single derivation of a tick rate into everything the
// engine schedules against. Every consumer reads one of its fields rather than
// recomputing 1000/TickRate; before CE-008 there were six such recomputations
// and they did not agree.
//
// PeriodMs is a whole number of milliseconds, and that is a constraint rather
// than a rounding convenience. ClusterClock.ClusterTick quantizes a millisecond
// wall clock by this value and TickTime multiplies it back
// (pkg/universe/cluster_clock.go:90, :113), so the tick stream and the
// producedAtMs stamp grid must be the same grid. A fractional period would put
// two consecutive ticks in one bucket, or skip a bucket, and the client's
// interpolator would see that as jitter across an authority handoff.
//
// The consequence, stated plainly rather than implied: a rate that does not
// divide 1000 cannot be scheduled exactly. Rounding picks the nearer of the two
// achievable rates instead of always picking the faster one, so 60 Hz runs at
// 58.8 Hz rather than 62.5 Hz — closer, not correct. 120 Hz still runs at 125.
type tickSchedule struct {
	// Rate is the requested rate, recorded verbatim for logging.
	Rate int
	// PeriodMs is the scheduled tick period and the cluster-clock quantum.
	PeriodMs uint64
	// Period is PeriodMs as a duration; what the ticker is built from.
	Period time.Duration
	// Dt is the fixed timestep handed to every system, in seconds. It is
	// derived from PeriodMs rather than from Rate so simulated time and
	// scheduled time cannot disagree.
	Dt float32
	// Exact reports that the requested rate is achievable — 1000 is
	// divisible by it and it was not clamped.
	Exact bool
	// Clamped reports that the requested rate was unusable and a substitute
	// period was installed.
	Clamped bool
	// DrainBudget is the soft per-tick allowance the loop spends running
	// queued loop jobs. Derived from the period rather than fixed, so a
	// faster tick rate does not hand the admin queue most of the frame.
	DrainBudget time.Duration
	// SlowJobThreshold is the per-job wall time above which the drain logs
	// a warning, for handlers that claim to be fast and are not.
	SlowJobThreshold time.Duration
}

// maxDrainBudget caps the drain allowance at its historical value, so slowing
// the tick rate down does not let admin work grow without bound.
const maxDrainBudget = 8 * time.Millisecond

// newTickSchedule derives the schedule for tickRate. A rate at or below zero,
// or above maxTickRate, is clamped; anything else rounds to the nearest whole
// millisecond.
func newTickSchedule(tickRate int) tickSchedule {
	s := tickSchedule{Rate: tickRate}
	switch {
	case tickRate <= 0:
		s.PeriodMs = defaultTickPeriodMs
		s.Clamped = true
	case tickRate > maxTickRate:
		s.PeriodMs = 1
		s.Clamped = true
	default:
		// Round half up: the exact integer form of round(1000/tickRate).
		s.PeriodMs = uint64((2000 + tickRate) / (2 * tickRate))
		if s.PeriodMs < 1 {
			s.PeriodMs = 1
		}
		s.Exact = 1000%tickRate == 0
	}
	s.Period = time.Duration(s.PeriodMs) * time.Millisecond
	s.Dt = float32(s.PeriodMs) / 1000

	// A quarter of the frame, never more than 8ms and never less than 1ms.
	// The fixed 8ms this replaces was 16% of a 50ms tick but 80% of the
	// 10ms tick at 100Hz, and the fixed 5ms warning threshold exceeded the
	// entire period above 167Hz.
	s.DrainBudget = s.Period / 4
	if s.DrainBudget > maxDrainBudget {
		s.DrainBudget = maxDrainBudget
	}
	if s.DrainBudget < time.Millisecond {
		s.DrainBudget = time.Millisecond
	}
	s.SlowJobThreshold = s.DrainBudget / 2
	return s
}

// EffectiveRate is the rate actually scheduled, which equals Rate exactly when
// Exact is set and is the nearer achievable neighbour otherwise.
func (s tickSchedule) EffectiveRate() float64 {
	return 1000 / float64(s.PeriodMs)
}
