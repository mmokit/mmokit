package universe

import "testing"

// TestConfigProtocolRoundTrip verifies that Config.Protocol passes through
// Process.Protocol() unchanged.
func TestConfigProtocolRoundTrip(t *testing.T) {
	marker := "my-protocol-marker"
	p := New(Config{
		Mode:     "all",
		Protocol: marker,
	})
	if p.Protocol() != marker {
		t.Errorf("Protocol() = %v, want %q", p.Protocol(), marker)
	}
}

// TestConfigProtocolUnset verifies that Process.Protocol() returns nil when
// Config.Protocol is not set.
func TestConfigProtocolUnset(t *testing.T) {
	p := New(Config{Mode: "all"})
	if p.Protocol() != nil {
		t.Errorf("Protocol() = %v, want nil", p.Protocol())
	}
}

// TestConfigWorldAndOnInitMutuallyExclusive verifies that Build panics when
// both Config.World and Config.OnInit are set.
func TestConfigWorldAndOnInitMutuallyExclusive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when both Config.World and Config.OnInit are set")
		}
	}()
	cfg := Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
		World:    func(b *Stage) GameWorld { return b },
		OnInit:   func(b *Stage) {},
	}
	p := New(cfg)
	p.Build()
}

// TestConfigWorldDefaultsToBareWorldBase verifies that when neither World nor
// OnInit is set, Build creates a bare *Stage per cell.
func TestConfigWorldDefaultsToBareWorldBase(t *testing.T) {
	cfg := Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	}
	p := New(cfg)
	p.Build()
	if len(p.Cells) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(p.Cells))
	}
	cell := p.Cells["cell_0_0"]
	if cell == nil {
		t.Fatal("cell_0_0 not found")
	}
	if cell.Stage == nil {
		t.Error("cell.Stage is nil; expected default *Stage")
	}
	if cell.World != GameWorld(cell.Stage) {
		t.Errorf("expected cell.World to be the bare *Stage; got %T", cell.World)
	}
}

// TestConfigOnInitRunsOnceAfterConstruction verifies that Config.OnInit is
// called exactly once with the cell's *Stage after Build.
func TestConfigOnInitRunsOnceAfterConstruction(t *testing.T) {
	var calls int
	var seen *Stage
	cfg := Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
		OnInit: func(b *Stage) {
			calls++
			seen = b
		},
	}
	p := New(cfg)
	p.Build()
	if calls != 1 {
		t.Errorf("OnInit called %d times, want 1", calls)
	}
	if seen != p.Cells["cell_0_0"].Stage {
		t.Error("OnInit did not receive the cell's *Stage")
	}
}
