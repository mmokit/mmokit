package game

import "github.com/zenion/mmoserver/pkg/engine"

// Type aliases re-exported from pkg/engine for backwards compatibility.
type (
	EntityDef      = engine.EntityDef
	EntityRegistry = engine.EntityRegistry
)

// NewEntityRegistry creates an empty entity registry.
var NewEntityRegistry = engine.NewEntityRegistry
