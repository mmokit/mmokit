package engine

import (
	"context"
	"fmt"
	"reflect"
	"sort"
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

	// ExecOnLoop runs fn on the game loop and returns its result.
	// Wired by Console at construction time via SetExecFunc.
	ExecOnLoop func(fn func() string) string

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

// DispatchRaw is like Dispatch but skips ExecOnLoop — used for commands that
// must run synchronously (e.g. help) or in tests.
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

// resultRenderers maps result types (by reflect.Type) to custom renderers.
// Registered via registerResultRenderer; checked first in renderResult.
var resultRenderers = map[reflect.Type]func(any) string{}

// registerResultRenderer registers a custom text renderer for a given result type.
// proto is a zero value of the type (e.g. configGetResult{}).
func registerResultRenderer(proto any, fn func(any) string) {
	resultRenderers[reflect.TypeOf(proto)] = fn
}

// renderResult formats a typed result struct into human-readable console text.
// If a custom renderer is registered for the type, it is used first.
// If the result contains a slice field tagged cmd:"table", that slice is rendered
// as a Table. Otherwise each exported field is rendered as "Field: Value".
func renderResult(v any) string {
	if v == nil {
		return ""
	}
	if fn, ok := resultRenderers[reflect.TypeOf(v)]; ok {
		return fn(v)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return fmt.Sprintf("  %v\n", v)
	}
	rt := rv.Type()

	// Check for a table-tagged slice field.
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("cmd")
		if !containsTagFlag(tag, "table") {
			continue
		}
		fv := rv.Field(i)
		if fv.Kind() != reflect.Slice {
			continue
		}
		return renderSliceAsTable(fv)
	}

	// Fallback: render as key: value lines.
	var b strings.Builder
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		fmt.Fprintf(&b, "  %s: %v\n", f.Name, rv.Field(i).Interface())
	}
	return b.String()
}

// renderSliceAsTable renders a []SomeStruct as a Table using field names as headers.
func renderSliceAsTable(sv reflect.Value) string {
	if sv.Len() == 0 {
		return "  (empty)\n"
	}
	elemType := sv.Type().Elem()
	if elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}
	if elemType.Kind() != reflect.Struct {
		// Flat slice: just list values.
		var b strings.Builder
		for i := range sv.Len() {
			fmt.Fprintf(&b, "  %v\n", sv.Index(i).Interface())
		}
		return b.String()
	}

	// Collect headers from exported fields.
	var headers []string
	for i := range elemType.NumField() {
		if elemType.Field(i).IsExported() {
			headers = append(headers, elemType.Field(i).Name)
		}
	}
	t := NewTable(headers...)
	for i := range sv.Len() {
		elem := sv.Index(i)
		if elem.Kind() == reflect.Ptr {
			elem = elem.Elem()
		}
		row := make([]any, len(headers))
		for j, h := range headers {
			row[j] = elem.FieldByName(h).Interface()
		}
		t.Row(row...)
	}
	return t.String()
}

// containsTagFlag checks whether tag contains a bare flag word.
func containsTagFlag(tag, flag string) bool {
	for _, part := range strings.Split(tag, ",") {
		if strings.TrimSpace(part) == flag {
			return true
		}
	}
	return false
}

// buildHelpText generates categorized help text by walking the Registry
// directly. Every registered command shows up regardless of whether it was
// registered via the adapter helpers or via cmdsys.Registry.Register. verbMeta
// is only consulted for optional usage/alias overrides.
//
// Commands are grouped by capability namespace (everything before the first
// '.' in the verb). A top-level verb with no dot (e.g. "perf", "load") forms
// its own category. Group verbs (e.g. "log", "entity") are shown as a single
// collapsed entry for the whole group.
//
// builtinCats is the set of categories registered before game builtins were
// added (used to emit the "── Game Commands ──" separator).
func (a *cmdsysAdapter) buildHelpText(builtinCats map[string]bool) string {
	verbs := a.Registry.List()
	sort.Strings(verbs)

	// Group verbs by category (namespace prefix, or verb itself if no dot).
	catVerbs := make(map[string][]string)
	catOrder := []string{}
	seenCat := make(map[string]bool)
	for _, v := range verbs {
		cat := v
		if dot := strings.IndexByte(v, '.'); dot >= 0 {
			cat = v[:dot]
		}
		if !seenCat[cat] {
			seenCat[cat] = true
			catOrder = append(catOrder, cat)
		}
		catVerbs[cat] = append(catVerbs[cat], v)
	}
	sort.Strings(catOrder)

	var b strings.Builder
	b.WriteString("\n")
	gameSectionPrinted := false

	for _, cat := range catOrder {
		catVerbList := catVerbs[cat]
		if len(catVerbList) == 0 {
			continue
		}

		if !builtinCats[cat] && !gameSectionPrinted {
			b.WriteString("  ── Game Commands ──\n\n")
			gameSectionPrinted = true
		}

		b.WriteString(fmt.Sprintf("  %s%s:\n", strings.ToUpper(cat[:1]), cat[1:]))

		type entry struct {
			usage string
			desc  string
		}
		var entries []entry
		// seenGroups tracks which top-level verbs have already been rendered
		// (either as their own entry, or collapsed under a group shim). Prevents
		// the same "log" / "entity" / etc. verb appearing twice when both a
		// top-level shim AND sub-verbs exist.
		seenGroups := make(map[string]bool)

		for _, v := range catVerbList {
			// Grouped sub-verb (e.g. "log.status"): if a group shim exists at
			// the parent verb, collapse all sub-verbs into a single entry using
			// the shim's description. Otherwise render each sub-verb as its own
			// entry (e.g. bot.spawn / bot.clear / bot.list when there's no
			// top-level "bot" command).
			if dot := strings.IndexByte(v, '.'); dot >= 0 {
				groupVerb := v[:dot]
				if seenGroups[groupVerb] {
					continue
				}
				if gcmd, hasShim := a.Registry.Lookup(groupVerb); hasShim {
					seenGroups[groupVerb] = true
					groupUsage := groupVerb
					if meta, ok := a.verbMeta[groupVerb]; ok && meta.usage != "" {
						groupUsage = meta.usage
					}
					entries = append(entries, entry{usage: groupUsage, desc: gcmd.Description})
					continue
				}
				// No group shim: render this sub-verb directly.
				cmd, ok := a.Registry.Lookup(v)
				if !ok {
					continue
				}
				entries = append(entries, entry{usage: v, desc: cmd.Description})
				continue
			}
			// Top-level verb.
			if seenGroups[v] {
				continue
			}
			cmd, ok := a.Registry.Lookup(v)
			if !ok {
				continue
			}
			// Mark this verb as seen so later sub-verb iterations collapse into
			// this entry instead of adding a duplicate line.
			seenGroups[v] = true
			usage := v
			var aliases []string
			if meta, ok := a.verbMeta[v]; ok {
				if meta.usage != "" {
					usage = meta.usage
				}
				aliases = meta.aliases
			}
			if len(aliases) > 0 {
				aliasStr := strings.Join(aliases, ", ")
				nameEnd := strings.IndexByte(usage, ' ')
				if nameEnd == -1 {
					usage = fmt.Sprintf("%s (%s)", usage, aliasStr)
				} else {
					usage = fmt.Sprintf("%s (%s)%s", usage[:nameEnd], aliasStr, usage[nameEnd:])
				}
			}
			entries = append(entries, entry{usage: usage, desc: cmd.Description})
		}

		for _, e := range entries {
			fmt.Fprintf(&b, "    %-32s %s\n", e.usage, e.desc)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sortedSubVerbs returns all sub-verbs of a group (verbs whose Verb begins
// with "<groupVerb>.") from the Registry, sorted alphabetically. Walks the
// full registry rather than verbOrder so commands registered directly via
// cmdsys.Registry.Register are included.
func (a *cmdsysAdapter) sortedSubVerbs(groupVerb string) []string {
	prefix := groupVerb + "."
	var out []string
	for _, v := range a.Registry.List() {
		if strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// printGroupHelp returns help text for a group: a one-line header using the
// group shim's description (if present), followed by every sub-verb with its
// own description. Works for both shim-registered groups (config, entity,
// log) and namespace-only groups (bot, cell, host).
func (a *cmdsysAdapter) printGroupHelp(groupVerb string) string {
	subs := a.sortedSubVerbs(groupVerb)
	if len(subs) == 0 {
		return fmt.Sprintf("  unknown command: %s\n", groupVerb)
	}
	var b strings.Builder
	if shim, ok := a.Registry.Lookup(groupVerb); ok {
		fmt.Fprintf(&b, "\n  %s — %s\n", groupVerb, shim.Description)
	} else {
		fmt.Fprintf(&b, "\n  %s commands:\n", groupVerb)
	}
	b.WriteString("  subcommands:\n")
	for _, sv := range subs {
		subName := sv[len(groupVerb)+1:]
		usage := subName
		desc := ""
		if cmd, ok := a.Registry.Lookup(sv); ok {
			desc = cmd.Description
		}
		if meta, ok := a.verbMeta[sv]; ok && meta.usage != "" {
			usage = meta.usage
		}
		fmt.Fprintf(&b, "    %-28s %s\n", usage, desc)
	}
	b.WriteString("\n")
	return b.String()
}
