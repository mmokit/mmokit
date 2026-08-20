package engine

// Config holds platform-level configuration.
type Config struct {
	ListenAddr string
	UDPAddr    string
	TickRate   int

	// Gravity is the downward acceleration applied to entities whose
	// component.Motion mode is not MoveFly, in world units per second
	// squared along -Z. Zero disables it, which is every 2D process.
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
