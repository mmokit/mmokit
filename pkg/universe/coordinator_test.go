package universe

import "testing"

// TestConfig_DefaultClientRenderMode verifies that a zero-value Config
// has ClientRenderMode defaulted to ClientRenderInterpolated by New.
func TestConfig_DefaultClientRenderMode(t *testing.T) {
	c := New(Config{Mode: "all", LoginHandler: stubLoginHandler})
	if c.cfg.ClientRenderMode != ClientRenderInterpolated {
		t.Errorf("default ClientRenderMode = %q, want %q",
			c.cfg.ClientRenderMode, ClientRenderInterpolated)
	}
}

// TestConfig_ClientRenderSnap_Preserved verifies an explicitly-set
// ClientRenderSnap survives default application.
func TestConfig_ClientRenderSnap_Preserved(t *testing.T) {
	c := New(Config{
		Mode:             "all",
		LoginHandler:     stubLoginHandler,
		ClientRenderMode: ClientRenderSnap,
	})
	if c.cfg.ClientRenderMode != ClientRenderSnap {
		t.Errorf("explicit Snap mode overwritten: got %q",
			c.cfg.ClientRenderMode)
	}
}
