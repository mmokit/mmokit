package game

import (
	"encoding/binary"
	"math"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
)

// --- Inventory: has a map field, needs custom marshal/unmarshal ---

// MarshalInventory serializes an Inventory to binary. Exported for use by
// the replication registry (WithMarshal).
func MarshalInventory(inv *gamecomp.Inventory) []byte {
	return marshalInventory(inv)
}

// UnmarshalInventoryInto deserializes binary data into an existing Inventory
// pointer. Exported for use by the replication registry (WithMarshal).
func UnmarshalInventoryInto(data []byte, inv *gamecomp.Inventory) {
	if result := unmarshalInventory(data); result != nil {
		*inv = *result
	}
}

func marshalInventory(inv *gamecomp.Inventory) []byte {
	count := len(inv.Items)
	b := make([]byte, 6+count*8)
	binary.LittleEndian.PutUint32(b[0:4], math.Float32bits(inv.MaxMass))
	binary.LittleEndian.PutUint16(b[4:6], uint16(count))
	off := 6
	for id, qty := range inv.Items {
		binary.LittleEndian.PutUint32(b[off:off+4], id)
		binary.LittleEndian.PutUint32(b[off+4:off+8], uint32(qty))
		off += 8
	}
	return b
}

func unmarshalInventory(data []byte) *gamecomp.Inventory {
	if len(data) < 6 {
		return nil
	}
	inv := &gamecomp.Inventory{
		MaxMass: math.Float32frombits(binary.LittleEndian.Uint32(data[0:4])),
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count > 0 {
		inv.Items = make(map[uint32]int32, count)
		off := 6
		for i := 0; i < count && off+8 <= len(data); i++ {
			id := binary.LittleEndian.Uint32(data[off : off+4])
			qty := int32(binary.LittleEndian.Uint32(data[off+4 : off+8]))
			inv.Items[id] = qty
			off += 8
		}
	}
	return inv
}
