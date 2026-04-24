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

// TestConfigProtocolRoundTrip verifies that Config.Protocol passes through
// Process.Protocol() unchanged.
func TestConfigProtocolRoundTrip(t *testing.T) {
	marker := "my-protocol-marker"
	p := New(Config{
		Mode:         "all",
		LoginHandler: stubLoginHandler,
		Protocol:     marker,
	})
	if p.Protocol() != marker {
		t.Errorf("Protocol() = %v, want %q", p.Protocol(), marker)
	}
}

// TestConfigProtocolUnset verifies that Process.Protocol() returns nil when
// Config.Protocol is not set.
func TestConfigProtocolUnset(t *testing.T) {
	p := New(Config{Mode: "all", LoginHandler: stubLoginHandler})
	if p.Protocol() != nil {
		t.Errorf("Protocol() = %v, want nil", p.Protocol())
	}
}
