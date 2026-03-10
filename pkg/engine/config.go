package engine

// Config holds platform-level configuration.
type Config struct {
	ListenAddr string
	UDPAddr    string
	TickRate   int
}

// DefaultConfig returns sensible defaults for the platform.
func DefaultConfig() Config {
	return Config{
		ListenAddr: ":8080",
		UDPAddr:    ":9000",
		TickRate:   20,
	}
}
