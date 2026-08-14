# Per-Command Help Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-command help triggered by `--help`, `-h`, or `?` after any command verb. Output includes USAGE, ARGUMENTS, FLAGS, EXAMPLES, and (for namespaces) SUBCOMMANDS sections derived from the command's existing schema plus a new `Examples []string` field.

**Architecture:** Help-rendering metadata (`Usage`, `Aliases`, `Examples`) moves onto `cmdsys.Command` so a single new `cmdsys.RenderHelp(reg, verb)` function produces help text usable by every frontend. The console-side `verbDisplayMeta` map is deleted. Help-token interception happens once in `cmdsys.Dispatcher.Invoke` (post-RBAC, pre-parse, position-independent) and returns a typed `cmdsys.HelpResult{Verb, Text}` that the console's renderer prints verbatim.

**Tech Stack:** Go 1.22+, `pkg/cmdsys` (registry/dispatcher), `pkg/engine` (console), `chzyer/readline` keystroke listener.

**Spec:** [docs/superpowers/specs/2026-05-02-per-command-help-design.md](../specs/2026-05-02-per-command-help-design.md)

---

## File Map

**New files:**
- `pkg/cmdsys/help.go` — `RenderHelp` + `HelpResult` + auto-usage builder + section renderers
- `pkg/cmdsys/help_test.go` — RenderHelp unit tests

**Modified files:**
- `pkg/cmdsys/command.go` — add `Usage`, `Aliases`, `Examples` fields to `Command`
- `pkg/cmdsys/dispatcher.go` — help-token interception in `Invoke`
- `pkg/cmdsys/dispatcher_test.go` — help-trigger tests
- `pkg/engine/console_cmdsys.go` — drop `verbDisplayMeta`, change `registerTyped` signature, wire `HelpResult` rendering
- `pkg/engine/console.go` — change `Console.RegisterTyped` signature, route `?` listener and `printContextualHelp` through `cmdsys.RenderHelp`, route `help` command handler through `cmdsys.RenderHelp`
- `pkg/engine/console_help.go` — `buildHelpText` reads `Command.Usage`/`Aliases` directly; delete `printGroupHelp` (moved into `cmdsys/help.go`)
- `pkg/engine/console_cmdsys_test.go` — update `registerTyped` calls + add `HelpResult` rendering test
- `pkg/engine/console_completion_test.go` — update `registerTyped` calls
- `pkg/engine/builtins_config.go` — update `registerTyped` call
- `pkg/engine/builtins_log.go` — add `Examples` to high-value log commands
- `pkg/universe/builtins_cell.go` — add `Examples` to `cell split`/`cell merge`/`cell migrate`
- `pkg/universe/builtins_perf.go` — add `Examples` to `perf`
- `pkg/auth/console.go` — add `Examples` to high-value auth commands
- `examples/4node-basic/main.go` — add `Examples` to `bot spawn`

---

## Task 1: Add `Usage`, `Aliases`, `Examples` fields to `cmdsys.Command`

**Files:**
- Modify: `pkg/cmdsys/command.go:127-142`

- [ ] **Step 1: Open the file and locate the `Command` struct.**

The struct is at lines 127-142 in `pkg/cmdsys/command.go`. After the `Hidden bool` field, add three new fields.

- [ ] **Step 2: Edit the struct.**

Find this block:
```go
	// Hidden suppresses the verb from help listings and tab completion. The
	// command remains fully dispatchable — power users can still call it by
	// name. Used for internal worker verbs that user-facing frontends fan
	// out to (e.g. perf.snapshot, perf.reset behind `perf`).
	Hidden bool
}
```

Replace with:
```go
	// Hidden suppresses the verb from help listings and tab completion. The
	// command remains fully dispatchable — power users can still call it by
	// name. Used for internal worker verbs that user-facing frontends fan
	// out to (e.g. perf.snapshot, perf.reset behind `perf`).
	Hidden bool

	// Usage is an optional one-line usage hint shown by the help renderer,
	// e.g. "cell split <cellID> [--bypass]". When empty, RenderHelp
	// auto-derives one from the Args schema.
	Usage string

	// Aliases lists alternate display names. Routing always uses Verb;
	// aliases are display-only (e.g. ["h", "?"] on the "help" command).
	Aliases []string

	// Examples are concrete invocations rendered under the EXAMPLES section
	// of per-command help. Empty slice → no EXAMPLES section.
	Examples []string
}
```

- [ ] **Step 3: Run `go vet` to confirm no breakage.**

Run: `go vet ./pkg/cmdsys/...`
Expected: no output (success).

- [ ] **Step 4: Commit.**

```bash
git add pkg/cmdsys/command.go
git commit -m "cmdsys: add Usage/Aliases/Examples fields to Command"
```

---

## Task 2: Add `cmdsys.HelpResult` type and `RenderHelp` skeleton

**Files:**
- Create: `pkg/cmdsys/help.go`

- [ ] **Step 1: Create `pkg/cmdsys/help.go` with the public types and a stub `RenderHelp`.**

```go
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
```

- [ ] **Step 2: Verify it compiles.**

Run: `go vet ./pkg/cmdsys/...`
Expected: no output (success).

- [ ] **Step 3: Commit.**

```bash
git add pkg/cmdsys/help.go
git commit -m "cmdsys: add HelpResult type and RenderHelp skeleton"
```

---

## Task 3: TDD `RenderHelp` per-command rendering

**Files:**
- Create: `pkg/cmdsys/help_test.go`
- Modify: `pkg/cmdsys/help.go`

This task drives the full per-command help rendering through tests added one at a time.

- [ ] **Step 1: Create the test file with the first test.**

Create `pkg/cmdsys/help_test.go`:

```go
package cmdsys

import (
	"strings"
	"testing"
)

// helpTestCommand returns a Command with a representative shape used by
// the per-command help tests.
func helpTestCommand() Command {
	type splitArgs struct {
		CellID string `cmd:"help=target cell ID (e.g. \"0_0\")"`
		Bypass bool   `cmd:"named-only,optional,default=false,help=skip cooldown check"`
	}
	cmd := Command{
		Verb:        "cell.split",
		Capability:  "cell.split",
		Description: "split a cell into 4 children at +1 depth",
		Route:       RouteLocal,
		Args:        splitArgs{},
		Usage:       "cell split <cellID> [--bypass]",
		Examples: []string{
			"cell split 0_0",
			"cell split 0_0 --bypass",
		},
	}
	// Compute schema hashes the way Registry.Register does so cmd is realistic.
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	return cmd
}

func TestRenderHelp_BasicCommand(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(helpTestCommand()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "cell.split")

	// Title + description.
	if !strings.Contains(out, "cell split — split a cell into 4 children at +1 depth") {
		t.Errorf("missing title line. output:\n%s", out)
	}
	// USAGE section uses the explicit Usage field.
	if !strings.Contains(out, "USAGE\n    cell split <cellID> [--bypass]") {
		t.Errorf("missing USAGE block. output:\n%s", out)
	}
	// ARGUMENTS section.
	if !strings.Contains(out, "ARGUMENTS") {
		t.Errorf("missing ARGUMENTS section. output:\n%s", out)
	}
	if !strings.Contains(out, "CellID") {
		t.Errorf("expected positional CellID listed. output:\n%s", out)
	}
	// FLAGS section.
	if !strings.Contains(out, "FLAGS") {
		t.Errorf("missing FLAGS section. output:\n%s", out)
	}
	if !strings.Contains(out, "--bypass") {
		t.Errorf("expected --bypass flag listed. output:\n%s", out)
	}
	// EXAMPLES section.
	if !strings.Contains(out, "EXAMPLES") {
		t.Errorf("missing EXAMPLES section. output:\n%s", out)
	}
	if !strings.Contains(out, "cell split 0_0 --bypass") {
		t.Errorf("expected example line. output:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails.**

Run: `go test ./pkg/cmdsys/ -run TestRenderHelp_BasicCommand -v`
Expected: FAIL — output is empty so all `Contains` checks fail.

- [ ] **Step 3: Implement `renderCommandHelp` in `pkg/cmdsys/help.go`.**

Replace the stub `renderCommandHelp` with:

```go
func renderCommandHelp(reg *Registry, cmd Command) string {
	var b strings.Builder
	b.WriteString("\n")

	// Title: "<verb-with-spaces> — <Description>"
	displayVerb := strings.ReplaceAll(cmd.Verb, ".", " ")
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
			subDesc := ""
			if sc, ok := reg.Lookup(sv); ok {
				subDesc = sc.Description
			}
			fmt.Fprintf(&b, "    %-28s %s\n", sv, subDesc)
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
	fmt.Fprintf(b, "    %-14s %s%s\n", name, help, suffix)
}
```

- [ ] **Step 4: Run the first test — should pass.**

Run: `go test ./pkg/cmdsys/ -run TestRenderHelp_BasicCommand -v`
Expected: PASS.

- [ ] **Step 5: Add `TestRenderHelp_AutoUsage`.**

Append to `pkg/cmdsys/help_test.go`:

```go
func TestRenderHelp_AutoUsage(t *testing.T) {
	type fooArgs struct {
		Target string `cmd:"help=target name"`
		Force  bool   `cmd:"named-only,optional,default=false,help=force operation"`
	}
	cmd := Command{
		Verb:        "foo",
		Capability:  "foo",
		Description: "do foo",
		Route:       RouteLocal,
		Args:        fooArgs{},
		// Usage intentionally empty — must be auto-derived.
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	reg := NewRegistry()
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "foo")
	want := "foo <target> [--force]"
	if !strings.Contains(out, want) {
		t.Errorf("expected auto-usage %q in output:\n%s", want, out)
	}
}
```

- [ ] **Step 6: Run; should pass given the auto-usage builder.**

Run: `go test ./pkg/cmdsys/ -run TestRenderHelp_AutoUsage -v`
Expected: PASS.

- [ ] **Step 7: Add `TestRenderHelp_NoFlags_NoExamples`.**

```go
func TestRenderHelp_NoFlags_NoExamples(t *testing.T) {
	type emptyArgs struct{}
	cmd := Command{
		Verb:        "ping",
		Capability:  "ping",
		Description: "ping the server",
		Route:       RouteLocal,
		Args:        emptyArgs{},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	reg := NewRegistry()
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "ping")
	if strings.Contains(out, "ARGUMENTS") {
		t.Errorf("ARGUMENTS section should be elided. output:\n%s", out)
	}
	if strings.Contains(out, "FLAGS") {
		t.Errorf("FLAGS section should be elided. output:\n%s", out)
	}
	if strings.Contains(out, "EXAMPLES") {
		t.Errorf("EXAMPLES section should be elided. output:\n%s", out)
	}
	if !strings.Contains(out, "ping — ping the server") {
		t.Errorf("missing title. output:\n%s", out)
	}
	if !strings.Contains(out, "USAGE") {
		t.Errorf("USAGE section should always render. output:\n%s", out)
	}
}
```

- [ ] **Step 8: Run; should pass.**

Run: `go test ./pkg/cmdsys/ -run TestRenderHelp_NoFlags_NoExamples -v`
Expected: PASS.

- [ ] **Step 9: Add `TestRenderHelp_Aliases` and `TestRenderHelp_EnumAndDefaults`.**

```go
func TestRenderHelp_Aliases(t *testing.T) {
	type emptyArgs struct{}
	cmd := Command{
		Verb:        "help",
		Capability:  "help",
		Description: "show help",
		Route:       RouteLocal,
		Args:        emptyArgs{},
		Aliases:     []string{"h", "?"},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	reg := NewRegistry()
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "help")
	if !strings.Contains(out, "Aliases: h, ?") {
		t.Errorf("missing aliases line. output:\n%s", out)
	}
}

func TestRenderHelp_EnumAndDefaults(t *testing.T) {
	type colorArgs struct {
		Color string `cmd:"enum=red|green|blue,help=output color"`
		Loud  bool   `cmd:"named-only,optional,default=true,help=verbose mode"`
	}
	cmd := Command{
		Verb:        "tone",
		Capability:  "tone",
		Description: "set tone",
		Route:       RouteLocal,
		Args:        colorArgs{},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	reg := NewRegistry()
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "tone")
	if !strings.Contains(out, "enum: red|green|blue") {
		t.Errorf("missing enum annotation. output:\n%s", out)
	}
	if !strings.Contains(out, "default: true") {
		t.Errorf("missing default annotation. output:\n%s", out)
	}
}
```

- [ ] **Step 10: Run both — should pass.**

Run: `go test ./pkg/cmdsys/ -run "TestRenderHelp_Aliases|TestRenderHelp_EnumAndDefaults" -v`
Expected: PASS.

- [ ] **Step 11: Add `TestRenderHelp_UnknownVerb`.**

```go
func TestRenderHelp_UnknownVerb(t *testing.T) {
	reg := NewRegistry()
	out := RenderHelp(reg, "doesnotexist")
	if !strings.Contains(out, "unknown command") {
		t.Errorf("expected unknown command message. output:\n%s", out)
	}
}
```

- [ ] **Step 12: Run; should pass.**

Run: `go test ./pkg/cmdsys/ -run TestRenderHelp_UnknownVerb -v`
Expected: PASS.

- [ ] **Step 13: Add `TestRenderHelp_Group`.**

This requires the group-help branch to actually render content, so we'll fill in `renderGroupHelp` next.

```go
func TestRenderHelp_Group(t *testing.T) {
	type spawnArgs struct {
		Count  int    `cmd:"help=number of bots"`
		CellID string `cmd:"help=target cell ID"`
	}
	type clearArgs struct{}
	reg := NewRegistry()
	cmd1 := Command{Verb: "bot.spawn", Capability: "bot.spawn",
		Description: "spawn N bots in a cell", Route: RouteLocal, Args: spawnArgs{}}
	if h, err := schemaHashOf(cmd1.Args); err == nil {
		cmd1.ArgsSchemaHash = h
	}
	cmd2 := Command{Verb: "bot.clear", Capability: "bot.clear",
		Description: "remove all bots", Route: RouteLocal, Args: clearArgs{}}
	if h, err := schemaHashOf(cmd2.Args); err == nil {
		cmd2.ArgsSchemaHash = h
	}
	if err := reg.Register(cmd1); err != nil {
		t.Fatalf("Register bot.spawn: %v", err)
	}
	if err := reg.Register(cmd2); err != nil {
		t.Fatalf("Register bot.clear: %v", err)
	}
	out := RenderHelp(reg, "bot")
	if !strings.Contains(out, "bot commands:") {
		t.Errorf("missing group header. output:\n%s", out)
	}
	if !strings.Contains(out, "bot.spawn") || !strings.Contains(out, "bot.clear") {
		t.Errorf("missing sub-verb listing. output:\n%s", out)
	}
	if !strings.Contains(out, "spawn N bots in a cell") {
		t.Errorf("missing sub-verb description. output:\n%s", out)
	}
}
```

- [ ] **Step 14: Run — will fail because `renderGroupHelp` is still a stub.**

Run: `go test ./pkg/cmdsys/ -run TestRenderHelp_Group -v`
Expected: FAIL — output is empty.

- [ ] **Step 15: Implement `renderGroupHelp`.**

Replace the stub in `pkg/cmdsys/help.go`:

```go
func renderGroupHelp(reg *Registry, groupVerb string, subs []string) string {
	var b strings.Builder
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %s commands:\n", groupVerb)
	for _, sv := range subs {
		desc := ""
		usage := sv
		if cmd, ok := reg.Lookup(sv); ok {
			desc = cmd.Description
			if cmd.Usage != "" {
				usage = cmd.Usage
			}
		}
		fmt.Fprintf(&b, "    %-28s %s\n", usage, desc)
	}
	b.WriteString("\n")
	return b.String()
}
```

- [ ] **Step 16: Run — should pass.**

Run: `go test ./pkg/cmdsys/ -run TestRenderHelp_Group -v`
Expected: PASS.

- [ ] **Step 17: Add `TestRenderHelp_GroupShimWithSubcommands`.**

```go
func TestRenderHelp_GroupShimWithSubcommands(t *testing.T) {
	type subArgs struct {
		Sub string `cmd:"optional,rest"`
	}
	type catArgs struct {
		Category string
	}
	reg := NewRegistry()
	shim := Command{Verb: "log", Capability: "log",
		Description: "manage log categories", Route: RouteLocal, Args: subArgs{}}
	if h, err := schemaHashOf(shim.Args); err == nil {
		shim.ArgsSchemaHash = h
	}
	statusCmd := Command{Verb: "log.status", Capability: "log.status",
		Description: "show log categories on/off", Route: RouteLocal, Args: subArgs{}}
	if h, err := schemaHashOf(statusCmd.Args); err == nil {
		statusCmd.ArgsSchemaHash = h
	}
	onCmd := Command{Verb: "log.on", Capability: "log.on",
		Description: "enable a log category", Route: RouteLocal, Args: catArgs{}}
	if h, err := schemaHashOf(onCmd.Args); err == nil {
		onCmd.ArgsSchemaHash = h
	}
	if err := reg.Register(shim); err != nil {
		t.Fatalf("Register log: %v", err)
	}
	if err := reg.Register(statusCmd); err != nil {
		t.Fatalf("Register log.status: %v", err)
	}
	if err := reg.Register(onCmd); err != nil {
		t.Fatalf("Register log.on: %v", err)
	}
	out := RenderHelp(reg, "log")
	if !strings.Contains(out, "log — manage log categories") {
		t.Errorf("missing per-command title. output:\n%s", out)
	}
	if !strings.Contains(out, "SUBCOMMANDS") {
		t.Errorf("missing SUBCOMMANDS section. output:\n%s", out)
	}
	if !strings.Contains(out, "log.status") || !strings.Contains(out, "log.on") {
		t.Errorf("missing sub-verb entries. output:\n%s", out)
	}
}
```

- [ ] **Step 18: Run — should pass (SUBCOMMANDS is already wired in `renderCommandHelp`).**

Run: `go test ./pkg/cmdsys/ -run TestRenderHelp_GroupShimWithSubcommands -v`
Expected: PASS.

- [ ] **Step 19: Run the full cmdsys test suite to confirm no regressions.**

Run: `go test ./pkg/cmdsys/...`
Expected: PASS (all existing tests + new ones).

- [ ] **Step 20: Commit.**

```bash
git add pkg/cmdsys/help.go pkg/cmdsys/help_test.go
git commit -m "cmdsys: implement RenderHelp with USAGE/ARGUMENTS/FLAGS/EXAMPLES/SUBCOMMANDS sections"
```

---

## Task 4: Wire help-token interception into `Dispatcher.Invoke`

**Files:**
- Modify: `pkg/cmdsys/dispatcher.go:168-234` (insert interception after RBAC, before parse)
- Modify: `pkg/cmdsys/dispatcher_test.go` (add help-trigger tests)

- [ ] **Step 1: Add the first failing test.**

Open `pkg/cmdsys/dispatcher_test.go` and append:

```go
func TestDispatcher_HelpFlag_LongForm(t *testing.T) {
	type echoArgs struct{ Msg string }
	reg := NewRegistry()
	handlerCalled := false
	cmd := Command{
		Verb: "echo", Capability: "echo", Description: "echo a message",
		Route: RouteLocal, Args: echoArgs{},
		Handler: func(ctx context.Context, env *Env, raw any) (any, error) {
			handlerCalled = true
			return nil, nil
		},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	d := NewDispatcher(DispatcherConfig{Registry: reg})
	defer d.Close()

	caller := Caller{ID: "test", Source: SourceTest, Grants: []Grant{{Pattern: "*.*", Allow: true}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	res, err := d.Invoke(ctx, caller, "echo", "--help")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if handlerCalled {
		t.Errorf("handler should NOT be called when --help is present")
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("expected one OK target. got: %+v", res)
	}
	hr, ok := res.PerTarget[0].Result.(HelpResult)
	if !ok {
		t.Fatalf("expected HelpResult. got: %T = %+v", res.PerTarget[0].Result, res.PerTarget[0].Result)
	}
	if hr.Verb != "echo" {
		t.Errorf("HelpResult.Verb = %q; want \"echo\"", hr.Verb)
	}
	if !strings.Contains(hr.Text, "echo — echo a message") {
		t.Errorf("HelpResult.Text missing title. got:\n%s", hr.Text)
	}
}
```

If the imports `"context"`, `"strings"`, `"time"` aren't already in the file, add them.

- [ ] **Step 2: Run — should fail (handler runs, no HelpResult).**

Run: `go test ./pkg/cmdsys/ -run TestDispatcher_HelpFlag_LongForm -v`
Expected: FAIL — handler is called or result type wrong.

- [ ] **Step 3: Add help-token detection to `Invoke`.**

In `pkg/cmdsys/dispatcher.go`, find the block immediately after the RBAC check (after `if !Check(caller, cmd.Capability) { ... }` returns) and BEFORE `// Parse / coerce args.`. Insert:

```go
	// Help-token interception. If the raw args contain --help, -h, or ? as a
	// standalone token (any position), skip parsing and the handler entirely
	// and return a HelpResult. This is dispatcher-level so every frontend
	// (console, HTTP, future remote CLI) gets help for free.
	if rawStr, isStr := raw.(string); isStr {
		if tokens, terr := tokenize(rawStr); terr == nil {
			for _, tok := range tokens {
				if IsHelpToken(tok) {
					emitDone(true, "", "", []string{"local"})
					return Result{
						Verb:       verb,
						Caller:     caller,
						TraceID:    traceID,
						PerTarget:  []TargetResult{{TargetID: "local", OK: true, Result: HelpResult{Verb: verb, Text: RenderHelp(d.registry, verb)}}},
						DurationMS: time.Since(start).Milliseconds(),
					}, nil
				}
			}
		}
	}
```

- [ ] **Step 4: Run the test — should pass.**

Run: `go test ./pkg/cmdsys/ -run TestDispatcher_HelpFlag_LongForm -v`
Expected: PASS.

- [ ] **Step 5: Add the remaining help-trigger tests.**

Append to `pkg/cmdsys/dispatcher_test.go`:

```go
func TestDispatcher_HelpFlag_ShortForm(t *testing.T) {
	reg, d, caller, ctx, cancel := setupHelpTriggerHarness(t)
	defer cancel()
	defer d.Close()
	res, err := d.Invoke(ctx, caller, "echo", "-h")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if _, ok := res.PerTarget[0].Result.(HelpResult); !ok {
		t.Fatalf("expected HelpResult; got %T", res.PerTarget[0].Result)
	}
	_ = reg
}

func TestDispatcher_HelpFlag_QuestionMark(t *testing.T) {
	reg, d, caller, ctx, cancel := setupHelpTriggerHarness(t)
	defer cancel()
	defer d.Close()
	res, err := d.Invoke(ctx, caller, "echo", "?")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if _, ok := res.PerTarget[0].Result.(HelpResult); !ok {
		t.Fatalf("expected HelpResult; got %T", res.PerTarget[0].Result)
	}
	_ = reg
}

func TestDispatcher_HelpFlag_AnyPosition(t *testing.T) {
	reg, d, caller, ctx, cancel := setupHelpTriggerHarness(t)
	defer cancel()
	defer d.Close()
	res, err := d.Invoke(ctx, caller, "echo", "hello --help")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if _, ok := res.PerTarget[0].Result.(HelpResult); !ok {
		t.Fatalf("expected HelpResult; got %T", res.PerTarget[0].Result)
	}
	_ = reg
}

// setupHelpTriggerHarness builds a registry+dispatcher with an "echo"
// command whose handler records whether it ran. Returns (reg, dispatcher,
// caller, ctx, cancel). The handler-called bool is asserted via package-
// level `helpTriggerHandlerCalled`, reset on each setup call.
var helpTriggerHandlerCalled bool

func setupHelpTriggerHarness(t *testing.T) (*Registry, *Dispatcher, Caller, context.Context, context.CancelFunc) {
	t.Helper()
	helpTriggerHandlerCalled = false
	type echoArgs struct{ Msg string }
	reg := NewRegistry()
	cmd := Command{
		Verb: "echo", Capability: "echo", Description: "echo a message",
		Route: RouteLocal, Args: echoArgs{},
		Handler: func(ctx context.Context, env *Env, raw any) (any, error) {
			helpTriggerHandlerCalled = true
			return nil, nil
		},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	d := NewDispatcher(DispatcherConfig{Registry: reg})
	caller := Caller{ID: "test", Source: SourceTest, Grants: []Grant{{Pattern: "*.*", Allow: true}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	return reg, d, caller, ctx, cancel
}

func TestDispatcher_HelpFlag_HandlerNotCalled(t *testing.T) {
	_, d, caller, ctx, cancel := setupHelpTriggerHarness(t)
	defer cancel()
	defer d.Close()
	if _, err := d.Invoke(ctx, caller, "echo", "--help"); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if helpTriggerHandlerCalled {
		t.Errorf("handler must not be called when --help is present")
	}
}
```

- [ ] **Step 6: Run all four — should pass.**

Run: `go test ./pkg/cmdsys/ -run "TestDispatcher_HelpFlag_" -v`
Expected: PASS for all four tests.

- [ ] **Step 7: Run the full cmdsys test suite to confirm no regressions.**

Run: `go test ./pkg/cmdsys/...`
Expected: PASS for all tests.

- [ ] **Step 8: Commit.**

```bash
git add pkg/cmdsys/dispatcher.go pkg/cmdsys/dispatcher_test.go
git commit -m "cmdsys: intercept --help/-h/? in Dispatcher.Invoke and return HelpResult"
```

---

## Task 5: Migrate `registerTyped`/`Console.RegisterTyped` to single-arg signature

This task drops the `usage`/`aliases` parameters from `cmdsysAdapter.registerTyped` and `Console.RegisterTyped` and removes `verbDisplayMeta`. Top-level help listing reads `Usage`/`Aliases`/`Description` straight off `Command`.

**Files:**
- Modify: `pkg/engine/console_cmdsys.go` (drop `verbDisplayMeta`, change signature)
- Modify: `pkg/engine/console.go` (signature change, registration call sites)
- Modify: `pkg/engine/console_help.go` (`buildHelpText` reads from `Command`; delete `printGroupHelp`/`sortedSubVerbs` — moved to `pkg/cmdsys/help.go`)
- Modify: `pkg/engine/builtins_config.go` (call site)
- Modify: `pkg/engine/console_cmdsys_test.go` (call sites)
- Modify: `pkg/engine/console_completion_test.go` (call sites)

- [ ] **Step 1: Update `cmdsysAdapter.registerTyped` to take only `cmd cmdsys.Command`.**

In `pkg/engine/console_cmdsys.go`, replace:

```go
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
// Registry and Dispatcher instances. Used by Process.startConsole so the
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
```

with:

```go
type cmdsysAdapter struct {
	Registry   *cmdsys.Registry
	Dispatcher *cmdsys.Dispatcher
}

func newCmdsysAdapter() *cmdsysAdapter {
	reg := cmdsys.NewRegistry()
	d := cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry: reg,
	})
	return &cmdsysAdapter{
		Registry:   reg,
		Dispatcher: d,
	}
}

// newCmdsysAdapterWith creates a cmdsysAdapter backed by externally-owned
// Registry and Dispatcher instances. Used by Process.startConsole so the
// console shares the coordinator's command pipeline.
func newCmdsysAdapterWith(reg *cmdsys.Registry, d *cmdsys.Dispatcher) *cmdsysAdapter {
	return &cmdsysAdapter{
		Registry:   reg,
		Dispatcher: d,
	}
}

// registerTyped registers a typed cmdsys.Command. Display metadata
// (Usage, Aliases, Examples) lives on the Command itself.
func (a *cmdsysAdapter) registerTyped(cmd cmdsys.Command) error {
	return a.Registry.Register(cmd)
}
```

- [ ] **Step 2: Update the group-shim registrar in the same file.**

Find:

```go
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
			if sub == "" || sub == "?" {
				// Default: show help for the group. "?" is the discoverability
				// shortcut — `cell ?` is equivalent to bare `cell`.
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
```

Replace with:

```go
func (a *cmdsysAdapter) registerGroupShim(verb, description string) error {
	av := a // capture
	cmd := cmdsys.Command{
		Verb:        verb,
		Capability:  cmdsys.Capability(verb),
		Description: description,
		Route:       cmdsys.RouteLocal,
		Args:        groupDispatchArgs{},
		Result:      nil,
		Usage:       verb,
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(groupDispatchArgs)
			sub := strings.TrimSpace(args.Sub)
			if sub == "" {
				// Bare invocation prints the group's help (per-command help
				// with SUBCOMMANDS section appended via RenderHelp).
				fmt.Print(cmdsys.RenderHelp(av.Registry, verb))
				return nil, nil
			}
			// Re-dispatch as "verb.firstword rest…". Note: "verb ?" never
			// reaches here because Dispatcher.Invoke intercepts ? before
			// the handler runs.
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
	return a.Registry.Register(cmd)
}
```

- [ ] **Step 3: Wire `HelpResult` rendering in `renderDispatchResult`.**

In `pkg/engine/console_cmdsys.go`, find the function `renderDispatchResult` and the helper it calls per result. Update the per-target rendering branch so that a `cmdsys.HelpResult` prints `Text` verbatim instead of going through `renderResult`.

Replace this block in `renderDispatchResult`:

```go
	if len(res.PerTarget) == 1 {
		tr := res.PerTarget[0]
		if !tr.OK {
			return fmt.Sprintf("  error: %s\n", tr.Error)
		}
		if tr.Result == nil {
			return ""
		}
		return renderResult(tr.Result)
	}
```

with:

```go
	if len(res.PerTarget) == 1 {
		tr := res.PerTarget[0]
		if !tr.OK {
			return fmt.Sprintf("  error: %s\n", tr.Error)
		}
		if tr.Result == nil {
			return ""
		}
		if hr, ok := tr.Result.(cmdsys.HelpResult); ok {
			return hr.Text
		}
		return renderResult(tr.Result)
	}
```

And in the multi-target loop further down, replace the body that calls `renderResult(tr.Result)` similarly:

Find:

```go
		if tr.Result == nil {
			fmt.Fprintln(&sb, "  (no result)")
			continue
		}
		sb.WriteString(renderResult(tr.Result))
```

Replace with:

```go
		if tr.Result == nil {
			fmt.Fprintln(&sb, "  (no result)")
			continue
		}
		if hr, ok := tr.Result.(cmdsys.HelpResult); ok {
			sb.WriteString(hr.Text)
			continue
		}
		sb.WriteString(renderResult(tr.Result))
```

Also update `DispatchRaw`'s tail to handle `HelpResult`. Find:

```go
	tr := res.PerTarget[0]
	if !tr.OK {
		return fmt.Sprintf("  error: %s\n", tr.Error)
	}
	if tr.Result == nil {
		return ""
	}
	return renderResult(tr.Result)
}
```

Replace with:

```go
	tr := res.PerTarget[0]
	if !tr.OK {
		return fmt.Sprintf("  error: %s\n", tr.Error)
	}
	if tr.Result == nil {
		return ""
	}
	if hr, ok := tr.Result.(cmdsys.HelpResult); ok {
		return hr.Text
	}
	return renderResult(tr.Result)
}
```

- [ ] **Step 4: Update `Console.RegisterTyped` signature in `pkg/engine/console.go`.**

Find:

```go
// RegisterTyped adds a fully typed cmdsys.Command plus optional display metadata.
func (c *Console) RegisterTyped(cmd cmdsys.Command, usage string, aliases []string) error {
	return c.adapter.registerTyped(cmd, usage, aliases)
}
```

Replace with:

```go
// RegisterTyped registers a typed cmdsys.Command. Display metadata
// (Usage, Aliases, Examples) lives on the Command itself.
func (c *Console) RegisterTyped(cmd cmdsys.Command) error {
	return c.adapter.registerTyped(cmd)
}
```

- [ ] **Step 5: Update `registerPlatformCommands` in `pkg/engine/console.go` (around line 262).**

Find:

```go
func (c *Console) registerPlatformCommands() {
	mustReg := func(cmd cmdsys.Command, usage string, aliases []string) {
		if err := c.adapter.registerTyped(cmd, usage, aliases); err != nil {
			panic(fmt.Sprintf("console: registerPlatformCommands %q: %v", cmd.Verb, err))
		}
	}

	// help
	mustReg(cmdsys.Command{
		Verb:        "help",
		Capability:  "help",
		Description: "show help (optionally for a specific command or group)",
		Route:       cmdsys.RouteLocal,
		Args:        helpArgs{},
		Result:      helpResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(helpArgs)
			name := strings.TrimSpace(args.Name)
			if name == "" {
				fmt.Print(c.adapter.buildHelpText(c.builtinCats))
				c.printStatusFooter()
				return helpResult{}, nil
			}
			if subs := c.adapter.sortedSubVerbs(name); len(subs) > 0 {
				fmt.Print(c.adapter.printGroupHelp(name))
				return helpResult{}, nil
			}
			if _, found := c.adapter.Registry.Lookup(name); found {
				meta := c.adapter.verbMeta[name]
				usage := meta.usage
				if usage == "" {
					usage = name
				}
				fmt.Println()
				fmt.Printf("  %s\n", usage)
				if meta.description != "" {
					fmt.Printf("  %s\n", meta.description)
				}
				fmt.Println()
				return helpResult{}, nil
			}
			fmt.Printf("  unknown command: %s\n", name)
			return helpResult{}, nil
		},
	}, "help [command|group]", []string{"h", "?"})

	// quit
	mustReg(cmdsys.Command{
		Verb:        "quit",
		Capability:  "quit",
		Description: "stop the server (Ctrl+C)",
		Route:       cmdsys.RouteLocal,
		Args:        nil,
		Result:      nil,
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			fmt.Println("  use Ctrl+C to stop the server")
			return nil, nil
		},
	}, "quit", []string{"q", "exit"})

	// log group typed commands
	if err := registerLogBuiltins(c.adapter.Registry, c.log); err != nil {
		panic(fmt.Sprintf("console: registerLogBuiltins: %v", err))
	}
	// Register metadata for log verbs (they're already in the registry; add display meta).
	logVerbs := []struct{ verb, usage, desc string }{
		{"log.status", "log status", "show log categories on/off"},
		{"log.on", "log on <cat|all>", "enable log category"},
		{"log.off", "log off <cat|all>", "disable log category"},
		{"log.toggle", "log toggle <cat>", "toggle log category"},
		{"log.only", "log only <cat> [cat...]", "enable only these, disable rest"},
		{"log.filter", "log filter [cat pattern | clear [cat]]", "set/show/clear message filters"},
	}
	for _, lv := range logVerbs {
		// registerTyped will fail with duplicate, so we only add meta via verbMeta directly.
		// The commands are already registered; just add display metadata.
		if _, exists := c.adapter.verbMeta[lv.verb]; !exists {
			c.adapter.verbOrder = append(c.adapter.verbOrder, lv.verb)
			c.adapter.verbMeta[lv.verb] = verbDisplayMeta{
				category:    "log",
				description: lv.desc,
				usage:       lv.usage,
			}
		}
	}
	// Top-level "log" group dispatch entry.
	_ = c.adapter.registerGroupShim("log", "manage log categories")
}
```

Replace with:

```go
func (c *Console) registerPlatformCommands() {
	mustReg := func(cmd cmdsys.Command) {
		if err := c.adapter.registerTyped(cmd); err != nil {
			panic(fmt.Sprintf("console: registerPlatformCommands %q: %v", cmd.Verb, err))
		}
	}

	// help
	mustReg(cmdsys.Command{
		Verb:        "help",
		Capability:  "help",
		Description: "show help (optionally for a specific command or group)",
		Route:       cmdsys.RouteLocal,
		Args:        helpArgs{},
		Result:      helpResult{},
		Usage:       "help [command|group]",
		Aliases:     []string{"h", "?"},
		Examples: []string{
			"help",
			"help cell.split",
			"help bot",
		},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(helpArgs)
			name := strings.TrimSpace(args.Name)
			if name == "" {
				fmt.Print(c.adapter.buildHelpText(c.builtinCats))
				c.printStatusFooter()
				return helpResult{}, nil
			}
			fmt.Print(cmdsys.RenderHelp(c.adapter.Registry, name))
			return helpResult{}, nil
		},
	})

	// quit
	mustReg(cmdsys.Command{
		Verb:        "quit",
		Capability:  "quit",
		Description: "stop the server (Ctrl+C)",
		Route:       cmdsys.RouteLocal,
		Args:        nil,
		Result:      nil,
		Usage:       "quit",
		Aliases:     []string{"q", "exit"},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			fmt.Println("  use Ctrl+C to stop the server")
			return nil, nil
		},
	})

	// log group typed commands
	if err := registerLogBuiltins(c.adapter.Registry, c.log); err != nil {
		panic(fmt.Sprintf("console: registerLogBuiltins: %v", err))
	}
	// Top-level "log" group dispatch entry.
	_ = c.adapter.registerGroupShim("log", "manage log categories")
}
```

- [ ] **Step 6: Update the readline `?` listener and `printContextualHelp` in `pkg/engine/console.go`.**

Find:

```go
		Listener: readline.FuncListener(func(line []rune, pos int, key rune) ([]rune, int, bool) {
			if key != '?' {
				return nil, 0, false
			}
			prefix := strings.TrimSpace(strings.TrimRight(string(line[:pos]), "?"))
			c.Print("\n")
			c.printContextualHelp(prefix)
			return []rune{}, 0, true
		}),
```

Leave as-is (the listener still calls `printContextualHelp`).

Find `printContextualHelp` and replace its body:

```go
func (c *Console) printContextualHelp(prefix string) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		c.printHelp()
		return
	}

	tokens := strings.Fields(prefix)

	if len(tokens) == 1 {
		if subs := c.adapter.sortedSubVerbs(tokens[0]); len(subs) > 0 {
			c.Print(c.adapter.printGroupHelp(tokens[0]))
			return
		}
	}

	verb, _, _ := c.adapter.resolveDottedVerb(tokens)
	if verb != "" {
		meta := c.adapter.verbMeta[verb]
		usage := meta.usage
		if usage == "" {
			usage = verb
		}
		desc := meta.description
		if desc == "" {
			if cmd, ok := c.adapter.Registry.Lookup(verb); ok {
				desc = cmd.Description
			}
		}
		c.Printf("\n  %s\n", usage)
		if desc != "" {
			c.Printf("  %s\n", desc)
		}
		c.Print("\n")
		return
	}

	c.Printf("  no help for %q\n", prefix)
	c.printHelp()
}
```

with:

```go
func (c *Console) printContextualHelp(prefix string) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		c.printHelp()
		return
	}
	tokens := strings.Fields(prefix)
	// Try longest-match dotted verb first ("auth session list" → "auth.session.list").
	if verb, _, ok := c.adapter.resolveDottedVerb(tokens); ok {
		c.Print(cmdsys.RenderHelp(c.adapter.Registry, verb))
		return
	}
	// Single-token namespace prefix with sub-verbs (e.g. "bot").
	if len(tokens) == 1 {
		c.Print(cmdsys.RenderHelp(c.adapter.Registry, tokens[0]))
		return
	}
	c.Printf("  no help for %q\n", prefix)
	c.printHelp()
}
```

- [ ] **Step 7: Update the unknown-command branch in `Console.Run` to use `cmdsys.RenderHelp` for namespace fallback.**

Find this block in `Console.Run`:

```go
		// No verb found. If the user typed a namespace that has sub-verbs
		// (e.g. "bot", "bot ?", "bot help"), print the group's help listing
		// instead of an "unknown command" error. This makes bot/cell/host
		// discoverable even without a top-level group shim.
		if subs := c.adapter.sortedSubVerbs(verb); len(subs) > 0 {
			c.Print(c.adapter.printGroupHelp(verb))
			continue
		}
```

Replace with:

```go
		// No verb found. If the user typed a namespace that has sub-verbs
		// (e.g. "bot", "bot ?", "bot help"), print the group's help listing
		// instead of an "unknown command" error.
		if hasSubVerbs(c.adapter.Registry, verb) {
			c.Print(cmdsys.RenderHelp(c.adapter.Registry, verb))
			continue
		}
```

Add this helper at the bottom of `pkg/engine/console.go`:

```go
// hasSubVerbs reports whether reg has at least one verb starting with
// "<groupVerb>." (excluding hidden commands).
func hasSubVerbs(reg *cmdsys.Registry, groupVerb string) bool {
	prefix := groupVerb + "."
	for _, v := range reg.List() {
		if !strings.HasPrefix(v, prefix) {
			continue
		}
		if cmd, ok := reg.Lookup(v); ok && cmd.Hidden {
			continue
		}
		return true
	}
	return false
}
```

- [ ] **Step 8: Replace `buildHelpText` to read directly from `Command`.**

In `pkg/engine/console_help.go`, replace the entire file with:

```go
package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zenion/mmokit/pkg/cmdsys"
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
					usage := shim.Usage
					if usage == "" {
						usage = groupVerb
					}
					fmt.Fprintf(&b, "    %-32s %s\n", usage, shim.Description)
					continue
				}
				cmd, ok := a.Registry.Lookup(v)
				if !ok {
					continue
				}
				usage := cmd.Usage
				if usage == "" {
					usage = v
				}
				fmt.Fprintf(&b, "    %-32s %s\n", usage, cmd.Description)
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
			usage := cmd.Usage
			if usage == "" {
				usage = v
			}
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

	// Use cmdsys.RenderHelp's helpers for symmetry — but we don't actually
	// need anything else here. Reference cmdsys to avoid an unused-import.
	_ = cmdsys.IsHelpToken
	return b.String()
}
```

This deletes the old `sortedSubVerbs` and `printGroupHelp` (their group-rendering responsibility moved to `cmdsys.RenderHelp`).

- [ ] **Step 9: Update `pkg/engine/builtins_config.go:22` registration call.**

Find:

```go
		if err := c.adapter.registerTyped(cmd, usage, nil); err != nil {
			panic(fmt.Sprintf("console: registerTyped %q: %v", cmd.Verb, err))
		}
```

Replace with (assuming `usage` is the local variable currently passed):

```go
		cmd.Usage = usage
		if err := c.adapter.registerTyped(cmd); err != nil {
			panic(fmt.Sprintf("console: registerTyped %q: %v", cmd.Verb, err))
		}
```

The surrounding code likely passes a `cmd` and `usage` variable into a local helper. If `cmd` is not addressable (e.g. it's a function parameter), assign to a local variable first:

```go
		c := cmd
		c.Usage = usage
		if err := c.adapter.registerTyped(c); err != nil {
			panic(fmt.Sprintf("console: registerTyped %q: %v", c.Verb, err))
		}
```

Adjust based on the actual surrounding code.

- [ ] **Step 10: Update test fixtures.**

In `pkg/engine/console_cmdsys_test.go`, find every call of the form:

```go
err := a.registerTyped(cmdsys.Command{
    ...
}, "test.echo <message>", nil)
```

Convert to:

```go
err := a.registerTyped(cmdsys.Command{
    ...
    Usage: "test.echo <message>",
})
```

For calls with non-nil aliases, set `Aliases` similarly. Apply to all five `registerTyped` calls in the file (lines 37, 122, 154, 193, 232).

In `pkg/engine/console_completion_test.go`, do the same for the five calls at lines 47, 90, 136, 174, 180.

- [ ] **Step 11: Build and run all engine tests.**

Run: `go vet ./pkg/engine/... && go test ./pkg/engine/...`
Expected: PASS for all tests.

- [ ] **Step 12: Run the full test suite.**

Run: `go test ./...`
Expected: PASS for all packages.

- [ ] **Step 13: Build the server to confirm no breakage at link time.**

Run: `just build`
Expected: succeeds with `bin/server` produced.

- [ ] **Step 14: Commit.**

```bash
git add pkg/engine/console.go pkg/engine/console_cmdsys.go pkg/engine/console_help.go pkg/engine/console_cmdsys_test.go pkg/engine/console_completion_test.go pkg/engine/builtins_config.go
git commit -m "engine: drop verbDisplayMeta; route help through cmdsys.RenderHelp"
```

---

## Task 6: Add console-level help end-to-end tests

**Files:**
- Modify: `pkg/engine/console_cmdsys_test.go`

- [ ] **Step 1: Add a `HelpResult` rendering test.**

Append to `pkg/engine/console_cmdsys_test.go`:

```go
func TestConsole_HelpResult_RendersText(t *testing.T) {
	type echoArgs struct {
		Message string `cmd:"help=text to echo"`
	}
	a := newTestAdapter()
	err := a.registerTyped(cmdsys.Command{
		Verb:        "test.echo",
		Capability:  "test.echo",
		Description: "echo a message",
		Route:       cmdsys.RouteLocal,
		Args:        echoArgs{},
		Result:      nil,
		Usage:       "test.echo <message>",
		Examples:    []string{"test.echo hello"},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("registerTyped: %v", err)
	}
	out := a.Dispatch("test.echo --help")
	if !strings.Contains(out, "test echo — echo a message") {
		t.Errorf("expected help title in output. got:\n%s", out)
	}
	if !strings.Contains(out, "USAGE") {
		t.Errorf("expected USAGE section. got:\n%s", out)
	}
	if !strings.Contains(out, "EXAMPLES") {
		t.Errorf("expected EXAMPLES section. got:\n%s", out)
	}
	if strings.Contains(out, "{") {
		t.Errorf("output should not contain typed-struct braces. got:\n%s", out)
	}
}

func TestConsole_QuestionMarkSuffix_End2End(t *testing.T) {
	type splitArgs struct {
		CellID string `cmd:"help=target cell ID"`
	}
	a := newTestAdapter()
	err := a.registerTyped(cmdsys.Command{
		Verb:        "cell.split",
		Capability:  "cell.split",
		Description: "split a cell",
		Route:       cmdsys.RouteLocal,
		Args:        splitArgs{},
		Usage:       "cell split <cellID>",
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("registerTyped: %v", err)
	}
	out := a.Dispatch("cell split ?")
	if !strings.Contains(out, "cell split — split a cell") {
		t.Errorf("expected per-command help. got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run.**

Run: `go test ./pkg/engine/ -run "TestConsole_HelpResult_RendersText|TestConsole_QuestionMarkSuffix_End2End" -v`
Expected: PASS.

- [ ] **Step 3: Commit.**

```bash
git add pkg/engine/console_cmdsys_test.go
git commit -m "engine: add per-command help end-to-end tests"
```

---

## Task 7: Add `Examples` to high-value built-in commands

This task adds concrete `Examples` entries to the most-used commands so operators encounter useful help on day one. Other commands can be filled in incrementally.

**Files:**
- Modify: `pkg/engine/builtins_log.go`
- Modify: `pkg/universe/builtins_cell.go`
- Modify: `pkg/universe/builtins_perf.go`
- Modify: `pkg/auth/console.go`
- Modify: `examples/4node-basic/command_bots.go` (bot commands live here, not main.go)

- [ ] **Step 1: Add Examples to log commands.**

In `pkg/engine/builtins_log.go`, find the `log.on` registration (around line 83) and add `Examples`:

```go
	mustReg(cmdsys.Command{
		Verb:        "log.on",
		Capability:  "log.on",
		Description: "enable a log category (or 'all')",
		Route:       cmdsys.RouteLocal,
		Args:        logOnArgs{},
		Result:      logOnResult{},
		Usage:       "log on <cat|all>",
		Examples: []string{
			"log on combat",
			"log on all",
			"log on combat mining economy",
		},
		Handler: ...
	})
```

For `log.off` (line 105), `log.toggle` (line 127), `log.only` (line 152), `log.filter` (line 171), add similarly themed `Usage` + `Examples` entries. Use the verbs already documented in the original `logVerbs` table from `console.go`:

- `log.status` → `Usage: "log status"`
- `log.off` → `Usage: "log off <cat|all>"`, `Examples: []string{"log off combat", "log off all"}`
- `log.toggle` → `Usage: "log toggle <cat>"`, `Examples: []string{"log toggle combat"}`
- `log.only` → `Usage: "log only <cat> [cat...]"`, `Examples: []string{"log only combat", "log only combat mining"}`
- `log.filter` → `Usage: "log filter [cat pattern | clear [cat]]"`, `Examples: []string{"log filter combat \"laser hit\"", "log filter clear", "log filter clear combat"}`

- [ ] **Step 2: Add Examples to `cell split`/`cell merge`/`cell migrate` in `pkg/universe/builtins_cell.go`.**

Locate each registration in that file and add fields. For `cell.split`:

```go
	cmd := cmdsys.Command{
		Verb:        "cell.split",
		// ... existing fields ...
		Usage:    "cell split <cellID> [--bypass]",
		Examples: []string{"cell split 0_0", "cell split 0_0 --bypass"},
	}
```

For `cell.merge`: `Usage: "cell merge <cellID> [--bypass]"`, `Examples: []string{"cell merge 0_0", "cell merge 0_0/0 --bypass"}`.

For `cell.migrate`: `Usage: "cell migrate <cellID> <hostID>"`, `Examples: []string{"cell migrate 0_0 host-2"}`.

- [ ] **Step 3: Add Examples to `perf` in `pkg/universe/builtins_perf.go`.**

For the user-facing `perf` verb, add `Usage: "perf [--reset]"` and `Examples: []string{"perf", "perf --reset"}` (verify the actual flag name from the file before committing).

- [ ] **Step 4: Add Examples to a couple of auth commands in `pkg/auth/console.go`.**

For at least `auth.user.lock` and `auth.session.list` (or whichever verbs exist there): set a `Usage` and an `Examples` entry. Examples:

- `auth user create <username> <password>` → `Examples: []string{"auth user create alice s3cret"}`
- `auth user lock <username>` → `Examples: []string{"auth user lock alice"}`

Use whatever verbs are actually registered in the file.

- [ ] **Step 5: Add Examples to `bot.spawn` in `examples/4node-basic/main.go`.**

Locate the `bot.spawn` registration (it uses `RouteSpecificCell`) and set `Usage: "bot spawn <count> <cellID>"`, `Examples: []string{"bot spawn 30 0_0", "bot spawn 100 cell_0_0"}`.

- [ ] **Step 6: Build the server.**

Run: `just build`
Expected: succeeds.

- [ ] **Step 7: Run all tests.**

Run: `go test ./...`
Expected: PASS for all packages.

- [ ] **Step 8: Commit.**

```bash
git add pkg/engine/builtins_log.go pkg/universe/builtins_cell.go pkg/universe/builtins_perf.go pkg/auth/console.go examples/4node-basic/main.go
git commit -m "commands: add Examples to high-value built-in commands"
```

---

## Task 8: Manual smoke test

**Files:** None (manual verification only).

- [ ] **Step 1: Start the dev server.**

Run: `just dev`
Expected: server starts on `:8080`, console prompt appears.

- [ ] **Step 2: Verify trigger forms.**

In the console, type each of the following and visually confirm structured per-command help renders:

```
cell split --help
cell split -h
cell split ?
bot spawn --help
log on --help
log --help
help cell.split
help bot
?
```

Each of `--help`, `-h`, `?` for the same verb must produce identical output. `log --help` must include a `SUBCOMMANDS` section listing `log.status`, `log.on`, `log.off`, etc. `bot --help` (if `bot` has no shim) must produce group help with the sub-verb table. Bare `?` must produce the full categorized help listing.

- [ ] **Step 3: Verify mid-typing `?` keystroke.**

Type `cell split` (do NOT press Enter), then press `?`. The buffer should clear and per-command help for `cell.split` should print. Compare visually with `cell split --help` — output should match.

- [ ] **Step 4: Verify `--json`.**

Run `cell split --help --json` and confirm output is valid JSON with shape `{"verb": "cell.split", "text": "..."}` (the text field contains the full multi-line help string).

- [ ] **Step 5: Stop the server (Ctrl+C).**

If any step fails, fix and re-test. If all pass, the implementation is complete.

---

## Self-Review Notes (planner-only — implementer can skip)

- Spec coverage: every spec section has a matching task. Data-model changes → Task 1; HelpResult + RenderHelp → Tasks 2–3; trigger interception → Task 4; verbDisplayMeta deletion + signature migration + console rendering → Task 5; e2e tests → Task 6; high-value Examples → Task 7; manual smoke → Task 8.
- No placeholders: every code block is complete; every command shows expected output.
- Type consistency: `HelpResult{Verb, Text}` used identically across cmdsys, dispatcher, and console. `cmdsys.IsHelpToken` used in dispatcher. `Command.Usage`/`Aliases`/`Examples` named consistently.
