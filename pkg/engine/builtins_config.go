package engine

import (
	"context"
	"fmt"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

func (c *Console) registerConfigCommands(opts BuiltinOpts) {
	cfg := opts.Config
	eng := opts.Engine
	onLoop := func(ctx context.Context, fn func() error) error {
		if eng != nil {
			return eng.RunOnLoop(ctx, fn)
		}
		return fn()
	}
	c.SetCompletions("config_fields", cfg.Fields())

	mustRegister := func(cmd cmdsys.Command, usage string) {
		cmd.Usage = usage
		if err := c.adapter.registerTyped(cmd); err != nil {
			panic(fmt.Sprintf("console: registerTyped %q: %v", cmd.Verb, err))
		}
	}

	mustRegister(cmdsys.Command{
		Verb:        "config.list",
		Capability:  "config.list",
		Description: "list all config fields and values",
		Route:       cmdsys.RouteLocal,
		Args:        nil,
		Result:      configListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			var result any
			err := onLoop(ctx, func() error {
				var entries []configEntry
				for _, field := range cfg.Fields() {
					val, err := cfg.GetField(field)
					if err != nil {
						val = fmt.Sprintf("<error: %v>", err)
					}
					entries = append(entries, configEntry{Field: field, Value: val})
				}
				result = configListResult{Entries: entries}
				return nil
			})
			return result, err
		},
	}, "config list")

	mustRegister(cmdsys.Command{
		Verb:        "config.get",
		Capability:  "config.get",
		Description: "show a single config field value",
		Route:       cmdsys.RouteLocal,
		Args:        configGetArgs{},
		Result:      configGetResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			a := args.(configGetArgs)
			var result any
			err := onLoop(ctx, func() error {
				val, e := cfg.GetField(a.Field)
				if e != nil {
					return e
				}
				result = configGetResult{Field: a.Field, Value: val}
				return nil
			})
			return result, err
		},
	}, "config get <field>")

	onChanged := opts.ConfigOnChanged
	mustRegister(cmdsys.Command{
		Verb:        "config.set",
		Capability:  "config.set",
		Description: "change a config field at runtime",
		Route:       cmdsys.RouteLocal,
		Args:        configSetArgs{},
		Result:      configSetResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			a := args.(configSetArgs)
			var result any
			err := onLoop(ctx, func() error {
				old, e := cfg.GetField(a.Field)
				if e != nil {
					return e
				}
				if e := cfg.SetField(a.Field, a.Value); e != nil {
					return e
				}
				if onChanged != nil {
					onChanged(a.Field)
				}
				result = configSetResult{Field: a.Field, Old: old, New: a.Value}
				return nil
			})
			return result, err
		},
	}, "config set <field> <value>")

	if opts.ConfigSave != nil {
		saveFn := opts.ConfigSave
		mustRegister(cmdsys.Command{
			Verb:        "config.save",
			Capability:  "config.save",
			Description: "save current config to disk",
			Route:       cmdsys.RouteLocal,
			Args:        nil,
			Result:      configSaveResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				if err := saveFn(); err != nil {
					return configSaveResult{OK: false, Message: err.Error()}, nil
				}
				return configSaveResult{OK: true, Message: "config saved"}, nil
			},
		}, "config save")
	}

	if opts.ConfigReset != nil {
		resetFn := opts.ConfigReset
		mustRegister(cmdsys.Command{
			Verb:        "config.reset",
			Capability:  "config.reset",
			Description: "reset config to defaults",
			Route:       cmdsys.RouteLocal,
			Args:        nil,
			Result:      configResetResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				err := onLoop(ctx, func() error {
					resetFn()
					return nil
				})
				return configResetResult{OK: true}, err
			},
		}, "config reset")
	}

	// Top-level "config" group dispatcher.
	_ = c.adapter.registerGroupShim("config", "view and modify configuration")
}

// renderConfigGetResult formats a configGetResult for human display.
func renderConfigGetResult(r configGetResult) string {
	return fmt.Sprintf("  %s = %s\n", r.Field, r.Value)
}

// renderConfigSetResult formats a configSetResult for human display.
func renderConfigSetResult(r configSetResult) string {
	return fmt.Sprintf("  %s: %s -> %s\n", r.Field, r.Old, r.New)
}

func init() {
	registerResultRenderer(configGetResult{}, func(v any) string {
		return renderConfigGetResult(v.(configGetResult))
	})
	registerResultRenderer(configSetResult{}, func(v any) string {
		return renderConfigSetResult(v.(configSetResult))
	})
	registerResultRenderer(configSaveResult{}, func(v any) string {
		r := v.(configSaveResult)
		return "  " + r.Message + "\n"
	})
	registerResultRenderer(configResetResult{}, func(v any) string {
		return "  config reset to defaults\n"
	})
}
