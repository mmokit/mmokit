package logger

import (
	"fmt"
	"log"
	"sync"
)

// Logger provides category-based debug logging with dynamic registration.
type Logger struct {
	mu         sync.RWMutex
	enabled    map[string]bool
	categories []string
	catSet     map[string]bool
}

// New creates a Logger. All categories in enabled list start on.
func New(enabled ...string) *Logger {
	l := &Logger{
		enabled: make(map[string]bool),
		catSet:  make(map[string]bool),
	}
	for _, cat := range enabled {
		l.enabled[cat] = true
	}
	return l
}

// RegisterCategories adds categories to the known set (deduplicates).
func (l *Logger) RegisterCategories(cats ...string) {
	l.mu.Lock()
	for _, cat := range cats {
		if !l.catSet[cat] {
			l.catSet[cat] = true
			l.categories = append(l.categories, cat)
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

// Enable turns on logging for the given categories.
func (l *Logger) Enable(cats ...string) {
	l.mu.Lock()
	for _, cat := range cats {
		l.enabled[cat] = true
		// Auto-register unknown categories
		if !l.catSet[cat] {
			l.catSet[cat] = true
			l.categories = append(l.categories, cat)
		}
	}
	l.mu.Unlock()
}

// Disable turns off logging for the given categories.
func (l *Logger) Disable(cats ...string) {
	l.mu.Lock()
	for _, cat := range cats {
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

// Log logs a message under a category. No-op if the category is disabled.
func (l *Logger) Log(cat string, format string, args ...any) {
	if !l.IsEnabled(cat) {
		return
	}
	log.Printf("[%s] %s", cat, fmt.Sprintf(format, args...))
}
