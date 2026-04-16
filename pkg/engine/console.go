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
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/logger"
)

// Console provides an interactive CLI for the server with readline support.
type Console struct {
	rl      *readline.Instance
	adapter *cmdsysAdapter
	log     *logger.Logger

	execFunc func(fn func() string) string

	compMu      sync.RWMutex
	completions map[string][]string

	// Categories registered by framework (before game builtins) for help separator.
	builtinCats map[string]bool
}

// NewConsole creates a new console with readline, redirects log output, and registers platform commands.
func NewConsole(gameLog *logger.Logger) *Console {
	return newConsoleWith(gameLog, newCmdsysAdapter())
}

// NewConsoleWithDispatcher creates a console that shares externally-owned
// Registry and Dispatcher instances. Used by Coordinator.startConsole so the
// REPL and the cross-process command dispatch pipeline share a single registry.
func NewConsoleWithDispatcher(gameLog *logger.Logger, reg *cmdsys.Registry, d *cmdsys.Dispatcher) *Console {
	return newConsoleWith(gameLog, newCmdsysAdapterWith(reg, d))
}

func newConsoleWith(gameLog *logger.Logger, adapter *cmdsysAdapter) *Console {
	c := &Console{
		adapter:     adapter,
		log:         gameLog,
		completions: make(map[string][]string),
		builtinCats: make(map[string]bool),
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
	})
	if err != nil {
		log.Fatalf("failed to create readline: %v", err)
	}
	c.rl = rl

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

// snapshotBuiltinCategories marks every category currently present in the
// Registry as framework-provided. Called after the engine's own RegisterBuiltins
// path runs so the help renderer can emit the "── Game Commands ──" separator
// between framework and game-registered commands.
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

// SetExecFunc sets the function used to execute closures on a game loop.
func (c *Console) SetExecFunc(fn func(func() string) string) {
	c.execFunc = fn
	c.adapter.ExecOnLoop = fn
}

// ExecOnGameLoop sends a closure to the game loop and waits for the result.
func (c *Console) ExecOnGameLoop(fn func() string) string {
	if c.execFunc != nil {
		return c.execFunc(fn)
	}
	return "  no game loop connected\n"
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

// RegisterTyped adds a fully typed cmdsys.Command plus optional display metadata.
func (c *Console) RegisterTyped(cmd cmdsys.Command, usage string, aliases []string) error {
	return c.adapter.registerTyped(cmd, usage, aliases)
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

		parts := strings.Fields(line)
		verb := parts[0]

		// Check if verb is registered in the adapter.
		_, verbFound := c.adapter.Registry.Lookup(verb)
		if !verbFound {
			// Try group sub-verb: "config get" → "config.get"
			if len(parts) > 1 {
				candidate := verb + "." + parts[1]
				if _, ok := c.adapter.Registry.Lookup(candidate); ok {
					verbFound = true
				}
			}
		}

		if verbFound {
			output := c.adapter.Dispatch(line)
			if output != "" {
				c.Print(output)
			}
			continue
		}

		// No verb found. If the user typed a namespace that has sub-verbs
		// (e.g. "bot", "bot ?", "bot help"), print the group's help listing
		// instead of an "unknown command" error. This makes bot/cell/host
		// discoverable even without a top-level group shim.
		if subs := c.adapter.sortedSubVerbs(verb); len(subs) > 0 {
			c.Print(c.adapter.printGroupHelp(verb))
			continue
		}

		// Category toggle shortcut (e.g. typing a log category name directly).
		cats := c.resolveCats(parts)
		if len(cats) > 0 {
			for _, cat := range cats {
				if c.log.IsEnabled(cat) {
					c.log.Disable(cat)
					fmt.Printf("  %s: OFF\n", cat)
				} else {
					c.log.Enable(cat)
					fmt.Printf("  %s: ON\n", cat)
				}
			}
		} else {
			fmt.Printf("  unknown command: %s (type 'help' for commands)\n", verb)
		}
	}
}

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

func (c *Console) printHelp() {
	fmt.Print(c.adapter.buildHelpText(c.builtinCats))
	c.printStatusFooter()
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

// resolveCats matches input strings to known categories.
func (c *Console) resolveCats(inputs []string) []string {
	return resolveCatsFromLog(c.log, inputs)
}

// consoleCompleter implements readline.AutoCompleter.
type consoleCompleter struct {
	console *Console
}

func (cc *consoleCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	lineStr := string(line[:pos])
	parts := strings.Fields(lineStr)
	trailingSpace := len(lineStr) > 0 && lineStr[len(lineStr)-1] == ' '

	if len(parts) == 0 || (len(parts) == 1 && !trailingSpace) {
		prefix := ""
		if len(parts) == 1 {
			prefix = parts[0]
		}
		return cc.completePrefix(prefix)
	}
	return nil, 0
}

func (cc *consoleCompleter) completePrefix(prefix string) ([][]rune, int) {
	var candidates []string
	seen := make(map[string]bool)
	for _, v := range cc.console.adapter.verbOrder {
		// Only show top-level verbs (no dot) or group-prefix verbs for completion.
		top := v
		if dot := strings.IndexByte(v, '.'); dot >= 0 {
			top = v[:dot]
		}
		if !seen[top] {
			seen[top] = true
			candidates = append(candidates, top)
		}
	}
	for _, cat := range cc.console.log.Categories() {
		if !seen[cat] {
			candidates = append(candidates, cat)
		}
	}
	return cc.filterCandidates(candidates, prefix)
}

func (cc *consoleCompleter) filterCandidates(candidates []string, prefix string) ([][]rune, int) {
	prefix = strings.ToLower(prefix)
	var matches [][]rune
	for _, c := range candidates {
		if strings.HasPrefix(strings.ToLower(c), prefix) {
			suffix := c[len(prefix):]
			matches = append(matches, []rune(suffix+" "))
		}
	}
	return matches, len(prefix)
}

// FormatPerfOutput builds the console perf display from engine state.
// Must be called from the game loop goroutine.
func FormatPerfOutput(eng *Engine) string {
	var b strings.Builder

	stats := eng.Perf.Stats()
	budgetMs := 1000.0 / float64(eng.Config.TickRate)
	t := stats.Total
	fmt.Fprintf(&b, "  Tick (%dHz, budget %.0fms):\n", eng.Config.TickRate, budgetMs)
	fmt.Fprintf(&b, "    avg %s  p50 %s  p95 %s  p99 %s  max %s\n",
		fmtDur(t.Avg), fmtDur(t.P50), fmtDur(t.P95), fmtDur(t.P99), fmtDur(t.Max))

	if len(stats.Systems) > 0 {
		fmt.Fprintf(&b, "  Systems:\n")
		for i, sys := range stats.Systems {
			fmt.Fprintf(&b, "    %-20s avg %s  p95 %s\n", stats.SystemNames[i], fmtDur(sys.Avg), fmtDur(sys.P95))
		}
	}

	if eng.Metrics != nil {
		snap := eng.Metrics.Snapshot()
		e := snap.Entities
		fmt.Fprintf(&b, "  Entities: %d real, %d replica, %d ghost (%d total), %d connected\n",
			e.Real, e.Replica, e.Ghost, e.Real+e.Replica+e.Ghost, e.Connected)
		fmt.Fprintf(&b, "  Network: %d conns, sent %s, recv %s\n",
			snap.Network.Connections, fmtBytes(snap.Network.BytesSent), fmtBytes(snap.Network.BytesRecv))
		fmt.Fprintf(&b, "  Load: %.2f", snap.CompositeLoad)
		if snap.Tick.OverbudgetPct > 0 {
			fmt.Fprintf(&b, "  overbudget: %.1f%%", snap.Tick.OverbudgetPct*100)
		}
		if snap.Tick.EffectiveHz > 0 {
			fmt.Fprintf(&b, "  capacity: %.0fHz", snap.Tick.EffectiveHz)
		}
		fmt.Fprintln(&b)
	}

	return b.String()
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

// ── arg/result types for platform commands ───────────────────────────────────

type helpArgs struct {
	Name string `cmd:"optional,help=command or group name"`
}

type helpResult struct{}
