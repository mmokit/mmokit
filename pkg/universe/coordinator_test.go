package universe

import "testing"

// TestConfig_DefaultClientRenderMode verifies that a zero-value Config
// has ClientRenderMode defaulted to ClientRenderSnap by New.
func TestConfig_DefaultClientRenderMode(t *testing.T) {
	c := New(Config{Mode: "all", LoginHandler: stubLoginHandler})
	if c.cfg.ClientRenderMode != ClientRenderSnap {
		t.Errorf("default ClientRenderMode = %q, want %q",
			c.cfg.ClientRenderMode, ClientRenderSnap)
	}
}

// TestConfig_ClientRenderInterpolated_Preserved verifies an
// explicitly-set ClientRenderInterpolated survives default application.
func TestConfig_ClientRenderInterpolated_Preserved(t *testing.T) {
	c := New(Config{
		Mode:             "all",
		LoginHandler:     stubLoginHandler,
		ClientRenderMode: ClientRenderInterpolated,
	})
	if c.cfg.ClientRenderMode != ClientRenderInterpolated {
		t.Errorf("explicit Interpolated mode overwritten: got %q",
			c.cfg.ClientRenderMode)
	}
}
