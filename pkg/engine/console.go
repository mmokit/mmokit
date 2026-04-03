package engine

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chzyer/readline"
	"github.com/zenion/mmoserver/pkg/logger"
)

// Command represents a console command with metadata for help and tab completion.
type Command struct {
	Name        string
	Aliases     []string
	Category    string                      // "server", "logging", "admin", "config"
	Usage       string                      // e.g. "damage <player> <amount>"
	Description string                      // e.g. "deal damage (bypasses shield)"
	Fn          func(args []string)
	Complete    func(args []string) []string // given args typed so far, return completions for next arg
}

// CommandGroup is a named prefix that dispatches to child subcommands.
// "config set AoIRadius 500" -> group "config", subcommand "set", args ["AoIRadius", "500"]
type CommandGroup struct {
	Name        string
	Category    string
	Description string
	DefaultFn   func() // called when group is invoked with no subcommand (falls back to help)
	commands    map[string]*Command
	cmdOrder    []string
}

// NewCommandGroup creates a new command group with the given name, category, and description.
func NewCommandGroup(name, category, description string) *CommandGroup {
	return &CommandGroup{
		Name:        name,
		Category:    category,
		Description: description,
		commands:    make(map[string]*Command),
	}
}

// Add adds a subcommand to the group.
func (g *CommandGroup) Add(cmd Command) {
	p := &cmd
	g.commands[cmd.Name] = p
	g.cmdOrder = append(g.cmdOrder, cmd.Name)
}

// Console provides an interactive CLI for the server with readline support.
type Console struct {
	rl         *readline.Instance
	commands   map[string]*Command // name + aliases -> *Command
	cmdList    []*Command          // unique commands in registration order
	categories []string            // display order
	groups     map[string]*CommandGroup // group name -> group
	engine     *Engine
	log        *logger.Logger

	compMu             sync.RWMutex
	completions        map[string][]string // thread-safe completion data
	builtinCategories  map[string]bool     // categories registered by framework (shown above game commands)
}

// NewConsole creates a new console with readline, redirects log output, and registers platform commands.
func NewConsole(eng *Engine, gameLog *logger.Logger) *Console {
	c := &Console{
		commands:    make(map[string]*Command),
		categories:  nil,
		groups:      make(map[string]*CommandGroup),
		engine:      eng,
		log:         gameLog,
		completions:       make(map[string][]string),
		builtinCategories: make(map[string]bool),
	}

	// Set completions for log categories (includes group names)
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

	// Redirect log output through readline's writer so log messages don't corrupt the prompt
	log.SetOutput(rl.Stdout())

	return c
}

// Stdout returns a writer that outputs through readline without corrupting the prompt.
// Use this for any output from other goroutines.
func (c *Console) Stdout() io.Writer {
	return c.rl.Stdout()
}

// Register adds a command to the console.
func (c *Console) Register(cmd Command) {
	p := &cmd
	c.commands[cmd.Name] = p
	for _, alias := range cmd.Aliases {
		c.commands[alias] = p
	}
	c.cmdList = append(c.cmdList, p)

	// Auto-collect categories in first-seen order
	if cmd.Category != "" {
		found := false
		for _, cat := range c.categories {
			if cat == cmd.Category {
				found = true
				break
			}
		}
		if !found {
			c.categories = append(c.categories, cmd.Category)
		}
	}
}

// RegisterGroup registers a command group. Creates a synthetic top-level Command
// that dispatches to subcommands. If no subcommand given, prints subcommand list.
func (c *Console) RegisterGroup(g *CommandGroup) {
	c.groups[g.Name] = g

	syntheticCmd := Command{
		Name:        g.Name,
		Category:    g.Category,
		Description: g.Description,
		Fn: func(args []string) {
			if len(args) == 0 {
				if g.DefaultFn != nil {
					g.DefaultFn()
				} else {
					c.printGroupHelp(g)
				}
				return
			}
			sub, ok := g.commands[args[0]]
			if !ok {
				fmt.Printf("  unknown subcommand: %s %s\n", g.Name, args[0])
				c.printGroupHelp(g)
				return
			}
			sub.Fn(args[1:])
		},
		Complete: func(args []string) []string {
			if len(args) == 0 {
				// Completing subcommand name
				names := make([]string, len(g.cmdOrder))
				copy(names, g.cmdOrder)
				return names
			}
			// Delegate to subcommand's Complete
			sub, ok := g.commands[args[0]]
			if !ok || sub.Complete == nil {
				return nil
			}
			return sub.Complete(args[1:])
		},
	}
	c.Register(syntheticCmd)
}

// ExtendGroup adds a subcommand to an existing group. Panics if group doesn't exist.
func (c *Console) ExtendGroup(name string, cmd Command) {
	g, ok := c.groups[name]
	if !ok {
		panic(fmt.Sprintf("console: ExtendGroup called for unknown group %q", name))
	}
	g.Add(cmd)
}

// printGroupHelp prints the list of subcommands in a group.
func (c *Console) printGroupHelp(g *CommandGroup) {
	fmt.Printf("  %s — %s\n", g.Name, g.Description)
	fmt.Println("  subcommands:")
	for _, name := range g.cmdOrder {
		sub := g.commands[name]
		usage := sub.Usage
		if usage == "" {
			usage = sub.Name
		}
		fmt.Printf("    %-28s %s\n", usage, sub.Description)
	}
	fmt.Println()
}

// printCommandHelp prints detailed help for a specific command or group.
func (c *Console) printCommandHelp(args []string) {
	name := args[0]

	// Check if it's a group
	if g, ok := c.groups[name]; ok {
		c.printGroupHelp(g)
		return
	}

	// Check if it's a command (by name or alias)
	if cmd, ok := c.commands[name]; ok {
		usage := cmd.Usage
		if usage == "" {
			usage = cmd.Name
		}
		fmt.Println()
		fmt.Printf("  %s\n", usage)
		if len(cmd.Aliases) > 0 {
			fmt.Printf("  aliases: %s\n", strings.Join(cmd.Aliases, ", "))
		}
		if cmd.Description != "" {
			fmt.Printf("  %s\n", cmd.Description)
		}
		fmt.Println()
		return
	}

	fmt.Printf("  unknown command: %s\n", name)
}

// snapshotBuiltinCategories marks all currently registered categories as framework-provided.
// Called after registerPlatformCommands and RegisterBuiltins so printHelp can draw a
// separator between framework and game commands.
func (c *Console) snapshotBuiltinCategories() {
	for _, cat := range c.categories {
		c.builtinCategories[cat] = true
	}
}

// Print writes a string through readline's safe writer so output doesn't corrupt the prompt.
func (c *Console) Print(s string) {
	fmt.Fprint(c.rl.Stdout(), s)
}

// Printf formats and writes through readline's safe writer so output doesn't corrupt the prompt.
func (c *Console) Printf(format string, args ...any) {
	fmt.Fprintf(c.rl.Stdout(), format, args...)
}

// ExecOnGameLoop sends a closure to the game loop and waits for the result.
// Returns a timeout message if the game loop does not respond within 5 seconds.
func (c *Console) ExecOnGameLoop(fn func() string) string {
	result := make(chan string, 1)
	c.engine.PendingAdminCmds <- func() {
		result <- fn()
	}
	select {
	case r := <-result:
		return r
	case <-time.After(5 * time.Second):
		return "  game loop not responding (timeout)\n"
	}
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
			// ErrInterrupt (Ctrl+C) or io.EOF
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		cmd := parts[0]

		if p, ok := c.commands[cmd]; ok {
			p.Fn(parts[1:])
		} else {
			// Try as category toggle shortcut
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
				fmt.Printf("  unknown command: %s (type 'help' for commands)\n", cmd)
			}
		}
	}
}

func (c *Console) registerPlatformCommands() {
	c.Register(Command{
		Name: "help", Aliases: []string{"h", "?"},
		Category: "server", Usage: "help [command|group]", Description: "show help (optionally for a specific command or group)",
		Fn: func(args []string) {
			if len(args) == 0 {
				c.printHelp()
			} else {
				c.printCommandHelp(args)
			}
		},
		Complete: func(args []string) []string {
			var names []string
			seen := make(map[string]bool)
			for _, cmd := range c.cmdList {
				if !seen[cmd.Name] {
					seen[cmd.Name] = true
					names = append(names, cmd.Name)
				}
			}
			return names
		},
	})

	c.Register(Command{
		Name: "quit", Aliases: []string{"q", "exit"},
		Category: "server", Usage: "quit", Description: "stop the server (Ctrl+C)",
		Fn: func(args []string) {
			fmt.Println("  use Ctrl+C to stop the server")
		},
	})

	c.Register(Command{
		Name: "perf", Aliases: []string{"p"},
		Category: "perf", Usage: "perf [reset]", Description: "show tick timing, entities, network, load",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return []string{"reset"}
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) > 0 && args[0] == "reset" {
				output := c.ExecOnGameLoop(func() string {
					c.engine.Perf.Reset()
					return "  perf counters reset\n"
				})
				fmt.Print(output)
				return
			}
			output := c.ExecOnGameLoop(func() string { return formatPerfOutput(c.engine) })
			fmt.Print(output)
		},
	})

	c.Register(Command{
		Name: "load",
		Category: "perf", Usage: "load", Description: "show composite load score",
		Fn: func(args []string) {
			output := c.ExecOnGameLoop(func() string {
				if c.engine.Metrics == nil {
					return "  metrics not wired\n"
				}
				snap := c.engine.Metrics.Snapshot()
				tickBudget := time.Duration(1000/c.engine.Config.TickRate) * time.Millisecond
				return fmt.Sprintf("  load: %.2f (tick=%.1f%% entity=%.1f%%)\n",
					snap.CompositeLoad,
					float64(snap.Tick.AvgDuration)/float64(tickBudget)*100,
					float64(snap.Entities.Real)/1000.0*100,
				)
			})
			fmt.Print(output)
		},
	})

	// Log command group — replaces flat on/off/toggle/only/status commands
	catComplete := func(args []string) []string {
		return c.GetCompletions("categories")
	}

	logGroup := NewCommandGroup("log", "logging", "manage log categories")
	logGroup.DefaultFn = func() { c.printStatus() }
	logGroup.Add(Command{
		Name: "status", Aliases: []string{"s"},
		Usage: "status", Description: "show log categories on/off",
		Fn: func(args []string) { c.printStatus() },
	})
	logGroup.Add(Command{
		Name: "on",
		Usage: "on <cat|all>", Description: "enable log category",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: log on <category|all>")
			} else if args[0] == "all" {
				c.log.Enable(c.log.Categories()...)
				fmt.Println("  all categories enabled")
			} else {
				cats := c.resolveCats(args)
				if len(cats) > 0 {
					c.log.Enable(cats...)
					fmt.Printf("  enabled: %s\n", strings.Join(cats, ", "))
				}
			}
		},
	})
	logGroup.Add(Command{
		Name: "off",
		Usage: "off <cat|all>", Description: "disable log category",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: log off <category|all>")
			} else if args[0] == "all" {
				c.log.Disable(c.log.Categories()...)
				fmt.Println("  all categories disabled")
			} else {
				cats := c.resolveCats(args)
				if len(cats) > 0 {
					c.log.Disable(cats...)
					fmt.Printf("  disabled: %s\n", strings.Join(cats, ", "))
				}
			}
		},
	})
	logGroup.Add(Command{
		Name: "toggle", Aliases: []string{"t"},
		Usage: "toggle <cat>", Description: "toggle log category",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: log toggle <category>")
			} else {
				for _, cat := range c.resolveCats(args) {
					if c.log.IsEnabled(cat) {
						c.log.Disable(cat)
						fmt.Printf("  %s: OFF\n", cat)
					} else {
						c.log.Enable(cat)
						fmt.Printf("  %s: ON\n", cat)
					}
				}
			}
		},
	})
	logGroup.Add(Command{
		Name: "only",
		Usage: "only <cat> [cat...]", Description: "enable only these, disable rest",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: log only <category> [category...]")
			} else {
				c.log.Disable(c.log.Categories()...)
				cats := c.resolveCats(args)
				c.log.Enable(cats...)
				fmt.Printf("  only: %s\n", strings.Join(cats, ", "))
			}
		},
	})
	logGroup.Add(Command{
		Name: "filter", Aliases: []string{"f"},
		Usage: "filter [cat pattern | clear [cat]]", Description: "set/show/clear message filters",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) == 0 {
				// Show active filters
				filters := c.log.Filters()
				if len(filters) == 0 {
					fmt.Println("  no active filters")
					return
				}
				fmt.Println("  active filters:")
				for cat, pat := range filters {
					fmt.Printf("    %s: %s\n", cat, pat)
				}
				return
			}
			if args[0] == "clear" {
				if len(args) > 1 {
					cats := c.resolveCats(args[1:])
					c.log.ClearFilter(cats...)
					fmt.Printf("  cleared filters: %s\n", strings.Join(cats, ", "))
				} else {
					c.log.ClearFilter()
					fmt.Println("  all filters cleared")
				}
				return
			}
			if len(args) < 2 {
				fmt.Println("  usage: log filter <category> <pattern>")
				fmt.Println("         log filter clear [category]")
				return
			}
			cat := args[0]
			pattern := strings.Join(args[1:], " ")
			src := pattern
			// /regex/ syntax
			if len(pattern) >= 2 && pattern[0] == '/' && pattern[len(pattern)-1] == '/' {
				pattern = pattern[1 : len(pattern)-1]
			} else {
				pattern = regexp.QuoteMeta(pattern)
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				fmt.Printf("  invalid regex: %v\n", err)
				return
			}
			cats := c.resolveCats([]string{cat})
			if len(cats) == 0 {
				fmt.Printf("  unknown category: %s\n", cat)
				return
			}
			for _, resolved := range cats {
				c.log.SetFilter(resolved, re, src)
			}
			fmt.Printf("  filter set on %s: %s\n", strings.Join(cats, ", "), src)
		},
	})
	c.RegisterGroup(logGroup)
}

func (c *Console) printHelp() {
	fmt.Println()
	gameSectionPrinted := false
	for _, category := range c.categories {
		// Collect commands in this category
		var cmds []*Command
		for _, cmd := range c.cmdList {
			if cmd.Category == category {
				cmds = append(cmds, cmd)
			}
		}
		if len(cmds) == 0 {
			continue
		}

		// Print separator before the first game-specific category
		if !c.builtinCategories[category] && !gameSectionPrinted {
			fmt.Println("  ── Game Commands ──")
			fmt.Println()
			gameSectionPrinted = true
		}

		// Print category header (capitalized)
		fmt.Printf("  %s%s:\n", strings.ToUpper(category[:1]), category[1:])

		for _, cmd := range cmds {
			// Build usage with aliases
			usage := cmd.Usage
			if usage == "" {
				usage = cmd.Name
			}
			if len(cmd.Aliases) > 0 {
				aliasStr := strings.Join(cmd.Aliases, ", ")
				// Insert aliases after the command name
				nameEnd := strings.IndexByte(usage, ' ')
				if nameEnd == -1 {
					usage = fmt.Sprintf("%s (%s)", usage, aliasStr)
				} else {
					usage = fmt.Sprintf("%s (%s)%s", usage[:nameEnd], aliasStr, usage[nameEnd:])
				}
			}
			fmt.Printf("    %-32s %s\n", usage, cmd.Description)
		}
		fmt.Println()
	}

	groups := c.log.Groups()
	if len(groups) > 0 {
		fmt.Printf("  Log groups: %s\n", strings.Join(groups, ", "))
	}
	fmt.Printf("  Log categories: %s\n", strings.Join(c.log.Categories(), ", "))
	fmt.Println("  Tip: type a group or category name to toggle it")
	fmt.Println()
}

func (c *Console) printStatus() {
	cats := c.log.Categories()
	filters := c.log.Filters()

	// Group categories by prefix
	type groupEntry struct {
		name string
		cats []string
	}
	var groups []groupEntry
	groupIdx := make(map[string]int)
	var ungrouped []string

	sorted := make([]string, len(cats))
	copy(sorted, cats)
	sort.Strings(sorted)

	for _, cat := range sorted {
		if g, _, ok := strings.Cut(cat, ":"); ok {
			if idx, exists := groupIdx[g]; exists {
				groups[idx].cats = append(groups[idx].cats, cat)
			} else {
				groupIdx[g] = len(groups)
				groups = append(groups, groupEntry{name: g, cats: []string{cat}})
			}
		} else {
			ungrouped = append(ungrouped, cat)
		}
	}

	fmt.Println("  log categories:")
	for _, g := range groups {
		fmt.Printf("    %s:\n", g.name)
		for _, cat := range g.cats {
			state := "OFF"
			if c.log.IsEnabled(cat) {
				state = "ON "
			}
			extra := ""
			if f, ok := filters[cat]; ok {
				extra = fmt.Sprintf("  filter: %s", f)
			}
			fmt.Printf("      [%s] %s%s\n", state, cat, extra)
		}
	}
	if len(ungrouped) > 0 {
		fmt.Println("    other:")
		for _, cat := range ungrouped {
			state := "OFF"
			if c.log.IsEnabled(cat) {
				state = "ON "
			}
			fmt.Printf("      [%s] %s\n", state, cat)
		}
	}
	fmt.Println()
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
// Priority: exact match > group expansion > prefix match.
func (c *Console) resolveCats(inputs []string) []string {
	allCats := c.log.Categories()
	allGroups := c.log.Groups()
	var result []string
	for _, input := range inputs {
		input = strings.ToLower(input)

		// 1. Exact category match
		exactFound := false
		for _, cat := range allCats {
			if cat == input {
				result = append(result, cat)
				exactFound = true
				break
			}
		}
		if exactFound {
			continue
		}

		// 2. Group match — expand to all subcategories
		groupFound := false
		for _, g := range allGroups {
			if g == input {
				result = append(result, c.log.CategoriesInGroup(g)...)
				groupFound = true
				break
			}
		}
		if groupFound {
			continue
		}

		// 3. Prefix match on category names
		for _, cat := range allCats {
			if strings.HasPrefix(cat, input) {
				result = append(result, cat)
				break
			}
		}
	}
	return result
}

// consoleCompleter implements readline.AutoCompleter.
type consoleCompleter struct {
	console *Console
}

func (cc *consoleCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	lineStr := string(line[:pos])
	parts := strings.Fields(lineStr)

	// If the line ends with a space, we're completing the next argument
	trailingSpace := len(lineStr) > 0 && lineStr[len(lineStr)-1] == ' '

	if len(parts) == 0 || (len(parts) == 1 && !trailingSpace) {
		// Complete command name
		prefix := ""
		if len(parts) == 1 {
			prefix = parts[0]
		}
		return cc.completePrefix(prefix)
	}

	// Completing an argument — delegate to the command's Complete function
	cmdName := parts[0]
	cmd, ok := cc.console.commands[cmdName]
	if !ok || cmd.Complete == nil {
		return nil, 0
	}

	// Build the args already typed (excluding the partial one being completed)
	var args []string
	if trailingSpace {
		args = parts[1:]
	} else {
		args = parts[1 : len(parts)-1]
	}

	candidates := cmd.Complete(args)
	if candidates == nil {
		return nil, 0
	}

	// Filter by prefix of current partial arg
	prefix := ""
	if !trailingSpace && len(parts) > 1 {
		prefix = parts[len(parts)-1]
	}

	return cc.filterCandidates(candidates, prefix)
}

func (cc *consoleCompleter) completePrefix(prefix string) ([][]rune, int) {
	// Collect unique command names (not aliases, but include aliases as completions)
	var candidates []string
	seen := make(map[string]bool)
	for name := range cc.console.commands {
		if !seen[name] {
			seen[name] = true
			candidates = append(candidates, name)
		}
	}
	// Also include category names for the toggle shortcut
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

// formatPerfOutput builds the console perf display from engine state.
// Must be called from the game loop goroutine.
func formatPerfOutput(eng *Engine) string {
	var b strings.Builder

	// Tick timing from TickProfile
	stats := eng.Perf.Stats()
	budgetMs := 1000.0 / float64(eng.Config.TickRate)
	t := stats.Total
	fmt.Fprintf(&b, "  Tick (%dHz, budget %.0fms):\n", eng.Config.TickRate, budgetMs)
	fmt.Fprintf(&b, "    avg %s  p50 %s  p95 %s  p99 %s  max %s\n",
		fmtDur(t.Avg), fmtDur(t.P50), fmtDur(t.P95), fmtDur(t.P99), fmtDur(t.Max))

	// Per-system breakdown
	if len(stats.Systems) > 0 {
		fmt.Fprintf(&b, "  Systems:\n")
		for i, sys := range stats.Systems {
			fmt.Fprintf(&b, "    %-20s avg %s  p95 %s\n", stats.SystemNames[i], fmtDur(sys.Avg), fmtDur(sys.P95))
		}
	}

	// Metrics (entities, network, load) — only if wired
	if eng.Metrics != nil {
		snap := eng.Metrics.Snapshot()
		e := snap.Entities
		fmt.Fprintf(&b, "  Entities: %d real, %d replica, %d ghost (%d total), %d players\n",
			e.Real, e.Replica, e.Ghost, e.Real+e.Replica+e.Ghost, e.Players)
		fmt.Fprintf(&b, "  Network: %d conns, sent %s, recv %s\n",
			snap.Network.Connections, fmtBytes(snap.Network.BytesSent), fmtBytes(snap.Network.BytesRecv))
		fmt.Fprintf(&b, "  Load: %.2f", snap.CompositeLoad)
		if snap.Tick.OverbudgetPct > 0 {
			fmt.Fprintf(&b, "  overbudget: %.1f%%", snap.Tick.OverbudgetPct*100)
		}
		if snap.Tick.EffectiveHz > 0 {
			fmt.Fprintf(&b, "  rate: %.1fHz", snap.Tick.EffectiveHz)
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

// fmtDur is an internal alias for FmtDuration.
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
