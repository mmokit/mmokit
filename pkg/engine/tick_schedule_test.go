package engine

import (
	"math"
	"testing"
	"time"
)

func TestNewTickSchedule_Table(t *testing.T) {
	cases := []struct {
		rate     int
		periodMs uint64
		dt       float32
		exact    bool
		clamped  bool
	}{
		{rate: 20, periodMs: 50, dt: 0.05, exact: true},
		{rate: 25, periodMs: 40, dt: 0.04, exact: true},
		{rate: 50, periodMs: 20, dt: 0.02, exact: true},
		{rate: 100, periodMs: 10, dt: 0.01, exact: true},
		// 1000/60 is 16.67: truncation picks 16 (62.5 Hz, +4.2%),
		// rounding picks 17 (58.8 Hz, -2.0%).
		{rate: 60, periodMs: 17, dt: 0.017},
		{rate: 30, periodMs: 33, dt: 0.033},
		{rate: 128, periodMs: 8, dt: 0.008},
		{rate: 144, periodMs: 7, dt: 0.007},
		// 62.5 Hz is an exact tie at 16 ms; round half up.
		{rate: 16, periodMs: 63, dt: 0.063},
		{rate: 1000, periodMs: 1, dt: 0.001, exact: true},
		{rate: 0, periodMs: 50, dt: 0.05, clamped: true},
		{rate: -1, periodMs: 50, dt: 0.05, clamped: true},
		{rate: 1500, periodMs: 1, dt: 0.001, clamped: true},
	}
	for _, c := range cases {
		s := newTickSchedule(c.rate)
		if s.Rate != c.rate {
			t.Errorf("rate %d: Rate = %d, want %d", c.rate, s.Rate, c.rate)
		}
		if s.PeriodMs != c.periodMs {
			t.Errorf("rate %d: PeriodMs = %d, want %d", c.rate, s.PeriodMs, c.periodMs)
		}
		if s.Period != time.Duration(c.periodMs)*time.Millisecond {
			t.Errorf("rate %d: Period = %v, want %v", c.rate, s.Period, time.Duration(c.periodMs)*time.Millisecond)
		}
		if s.Dt != c.dt {
			t.Errorf("rate %d: Dt = %v, want %v", c.rate, s.Dt, c.dt)
		}
		if s.Exact != c.exact {
			t.Errorf("rate %d: Exact = %v, want %v", c.rate, s.Exact, c.exact)
		}
		if s.Clamped != c.clamped {
			t.Errorf("rate %d: Clamped = %v, want %v", c.rate, s.Clamped, c.clamped)
		}
	}
}

// TestNewTickSchedule_DrainBudget pins the loop-job allowance to the frame.
// The fixed 8ms it replaces was 16% of a 50ms tick but 80% of the 10ms tick
// at 100Hz — a rate this repository already runs in op_dispatch_cell_test —
// and the fixed 5ms warning threshold exceeded the entire period above 167Hz.
func TestNewTickSchedule_DrainBudget(t *testing.T) {
	cases := []struct {
		rate      int
		budget    time.Duration
		slowAfter time.Duration
	}{
		// 50ms/4 is 12.5ms, capped at the historical 8ms.
		{rate: 20, budget: 8 * time.Millisecond, slowAfter: 4 * time.Millisecond},
		{rate: 1, budget: 8 * time.Millisecond, slowAfter: 4 * time.Millisecond},
		{rate: 100, budget: 2500 * time.Microsecond, slowAfter: 1250 * time.Microsecond},
		{rate: 60, budget: 4250 * time.Microsecond, slowAfter: 2125 * time.Microsecond},
		// Floored: a 1ms period would otherwise budget 250us.
		{rate: 1000, budget: time.Millisecond, slowAfter: 500 * time.Microsecond},
	}
	for _, c := range cases {
		s := newTickSchedule(c.rate)
		if s.DrainBudget != c.budget {
			t.Errorf("rate %d: DrainBudget = %v, want %v", c.rate, s.DrainBudget, c.budget)
		}
		if s.SlowJobThreshold != c.slowAfter {
			t.Errorf("rate %d: SlowJobThreshold = %v, want %v", c.rate, s.SlowJobThreshold, c.slowAfter)
		}
	}
	for rate := 1; rate <= maxTickRate; rate++ {
		s := newTickSchedule(rate)
		if s.DrainBudget > s.Period && s.PeriodMs > 1 {
			t.Fatalf("rate %d: drain budget %v exceeds the whole %v frame", rate, s.DrainBudget, s.Period)
		}
		if s.DrainBudget < time.Millisecond {
			t.Fatalf("rate %d: drain budget %v starves the queue", rate, s.DrainBudget)
		}
	}
}

// TestNewTickSchedule_Invariants pins the three properties every consumer
// relies on, across the whole schedulable range: the period is a usable
// ticker argument, the timestep is the period, and the duration is the
// millisecond count.
func TestNewTickSchedule_Invariants(t *testing.T) {
	for rate := 1; rate <= maxTickRate; rate++ {
		s := newTickSchedule(rate)
		if s.PeriodMs < 1 {
			t.Fatalf("rate %d: PeriodMs = %d, want >= 1 (time.NewTicker panics on 0)", rate, s.PeriodMs)
		}
		if got := s.Dt * 1000; got != float32(s.PeriodMs) {
			t.Fatalf("rate %d: Dt*1000 = %v, want %v — simulated and scheduled time disagree", rate, got, float32(s.PeriodMs))
		}
		if s.Period != time.Duration(s.PeriodMs)*time.Millisecond {
			t.Fatalf("rate %d: Period = %v, want %v", rate, s.Period, time.Duration(s.PeriodMs)*time.Millisecond)
		}
		if s.Clamped {
			t.Fatalf("rate %d: Clamped, want a schedulable rate", rate)
		}
	}
}

// TestNewTickSchedule_DtMatchesExactFraction is the assertion that licenses
// deleting the coordinator's own tickDt (pkg/universe/coordinator.go's
// float32(1.0)/float32(TickRate)). At every rate that divides 1000 — which is
// every rate this repository configures — the schedule's Dt is bit-identical
// to that exact fraction, so collapsing the two derivations moves no float.
func TestNewTickSchedule_DtMatchesExactFraction(t *testing.T) {
	divisors := 0
	for rate := 1; rate <= maxTickRate; rate++ {
		if 1000%rate != 0 {
			continue
		}
		divisors++
		s := newTickSchedule(rate)
		if !s.Exact {
			t.Errorf("rate %d divides 1000 but Exact is false", rate)
		}
		want := float32(1.0) / float32(rate)
		if math.Float32bits(s.Dt) != math.Float32bits(want) {
			t.Errorf("rate %d: Dt = %v (%#x), exact fraction = %v (%#x)",
				rate, s.Dt, math.Float32bits(s.Dt), want, math.Float32bits(want))
		}
		if s.EffectiveRate() != float64(rate) {
			t.Errorf("rate %d: EffectiveRate = %v, want %d", rate, s.EffectiveRate(), rate)
		}
	}
	if divisors != 16 {
		t.Fatalf("found %d divisors of 1000 in [1,1000], want 16", divisors)
	}
}

// TestNewTickSchedule_RoundIsNeverWorseThanTruncate compares the rounded period
// against the truncating expression CE-008 replaced. Rounding must never move
// the effective rate further from the requested one.
func TestNewTickSchedule_RoundIsNeverWorseThanTruncate(t *testing.T) {
	improved := 0
	for rate := 1; rate <= maxTickRate; rate++ {
		truncMs := 1000 / rate
		if truncMs < 1 {
			continue // the old expression scheduled a zero period and panicked
		}
		s := newTickSchedule(rate)
		roundErr := math.Abs(s.EffectiveRate() - float64(rate))
		truncErr := math.Abs(1000/float64(truncMs) - float64(rate))
		if roundErr > truncErr {
			t.Errorf("rate %d: rounded to %dms (err %.4f Hz) is worse than truncating to %dms (err %.4f Hz)",
				rate, s.PeriodMs, roundErr, truncMs, truncErr)
		}
		if roundErr < truncErr {
			improved++
		}
	}
	if improved == 0 {
		t.Fatal("rounding improved no rate; the change would be vacuous")
	}
	t.Logf("rounding strictly improves %d of %d schedulable rates", improved, maxTickRate)
}

// TestNewTickSchedule_ResidualErrorIsNamed records the rates where rounding
// closes nothing, so the documentation claim "closer, not correct" stays
// honest and any future fix has a fixture to flip.
func TestNewTickSchedule_ResidualErrorIsNamed(t *testing.T) {
	cases := []struct {
		rate          int
		effectiveRate float64
	}{
		{rate: 60, effectiveRate: 1000.0 / 17},
		{rate: 120, effectiveRate: 125},
		{rate: 144, effectiveRate: 1000.0 / 7},
	}
	for _, c := range cases {
		s := newTickSchedule(c.rate)
		if s.Exact {
			t.Errorf("rate %d reports Exact; this test's premise is stale", c.rate)
		}
		if s.EffectiveRate() != c.effectiveRate {
			t.Errorf("rate %d: EffectiveRate = %v, want %v", c.rate, s.EffectiveRate(), c.effectiveRate)
		}
	}
}
