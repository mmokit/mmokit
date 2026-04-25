package engine

import (
	"fmt"
	"sort"
	"strings"
)

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
	// Hidden commands are skipped — they're internal workers (e.g. perf.snapshot)
	// behind a user-facing frontend.
	catVerbs := make(map[string][]string)
	catOrder := []string{}
	seenCat := make(map[string]bool)
	for _, v := range verbs {
		if cmd, ok := a.Registry.Lookup(v); ok && cmd.Hidden {
			continue
		}
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
		if !strings.HasPrefix(v, prefix) {
			continue
		}
		if cmd, ok := a.Registry.Lookup(v); ok && cmd.Hidden {
			continue
		}
		out = append(out, v)
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
