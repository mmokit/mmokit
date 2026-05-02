# Per-Command Help (`<verb> --help`/`-h`/`?`)

**Status:** Approved
**Date:** 2026-05-02

## Problem

The command system has no per-command help. Operators can list verbs (`help`) and see a one-line description, but there is no way to discover a specific command's flags, positional arguments, defaults, enum values, or example usage. Today:

- `help <verb>` and the `?` keystroke listener both render only `Command.Description` plus a one-line `usage` hint stored in `verbDisplayMeta`.
- `<verb> --help` and `<verb> -h` are not intercepted; they hit the parser as unknown flags and error out.
- `<verb> ?` (trailing `?` as a separate token) is not intercepted either.
- `FieldSchema` already carries rich per-field metadata (`Required`, `Default`, `Enum`, `Help`, `NamedOnly`) that nothing renders.

The goal is parity with how `git --help`, `kubectl --help`, etc. document themselves: type any of `--help`, `-h`, or `?` after a command and get a structured breakdown of its inputs and a couple of example invocations.

## Goals

1. `<verb> --help`, `<verb> -h`, and `<verb> ?` all render the same per-command help.
2. Help renders from the command's existing schema metadata (no boilerplate) plus an optional `Examples []string` field.
3. The renderer lives in `pkg/cmdsys/` so every frontend (console, HTTP `/commands`, future remote CLI, future in-game chat) reuses the same output.
4. Existing usage/aliases display metadata, currently console-side `verbDisplayMeta`, moves onto `cmdsys.Command` so the renderer has a single source of truth.

## Non-Goals

- No long-form prose body / man-page sections. `Description` stays one line.
- No localization, color, or ANSI styling. Plain text only.
- No auto-generated examples. Examples are author-provided strings.
- No per-flag examples or "see also" cross-references.

## Design

### Data model: `cmdsys.Command` extensions

Three new fields on `pkg/cmdsys/command.go`:

```go
type Command struct {
    // ... existing fields (Verb, Capability, Description, Route, Args, Result, Handler,
    //                     ArgsSchemaHash, ResultSchemaHash, Hidden) ...

    // Usage is an optional one-line usage hint shown in help and the help
    // listing, e.g. "cell split <cellID> [--bypass]". When empty, the
    // renderer auto-derives one from the Args schema.
    Usage string

    // Aliases lists alternate names for display only. Routing always uses
    // Verb. e.g. ["h", "?"] on the "help" command.
    Aliases []string

    // Examples are concrete invocations rendered under the EXAMPLES section
    // of per-command help. Empty slice → no EXAMPLES section.
    Examples []string
}
```

The console-side `verbDisplayMeta` struct (in `pkg/engine/console_cmdsys.go`) is deleted. Its `category` field was already derivable from the verb's namespace prefix; `usage`/`aliases` move to the Command.

### Help renderer: `pkg/cmdsys/help.go` (new file)

```go
// RenderHelp returns a formatted help string for verb. Resolution order:
//   1. If verb resolves to a registered Command, render per-command help
//      using the schema and metadata. (This includes group-shim commands
//      like "log" that have a top-level Command — they still get the
//      per-command format with their own description and Examples.)
//   2. Otherwise, if verb is a namespace prefix with at least one sub-verb
//      (e.g. "bot" with sub-verbs "bot.spawn", "bot.clear", "bot.list"),
//      render group help: header + sub-verb table.
//   3. Otherwise return a friendly "unknown command" message.
func RenderHelp(reg *Registry, verb string) string

// HelpResult is the typed result returned by Dispatcher.Invoke when the
// caller passed --help / -h / ?. It JSON-marshals to {"verb": "...", "text": "..."}.
type HelpResult struct {
    Verb string `json:"verb"`
    Text string `json:"text"`
}
```

**Per-command output (the X format approved in brainstorming):**

```text
  cell split — split a cell into 4 children at +1 depth
  Aliases: csplit

  USAGE
    cell split <cellID> [--bypass]

  ARGUMENTS
    cellID         target cell ID (e.g. "0_0", "0_0/1")

  FLAGS
    --bypass       skip cooldown check  (default: false)

  EXAMPLES
    cell split 0_0
    cell split 0_0 --bypass
```

**Section rules:**

- Title line: two-space indent, `verb — Description`. Uses spaces in place of dots in the verb (`cell.split` rendered as `cell split`) to match the way operators type it.
- `Aliases:` line emitted only when `len(Command.Aliases) > 0`.
- USAGE: uses `Command.Usage` verbatim if non-empty. Otherwise auto-derived from schema as `<verb> <required-positional> [<optional-positional>] [--flag]…` where:
  - Positional fields use `<name>` if required, `[<name>]` if optional, `[<name>...]` if `Rest`.
  - Named-only fields render as `[--name]` for bool flags, `[--name=<value>]` otherwise.
- ARGUMENTS: lists positional fields (`!NamedOnly`). Each line is `<name>  <Help>` plus parenthetical annotations: `(default: x)`, `(enum: a|b|c)`, `(required)` only when no default and not optional. Section omitted when no positional fields.
- FLAGS: lists named-only fields. Same annotation rules. Section omitted when no flags.
- EXAMPLES: one line per entry in `Command.Examples`. Section omitted when empty.
- SUBCOMMANDS: when the verb has at least one registered sub-verb (e.g. `log --help` and `log.status`/`log.on`/`log.off` are registered), append a SUBCOMMANDS section after EXAMPLES listing each sub-verb with its one-line `Description`. This keeps `log --help` discoverable for sub-verbs without forcing operators to remember a separate `log ?` ritual.

**Group help** (when verb has sub-verbs but no top-level Command, e.g. `bot`):

```text
  bot commands:
    bot.spawn <count> <cellID>     spawn N bots in a cell
    bot.clear                      remove all bots
    bot.list                       list active bots
```

This is the existing `printGroupHelp` output (in `console_help.go`), relocated into `pkg/cmdsys/help.go` so it's available everywhere. The console keeps a thin wrapper for backwards compatibility during the migration.

### Trigger interception: `cmdsys.Dispatcher.Invoke`

Insert help-token detection after verb resolution and before parser dispatch:

1. Tokenize the raw args string (reuse `tokenize` from `parser.go`).
2. If any token equals `--help`, `-h`, or `?`, skip parsing and the handler.
3. Build a `Result` with `PerTarget[0] = TargetResult{TargetID: "local", OK: true, Result: HelpResult{Verb: verb, Text: RenderHelp(reg, verb)}}`.
4. Return immediately.

The detection is position-independent (matches gnu convention — `cell split 0_0 --help` triggers help). A literal `?` argument value would be hijacked, but no current command takes `?` as a value; accepted risk.

The `Hidden` flag does NOT suppress help — operators may need to see flags on internal worker verbs they're invoking directly.

### Console rendering of `HelpResult`

In `pkg/engine/console_cmdsys.go`'s `renderDispatchResult` / `renderResult`: when `tr.Result` is a `cmdsys.HelpResult`, print `tr.Result.(HelpResult).Text` verbatim instead of the generic struct renderer. This ensures help text doesn't get wrapped in the typed-struct field labels that `renderResult` applies to other typed results.

For `--json`, no special case is needed — `HelpResult` JSON-marshals naturally as `{"verb": "...", "text": "..."}`.

### Console-side wiring updates

- `Console.RegisterTyped(cmd cmdsys.Command, usage string, aliases []string)` becomes `Console.RegisterTyped(cmd cmdsys.Command)`. Callers populate `cmd.Usage` and `cmd.Aliases` directly.
- `cmdsysAdapter.registerTyped(cmd, usage, aliases)` becomes `cmdsysAdapter.registerTyped(cmd)`. Same migration.
- Existing `verbDisplayMeta` map and all reads of it go away. `buildHelpText` (top-level help listing) reads `Usage`/`Description`/`Aliases` straight off each `Command`.
- `console_help.go` `printGroupHelp` becomes a thin wrapper that delegates to `cmdsys.RenderHelp(reg, groupVerb)` for group help.
- The readline `?` keystroke listener (`pkg/engine/console.go:72`) and `printContextualHelp` route through `cmdsys.RenderHelp` so all paths produce identical output.
- The `help` command's handler also routes through `cmdsys.RenderHelp` for the per-verb case.

### Call-site migration scope

Roughly 20 call sites total across:

- `pkg/engine/console.go` — `registerPlatformCommands` registers `help`, `quit`, `log` group shim.
- `pkg/engine/builtins_config.go` — config commands.
- `pkg/universe/builtins_*.go` — host/cell/perf/player/auth commands.
- `pkg/engine/console_cmdsys_test.go`, `console_completion_test.go` — test fixtures.
- `internal/game/*.go` — game-specific console commands (if any).
- `examples/4node-basic/main.go` — bot commands.

Each call site is a mechanical rewrite: move `usage`/`aliases` arguments into the `Command{...}` literal as `Usage:`/`Aliases:` fields. The migration is a single commit.

A small handful of high-value commands also gain `Examples` entries during the migration — at minimum: `help`, `cell split`, `cell merge`, `cell migrate`, `bot spawn`, `log on`, `log filter`, `config set`, `perf`. The rest land with empty `Examples` and can be filled in incrementally.

## Edge Cases

- **Trigger inside quoted strings:** `tokenize` already strips quotes, so `"--help"` would be detected. Acceptable; quoted help tokens are not a real use case.
- **Help on a hidden verb:** allowed. `<hidden-verb> --help` still renders. Hidden status only suppresses listing.
- **Help on an unknown verb:** `Dispatcher.Invoke` already returns `ErrUnknownVerb` from registry lookup, which happens before token scanning. Bare `help` covers discovery for unknown verbs.
- **Group with both top-level Command and sub-verbs (e.g. `log`):** `log --help`, `log -h`, and `log ?` all route through the dispatcher's help interception (since these tokens hijack before the handler runs) and produce per-command help with the SUBCOMMANDS section appended — so operators see both the shim's own usage and the sub-verb listing in one shot. Bare `log` (with no args) still falls through to the handler, which retains its existing behavior of printing group help.
- **Mid-typing `?` keystroke vs. trailing `?` token:** these are two distinct paths. The readline keystroke listener fires when the operator presses `?` mid-typing (no Enter); it inspects the buffer prefix and calls `RenderHelp` directly without going through the dispatcher. The trailing `?` form (`cell split ?` followed by Enter) is dispatcher-intercepted via the help-token scan. Both paths produce identical output by calling the same `RenderHelp`.
- **Help with `--json`:** returns `{"verb":"cell.split","text":"…multi-line…"}`. Consumers can parse and re-render.

## Test Plan

In `pkg/cmdsys/help_test.go` (new):

- `TestRenderHelp_BasicCommand` — verb with required positional + flag + examples. Snapshot the output.
- `TestRenderHelp_AutoUsage` — Usage field empty, output uses derived `<verb> <pos> [--flag]` form.
- `TestRenderHelp_NoFlags_NoExamples` — sections elide cleanly.
- `TestRenderHelp_Aliases` — emits `Aliases:` line when set, omits when empty.
- `TestRenderHelp_Group` — namespace prefix with no top-level Command renders sub-verb table.
- `TestRenderHelp_GroupShimWithSubcommands` — verb has both a Command and sub-verbs; output includes SUBCOMMANDS section.
- `TestRenderHelp_UnknownVerb` — unknown name returns friendly message.
- `TestRenderHelp_EnumAndDefaults` — annotations render correctly.

In `pkg/cmdsys/dispatcher_test.go` (extend existing):

- `TestDispatcher_HelpFlag_LongForm` — `--help` token returns `HelpResult`, skips Handler.
- `TestDispatcher_HelpFlag_ShortForm` — `-h` token same.
- `TestDispatcher_HelpFlag_QuestionMark` — `?` token same.
- `TestDispatcher_HelpFlag_AnyPosition` — `<verb> arg --help` triggers (handler not called).
- `TestDispatcher_HelpFlag_HandlerNotCalled` — explicit assertion that the registered Handler does not run.

In `pkg/engine/console_cmdsys_test.go` (extend existing):

- `TestConsole_HelpResult_RendersText` — typed `HelpResult` from dispatch renders verbatim, not via struct-field renderer.
- `TestConsole_QuestionMarkSuffix_End2End` — typing `cell split ?` end-to-end yields per-command help.

Manual verification: run `just dev`, type the following and visually confirm output:

- `cell split --help`
- `cell split -h`
- `cell split ?`
- `bot ?` (group help)
- `?` alone (full help listing)
- `help cell.split`

## Open Questions

None. Design fully specified.
