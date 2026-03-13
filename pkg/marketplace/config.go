package marketplace

// Config holds tunable marketplace parameters.
type Config struct {
	TaxPct      float64 // transaction tax (default 0.02 = 2%)
	OrderExpiry int64   // seconds until expiry (default 604800 = 7 days)
	MinPrice    float64 // minimum price per unit (default 0.01)
	MaxOrders   int     // max active orders per player (default 50)
}

// DefaultConfig returns sensible marketplace defaults.
func DefaultConfig() Config {
	return Config{
		TaxPct:      0.02,
		OrderExpiry: 604800, // 7 days
		MinPrice:    0.01,
		MaxOrders:   50,
	}
}
