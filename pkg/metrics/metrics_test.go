package metrics

import (
	"math"
	"testing"
)

func TestCounter(t *testing.T) {
	var c Counter
	if c.Load() != 0 {
		t.Fatal("expected zero")
	}
	c.Add(10)
	c.Add(5)
	if c.Load() != 15 {
		t.Fatalf("expected 15, got %d", c.Load())
	}
	old := c.Reset()
	if old != 15 {
		t.Fatalf("expected reset to return 15, got %d", old)
	}
	if c.Load() != 0 {
		t.Fatal("expected zero after reset")
	}
}

func TestGauge(t *testing.T) {
	var g Gauge
	g.Set(42)
	if g.Load() != 42 {
		t.Fatalf("expected 42, got %d", g.Load())
	}
	g.Add(-10)
	if g.Load() != 32 {
		t.Fatalf("expected 32, got %d", g.Load())
	}
	g.Add(8)
	if g.Load() != 40 {
		t.Fatalf("expected 40, got %d", g.Load())
	}
}

func TestEWMA(t *testing.T) {
	e := NewEWMA(1.0) // alpha=1.0 means no smoothing
	e.Update(10)
	if e.Value() != 10 {
		t.Fatalf("expected 10, got %f", e.Value())
	}
	e.Update(20)
	if e.Value() != 20 {
		t.Fatalf("alpha=1.0 should track exactly, got %f", e.Value())
	}

	// With alpha=0.5, value should converge
	e2 := NewEWMA(0.5)
	e2.Update(100)
	if e2.Value() != 100 {
		t.Fatalf("first sample should be exact, got %f", e2.Value())
	}
	e2.Update(0)
	if e2.Value() != 50 {
		t.Fatalf("expected 50, got %f", e2.Value())
	}
	e2.Update(0)
	if e2.Value() != 25 {
		t.Fatalf("expected 25, got %f", e2.Value())
	}
}

func TestEWMA_ClampedAlpha(t *testing.T) {
	// Alpha should be clamped to [0.001, 1.0]
	e := NewEWMA(0)
	if e.alpha != 0.001 {
		t.Fatalf("expected alpha clamped to 0.001, got %f", e.alpha)
	}
	e2 := NewEWMA(5.0)
	if e2.alpha != 1.0 {
		t.Fatalf("expected alpha clamped to 1.0, got %f", e2.alpha)
	}
}

func TestEWMA_Convergence(t *testing.T) {
	e := NewEWMA(0.1)
	// Feed constant value, should converge
	for range 100 {
		e.Update(42)
	}
	if math.Abs(e.Value()-42) > 0.01 {
		t.Fatalf("expected convergence to 42, got %f", e.Value())
	}
}
