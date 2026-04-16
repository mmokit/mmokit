package universe

import (
	"encoding/binary"
	"fmt"
)

// SideEffectType identifies a game-defined side effect kind.
type SideEffectType uint16

// SideEffect is a single side effect emitted during cross-cell action handling.
type SideEffect struct {
	Type    SideEffectType
	Payload []byte
}

// SideEffectCollector accumulates side effects during a cross-cell action.
// Not thread-safe — only used on the game loop goroutine.
type SideEffectCollector struct {
	effects []SideEffect
}

// Emit adds a side effect. Called by any game code during action handling.
func (c *SideEffectCollector) Emit(t SideEffectType, payload []byte) {
	c.effects = append(c.effects, SideEffect{Type: t, Payload: payload})
}

// Drain returns all accumulated effects and resets the collector.
func (c *SideEffectCollector) Drain() []SideEffect {
	if len(c.effects) == 0 {
		return nil
	}
	out := c.effects
	c.effects = c.effects[:0]
	return out
}

// SideEffectHandler processes a single side effect type on the originating node.
// The Handle closure captures typed game state at registration time.
type SideEffectHandler struct {
	Type   SideEffectType
	Handle func(sourceNetID uint32, payload []byte)
}

// SideEffectRegistry maps effect types to handlers on the receiving node.
type SideEffectRegistry struct {
	handlers map[SideEffectType]*SideEffectHandler
}

// NewSideEffectRegistry creates an empty side-effect registry.
func NewSideEffectRegistry() *SideEffectRegistry {
	return &SideEffectRegistry{
		handlers: make(map[SideEffectType]*SideEffectHandler),
	}
}

// Register adds a handler. Panics on duplicate type.
func (r *SideEffectRegistry) Register(h SideEffectHandler) {
	if _, exists := r.handlers[h.Type]; exists {
		panic(fmt.Sprintf("side effect: duplicate type %d", h.Type))
	}
	r.handlers[h.Type] = &h
}

// Dispatch routes each side effect to its registered handler.
func (r *SideEffectRegistry) Dispatch(sourceNetID uint32, effects []SideEffect) {
	for _, e := range effects {
		if h, ok := r.handlers[e.Type]; ok {
			h.Handle(sourceNetID, e.Payload)
		}
	}
}

// ---------------------------------------------------------------------------
// Wire codec for []SideEffect
// ---------------------------------------------------------------------------

// MarshalSideEffects encodes a slice of SideEffect to binary.
// Wire format: [2] count, per effect: [2] Type, [2] data length, [N] data bytes.
func MarshalSideEffects(effects []SideEffect) []byte {
	if len(effects) == 0 {
		return nil
	}
	size := 2
	for _, e := range effects {
		size += 2 + 2 + len(e.Payload)
	}
	buf := make([]byte, size)
	off := 0
	binary.LittleEndian.PutUint16(buf[off:], uint16(len(effects)))
	off += 2
	for _, e := range effects {
		binary.LittleEndian.PutUint16(buf[off:], uint16(e.Type))
		off += 2
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(e.Payload)))
		off += 2
		copy(buf[off:], e.Payload)
		off += len(e.Payload)
	}
	return buf
}

// UnmarshalSideEffects decodes a slice of SideEffect from binary.
func UnmarshalSideEffects(data []byte) ([]SideEffect, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("side effects: need at least 2 bytes, got %d", len(data))
	}
	off := 0
	count := int(binary.LittleEndian.Uint16(data[off:]))
	off += 2

	effects := make([]SideEffect, 0, count)
	for i := 0; i < count; i++ {
		if off+4 > len(data) {
			return nil, fmt.Errorf("side effects: truncated at index %d", i)
		}
		t := SideEffectType(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		dlen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		if off+dlen > len(data) {
			return nil, fmt.Errorf("side effects: truncated data at index %d", i)
		}
		effects = append(effects, SideEffect{
			Type:    t,
			Payload: data[off : off+dlen],
		})
		off += dlen
	}
	return effects, nil
}
