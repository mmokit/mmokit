package admin

import (
	"sync"
	"time"
)

// LogEntry is one captured log line. Host is "local" for the coordinator's
// own logs and the host_id for remote-host logs forwarded over MeshControl.
type LogEntry struct {
	Host string    `json:"host"`
	Cat  string    `json:"cat"`
	Msg  string    `json:"msg"`
	T    time.Time `json:"t"`
}

// LogRing is a bounded in-memory ring of LogEntry. Persistent storage is
// out of scope for v1; restart wipes history.
type LogRing struct {
	mu   sync.Mutex
	ring []LogEntry
	head int
	cap  int
}

// NewLogRing creates a LogRing with the given capacity. Defaults to 4096
// when capacity <= 0.
func NewLogRing(capacity int) *LogRing {
	if capacity <= 0 {
		capacity = 4096
	}
	return &LogRing{ring: make([]LogEntry, capacity), cap: capacity}
}

// Append records an entry, overwriting the oldest when the ring is full.
func (r *LogRing) Append(e LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ring[r.head] = e
	r.head = (r.head + 1) % r.cap
}

// Recent returns up to n most recent entries, newest first. Use n<=0 or
// n>cap to fetch everything currently buffered.
func (r *LogRing) Recent(n int) []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > r.cap {
		n = r.cap
	}
	out := make([]LogEntry, 0, n)
	idx := r.head - 1
	if idx < 0 {
		idx = r.cap - 1
	}
	for i := 0; i < n; i++ {
		e := r.ring[idx]
		if e.T.IsZero() {
			break
		}
		out = append(out, e)
		idx--
		if idx < 0 {
			idx = r.cap - 1
		}
	}
	return out
}
