package logger

import (
	"fmt"
	"log"
	"sync"
)

// Event categories for toggling.
const (
	CatCombat    = "combat"    // projectile hits, damage dealt
	CatKill      = "kill"      // entity destroyed
	CatMining    = "mining"    // mining start/stop, resource gained
	CatConnect   = "connect"   // player connect/disconnect
	CatSpawn     = "spawn"     // entity spawn/despawn
	CatCollision = "collision" // terrain collisions
	CatInput     = "input"     // player input received
	CatPhysics   = "physics"   // physics events (wrapping, etc.)
	CatEconomy   = "economy"   // sell, loot pickup
	CatChat      = "chat"      // player chat messages
)

// AllCategories lists every known category for use by the interactive console.
var AllCategories = []string{
	CatCombat,
	CatKill,
	CatMining,
	CatConnect,
	CatSpawn,
	CatCollision,
	CatInput,
	CatPhysics,
	CatEconomy,
	CatChat,
}

// Logger provides category-based debug logging.
type Logger struct {
	mu       sync.RWMutex
	enabled  map[string]bool
	prefix   string
}

// New creates a Logger. All categories in enabled list start on.
func New(enabled ...string) *Logger {
	l := &Logger{
		enabled: make(map[string]bool),
	}
	for _, cat := range enabled {
		l.enabled[cat] = true
	}
	return l
}

// Enable turns on logging for the given categories.
func (l *Logger) Enable(cats ...string) {
	l.mu.Lock()
	for _, cat := range cats {
		l.enabled[cat] = true
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

// Categories returns all currently enabled categories.
func (l *Logger) Categories() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	cats := make([]string, 0, len(l.enabled))
	for cat := range l.enabled {
		cats = append(cats, cat)
	}
	return cats
}
