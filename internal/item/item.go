package item

// ItemCategory classifies items for filtering and future gameplay logic.
type ItemCategory uint8

const (
	CategoryCurrency   ItemCategory = iota
	CategoryResource
	CategoryEquipment  // future
	CategoryConsumable // future
	CategoryModule     // future
)

// Well-known item IDs.
const FluxItemID uint32 = 1

// ItemDef defines a type of item in the game.
type ItemDef struct {
	ID          uint32
	Name        string
	Category    ItemCategory
	MassPerUnit float32 // mass contribution per unit of quantity
	SellPrice   float64 // FLUX earned per unit when sold (0 = not sellable)
}

var registry map[uint32]*ItemDef
var byName map[string]*ItemDef

// Init populates the item registry. Call once at startup.
func Init() {
	registry = make(map[uint32]*ItemDef)
	byName = make(map[string]*ItemDef)

	register(&ItemDef{ID: 1, Name: "Flux", Category: CategoryCurrency, MassPerUnit: 0, SellPrice: 0})
	register(&ItemDef{ID: 2, Name: "Ore", Category: CategoryResource, MassPerUnit: 1.0, SellPrice: 1.0})
	register(&ItemDef{ID: 3, Name: "Crystal", Category: CategoryResource, MassPerUnit: 1.0, SellPrice: 3.0})
	register(&ItemDef{ID: 4, Name: "Gas", Category: CategoryResource, MassPerUnit: 0.5, SellPrice: 2.0})
	register(&ItemDef{ID: 5, Name: "Metal", Category: CategoryResource, MassPerUnit: 2.0, SellPrice: 5.0})
}

func register(def *ItemDef) {
	registry[def.ID] = def
	byName[def.Name] = def
}

// Get returns the item definition for the given ID, or nil if not found.
func Get(id uint32) *ItemDef {
	return registry[id]
}

// GetByName returns the item definition for the given name, or nil if not found.
func GetByName(name string) *ItemDef {
	return byName[name]
}

// All returns all registered item definitions.
func All() []*ItemDef {
	items := make([]*ItemDef, 0, len(registry))
	for _, def := range registry {
		items = append(items, def)
	}
	return items
}

// MassOf returns the mass per unit for the given item ID.
// Returns 1.0 as a safe default if the item is not found.
func MassOf(id uint32) float32 {
	if def := registry[id]; def != nil {
		return def.MassPerUnit
	}
	return 1.0
}

// ResourceItemID converts an old ResourceType index (0-3) to the new item ID (2-5).
func ResourceItemID(resourceType uint8) uint32 {
	return uint32(resourceType) + 2
}

// ItemIDToResourceType converts a new item ID (2-5) back to old ResourceType index (0-3).
// Returns 0 and false if the item is not a resource in the valid range.
func ItemIDToResourceType(itemID uint32) (uint8, bool) {
	if itemID >= 2 && itemID <= 5 {
		return uint8(itemID - 2), true
	}
	return 0, false
}
