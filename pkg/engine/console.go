package engine

import (
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"

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

// Console provides an interactive CLI for the server with readline support.
type Console struct {
	rl         *readline.Instance
	commands   map[string]*Command // name + aliases -> *Command
	cmdList    []*Command          // unique commands in registration order
	categories []string            // display order
	engine     *Engine
	log        *logger.Logger

	compMu      sync.RWMutex
	completions map[string][]string // thread-safe completion data
}

// NewConsole creates a new console with readline, redirects log output, and registers platform commands.
func NewConsole(eng *Engine, gameLog *logger.Logger) *Console {
	c := &Console{
		commands:    make(map[string]*Command),
		categories:  []string{"server", "logging", "admin", "config"},
		engine:      eng,
		log:         gameLog,
		completions: make(map[string][]string),
	}

	// Set static completions for log categories
	cats := gameLog.Categories()
	c.completions["categories"] = append(cats, "all")

	c.registerPlatformCommands()

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
}

// ExecOnGameLoop sends a closure to the game loop and waits for the result.
func (c *Console) ExecOnGameLoop(fn func() string) string {
	result := make(chan string, 1)
	c.engine.PendingAdminCmds <- func() {
		result <- fn()
	}
	return <-result
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
		Category: "server", Usage: "help", Description: "show this help",
		Fn: func(args []string) { c.printHelp() },
	})

	c.Register(Command{
		Name: "status", Aliases: []string{"s"},
		Category: "server", Usage: "status", Description: "show log categories on/off",
		Fn: func(args []string) { c.printStatus() },
	})

	c.Register(Command{
		Name: "quit", Aliases: []string{"q", "exit"},
		Category: "server", Usage: "quit", Description: "stop the server (Ctrl+C)",
		Fn: func(args []string) {
			fmt.Println("  use Ctrl+C to stop the server")
		},
	})

	catComplete := func(args []string) []string {
		return c.GetCompletions("categories")
	}

	c.Register(Command{
		Name: "on", Category: "logging",
		Usage: "on <cat|all>", Description: "enable log category",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: on <category|all>")
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

	c.Register(Command{
		Name: "off", Category: "logging",
		Usage: "off <cat|all>", Description: "disable log category",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: off <category|all>")
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

	c.Register(Command{
		Name: "toggle", Aliases: []string{"t"}, Category: "logging",
		Usage: "toggle <cat>", Description: "toggle log category",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: toggle <category>")
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

	c.Register(Command{
		Name: "only", Category: "logging",
		Usage: "only <cat> [cat...]", Description: "enable only these, disable rest",
		Complete: catComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: only <category> [category...]")
			} else {
				c.log.Disable(c.log.Categories()...)
				cats := c.resolveCats(args)
				c.log.Enable(cats...)
				fmt.Printf("  only: %s\n", strings.Join(cats, ", "))
			}
		},
	})
}

func (c *Console) printHelp() {
	fmt.Println()
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

	fmt.Printf("  Log categories: %s\n", strings.Join(c.log.Categories(), ", "))
	fmt.Println("  Tip: type a category name to toggle it")
	fmt.Println()
}

func (c *Console) printStatus() {
	fmt.Println("  log categories:")
	cats := c.log.Categories()
	sort.Strings(cats)
	for _, cat := range cats {
		state := "OFF"
		if c.log.IsEnabled(cat) {
			state = "ON "
		}
		fmt.Printf("    [%s] %s\n", state, cat)
	}
	fmt.Println()
}

// resolveCats matches input strings to known categories (prefix match).
func (c *Console) resolveCats(inputs []string) []string {
	allCats := c.log.Categories()
	var result []string
	for _, input := range inputs {
		input = strings.ToLower(input)
		for _, cat := range allCats {
			if cat == input || strings.HasPrefix(cat, input) {
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
