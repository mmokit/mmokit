package engine

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/chzyer/readline"
	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/logger"
)

// Console provides an interactive CLI for the server with readline support.
type Console struct {
	rl      *readline.Instance
	adapter *cmdsysAdapter
	log     *logger.Logger

	compMu               sync.RWMutex
	completions          map[string][]string
	completionSources    map[string]func() []string                // dynamic providers called per tab
	completionSourcesCtx map[string]func(tokens []string) []string // contextual providers; receive prior tokens

	// Categories registered by framework (before game builtins) for help separator.
	builtinCats map[string]bool
}

// NewConsole creates a new console with readline, redirects log output, and registers platform commands.
func NewConsole(gameLog *logger.Logger) *Console {
	return newConsoleWith(gameLog, newCmdsysAdapter())
}

// NewConsoleWithDispatcher creates a console that shares externally-owned
// Registry and Dispatcher instances. Used by Process.startConsole so the
// REPL and the cross-process command dispatch pipeline share a single registry.
func NewConsoleWithDispatcher(gameLog *logger.Logger, reg *cmdsys.Registry, d *cmdsys.Dispatcher) *Console {
	return newConsoleWith(gameLog, newCmdsysAdapterWith(reg, d))
}

func newConsoleWith(gameLog *logger.Logger, adapter *cmdsysAdapter) *Console {
	c := &Console{
		adapter:              adapter,
		log:                  gameLog,
		completions:          make(map[string][]string),
		completionSources:    make(map[string]func() []string),
		completionSourcesCtx: make(map[string]func(tokens []string) []string),
		builtinCats:          make(map[string]bool),
	}

	c.refreshCategoryCompletions()
	c.registerPlatformCommands()
	c.snapshotBuiltinCategories()

	completer := &consoleCompleter{console: c}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:            "> ",
		HistoryFile:       "data/.console_history",
		HistoryLimit:      1000,
		AutoComplete:      completer,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		HistorySearchFold: true,
		// Pressing '?' anywhere submits an inline help lookup based on what
		// was already typed: empty buffer → full help, "<group> ?" → group
		// help, "<verb> ?" → verb-level usage. The just-typed '?' is removed
		// so the buffer is preserved — the user can keep typing where they
		// left off after reading the help.
		Listener: readline.FuncListener(func(line []rune, pos int, key rune) ([]rune, int, bool) {
			if key != '?' {
				return nil, 0, false
			}
			// At this point pos is the position AFTER the '?' was inserted,
			// so line[pos-1] == '?'. Strip just that one rune; preserve the
			// rest of the buffer (and anything to the right of the cursor).
			if pos == 0 {
				return nil, 0, false
			}
			prefix := strings.TrimSpace(string(line[:pos-1]))
			newLine := make([]rune, 0, len(line)-1)
			newLine = append(newLine, line[:pos-1]...)
			newLine = append(newLine, line[pos:]...)
			c.Print("\n")
			c.printContextualHelp(prefix)
			return newLine, pos - 1, true
		}),
	})
	if err != nil {
		log.Fatalf("failed to create readline: %v", err)
	}
	c.rl = rl
	adapter.stdout = rl.Stdout()

	log.SetOutput(rl.Stdout())

	return c
}

// Dispatcher returns the cmdsys.Dispatcher backing this console.
func (c *Console) Dispatcher() *cmdsys.Dispatcher {
	return c.adapter.Dispatcher
}

// Registry returns the cmdsys.Registry backing this console.
func (c *Console) Registry() *cmdsys.Registry {
	return c.adapter.Registry
}

// Stdout returns a writer that outputs through readline without corrupting the prompt.
func (c *Console) Stdout() io.Writer {
	return c.rl.Stdout()
}

// SetPrompt updates the readline prompt. Safe to call at any time — the next
// Readline iteration picks up the new value.
func (c *Console) SetPrompt(s string) {
	c.rl.SetPrompt(s)
}

// snapshotBuiltinCategories marks every category currently present in the
// Registry as framework-provided. Called during console construction so the
// help renderer can emit the "── Game Commands ──" separator between framework
// and game-registered commands.
func (c *Console) snapshotBuiltinCategories() {
	for _, v := range c.adapter.Registry.List() {
		cat := v
		if dot := strings.IndexByte(v, '.'); dot >= 0 {
			cat = v[:dot]
		}
		c.builtinCats[cat] = true
	}
}

// Print writes a string through readline's safe writer.
func (c *Console) Print(s string) {
	fmt.Fprint(c.rl.Stdout(), s)
}

// Printf formats and writes through readline's safe writer.
func (c *Console) Printf(format string, args ...any) {
	fmt.Fprintf(c.rl.Stdout(), format, args...)
}

// SetCompletions updates the completion list for a key (thread-safe).
func (c *Console) SetCompletions(key string, values []string) {
	c.compMu.Lock()
	c.completions[key] = values
	c.compMu.Unlock()
}

// GetCompletions returns the completion list for a key (thread-safe).
func (c *Console) GetCompletions(key string) []string {
	c.compMu.RLock()
	vals := c.completions[key]
	c.compMu.RUnlock()
	return vals
}

// SetCompletionSource registers a dynamic provider for a completion key.
// The provider is called on every tab-complete event so results stay fresh
// without polling. Providers must be cheap and thread-safe. A source
// registered via this method wins over a static SetCompletions value for
// the same key.
func (c *Console) SetCompletionSource(key string, fn func() []string) {
	c.compMu.Lock()
	c.completionSources[key] = fn
	c.compMu.Unlock()
}

// SetCompletionSourceCtx registers a contextual provider that receives the
// already-typed tokens (including the verb and any preceding args). Used
// when completion needs to peek at what the operator typed earlier — e.g.
// completing the args field of `service call <kind> <op>` requires
// reading tokens[2] (kind) and tokens[3] (op) to find the request type.
//
// Contextual sources win over both SetCompletionSource and SetCompletions
// for the same key.
func (c *Console) SetCompletionSourceCtx(key string, fn func(tokens []string) []string) {
	c.compMu.Lock()
	c.completionSourcesCtx[key] = fn
	c.compMu.Unlock()
}

// completionsFor returns the current values for a completion key. Prefers
// the contextual source if registered, else the dynamic source, else the
// static list.
func (c *Console) completionsFor(key string, tokens []string) []string {
	c.compMu.RLock()
	fnCtx, hasCtx := c.completionSourcesCtx[key]
	fn, hasFn := c.completionSources[key]
	static := c.completions[key]
	c.compMu.RUnlock()
	if hasCtx {
		return fnCtx(tokens)
	}
	if hasFn {
		return fn()
	}
	return static
}

// RegisterTyped registers a typed cmdsys.Command. Display metadata
// (Usage, Aliases, Examples) lives on the Command itself.
func (c *Console) RegisterTyped(cmd cmdsys.Command) error {
	return c.adapter.registerTyped(cmd)
}

// Run starts the interactive console. Blocks until ctx is cancelled or readline returns an error.
func (c *Console) Run(ctx context.Context) {
	defer c.rl.Close()

	c.printHelp()
	c.printStatus()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := c.rl.Readline()
		if err != nil {
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Leading '?' is shorthand for help, equivalent to the keystroke
		// listener path: '?' alone shows the full help, '? cell' / '? cell list'
		// route to printContextualHelp.
		if line == "?" || strings.HasPrefix(line, "? ") {
			c.printContextualHelp(strings.TrimSpace(line[1:]))
			continue
		}

		parts := strings.Fields(line)
		verb := parts[0]

		// Try longest-match dotted verb so multi-level namespaces like
		// "auth user lock" / "auth session list" resolve to the registered
		// "auth.user.lock" / "auth.session.list" instead of falling through
		// to "unknown command".
		_, _, verbFound := c.adapter.resolveDottedVerb(parts)

		if verbFound {
			output := c.adapter.Dispatch(line)
			if output != "" {
				c.Print(output)
			}
			continue
		}

		// No verb found. If the user typed a namespace that has sub-verbs
		// (e.g. "bot", "bot ?", "bot help"), print the group's help listing
		// instead of an "unknown command" error.
		if hasSubVerbs(c.adapter.Registry, verb) {
			c.Print(cmdsys.RenderHelp(c.adapter.Registry, verb))
			continue
		}

		fmt.Printf("  unknown command: %s (type 'help' for commands; use 'log on/off/toggle' for category toggles)\n", verb)
	}
}

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
			"help cell split",
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
			// Users type space-separated ("help cell split"); the registry
			// keys are dot-form ("cell.split").
			canonical := strings.ReplaceAll(name, " ", ".")
			fmt.Print(cmdsys.RenderHelp(c.adapter.Registry, canonical))
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

func (c *Console) printHelp() {
	fmt.Print(c.adapter.buildHelpText(c.builtinCats))
	c.printStatusFooter()
}

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

func (c *Console) printStatusFooter() {
	groups := c.log.Groups()
	if len(groups) > 0 {
		fmt.Printf("  Log groups: %s\n", strings.Join(groups, ", "))
	}
	fmt.Printf("  Log categories: %s\n", strings.Join(c.log.Categories(), ", "))
	fmt.Println("  Tip: type a group or category name to toggle it")
	fmt.Println()
}

func (c *Console) printStatus() {
	fmt.Print(buildLogStatus(c.log))
}

// refreshCategoryCompletions updates tab-completion lists for log categories.
func (c *Console) refreshCategoryCompletions() {
	cats := c.log.Categories()
	groups := c.log.Groups()
	all := make([]string, 0, len(cats)+len(groups)+1)
	all = append(all, groups...)
	all = append(all, cats...)
	all = append(all, "all")
	c.completions["categories"] = all
}

// consoleCompleter implements readline.AutoCompleter.
type consoleCompleter struct {
	console *Console
}

// Do routes the user's current input to the appropriate completion strategy.
// The "completed" tokens are everything before the partial last token (when
// no trailing space) or all tokens (when a trailing space ends the prefix).
// If the completed tokens form a registered verb at any depth (longest match)
// the user is past the verb path → complete the next positional argument.
// Otherwise the user is mid-namespace at depth len(completed) → complete the
// next path segment under that namespace prefix. This makes completion work
// at every depth: `auth `, `auth user `, `auth user lock `, `auth user lock
// alice ` all surface the right candidates.
func (cc *consoleCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	lineStr := string(line[:pos])
	tokens := strings.Fields(lineStr)
	trailingSpace := len(lineStr) > 0 && lineStr[len(lineStr)-1] == ' '

	completed := tokens
	prefix := ""
	if !trailingSpace && len(tokens) > 0 {
		completed = tokens[:len(tokens)-1]
		prefix = tokens[len(tokens)-1]
	}

	if verb, consumed, ok := cc.console.adapter.resolveDottedVerb(completed); ok {
		argIdx := len(completed) - consumed
		return cc.completeArgFor(verb, argIdx, prefix, tokens)
	}
	return cc.completeNamespaceSegment(completed, prefix)
}

// completeNamespaceSegment offers the next path segment under the namespace
// formed by joining completed with '.'. Empty completed means top-level
// namespaces. Hidden verbs are excluded so internal worker verbs stay off the
// completion menu.
func (cc *consoleCompleter) completeNamespaceSegment(completed []string, prefix string) ([][]rune, int) {
	nsDot := ""
	if len(completed) > 0 {
		nsDot = strings.Join(completed, ".") + "."
	}
	seen := make(map[string]bool)
	for _, v := range cc.console.adapter.Registry.List() {
		if cmd, ok := cc.console.adapter.Registry.Lookup(v); ok && cmd.Hidden {
			continue
		}
		rest := v
		if nsDot != "" {
			if !strings.HasPrefix(v, nsDot) {
				continue
			}
			rest = v[len(nsDot):]
		}
		if dot := strings.IndexByte(rest, '.'); dot >= 0 {
			rest = rest[:dot]
		}
		if rest != "" {
			seen[rest] = true
		}
	}
	return filterMap(seen, prefix)
}

// completeArgFor completes the argIdx'th positional argument for verb,
// driven by the command's Args schema (cmd:"complete=<key>" / cmd:"enum=...").
func (cc *consoleCompleter) completeArgFor(verb string, argIdx int, prefix string, tokens []string) ([][]rune, int) {
	cmd, ok := cc.console.adapter.Registry.Lookup(verb)
	if !ok || cmd.Args == nil {
		return nil, 0
	}
	schema, err := cmdsys.SchemaOf(cmd.Args)
	if err != nil {
		return nil, 0
	}
	var positional []cmdsys.FieldSchema
	for _, f := range schema.Fields {
		if f.NamedOnly {
			continue
		}
		positional = append(positional, f)
	}
	if argIdx < 0 {
		return nil, 0
	}
	if argIdx >= len(positional) {
		// Past the last declared positional. If the last field is a Rest
		// field, clamp to it so additional tokens still get its completion
		// (e.g. 'service call kind op key1=val key2=val' — every token
		// after op falls into the Rest 'Args' field).
		if len(positional) > 0 && positional[len(positional)-1].Rest {
			argIdx = len(positional) - 1
		} else {
			return nil, 0
		}
	}
	field := positional[argIdx]

	seen := make(map[string]bool)
	if field.Complete != "" {
		for _, v := range cc.console.completionsFor(field.Complete, tokens) {
			seen[v] = true
		}
	} else if len(field.Enum) > 0 {
		for _, v := range field.Enum {
			seen[v] = true
		}
	}
	if len(seen) == 0 {
		return nil, 0
	}
	return filterMap(seen, prefix)
}

// filterMap returns the readline candidate slice for every key in m that
// has the given (lowercase-insensitive) prefix. Appends a trailing space so
// completion advances the cursor past the token.
func filterMap(m map[string]bool, prefix string) ([][]rune, int) {
	lower := strings.ToLower(prefix)
	var matches [][]rune
	for c := range m {
		if strings.HasPrefix(strings.ToLower(c), lower) {
			suffix := c[len(prefix):]
			// Don't append a trailing space when the suggestion ends in
			// '=' — that's a partial assignment (e.g. "key=") and the
			// operator is about to type the value. A space here breaks
			// "key=demo" into "key= demo".
			if !strings.HasSuffix(c, "=") {
				suffix += " "
			}
			matches = append(matches, []rune(suffix))
		}
	}
	return matches, len(prefix)
}

// PerfSnapshotText is the minimal input needed to render the human-readable
// perf block. It is populated by the universe layer from a PerfCellSnapshot;
// engine/console.go does no structural ECS or cell lookups itself.
type PerfSnapshotText struct {
	TickHz            int
	BudgetMS          int
	Tick              TimingStats
	SystemNames       []string
	SystemTimings     []TimingStats
	EntitiesReal      int
	EntitiesReplica   int
	EntitiesGhost     int
	EntitiesConnected int
	Connections       int
	BytesSent         uint64
	BytesRecv         uint64
	CompositeLoad     float64
	OverbudgetPct     float64
	EffectiveHz       float64
}

// FormatPerfSnapshotText renders a PerfSnapshotText as the indented console block.
// Pure function — safe to call from any goroutine.
func FormatPerfSnapshotText(s PerfSnapshotText) string {
	var b strings.Builder
	budgetMs := s.BudgetMS
	if budgetMs == 0 && s.TickHz > 0 {
		// FormatPerfSnapshotText is pure over its argument and holds no
		// engine, so this is the one tick-period derivation that cannot
		// read Engine.TickIntervalMs. It still goes through the same
		// schedule so the displayed budget matches the scheduled period.
		budgetMs = int(newTickSchedule(s.TickHz).PeriodMs)
	}
	fmt.Fprintf(&b, "  Tick (%dHz, budget %dms):\n", s.TickHz, budgetMs)
	fmt.Fprintf(&b, "    avg %s  p50 %s  p95 %s  p99 %s  max %s\n",
		fmtDur(s.Tick.Avg), fmtDur(s.Tick.P50), fmtDur(s.Tick.P95), fmtDur(s.Tick.P99), fmtDur(s.Tick.Max))

	if len(s.SystemTimings) > 0 {
		fmt.Fprintf(&b, "  Systems:\n")
		for i, sys := range s.SystemTimings {
			name := ""
			if i < len(s.SystemNames) {
				name = s.SystemNames[i]
			}
			fmt.Fprintf(&b, "    %-20s avg %s  p95 %s\n", name, fmtDur(sys.Avg), fmtDur(sys.P95))
		}
	}

	total := s.EntitiesReal + s.EntitiesReplica + s.EntitiesGhost
	fmt.Fprintf(&b, "  Entities: %d real, %d replica, %d ghost (%d total), %d connected\n",
		s.EntitiesReal, s.EntitiesReplica, s.EntitiesGhost, total, s.EntitiesConnected)
	fmt.Fprintf(&b, "  Network: %d conns, sent %s, recv %s\n",
		s.Connections, fmtBytes(s.BytesSent), fmtBytes(s.BytesRecv))
	fmt.Fprintf(&b, "  Load: %.2f", s.CompositeLoad)
	if s.OverbudgetPct > 0 {
		fmt.Fprintf(&b, "  overbudget: %.1f%%", s.OverbudgetPct*100)
	}
	if s.EffectiveHz > 0 {
		fmt.Fprintf(&b, "  capacity: %.0fHz", s.EffectiveHz)
	}
	fmt.Fprintln(&b)

	return b.String()
}

// FormatPerfOutput is the legacy single-engine formatter. Kept as a thin
// adapter so existing callers continue to work; delegates to
// FormatPerfSnapshotText. Must be called from the game loop goroutine.
func FormatPerfOutput(eng *Engine) string {
	stats := eng.Perf.Stats()
	text := PerfSnapshotText{
		TickHz:        eng.Config.TickRate,
		Tick:          stats.Total,
		SystemNames:   stats.SystemNames,
		SystemTimings: stats.Systems,
	}
	if eng.Metrics != nil {
		snap := eng.Metrics.Snapshot()
		text.EntitiesReal = snap.Entities.Real
		text.EntitiesReplica = snap.Entities.Replica
		text.EntitiesGhost = snap.Entities.Ghost
		text.EntitiesConnected = snap.Entities.Connected
		text.Connections = snap.Network.Connections
		text.BytesSent = snap.Network.BytesSent
		text.BytesRecv = snap.Network.BytesRecv
		text.CompositeLoad = snap.CompositeLoad
		text.OverbudgetPct = snap.Tick.OverbudgetPct
		text.EffectiveHz = snap.Tick.EffectiveHz
	}
	return FormatPerfSnapshotText(text)
}

// FmtDuration formats a duration as a human-readable string (e.g. "3.2ms" or "45us").
func FmtDuration(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	if ms < 0.1 {
		return fmt.Sprintf("%.0fus", float64(d)/float64(time.Microsecond))
	}
	return fmt.Sprintf("%.1fms", ms)
}

var fmtDur = FmtDuration

func fmtBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

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

// ── arg/result types for platform commands ───────────────────────────────────

type helpArgs struct {
	// Rest so multi-token names ("help cell split") arrive intact. The
	// handler converts spaces to dots before calling RenderHelp, which
	// expects canonical dot-form verbs.
	Name string `cmd:"optional,rest,help=command or group name"`
}

type helpResult struct{}
