package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// buildHelpText generates categorized top-level help text by walking the
// Registry. Commands are grouped by namespace prefix (everything before the
// first '.') or, for top-level verbs, the verb itself.
//
// Hidden commands are skipped. When a group shim verb (a top-level verb
// that also has sub-verbs registered, e.g. "log") exists, only the shim
// is shown for the group; otherwise each sub-verb is listed separately.
//
// builtinCats marks categories that were registered before game builtins
// were added so the renderer can emit the "── Game Commands ──" separator.
func (a *cmdsysAdapter) buildHelpText(builtinCats map[string]bool) string {
	verbs := a.Registry.List()
	sort.Strings(verbs)

	catVerbs := make(map[string][]string)
	catOrder := []string{}
	seenCat := make(map[string]bool)
	for _, v := range verbs {
		cmd, ok := a.Registry.Lookup(v)
		if !ok || cmd.Hidden {
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
		fmt.Fprintf(&b, "  %s%s:\n", strings.ToUpper(cat[:1]), cat[1:])

		seenGroups := make(map[string]bool)
		for _, v := range catVerbList {
			if dot := strings.IndexByte(v, '.'); dot >= 0 {
				groupVerb := v[:dot]
				if seenGroups[groupVerb] {
					continue
				}
				if shim, hasShim := a.Registry.Lookup(groupVerb); hasShim {
					seenGroups[groupVerb] = true
					fmt.Fprintf(&b, "    %-32s %s\n", cmdsys.DisplayUsage(shim), shim.Description)
					continue
				}
				cmd, ok := a.Registry.Lookup(v)
				if !ok {
					continue
				}
				fmt.Fprintf(&b, "    %-32s %s\n", cmdsys.DisplayUsage(cmd), cmd.Description)
				continue
			}
			if seenGroups[v] {
				continue
			}
			cmd, ok := a.Registry.Lookup(v)
			if !ok {
				continue
			}
			seenGroups[v] = true
			usage := cmdsys.DisplayUsage(cmd)
			if len(cmd.Aliases) > 0 {
				aliasStr := strings.Join(cmd.Aliases, ", ")
				nameEnd := strings.IndexByte(usage, ' ')
				if nameEnd == -1 {
					usage = fmt.Sprintf("%s (%s)", usage, aliasStr)
				} else {
					usage = fmt.Sprintf("%s (%s)%s", usage[:nameEnd], aliasStr, usage[nameEnd:])
				}
			}
			fmt.Fprintf(&b, "    %-32s %s\n", usage, cmd.Description)
		}
		b.WriteString("\n")
	}

	return b.String()
}
