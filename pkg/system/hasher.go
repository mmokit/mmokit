package system

import "math"

// Hasher is an inline FNV-64a hasher for entity diff detection.
// Zero-allocation, no interface dispatch — all methods are value-type operations.
type Hasher struct {
	hash uint64
}

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

// Reset reinitializes the hasher to the FNV offset basis.
func (h *Hasher) Reset() {
	h.hash = fnvOffset64
}

// Sum returns the current hash value.
func (h *Hasher) Sum() uint64 {
	return h.hash
}

// Float32 hashes a float32 value.
func (h *Hasher) Float32(v float32) {
	bits := math.Float32bits(v)
	h.hash ^= uint64(bits & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((bits >> 8) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((bits >> 16) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((bits >> 24) & 0xFF)
	h.hash *= fnvPrime64
}

// Uint32 hashes a uint32 value.
func (h *Hasher) Uint32(v uint32) {
	h.hash ^= uint64(v & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((v >> 8) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((v >> 16) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((v >> 24) & 0xFF)
	h.hash *= fnvPrime64
}

// Uint8 hashes a uint8 value.
func (h *Hasher) Uint8(v uint8) {
	h.hash ^= uint64(v)
	h.hash *= fnvPrime64
}

// Bool hashes a boolean value.
func (h *Hasher) Bool(v bool) {
	if v {
		h.Uint8(1)
	} else {
		h.Uint8(0)
	}
}

// Int32 hashes an int32 value.
func (h *Hasher) Int32(v int32) {
	u := uint32(v)
	h.hash ^= uint64(u & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((u >> 8) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((u >> 16) & 0xFF)
	h.hash *= fnvPrime64
	h.hash ^= uint64((u >> 24) & 0xFF)
	h.hash *= fnvPrime64
}

// Int64 hashes an int64 value.
func (h *Hasher) Int64(v int64) {
	u := uint64(v)
	h.hash ^= u & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 8) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 16) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 24) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 32) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 40) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 48) & 0xFF
	h.hash *= fnvPrime64
	h.hash ^= (u >> 56) & 0xFF
	h.hash *= fnvPrime64
}
