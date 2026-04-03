package logger

import (
	"fmt"
	"log"
	"maps"
	"regexp"
	"strings"
	"sync"
)

// Logger provides category-based debug logging with hierarchical groups
// and per-category regex filters. Categories use "group:sub" naming
// (e.g. "mesh:transfer"). Enabling a group name enables all subcategories.
type Logger struct {
	mu         sync.RWMutex
	enabled    map[string]bool
	categories []string
	catSet     map[string]bool
	groups     []string
	groupSet   map[string]bool
	filters    map[string]*regexp.Regexp
	filterSrc  map[string]string // original pattern strings for display
}

// New creates a Logger. All categories in enabled list start on.
func New(enabled ...string) *Logger {
	l := &Logger{
		enabled:   make(map[string]bool),
		catSet:    make(map[string]bool),
		groupSet:  make(map[string]bool),
		filters:   make(map[string]*regexp.Regexp),
		filterSrc: make(map[string]string),
	}
	for _, cat := range enabled {
		l.enabled[cat] = true
	}
	return l
}

// RegisterCategories adds categories to the known set (deduplicates).
// Group prefixes (part before ':') are extracted automatically.
func (l *Logger) RegisterCategories(cats ...string) {
	l.mu.Lock()
	for _, cat := range cats {
		if !l.catSet[cat] {
			l.catSet[cat] = true
			l.categories = append(l.categories, cat)
		}
		if g, _, ok := strings.Cut(cat, ":"); ok && !l.groupSet[g] {
			l.groupSet[g] = true
			l.groups = append(l.groups, g)
		}
	}
	l.mu.Unlock()
}

// Categories returns a copy of all registered categories.
func (l *Logger) Categories() []string {
	l.mu.RLock()
	out := make([]string, len(l.categories))
	copy(out, l.categories)
	l.mu.RUnlock()
	return out
}

// Groups returns the known group prefixes (e.g. "mesh", "combat").
func (l *Logger) Groups() []string {
	l.mu.RLock()
	out := make([]string, len(l.groups))
	copy(out, l.groups)
	l.mu.RUnlock()
	return out
}

// CategoriesInGroup returns all categories with the given group prefix.
func (l *Logger) CategoriesInGroup(group string) []string {
	prefix := group + ":"
	l.mu.RLock()
	var out []string
	for _, cat := range l.categories {
		if strings.HasPrefix(cat, prefix) {
			out = append(out, cat)
		}
	}
	l.mu.RUnlock()
	return out
}

// Enable turns on logging for the given categories.
// If a name matches a known group (no colon), all subcategories are enabled.
func (l *Logger) Enable(cats ...string) {
	l.mu.Lock()
	for _, cat := range cats {
		if !strings.Contains(cat, ":") && l.groupSet[cat] {
			// Group expansion
			prefix := cat + ":"
			for _, c := range l.categories {
				if strings.HasPrefix(c, prefix) {
					l.enabled[c] = true
				}
			}
			continue
		}
		l.enabled[cat] = true
		if !l.catSet[cat] {
			l.catSet[cat] = true
			l.categories = append(l.categories, cat)
		}
		if g, _, ok := strings.Cut(cat, ":"); ok && !l.groupSet[g] {
			l.groupSet[g] = true
			l.groups = append(l.groups, g)
		}
	}
	l.mu.Unlock()
}

// Disable turns off logging for the given categories.
// If a name matches a known group (no colon), all subcategories are disabled.
func (l *Logger) Disable(cats ...string) {
	l.mu.Lock()
	for _, cat := range cats {
		if !strings.Contains(cat, ":") && l.groupSet[cat] {
			prefix := cat + ":"
			for _, c := range l.categories {
				if strings.HasPrefix(c, prefix) {
					delete(l.enabled, c)
				}
			}
			continue
		}
		delete(l.enabled, cat)
	}
	l.mu.Unlock()
}

// IsEnabled checks if a category is active.
func (l *Logger) IsEnabled(cat string) bool {
	l.mu.RLock()
	on := l.enabled[cat]
	l.mu.RUnlock()
	return on
}

// SetFilter sets a regex filter for a category. Only messages matching the
// pattern will be printed. If cat is a group name, the filter applies to all
// subcategories in that group.
func (l *Logger) SetFilter(cat string, pattern *regexp.Regexp, src string) {
	l.mu.Lock()
	if !strings.Contains(cat, ":") && l.groupSet[cat] {
		prefix := cat + ":"
		for _, c := range l.categories {
			if strings.HasPrefix(c, prefix) {
				l.filters[c] = pattern
				l.filterSrc[c] = src
			}
		}
	} else {
		l.filters[cat] = pattern
		l.filterSrc[cat] = src
	}
	l.mu.Unlock()
}

// ClearFilter removes filters. With no args, clears all filters.
// If a name matches a group, clears all subcategory filters.
func (l *Logger) ClearFilter(cats ...string) {
	l.mu.Lock()
	if len(cats) == 0 {
		l.filters = make(map[string]*regexp.Regexp)
		l.filterSrc = make(map[string]string)
	} else {
		for _, cat := range cats {
			if !strings.Contains(cat, ":") && l.groupSet[cat] {
				prefix := cat + ":"
				for _, c := range l.categories {
					if strings.HasPrefix(c, prefix) {
						delete(l.filters, c)
						delete(l.filterSrc, c)
					}
				}
			} else {
				delete(l.filters, cat)
				delete(l.filterSrc, cat)
			}
		}
	}
	l.mu.Unlock()
}

// Filters returns active filters as category → pattern string.
func (l *Logger) Filters() map[string]string {
	l.mu.RLock()
	out := make(map[string]string, len(l.filterSrc))
	maps.Copy(out, l.filterSrc)
	l.mu.RUnlock()
	return out
}

// EnableFromFlag parses a comma-separated string of categories/groups and enables them.
// Disables all existing categories first, then enables only the listed ones
// (plus any that were already enabled via New()). Blank input is a no-op.
// Supports group names: "combat,mesh" enables all combat:* and mesh:* subcategories.
func (l *Logger) EnableFromFlag(csv string) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return
	}
	l.Disable(l.Categories()...)
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			l.Enable(part)
		}
	}
}

// Log logs a message under a category. No-op if the category is disabled.
// If a filter is set for the category, only matching messages are printed.
func (l *Logger) Log(cat string, format string, args ...any) {
	l.mu.RLock()
	on := l.enabled[cat]
	f := l.filters[cat]
	l.mu.RUnlock()
	if !on {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if f != nil && !f.MatchString(msg) {
		return
	}
	log.Printf("[%s] %s", cat, msg)
}
