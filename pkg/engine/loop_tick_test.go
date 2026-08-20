package engine

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
)

// captureHook collects the loop's log lines so the schedule banner can be
// asserted rather than eyeballed.
type captureHook struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureHook) Emit(cat, msg string, _ time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, cat+" "+msg)
}

func (c *captureHook) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

func newRateTestEngine(rate int) (*Engine, *captureHook) {
	log := logger.New("engine:loop")
	hook := &captureHook{}
	log.AddHook(hook)
	return New(Config{TickRate: rate}, net.NewConnManager(), log), hook
}

// TestGameLoop_SchedulesTheDerivedPeriod is the acceptance test for CE-008's
// first defect: what the loop asks its ticker for, and the dt it hands every
// system, are the tick schedule's — not a truncated 1000/TickRate.
func TestGameLoop_SchedulesTheDerivedPeriod(t *testing.T) {
	cases := []struct {
		rate     int
		period   time.Duration
		dt       float32
		bannerHz string
	}{
		{rate: 20, period: 50 * time.Millisecond, dt: 0.05, bannerHz: "20Hz"},
		{rate: 25, period: 40 * time.Millisecond, dt: 0.04, bannerHz: "25Hz"},
		{rate: 50, period: 20 * time.Millisecond, dt: 0.02, bannerHz: "50Hz"},
		{rate: 100, period: 10 * time.Millisecond, dt: 0.01, bannerHz: "100Hz"},
		// The roadmap's example. Truncation asked for 16ms (62.5Hz) while
		// the banner claimed 60Hz; both halves change here.
		{rate: 60, period: 17 * time.Millisecond, dt: 0.017, bannerHz: "60Hz requested -> 58.82Hz effective"},
	}
	for _, c := range cases {
		eng, hook := newRateTestEngine(c.rate)
		var gotDt []float32
		probe := updateFunc(func(dt float32) { gotDt = append(gotDt, dt) })
		h := newLoopHarness(t, eng, []System{probe}, []string{"probe"}, Hooks{})

		if got := h.start(); got != c.period {
			t.Errorf("rate %d: requested tick period = %v, want %v", c.rate, got, c.period)
		}
		h.tick()
		h.tick()
		h.stop()

		if len(gotDt) != 2 {
			t.Fatalf("rate %d: system ran %d times, want 2", c.rate, len(gotDt))
		}
		for _, dt := range gotDt {
			if dt != c.dt {
				t.Errorf("rate %d: system dt = %v, want %v", c.rate, dt, c.dt)
			}
		}
		if banner := hook.joined(); !strings.Contains(banner, c.bannerHz) {
			t.Errorf("rate %d: banner %q does not contain %q", c.rate, banner, c.bannerHz)
		}
	}
}

// TestGameLoop_ClampedRatesDoNotPanic covers the two live panics the schedule
// removes. TickRate 0 was an integer divide by zero; any rate above 1000
// truncated to a zero period, which time.NewTicker panics on — and which also
// made TickIntervalMs return 0, so ClusterTick(0) returned 0 and every
// producedAtMs stamp collapsed to zero.
func TestGameLoop_ClampedRatesDoNotPanic(t *testing.T) {
	cases := []struct {
		rate   int
		period time.Duration
	}{
		{rate: 0, period: 50 * time.Millisecond},
		{rate: -1, period: 50 * time.Millisecond},
		{rate: 1500, period: 1 * time.Millisecond},
	}
	for _, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("rate %d: loop panicked: %v", c.rate, r)
				}
			}()
			eng, hook := newRateTestEngine(c.rate)
			h := newLoopHarness(t, eng, nil, nil, Hooks{})
			if got := h.start(); got != c.period {
				t.Errorf("rate %d: requested tick period = %v, want %v", c.rate, got, c.period)
			}
			h.tick()
			h.stop()
			if eng.Tick != 1 {
				t.Errorf("rate %d: eng.Tick = %d, want 1", c.rate, eng.Tick)
			}
			if banner := hook.joined(); !strings.Contains(banner, "out of range") {
				t.Errorf("rate %d: banner %q does not report the substitution", c.rate, banner)
			}
		}()
	}
}

// TestEngine_TickIntervalMsMatchesScheduledPeriod pins the invariant that makes
// the cluster clock coherent: the quantum ClusterTick divides by is the period
// the loop actually schedules. They were separate expressions before CE-008.
func TestEngine_TickIntervalMsMatchesScheduledPeriod(t *testing.T) {
	for _, rate := range []int{0, -1, 1, 20, 25, 30, 60, 100, 144, 1000, 1500} {
		eng, _ := newRateTestEngine(rate)
		h := newLoopHarness(t, eng, nil, nil, Hooks{})
		period := h.start()
		h.stop()

		want := time.Duration(eng.TickIntervalMs()) * time.Millisecond
		if period != want {
			t.Errorf("rate %d: scheduled period %v, TickIntervalMs %v", rate, period, want)
		}
		if eng.TickIntervalMs() == 0 {
			t.Errorf("rate %d: TickIntervalMs is 0; ClusterTick would divide by zero", rate)
		}
	}
}

// TestGameLoop_PreFlushSeesTheSystemTimestep pins the second half of CE-008's
// timing defect: per-tick callbacks fire from PreFlush, and before this the
// coordinator handed them its own float32(1.0)/float32(TickRate) while systems
// integrated the loop's truncated dt. At 60Hz those were 0.0166667 and 0.016 —
// a 4% divergence between a system and a callback inside the same tick.
func TestGameLoop_PreFlushSeesTheSystemTimestep(t *testing.T) {
	for _, rate := range []int{20, 60, 144} {
		eng, _ := newRateTestEngine(rate)
		var sysDt, preFlushDt []float32
		probe := updateFunc(func(dt float32) { sysDt = append(sysDt, dt) })
		hooks := Hooks{PreFlush: func(dt float32) { preFlushDt = append(preFlushDt, dt) }}

		h := newLoopHarness(t, eng, []System{probe}, []string{"probe"}, hooks)
		h.start()
		h.tick()
		h.tick()
		h.stop()

		if len(sysDt) != 2 || len(preFlushDt) != 2 {
			t.Fatalf("rate %d: system ran %d times, PreFlush %d, want 2 each", rate, len(sysDt), len(preFlushDt))
		}
		for i := range sysDt {
			if sysDt[i] != preFlushDt[i] {
				t.Errorf("rate %d tick %d: system dt %v != PreFlush dt %v", rate, i, sysDt[i], preFlushDt[i])
			}
		}
		if want := newTickSchedule(rate).Dt; sysDt[0] != want {
			t.Errorf("rate %d: dt = %v, want the scheduled %v", rate, sysDt[0], want)
		}
	}
}

// TestProcessAdminCmds_DrainBudgetStopsAtTheDeadline exercises the drain
// budget for the first time. It was untestable before the clock seam: the
// only way to overrun an 8ms budget was to burn 8ms of real time inside a
// job, which is exactly the wall-clock assertion the roadmap names as an
// antipattern.
func TestProcessAdminCmds_DrainBudgetStopsAtTheDeadline(t *testing.T) {
	eng, hook := newRateTestEngine(20)
	h := newLoopHarness(t, eng, nil, nil, Hooks{})

	if want := 8 * time.Millisecond; h.gl.sched.DrainBudget != want {
		t.Fatalf("DrainBudget = %v, want %v", h.gl.sched.DrainBudget, want)
	}

	const jobs = 5
	var ran atomic.Int32
	for i := 0; i < jobs; i++ {
		overrun := i == 0
		job := &loopJob{done: make(chan struct{})}
		job.fn = func() error {
			ran.Add(1)
			if overrun {
				// Past the 8ms budget, and past the 4ms warn threshold.
				h.clock.advance(10 * time.Millisecond)
			}
			return nil
		}
		eng.loopQ.ch <- job
	}

	h.start()
	h.tick()

	// The budget is checked after a job runs, so the overrunning job itself
	// still completes — the drain stops before pulling the next one.
	if got := ran.Load(); got != 1 {
		t.Fatalf("first tick drained %d jobs, want exactly 1", got)
	}
	if n := len(eng.loopQ.ch); n != jobs-1 {
		t.Fatalf("queue holds %d jobs after the budget was spent, want %d", n, jobs-1)
	}
	if banner := hook.joined(); !strings.Contains(banner, "slow admin job") {
		t.Errorf("a 10ms job did not trip the 4ms warning; log was %q", banner)
	}

	// Nothing advances the clock now, so the remaining jobs all fit.
	h.tick()
	h.stop()

	if got := ran.Load(); got != jobs {
		t.Fatalf("drained %d jobs in total, want %d", got, jobs)
	}
	if n := len(eng.loopQ.ch); n != 0 {
		t.Fatalf("queue still holds %d jobs, want 0", n)
	}
}
