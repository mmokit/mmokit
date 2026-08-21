package engine

// Config holds platform-level configuration.
type Config struct {
	ListenAddr string
	UDPAddr    string
	TickRate   int

	// Gravity is the SIGNED acceleration along Z applied to entities whose
	// component.Motion mode is not MoveFly, in world units per second
	// squared. NEGATIVE pulls down: Earth is about -9.81. Zero disables it,
	// which is every 2D process.
	//
	// Signed rather than a magnitude, matching the convention Unity and
	// Unreal use, and stated in capitals because the ambiguity is not
	// theoretical: "downward acceleration along -Z" was the original wording
	// and examples/cube3d read it as a vector component while the integrator
	// read it as a magnitude, so every cube accelerated upward off the map.
	//
	// It lives here, copied from the process config at cell construction,
	// for the same reason TickRate does: pkg/system cannot import
	// pkg/universe, and a system reaches its cell's configuration through
	// Engine().Config.
	Gravity float32
}

// DefaultConfig returns sensible defaults for the platform.
func DefaultConfig() Config {
	return Config{
		ListenAddr: ":8080",
		UDPAddr:    ":9000",
		TickRate:   20,
	}
}
