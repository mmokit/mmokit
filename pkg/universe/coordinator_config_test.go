package universe

import "testing"

// TestConfig_VelQuantScaleDefault verifies that a zero VelQuantScale in Config
// is replaced with the default value of 2000 after New().
func TestConfig_VelQuantScaleDefault(t *testing.T) {
	p := New(Config{Mode: "all", LoginHandler: stubLoginHandler})
	if got := p.Cfg().VelQuantScale; got != 2000 {
		t.Errorf("Cfg().VelQuantScale = %v, want 2000 (default)", got)
	}
}

// TestConfig_SizeQuantScaleDefault verifies that a zero SizeQuantScale in Config
// is replaced with the default value of 500 after New().
func TestConfig_SizeQuantScaleDefault(t *testing.T) {
	p := New(Config{Mode: "all", LoginHandler: stubLoginHandler})
	if got := p.Cfg().SizeQuantScale; got != 500 {
		t.Errorf("Cfg().SizeQuantScale = %v, want 500 (default)", got)
	}
}

// TestConfig_VelQuantScaleOverride verifies that explicit non-zero values for
// VelQuantScale and SizeQuantScale are preserved unchanged after New().
func TestConfig_VelQuantScaleOverride(t *testing.T) {
	p := New(Config{
		Mode:           "all",
		LoginHandler:   stubLoginHandler,
		VelQuantScale:  999,
		SizeQuantScale: 333,
	})
	if got := p.Cfg().VelQuantScale; got != 999 {
		t.Errorf("Cfg().VelQuantScale = %v, want 999 (explicit override)", got)
	}
	if got := p.Cfg().SizeQuantScale; got != 333 {
		t.Errorf("Cfg().SizeQuantScale = %v, want 333 (explicit override)", got)
	}
}
