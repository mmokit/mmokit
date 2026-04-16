package engine

// EntityInfo holds generic entity data for console display.
type EntityInfo struct {
	NetID    uint32
	NodeID   string
	Type     string
	X, Y     float32
	VX, VY   float32
	CellSX   int32
	CellSY   int32
}

// EntityOpts configures the "entity" command group callbacks.
// All callbacks run on the game loop via engine.RunOnLoop.
type EntityOpts struct {
	Summary func() map[string]int
	List    func(typeName string) []EntityInfo
	Get     func(netID uint32) (EntityInfo, bool)
	Remove  func(netID uint32) bool
}

// BuiltinOpts configures which built-in command groups to register.
type BuiltinOpts struct {
	Config          Configurable
	ConfigSave      func() error
	ConfigReset     func()
	ConfigOnChanged func(field string)
	Registry        *EntityRegistry
	Entities        *EntityOpts
	// Engine is used by config and entity handlers to schedule ECS/config work
	// on the game loop via RunOnLoop. Required when Config or Entities are set.
	Engine *Engine
}

// RegisterBuiltins registers opt-in command groups based on which fields are set.
func (c *Console) RegisterBuiltins(opts BuiltinOpts) {
	if opts.Config != nil {
		c.registerConfigCommands(opts)
	}
	if opts.Entities != nil || opts.Registry != nil {
		c.registerEntityCommands(opts)
	}
	c.snapshotBuiltinCategories()
}

// ---------------------------------------------------------------------------
// Config typed args/results
// ---------------------------------------------------------------------------

type configGetArgs struct {
	Field string
}

type configGetResult struct {
	Field string
	Value string
}

type configSetArgs struct {
	Field string
	Value string
}

type configSetResult struct {
	Field string
	Old   string
	New   string
}

type configEntry struct {
	Field string
	Value string
}

type configListResult struct {
	Entries []configEntry `cmd:"table"`
}

type configSaveResult struct {
	OK      bool
	Message string
}

type configResetResult struct {
	OK bool
}

// ---------------------------------------------------------------------------
// Entity typed args/results
// ---------------------------------------------------------------------------

type entitySummaryEntry struct {
	Type  string
	Count int
}

type entitySummaryResult struct {
	Entries []entitySummaryEntry `cmd:"table"`
}

type entityListArgs struct {
	Type string `cmd:"optional"`
}

type entityListEntry struct {
	NetID    uint32
	Node     string
	Type     string
	Cell     string
	Position string
}

type entityListResult struct {
	Entries []entityListEntry `cmd:"table"`
}

type entityGetArgs struct {
	NetID uint32
}

type entityGetResult struct {
	NetID  uint32
	Node   string
	Type   string
	Cell   string
	Pos    string
	Vel    string
}

type entityAddArgs struct {
	Type string
	X    float32
	Y    float32
}

type entityAddResult struct {
	Type    string
	X, Y    float32
	Message string
}

type entityRemoveArgs struct {
	NetID uint32
}

type entityRemoveResult struct {
	NetID uint32
	OK    bool
}
