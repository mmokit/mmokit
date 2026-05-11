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

// DisplayVerb returns the interactive-CLI form of verb: dot-separated
// hierarchy converted to space-separated. "cell.list" → "cell list".
// HTTP and programmatic dispatch keep dot-notation; only display layers
// for humans (console help listings, completions, in-game chat) call this.
func DisplayVerb(verb string) string {
	return strings.ReplaceAll(verb, ".", " ")
}

// DisplayUsage returns the human-friendly usage string for cmd, used by
// interactive renderers. If cmd.Usage is set it is returned as-is (the
// command author has chosen the exact rendering). Otherwise the form
// "<spaced-verb> <args>" is auto-derived from the schema.
func DisplayUsage(cmd Command) string {
	if cmd.Usage != "" {
		return cmd.Usage
	}
	displayVerb := DisplayVerb(cmd.Verb)
	var schema Schema
	if cmd.Args != nil {
		if s, err := SchemaOf(cmd.Args); err == nil {
			schema = s
		}
	}
	return autoUsage(displayVerb, schema)
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
// FLAGS/EXAMPLES/SUBCOMMANDS) for cmd.
func renderCommandHelp(reg *Registry, cmd Command) string {
	var b strings.Builder
	b.WriteString("\n")

	// Title: "<verb-with-spaces> — <Description>"
	displayVerb := DisplayVerb(cmd.Verb)
	fmt.Fprintf(&b, "  %s — %s\n", displayVerb, cmd.Description)
	if len(cmd.Aliases) > 0 {
		fmt.Fprintf(&b, "  Aliases: %s\n", strings.Join(cmd.Aliases, ", "))
	}

	// Compute schema once for the remaining sections.
	var schema Schema
	if cmd.Args != nil {
		s, err := SchemaOf(cmd.Args)
		if err == nil {
			schema = s
		}
	}

	// USAGE
	usage := cmd.Usage
	if usage == "" {
		usage = autoUsage(displayVerb, schema)
	}
	b.WriteString("\n  USAGE\n")
	fmt.Fprintf(&b, "    %s\n", usage)

	// ARGUMENTS (positional, !NamedOnly)
	var positional, flags []FieldSchema
	for _, f := range schema.Fields {
		if f.NamedOnly {
			flags = append(flags, f)
		} else {
			positional = append(positional, f)
		}
	}
	if len(positional) > 0 {
		b.WriteString("\n  ARGUMENTS\n")
		for _, f := range positional {
			renderFieldLine(&b, f, false)
		}
	}

	// FLAGS (named-only)
	if len(flags) > 0 {
		b.WriteString("\n  FLAGS\n")
		for _, f := range flags {
			renderFieldLine(&b, f, true)
		}
	}

	// EXAMPLES
	if len(cmd.Examples) > 0 {
		b.WriteString("\n  EXAMPLES\n")
		for _, ex := range cmd.Examples {
			fmt.Fprintf(&b, "    %s\n", ex)
		}
	}

	// SUBCOMMANDS (when this verb has registered sub-verbs)
	if subs := subVerbsOf(reg, cmd.Verb); len(subs) > 0 {
		b.WriteString("\n  SUBCOMMANDS\n")
		for _, sv := range subs {
			sc, ok := reg.Lookup(sv)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "    %-28s %s\n", DisplayUsage(sc), sc.Description)
		}
	}

	b.WriteString("\n")
	return b.String()
}

// autoUsage returns a "<verb> <required> [<optional>] [--flag]…" string
// derived from the schema when Command.Usage is empty.
func autoUsage(displayVerb string, schema Schema) string {
	var parts []string
	parts = append(parts, displayVerb)
	for _, f := range schema.Fields {
		if f.NamedOnly {
			continue
		}
		name := strings.ToLower(f.Name)
		switch {
		case f.Rest:
			parts = append(parts, fmt.Sprintf("[<%s>...]", name))
		case f.Required && f.Default == "":
			parts = append(parts, fmt.Sprintf("<%s>", name))
		default:
			parts = append(parts, fmt.Sprintf("[<%s>]", name))
		}
	}
	for _, f := range schema.Fields {
		if !f.NamedOnly {
			continue
		}
		name := strings.ToLower(f.Name)
		if f.Kind == "bool" {
			parts = append(parts, fmt.Sprintf("[--%s]", name))
		} else {
			parts = append(parts, fmt.Sprintf("[--%s=<value>]", name))
		}
	}
	return strings.Join(parts, " ")
}

// renderFieldLine emits one line under ARGUMENTS or FLAGS.
// flag=true prefixes "--<name>"; flag=false uses the bare field name.
func renderFieldLine(b *strings.Builder, f FieldSchema, flag bool) {
	name := f.Name
	if flag {
		name = "--" + strings.ToLower(f.Name)
	}
	help := f.Help
	annotations := []string{}
	if f.Default != "" {
		annotations = append(annotations, fmt.Sprintf("default: %s", f.Default))
	}
	if len(f.Enum) > 0 {
		annotations = append(annotations, "enum: "+strings.Join(f.Enum, "|"))
	}
	if !flag && f.Required && f.Default == "" {
		annotations = append(annotations, "required")
	}
	suffix := ""
	if len(annotations) > 0 {
		suffix = "  (" + strings.Join(annotations, ", ") + ")"
	}
	if f.Secret {
		suffix += " (secret, prompted)"
	}
	fmt.Fprintf(b, "    %-14s %s%s\n", name, help, suffix)
}

// renderGroupHelp produces a header + sub-verb table for a namespace
// prefix that has no top-level Command.
func renderGroupHelp(reg *Registry, groupVerb string, subs []string) string {
	var b strings.Builder
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %s commands:\n", DisplayVerb(groupVerb))
	for _, sv := range subs {
		cmd, ok := reg.Lookup(sv)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "    %-28s %s\n", DisplayUsage(cmd), cmd.Description)
	}
	b.WriteString("\n")
	return b.String()
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
