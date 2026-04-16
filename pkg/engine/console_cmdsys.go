package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// operatorCaller is the Caller used for all interactive console invocations.
// It holds the global wildcard grant so the operator can run anything.
var operatorCaller = cmdsys.Caller{
	ID:     "console",
	Source: cmdsys.SourceConsole,
	Grants: []cmdsys.Grant{{Pattern: "*.*", Allow: true}},
}

// cmdsysAdapter owns the Registry and Dispatcher that back the Console.
// It provides the REPL entry point Dispatch and bridges typed commands.
type cmdsysAdapter struct {
	Registry   *cmdsys.Registry
	Dispatcher *cmdsys.Dispatcher

	// verbOrder tracks registration order for help rendering.
	verbOrder []string
	// verbMeta holds display metadata for each registered verb.
	verbMeta map[string]verbDisplayMeta
}

type verbDisplayMeta struct {
	category    string   // capability namespace (everything before first '.')
	description string
	usage       string   // optional usage hint for help display
	aliases     []string // display-only; routing uses primary verb only
}

func newCmdsysAdapter() *cmdsysAdapter {
	reg := cmdsys.NewRegistry()
	d := cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry: reg,
	})
	return &cmdsysAdapter{
		Registry:   reg,
		Dispatcher: d,
		verbMeta:   make(map[string]verbDisplayMeta),
	}
}

// newCmdsysAdapterWith creates a cmdsysAdapter backed by externally-owned
// Registry and Dispatcher instances. Used by Coordinator.startConsole so the
// console shares the coordinator's command pipeline (C3).
func newCmdsysAdapterWith(reg *cmdsys.Registry, d *cmdsys.Dispatcher) *cmdsysAdapter {
	return &cmdsysAdapter{
		Registry:   reg,
		Dispatcher: d,
		verbMeta:   make(map[string]verbDisplayMeta),
	}
}

// registerTyped adds a fully typed cmdsys.Command plus display metadata.
// category defaults to the namespace prefix of the verb (everything before the first '.').
func (a *cmdsysAdapter) registerTyped(cmd cmdsys.Command, usage string, aliases []string) error {
	if err := a.Registry.Register(cmd); err != nil {
		return err
	}
	cat := cmd.Verb
	if dot := strings.IndexByte(cmd.Verb, '.'); dot >= 0 {
		cat = cmd.Verb[:dot]
	}
	a.verbOrder = append(a.verbOrder, cmd.Verb)
	a.verbMeta[cmd.Verb] = verbDisplayMeta{
		category:    cat,
		description: cmd.Description,
		usage:       usage,
		aliases:     aliases,
	}
	return nil
}

// groupDispatchArgs is the args type for top-level group dispatcher verbs.
type groupDispatchArgs struct {
	Sub string `cmd:"optional,help=subcommand and arguments"`
}

// registerGroupShim registers a top-level group verb (e.g. "log", "config") that
// re-dispatches to "group.sub rest…" when called as "log status" or "log on cat".
// The verb's category in the help renderer is always derived from the verb
// itself (matching how sub-verbs derive theirs from the namespace prefix), so
// the group and its sub-verbs collapse into a single help section.
func (a *cmdsysAdapter) registerGroupShim(verb, description string) error {
	av := a // capture
	cmd := cmdsys.Command{
		Verb:        verb,
		Capability:  cmdsys.Capability(verb),
		Description: description,
		Route:       cmdsys.RouteLocal,
		Args:        groupDispatchArgs{},
		Result:      nil,
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(groupDispatchArgs)
			sub := strings.TrimSpace(args.Sub)
			if sub == "" {
				// Default: show help for the group.
				fmt.Print(av.printGroupHelp(verb))
				return nil, nil
			}
			// Re-dispatch as "verb.firstword rest…"
			parts := strings.SplitN(sub, " ", 2)
			dotVerb := verb + "." + parts[0]
			rest := ""
			if len(parts) > 1 {
				rest = parts[1]
			}
			output := av.DispatchRaw(dotVerb + " " + rest)
			if output != "" {
				fmt.Print(output)
			}
			return nil, nil
		},
	}
	if err := a.Registry.Register(cmd); err != nil {
		return err
	}
	a.verbOrder = append(a.verbOrder, verb)
	a.verbMeta[verb] = verbDisplayMeta{
		category:    verb,
		description: description,
		usage:       verb,
	}
	return nil
}

// Dispatch parses raw into (verb, rest), then calls Invoke directly on the
// calling goroutine (the REPL goroutine). Handlers that need game-loop access
// use engine.RunOnLoop internally — the loop stays free to drain other work
// (cell-transfer serializes, neighbor rewires, etc.) while the handler waits.
func (a *cmdsysAdapter) Dispatch(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.SplitN(raw, " ", 2)
	verb := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}

	// Check if this is a group verb like "config" — convert to "config.get" etc.
	// via the sub-verb: "config get Foo" → verb="config.get", rest="Foo"
	if _, found := a.Registry.Lookup(verb); !found {
		if rest != "" {
			subparts := strings.SplitN(rest, " ", 2)
			candidate := verb + "." + subparts[0]
			if _, found2 := a.Registry.Lookup(candidate); found2 {
				verb = candidate
				if len(subparts) > 1 {
					rest = subparts[1]
				} else {
					rest = ""
				}
			}
		}
	}

	var result string
	runFn := func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res, err := a.Dispatcher.Invoke(ctx, operatorCaller, verb, rest)
		if err != nil {
			if err == cmdsys.ErrUnknownVerb {
				return ""
			}
			return fmt.Sprintf("  error: %v\n", err)
		}
		if len(res.PerTarget) == 0 {
			return ""
		}
		tr := res.PerTarget[0]
		if !tr.OK {
			return fmt.Sprintf("  error: %s\n", tr.Error)
		}
		if tr.Result == nil {
			return ""
		}
		return renderResult(tr.Result)
	}

	result = runFn()
	return result
}

// DispatchRaw is like Dispatch but used for internal re-dispatch (e.g. group
// shim routing "config get" → "config.get") or in tests.
func (a *cmdsysAdapter) DispatchRaw(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.SplitN(raw, " ", 2)
	verb := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}
	if _, found := a.Registry.Lookup(verb); !found {
		if rest != "" {
			subparts := strings.SplitN(rest, " ", 2)
			candidate := verb + "." + subparts[0]
			if _, found2 := a.Registry.Lookup(candidate); found2 {
				verb = candidate
				if len(subparts) > 1 {
					rest = subparts[1]
				} else {
					rest = ""
				}
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := a.Dispatcher.Invoke(ctx, operatorCaller, verb, rest)
	if err != nil {
		if err == cmdsys.ErrUnknownVerb {
			return ""
		}
		return fmt.Sprintf("  error: %v\n", err)
	}
	if len(res.PerTarget) == 0 {
		return ""
	}
	tr := res.PerTarget[0]
	if !tr.OK {
		return fmt.Sprintf("  error: %s\n", tr.Error)
	}
	if tr.Result == nil {
		return ""
	}
	return renderResult(tr.Result)
}
