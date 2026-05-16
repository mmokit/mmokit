package spatial

// Layer bits for Collider.Layer. Layers are masks so multiple bits can
// be combined when querying (e.g. LayerStatic|LayerProp = "everything
// that blocks a projectile").
//
// Layer 0 is reserved as "no membership" — entities with Layer=0 are
// invisible to all layer-masked queries. Existing entities that pre-date
// the layer assignments default to 0; they will be brought into the
// scheme on a per-kind basis (see entity_*.go in internal/game).
const (
	LayerStatic uint8 = 1 << iota // walls, stations — block movement/sight/locks/shots
	LayerProp                     // asteroids — block shots; transparent to sight + selection
	LayerEntity                   // ships, NPCs — do not block anything
)
