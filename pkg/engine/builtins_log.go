package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/logger"
)

// ── arg/result types ─────────────────────────────────────────────────────────

type logStatusArgs struct{}

type logStatusResult struct {
	Output string
}

type logOnArgs struct {
	Category string `cmd:"help=category name or 'all'"`
}

type logOnResult struct {
	Enabled []string
}

type logOffArgs struct {
	Category string `cmd:"help=category name or 'all'"`
}

type logOffResult struct {
	Disabled []string
}

type logToggleArgs struct {
	Category string `cmd:"help=category name"`
}

type logToggleResult struct {
	Category string
	Enabled  bool
}

type logOnlyArgs struct {
	Categories string `cmd:"help=space-separated category names to keep on"`
}

type logOnlyResult struct {
	Enabled []string
}

type logFilterArgs struct {
	Args string `cmd:"optional,help=cat pattern | clear [cat]"`
}

type logFilterResult struct {
	Output string
}

// ── registration ─────────────────────────────────────────────────────────────

func registerLogBuiltins(reg *cmdsys.Registry, log *logger.Logger) error {
	mustReg := func(cmd cmdsys.Command) {
		if err := reg.Register(cmd); err != nil {
			panic(fmt.Sprintf("console: registerLogBuiltins %q: %v", cmd.Verb, err))
		}
	}

	mustReg(cmdsys.Command{
		Verb:        "log.status",
		Capability:  "log.status",
		Description: "show log categories on/off",
		Route:       cmdsys.RouteLocal,
		Args:        logStatusArgs{},
		Result:      logStatusResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			return logStatusResult{Output: buildLogStatus(log)}, nil
		},
	})

	mustReg(cmdsys.Command{
		Verb:        "log.on",
		Capability:  "log.on",
		Description: "enable a log category (or 'all')",
		Usage:       "log on <cat|all>",
		Examples:    []string{"log on combat", "log on all", "log on combat mining economy"},
		Route:       cmdsys.RouteLocal,
		Args:        logOnArgs{},
		Result:      logOnResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(logOnArgs)
			cat := strings.TrimSpace(args.Category)
			if cat == "all" {
				log.Enable(log.Categories()...)
				return logOnResult{Enabled: log.Categories()}, nil
			}
			resolved := resolveCatsFromLog(log, strings.Fields(cat))
			if len(resolved) > 0 {
				log.Enable(resolved...)
			}
			return logOnResult{Enabled: resolved}, nil
		},
	})

	mustReg(cmdsys.Command{
		Verb:        "log.off",
		Capability:  "log.off",
		Description: "disable a log category (or 'all')",
		Usage:       "log off <cat|all>",
		Examples:    []string{"log off combat", "log off all"},
		Route:       cmdsys.RouteLocal,
		Args:        logOffArgs{},
		Result:      logOffResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(logOffArgs)
			cat := strings.TrimSpace(args.Category)
			if cat == "all" {
				log.Disable(log.Categories()...)
				return logOffResult{Disabled: log.Categories()}, nil
			}
			resolved := resolveCatsFromLog(log, strings.Fields(cat))
			if len(resolved) > 0 {
				log.Disable(resolved...)
			}
			return logOffResult{Disabled: resolved}, nil
		},
	})

	mustReg(cmdsys.Command{
		Verb:        "log.toggle",
		Capability:  "log.toggle",
		Description: "toggle a log category on/off",
		Examples:    []string{"log toggle combat"},
		Route:       cmdsys.RouteLocal,
		Args:        logToggleArgs{},
		Result:      logToggleResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(logToggleArgs)
			cat := strings.TrimSpace(args.Category)
			resolved := resolveCatsFromLog(log, []string{cat})
			if len(resolved) == 0 {
				return nil, fmt.Errorf("unknown category: %s", cat)
			}
			name := resolved[0]
			enabled := !log.IsEnabled(name)
			if enabled {
				log.Enable(name)
			} else {
				log.Disable(name)
			}
			return logToggleResult{Category: name, Enabled: enabled}, nil
		},
	})

	mustReg(cmdsys.Command{
		Verb:        "log.only",
		Capability:  "log.only",
		Description: "enable only specified categories, disable all others",
		Usage:       "log only <cat> [cat...]",
		Examples:    []string{"log only combat", "log only combat mining"},
		Route:       cmdsys.RouteLocal,
		Args:        logOnlyArgs{},
		Result:      logOnlyResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(logOnlyArgs)
			inputs := strings.Fields(args.Categories)
			log.Disable(log.Categories()...)
			resolved := resolveCatsFromLog(log, inputs)
			if len(resolved) > 0 {
				log.Enable(resolved...)
			}
			return logOnlyResult{Enabled: resolved}, nil
		},
	})

	mustReg(cmdsys.Command{
		Verb:        "log.filter",
		Capability:  "log.filter",
		Description: "set/show/clear message filters: log.filter <cat> <pattern> | log.filter clear [cat]",
		Usage:       "log filter [cat pattern | clear [cat]]",
		Examples:    []string{"log filter combat \"laser hit\"", "log filter clear", "log filter clear combat"},
		Route:       cmdsys.RouteLocal,
		Args:        logFilterArgs{},
		Result:      logFilterResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(logFilterArgs)
			parts := strings.Fields(strings.TrimSpace(args.Args))

			if len(parts) == 0 {
				filters := log.Filters()
				if len(filters) == 0 {
					return logFilterResult{Output: "  no active filters\n"}, nil
				}
				var sb strings.Builder
				sb.WriteString("  active filters:\n")
				for cat, pat := range filters {
					fmt.Fprintf(&sb, "    %s: %s\n", cat, pat)
				}
				return logFilterResult{Output: sb.String()}, nil
			}

			if parts[0] == "clear" {
				if len(parts) > 1 {
					cats := resolveCatsFromLog(log, parts[1:])
					log.ClearFilter(cats...)
					return logFilterResult{Output: fmt.Sprintf("  cleared filters: %s\n", strings.Join(cats, ", "))}, nil
				}
				log.ClearFilter()
				return logFilterResult{Output: "  all filters cleared\n"}, nil
			}

			if len(parts) < 2 {
				return nil, fmt.Errorf("usage: log.filter <category> <pattern>\n       log.filter clear [category]")
			}

			cat := parts[0]
			pattern := strings.Join(parts[1:], " ")
			src := pattern
			if len(pattern) >= 2 && pattern[0] == '/' && pattern[len(pattern)-1] == '/' {
				pattern = pattern[1 : len(pattern)-1]
			} else {
				pattern = regexp.QuoteMeta(pattern)
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid regex: %w", err)
			}
			cats := resolveCatsFromLog(log, []string{cat})
			if len(cats) == 0 {
				return nil, fmt.Errorf("unknown category: %s", cat)
			}
			for _, resolved := range cats {
				log.SetFilter(resolved, re, src)
			}
			return logFilterResult{Output: fmt.Sprintf("  filter set on %s: %s\n", strings.Join(cats, ", "), src)}, nil
		},
	})

	return nil
}

// resolveCatsFromLog resolves input strings to known log categories.
func resolveCatsFromLog(log *logger.Logger, inputs []string) []string {
	allCats := log.Categories()
	allGroups := log.Groups()
	var result []string
	for _, input := range inputs {
		input = strings.ToLower(input)
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
		groupFound := false
		for _, g := range allGroups {
			if g == input {
				result = append(result, log.CategoriesInGroup(g)...)
				groupFound = true
				break
			}
		}
		if groupFound {
			continue
		}
		for _, cat := range allCats {
			if strings.HasPrefix(cat, input) {
				result = append(result, cat)
				break
			}
		}
	}
	return result
}

// buildLogStatus formats log category status as a string (mirrors printStatus).
func buildLogStatus(log *logger.Logger) string {
	cats := log.Categories()
	filters := log.Filters()
	type groupEntry struct {
		name string
		cats []string
	}
	var groups []groupEntry
	groupIdx := make(map[string]int)
	var ungrouped []string
	sorted := make([]string, len(cats))
	copy(sorted, cats)
	// stable sort by name
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
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
	var sb strings.Builder
	sb.WriteString("  log categories:\n")
	for _, g := range groups {
		fmt.Fprintf(&sb, "    %s:\n", g.name)
		for _, cat := range g.cats {
			state := "OFF"
			if log.IsEnabled(cat) {
				state = "ON "
			}
			extra := ""
			if f, ok := filters[cat]; ok {
				extra = fmt.Sprintf("  filter: %s", f)
			}
			fmt.Fprintf(&sb, "      [%s] %s%s\n", state, cat, extra)
		}
	}
	if len(ungrouped) > 0 {
		sb.WriteString("    other:\n")
		for _, cat := range ungrouped {
			state := "OFF"
			if log.IsEnabled(cat) {
				state = "ON "
			}
			fmt.Fprintf(&sb, "      [%s] %s\n", state, cat)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}
