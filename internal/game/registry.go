package game

// EntityDef describes a spawnable entity type for tooling and admin commands.
type EntityDef struct {
	Name        string
	Description string
	EntityType  uint8
	Spawnable   bool
	Spawn       func(x, y float32)
}

// EntityRegistry maps entity type names to their definitions.
type EntityRegistry struct {
	defs   map[string]*EntityDef
	order  []string
	byType map[uint8]*EntityDef
}

// NewEntityRegistry creates an empty entity registry.
func NewEntityRegistry() *EntityRegistry {
	return &EntityRegistry{
		defs:   make(map[string]*EntityDef),
		byType: make(map[uint8]*EntityDef),
	}
}

// Register adds an entity definition to the registry.
func (r *EntityRegistry) Register(def EntityDef) {
	r.defs[def.Name] = &def
	r.byType[def.EntityType] = &def
	r.order = append(r.order, def.Name)
}

// Get returns an entity definition by name.
func (r *EntityRegistry) Get(name string) (*EntityDef, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// ByType returns an entity definition by its EntityType constant.
func (r *EntityRegistry) ByType(t uint8) *EntityDef {
	return r.byType[t]
}

// All returns all registered entity definitions in registration order.
func (r *EntityRegistry) All() []*EntityDef {
	result := make([]*EntityDef, len(r.order))
	for i, name := range r.order {
		result[i] = r.defs[name]
	}
	return result
}

// SpawnableNames returns the names of all spawnable entity types.
func (r *EntityRegistry) SpawnableNames() []string {
	var names []string
	for _, name := range r.order {
		if r.defs[name].Spawnable {
			names = append(names, name)
		}
	}
	return names
}
