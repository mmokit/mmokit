package admin

import (
	"sync"
	"time"
)

// AuditEntry records one admin-side action (login, logout, command invoke).
type AuditEntry struct {
	TraceID    string    `json:"traceId,omitempty"`
	Username   string    `json:"username"`
	IP         string    `json:"ip"`
	Verb       string    `json:"verb"`
	ArgsJSON   string    `json:"args,omitempty"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// AuditLog is a bounded in-memory ring. Postgres-backed log is a v2 task —
// the interface stays stable for swap-in.
type AuditLog struct {
	mu   sync.Mutex
	ring []AuditEntry
	head int
	cap  int
}

// NewAuditLog creates an AuditLog with the given ring capacity.
// Defaults to 4096 when capacity <= 0.
func NewAuditLog(capacity int) *AuditLog {
	if capacity <= 0 {
		capacity = 4096
	}
	return &AuditLog{ring: make([]AuditEntry, capacity), cap: capacity}
}

// Append records an entry, overwriting the oldest when the ring is full.
func (a *AuditLog) Append(e AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ring[a.head] = e
	a.head = (a.head + 1) % a.cap
}

// Recent returns up to n most recent entries, newest first.
func (a *AuditLog) Recent(n int) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n <= 0 || n > a.cap {
		n = a.cap
	}
	out := make([]AuditEntry, 0, n)
	idx := a.head - 1
	if idx < 0 {
		idx = a.cap - 1
	}
	for i := 0; i < n; i++ {
		e := a.ring[idx]
		if e.StartedAt.IsZero() {
			break
		}
		out = append(out, e)
		idx--
		if idx < 0 {
			idx = a.cap - 1
		}
	}
	return out
}
