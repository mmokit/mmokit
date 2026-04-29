package engine

// BuiltinOpts configures which built-in command groups to register.
type BuiltinOpts struct {
	Config          Configurable
	ConfigSave      func() error
	ConfigReset     func()
	ConfigOnChanged func(field string)
	// Engine is used by config handlers to schedule ECS/config work on the
	// game loop via RunOnLoop. Required when Config is set.
	Engine *Engine
}

// RegisterBuiltins registers opt-in command groups based on which fields are set.
// The legacy entity.* console commands previously registered here moved to
// pkg/universe/builtins_entity.go (cluster-aware versions: entity.spawn,
// entity.despawn, entity.list, entity.tp, entity.summary).
func (c *Console) RegisterBuiltins(opts BuiltinOpts) {
	if opts.Config != nil {
		c.registerConfigCommands(opts)
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
