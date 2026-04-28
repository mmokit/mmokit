package engine

import (
	"fmt"
	"math/bits"
	"sort"
	"sync"
)

// DebugFlag is a bitmask of enabled debug capabilities for a player
// session. Engine reserves bits 0–15; games reserve bits 16–31. The
// raw bitmask travels across cell handoffs in TransferFrame.DebugFlags
// and is persisted in players.debug_flags as a JSONB string array of
// flag names.
type DebugFlag uint32

// DebugTopology covers cell-boundary overlay + AoI radius circle. The
// engine's only built-in flag in v1; future flags allocate bits 1+
// in the engine range, 16+ in the game range.
const DebugTopology DebugFlag = 1 << 0

type debugFlagEntry struct {
	bit         uint8
	name        string
	description string
}

var (
	debugFlagMu      sync.RWMutex
	debugFlagsByName = make(map[string]debugFlagEntry)
	debugFlagsByBit  = make(map[uint8]debugFlagEntry)
)

func init() {
	RegisterDebugFlag("topology", 0, "Cell-boundary overlay + AoI radius circle")
}

// RegisterDebugFlag declares a debug capability with a stable name
// and bit position. Engine reserves bits 0–15, games reserve 16–31.
// Panics on double-registration of the same bit (typo or coordination
// failure between engine + game flag tables).
func RegisterDebugFlag(name string, bit uint8, description string) {
	if bit > 31 {
		panic(fmt.Sprintf("RegisterDebugFlag: bit %d out of range (max 31)", bit))
	}
	debugFlagMu.Lock()
	defer debugFlagMu.Unlock()
	if _, exists := debugFlagsByBit[bit]; exists {
		panic(fmt.Sprintf("RegisterDebugFlag: bit %d already registered", bit))
	}
	if _, exists := debugFlagsByName[name]; exists {
		panic(fmt.Sprintf("RegisterDebugFlag: name %q already registered", name))
	}
	entry := debugFlagEntry{bit: bit, name: name, description: description}
	debugFlagsByBit[bit] = entry
	debugFlagsByName[name] = entry
}

// DebugFlagByName returns the bit value for a flag name, or false if
// unknown. Used by DB load (mapping JSONB names to bitmask) and
// console grant handler.
func DebugFlagByName(name string) (DebugFlag, bool) {
	debugFlagMu.RLock()
	defer debugFlagMu.RUnlock()
	e, ok := debugFlagsByName[name]
	if !ok {
		return 0, false
	}
	return DebugFlag(1 << e.bit), true
}

// DebugFlagName returns the registered name for a single-bit flag,
// or false if the bit isn't registered.
func DebugFlagName(f DebugFlag) (string, bool) {
	if f == 0 || (f&(f-1)) != 0 {
		return "", false
	}
	bit := uint8(bits.TrailingZeros32(uint32(f)))
	debugFlagMu.RLock()
	defer debugFlagMu.RUnlock()
	e, ok := debugFlagsByBit[bit]
	if !ok {
		return "", false
	}
	return e.name, true
}

// DebugFlagDescription returns the description for a registered
// flag, or false if the name is unknown.
func DebugFlagDescription(name string) (string, bool) {
	debugFlagMu.RLock()
	defer debugFlagMu.RUnlock()
	e, ok := debugFlagsByName[name]
	if !ok {
		return "", false
	}
	return e.description, true
}

// ListDebugFlags returns all registered flag names in bit-ascending
// order.
func ListDebugFlags() []string {
	debugFlagMu.RLock()
	defer debugFlagMu.RUnlock()
	bits := make([]int, 0, len(debugFlagsByBit))
	for bit := range debugFlagsByBit {
		bits = append(bits, int(bit))
	}
	sort.Ints(bits)
	out := make([]string, 0, len(bits))
	for _, bit := range bits {
		out = append(out, debugFlagsByBit[uint8(bit)].name)
	}
	return out
}

// DebugFlagsFromNames builds a bitmask from an array of flag names,
// dropping unknown names silently.
func DebugFlagsFromNames(names []string) DebugFlag {
	var f DebugFlag
	for _, n := range names {
		if bit, ok := DebugFlagByName(n); ok {
			f |= bit
		}
	}
	return f
}

// DebugFlagsToNames converts a bitmask to an array of registered
// flag names. Acquires the registry lock once for the full scan so
// callers see a consistent snapshot — important for runtime callers
// like the DB persistence layer that may race with other readers.
func DebugFlagsToNames(f DebugFlag) []string {
	debugFlagMu.RLock()
	defer debugFlagMu.RUnlock()
	var out []string
	for bit := uint8(0); bit < 32; bit++ {
		if f&(1<<bit) == 0 {
			continue
		}
		if e, ok := debugFlagsByBit[bit]; ok {
			out = append(out, e.name)
		}
	}
	return out
}

// resetDebugFlagRegistryForTest clears the registry entirely. Test-only
// — production callers must not call this. Tests that need the engine
// default "topology" flag should re-register it explicitly.
func resetDebugFlagRegistryForTest() {
	debugFlagMu.Lock()
	defer debugFlagMu.Unlock()
	debugFlagsByName = make(map[string]debugFlagEntry)
	debugFlagsByBit = make(map[uint8]debugFlagEntry)
}
