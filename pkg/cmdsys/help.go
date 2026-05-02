package cmdsys

import (
	"fmt"
	"sort"
	"strings"
)

// HelpResult is the typed result returned by Dispatcher.Invoke when the
// caller passed --help, -h, or ? as an argument token. Console renderers
// detect this type and print Text verbatim. JSON consumers receive
// {"verb": "...", "text": "..."}.
type HelpResult struct {
	Verb string `json:"verb"`
	Text string `json:"text"`
}

// IsHelpToken reports whether tok is one of the help-trigger tokens
// (--help, -h, ?). Used by Dispatcher.Invoke to detect help requests
// position-independently.
func IsHelpToken(tok string) bool {
	switch tok {
	case "--help", "-h", "?":
		return true
	default:
		return false
	}
}

// RenderHelp returns formatted help text for verb. Resolution order:
//  1. If verb resolves to a registered Command, render per-command help
//     using the schema and metadata. If the verb has at least one
//     registered sub-verb (e.g. "log" with "log.status" / "log.on"),
//     a SUBCOMMANDS section is appended.
//  2. Otherwise, if verb is a namespace prefix with at least one
//     registered sub-verb (e.g. "bot" with "bot.spawn" but no top-level
//     "bot" command), render group help: header + sub-verb table.
//  3. Otherwise return a friendly "unknown command" message.
func RenderHelp(reg *Registry, verb string) string {
	if cmd, ok := reg.Lookup(verb); ok {
		return renderCommandHelp(reg, cmd)
	}
	if subs := subVerbsOf(reg, verb); len(subs) > 0 {
		return renderGroupHelp(reg, verb, subs)
	}
	return fmt.Sprintf("  unknown command: %s\n", verb)
}

// renderCommandHelp produces the per-command help block (USAGE/ARGUMENTS/
// FLAGS/EXAMPLES/SUBCOMMANDS) for cmd. Stub for now — filled in by Task 3.
func renderCommandHelp(reg *Registry, cmd Command) string {
	return ""
}

// renderGroupHelp produces a header + sub-verb table for a namespace
// prefix that has no top-level Command. Stub for now — filled in by Task 3.
func renderGroupHelp(reg *Registry, groupVerb string, subs []string) string {
	return ""
}

// subVerbsOf returns sub-verbs of groupVerb (registered verbs starting with
// "<groupVerb>." and excluding hidden commands), sorted alphabetically.
func subVerbsOf(reg *Registry, groupVerb string) []string {
	prefix := groupVerb + "."
	var out []string
	for _, v := range reg.List() {
		if !strings.HasPrefix(v, prefix) {
			continue
		}
		if cmd, ok := reg.Lookup(v); ok && cmd.Hidden {
			continue
		}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
