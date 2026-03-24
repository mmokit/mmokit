package game

import (
	"math/rand/v2"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
)

// DropEntry defines a single possible drop from an NPC.
type DropEntry struct {
	ItemID      uint32
	DropChance  float32 // 0.0–1.0, 1.0 = always
	MinQuantity int32
	MaxQuantity int32
}

// DropTable is a named collection of possible drops.
type DropTable struct {
	Name    string
	Entries []DropEntry
}

// NPCDropTables maps entity type → drop table.
var NPCDropTables map[uint8]*DropTable

// InitDropTables registers the default drop tables for NPC types.
func InitDropTables() {
	NPCDropTables = map[uint8]*DropTable{
		component.TypeNPC: {
			Name: "npc_default",
			Entries: []DropEntry{
				{
					ItemID:      item.CreditsItemID,
					DropChance:  1.0,
					MinQuantity: 5,
					MaxQuantity: 25,
				},
			},
		},
	}
}

// RollDrops rolls each entry in the table and returns the resulting items map.
// Returns nil if nothing dropped.
func RollDrops(table *DropTable) map[uint32]int32 {
	if table == nil {
		return nil
	}
	var items map[uint32]int32
	for _, entry := range table.Entries {
		if rand.Float32() > entry.DropChance {
			continue
		}
		qty := entry.MinQuantity + int32(rand.Float32()*float32(entry.MaxQuantity-entry.MinQuantity+1))
		if qty > entry.MaxQuantity {
			qty = entry.MaxQuantity
		}
		if qty <= 0 {
			continue
		}
		if items == nil {
			items = make(map[uint32]int32)
		}
		items[entry.ItemID] += qty
	}
	return items
}
