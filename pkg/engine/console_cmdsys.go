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

// rawCmdArgs is the Args prototype for shim-converted legacy Command registrations.
type rawCmdArgs struct {
	// Raw holds the entire rest-of-line string after the verb.
	Raw string `cmd:"optional"`
}

// rawCmdResult is the Result prototype for shim-converted legacy Command registrations.
type rawCmdResult struct {
	Text string
}

// cmdsysAdapter owns the Registry and Dispatcher that back the Console.
// It bridges the old Command shim API to the cmdsys pipeline and provides
// the REPL entry point Dispatch.
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
	category    string // capability namespace (everything before first '.')
	description string
	usage       string    // optional usage hint for help display
	aliases     []string  // display-only; routing uses primary verb only
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

// registerShim wraps a legacy Command into the cmdsys pipeline.
// verb is the top-level verb for REPL lookup; the handler just calls fn(args).
func (a *cmdsysAdapter) registerShim(verb string, category string, description string, usage string, aliases []string, fn func(args []string)) error {
	cmd := cmdsys.Command{
		Verb:        verb,
		Capability:  cmdsys.Capability(verb),
		Description: description,
		Route:       cmdsys.RouteLocal,
		Args:        rawCmdArgs{},
		Result:      rawCmdResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			ra, ok := args.(rawCmdArgs)
			if !ok {
				return rawCmdResult{}, nil
			}
			parts := strings.Fields(ra.Raw)
			fn(parts)
			return rawCmdResult{}, nil
		},
	}
	if err := a.Registry.Register(cmd); err != nil {
		return err
	}
	if category == "" {
		category = verb
	}
	a.verbOrder = append(a.verbOrder, verb)
	a.verbMeta[verb] = verbDisplayMeta{
		category:    category,
		description: description,
		usage:       usage,
		aliases:     aliases,
	}
	return nil
}

// Dispatch parses raw into (verb, rest), then calls Invoke on the Dispatcher
// inside ExecOnLoop so handlers run on the game tick. Returns formatted output.
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
	// We do this by checking if verb itself is registered; if not, check verb+"."+firstword.
	if _, found := a.Registry.Lookup(verb); !found {
		// Try group dispatch: "config get Foo" → "config.get" + "Foo"
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
		// Shim results are printed directly by the handler (side-effecting).
		// Typed results are rendered here.
		if tr.Result == nil {
			return ""
		}
		if raw, ok := tr.Result.(rawCmdResult); ok {
			return raw.Text
		}
		return renderResult(tr.Result)
	}

	if a.ExecOnLoop != nil {
		result = a.ExecOnLoop(runFn)
	} else {
		result = runFn()
	}
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
	if raw2, ok := tr.Result.(rawCmdResult); ok {
		return raw2.Text
	}
	return renderResult(tr.Result)
}

// verbsInCategory returns verbs for a given category in registration order.
func (a *cmdsysAdapter) verbsInCategory(cat string) []string {
	var out []string
	for _, v := range a.verbOrder {
		if a.verbMeta[v].category == cat {
			out = append(out, v)
		}
	}
	return out
}

// categories returns all known categories in first-seen registration order.
func (a *cmdsysAdapter) categories() []string {
	seen := make(map[string]bool)
	var out []string
	for _, v := range a.verbOrder {
		cat := a.verbMeta[v].category
		if !seen[cat] {
			seen[cat] = true
			out = append(out, cat)
		}
	}
	return out
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

// buildHelpText generates categorized help text from registered verbs.
// builtinCats is the set of categories registered before game builtins were added.
func (a *cmdsysAdapter) buildHelpText(builtinCats map[string]bool) string {
	cats := a.categories()
	// Collect top-level group verbs to deduplicate (e.g. "config" vs "config.get").
	// For display, we want to show the group-level verb (e.g. "config") once,
	// and the sub-verbs only when they have no group parent.
	groups := make(map[string]bool)
	for _, v := range a.verbOrder {
		if dot := strings.IndexByte(v, '.'); dot >= 0 {
			groups[v[:dot]] = true
		}
	}

	var b strings.Builder
	b.WriteString("\n")
	gameSectionPrinted := false

	for _, cat := range cats {
		verbs := a.verbsInCategory(cat)
		if len(verbs) == 0 {
			continue
		}

		if !builtinCats[cat] && !gameSectionPrinted {
			b.WriteString("  ── Game Commands ──\n\n")
			gameSectionPrinted = true
		}

		b.WriteString(fmt.Sprintf("  %s%s:\n", strings.ToUpper(cat[:1]), cat[1:]))

		// Collect display entries for this category.
		// Group sub-verbs are omitted; only the top-level group verb is shown.
		type entry struct {
			usage string
			desc  string
		}
		var entries []entry
		seenGroups := make(map[string]bool)

		for _, v := range verbs {
			meta := a.verbMeta[v]
			// If this verb is a sub-verb (contains '.'), show as group entry once.
			dot := strings.IndexByte(v, '.')
			if dot >= 0 {
				groupVerb := v[:dot]
				if seenGroups[groupVerb] {
					continue
				}
				seenGroups[groupVerb] = true
				// Use the group verb's own metadata if registered, else synthesize.
				if gm, ok := a.verbMeta[groupVerb]; ok {
					usage := gm.usage
					if usage == "" {
						usage = groupVerb
					}
					entries = append(entries, entry{usage: usage, desc: gm.description})
				} else {
					entries = append(entries, entry{usage: groupVerb, desc: meta.description})
				}
				continue
			}
			// Top-level verb: show if not a group that was already shown.
			if seenGroups[v] {
				continue
			}
			usage := meta.usage
			if usage == "" {
				usage = v
			}
			if len(meta.aliases) > 0 {
				aliasStr := strings.Join(meta.aliases, ", ")
				nameEnd := strings.IndexByte(usage, ' ')
				if nameEnd == -1 {
					usage = fmt.Sprintf("%s (%s)", usage, aliasStr)
				} else {
					usage = fmt.Sprintf("%s (%s)%s", usage[:nameEnd], aliasStr, usage[nameEnd:])
				}
			}
			entries = append(entries, entry{usage: usage, desc: meta.description})
		}

		for _, e := range entries {
			fmt.Fprintf(&b, "    %-32s %s\n", e.usage, e.desc)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sortedSubVerbs returns all sub-verbs of a group in registration order.
func (a *cmdsysAdapter) sortedSubVerbs(groupVerb string) []string {
	prefix := groupVerb + "."
	var out []string
	for _, v := range a.verbOrder {
		if strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return out
}

// printGroupHelp returns help text for a single group.
func (a *cmdsysAdapter) printGroupHelp(groupVerb string) string {
	meta, ok := a.verbMeta[groupVerb]
	if !ok {
		return fmt.Sprintf("  unknown command: %s\n", groupVerb)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s — %s\n  subcommands:\n", groupVerb, meta.description)
	subs := a.sortedSubVerbs(groupVerb)
	sort.Slice(subs, func(i, j int) bool { return subs[i] < subs[j] })
	for _, sv := range subs {
		sm := a.verbMeta[sv]
		subName := sv[len(groupVerb)+1:]
		usage := sm.usage
		if usage == "" {
			usage = subName
		}
		fmt.Fprintf(&b, "    %-28s %s\n", usage, sm.description)
	}
	b.WriteString("\n")
	return b.String()
}
