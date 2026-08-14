package spatial

// Layer bits for Collider.Layer. Layers are masks so multiple bits can
// be combined when querying (e.g. LayerStatic|LayerProp = "everything
// that blocks a projectile").
//
// Layer 0 is reserved as "no membership" — entities with Layer=0 are
// invisible to all layer-masked queries. Entities default to 0 and are
// brought into the scheme per entity kind by the game.
//
// The three layers are named for what they OCCLUDE, not for any genre's
// nouns; the parenthesised examples are how the reference space game
// happens to assign them.
const (
	// LayerStatic blocks movement, sight, locks and shots. (Space game:
	// walls and stations.)
	LayerStatic uint8 = 1 << iota
	// LayerProp blocks shots but is transparent to sight and selection.
	// (Space game: asteroids.)
	LayerProp
	// LayerEntity blocks nothing; membership exists so queries can select
	// it. (Space game: ships and NPCs.)
	LayerEntity
)
