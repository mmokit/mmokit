package universe

import (
	"sync"
	"time"

	"github.com/mmokit/mmokit/pkg/logger"
)

// EventKind identifies the shape of a CommitEvent. Derived from the
// CommitKind for step-level events; distinct values for non-commit
// events (invariant violations, host joins, etc.).
type EventKind uint8

const (
	EventCommitSplit EventKind = iota
	EventCommitMerge
	EventCommitMigrate
	EventInvariantViolation
	EventHostJoin
	EventHostLeave
	EventSessionRouteRemap
)

func (k EventKind) String() string {
	switch k {
	case EventCommitSplit:
		return "split"
	case EventCommitMerge:
		return "merge"
	case EventCommitMigrate:
		return "migrate"
	case EventInvariantViolation:
		return "invariant"
	case EventHostJoin:
		return "host"
	case EventHostLeave:
		return "host"
	case EventSessionRouteRemap:
		return "session"
	default:
		return "unknown"
	}
}

// CommitEvent is the unit of the commit log — one append per plan
// step, one per invariant violation, one per host registry change.
//
// Diagnosing bugs in a distributed commit path is an exercise in
// filtering: the operator knows "split worked but merge didn't" and
// needs to narrow to the specific plan step that diverged. Every
// field below exists to make those queries trivial. If a new bug
// class requires a filter we don't support, add the field here rather
// than stuffing it into Context (which is slow to search).
type CommitEvent struct {
	// SeqNo is the process-local monotonic append counter. Use this
	// (not Timestamp) for ordering events within a single process.
	SeqNo uint64

	// Timestamp is the host's wall clock at append time. In a multi-
	// host deployment this DRIFTS across hosts (ntp skew, clock jumps) —
	// do NOT use for cross-host ordering; use CommitID or the
	// coordinator's SeqNo for that. Kept as time.Time for human-
	// readable display; converted/filtered numerically in queries.
	Timestamp time.Time

	// CommitID identifies the specific commit this event belongs to.
	// All steps of one Split/Merge/Migrate share a CommitID. Filter on
	// this to replay the full step sequence for a single commit.
	CommitID uint64

	// Kind distinguishes event families (commit-step vs invariant vs
	// host-event vs session-remap). Primary filter axis.
	Kind EventKind

	// Scenario identifies which commit scenario produced this event —
	// CommitKindSplit/Merge/Migrate (defined in Phase B). Zero-value
	// for non-commit events (invariant violations, host joins); readers
	// filter those out by checking Kind first. This is the single most
	// useful field when diagnosing asymmetric bugs — see T&T lessons
	// at top of plan ("asymmetric symptoms are the diagnosis").
	Scenario CommitKind

	// StepIndex is 0-based position within the plan. 0 = first step,
	// len(plan)-1 = last step; -1 for non-step events (invariant
	// violations, commit begin/end markers). Filter by step_index=3
	// across many commits to find out which step is the consistently-
	// broken one.
	StepIndex int

	// Step is the PlanStep name (e.g. "apply-cell-to-host-map",
	// "release-parent-cell"). Human-readable label for display.
	Step string

	// Success is true when the step completed without error (or the
	// invariant held). Invariant-violation events set Success=false
	// with Error populated.
	Success bool

	// DurationMs is the wall time spent in the step. Useful for
	// identifying slow steps under load; not used for ordering.
	DurationMs int64

	// Affected is the list of cell IDs touched by this event (for
	// filtering by cell). HostIDs is the list of hosts touched.
	Affected []string
	HostIDs  []string

	// Error is the failure message when Success=false; empty otherwise.
	Error string

	// Context is a free-form bag of key/value strings. Use it for
	// one-off diagnostic data that doesn't merit a top-level field;
	// graduate frequently-queried fields to the struct.
	Context map[string]string
}

// CommitLog is a bounded in-memory ring of CommitEvents. Thread-safe.
// Append also fans out to the category logger so operators can tail
// events live via `log events:<kind>`.
type CommitLog struct {
	mu     sync.RWMutex
	ring   []CommitEvent
	head   int
	size   int
	cap    int
	seq    uint64
	logger *logger.Logger

	subMu sync.Mutex
	subs  []chan CommitEvent
}

func newCommitLog(cap int, log *logger.Logger) *CommitLog {
	if cap < 1 {
		cap = 1
	}
	return &CommitLog{
		ring:   make([]CommitEvent, cap),
		cap:    cap,
		logger: log,
	}
}

func (l *CommitLog) Append(e CommitEvent) {
	l.mu.Lock()
	l.seq++
	e.SeqNo = l.seq
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	l.ring[l.head] = e
	l.head = (l.head + 1) % l.cap
	if l.size < l.cap {
		l.size++
	}
	l.mu.Unlock()

	// Fan out to live subscribers. Drop on slow consumers — admin
	// streams are observability, not load-bearing.
	l.subMu.Lock()
	for _, ch := range l.subs {
		select {
		case ch <- e:
		default:
		}
	}
	l.subMu.Unlock()

	if l.logger != nil {
		category := "events:" + e.Kind.String()
		status := "✓"
		if !e.Success && e.Kind != EventInvariantViolation {
			status = "✗"
		}
		l.logger.Log(category,
			"%s cid=%d step=%s dur=%dms affected=%v err=%s",
			status, e.CommitID, e.Step, e.DurationMs, e.Affected, e.Error)
	}
}

func (l *CommitLog) Recent(n int) []CommitEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if n > l.size {
		n = l.size
	}
	out := make([]CommitEvent, 0, n)
	// Ring is in append order; oldest is at (head - size) mod cap.
	start := (l.head - l.size + l.cap) % l.cap
	for i := l.size - n; i < l.size; i++ {
		out = append(out, l.ring[(start+i)%l.cap])
	}
	return out
}

func (l *CommitLog) ByCommitID(id uint64) []CommitEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []CommitEvent
	start := (l.head - l.size + l.cap) % l.cap
	for i := 0; i < l.size; i++ {
		e := l.ring[(start+i)%l.cap]
		if e.CommitID == id {
			out = append(out, e)
		}
	}
	return out
}

func (l *CommitLog) ByCell(cellID string) []CommitEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []CommitEvent
	start := (l.head - l.size + l.cap) % l.cap
	for i := 0; i < l.size; i++ {
		e := l.ring[(start+i)%l.cap]
		for _, c := range e.Affected {
			if c == cellID {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func (l *CommitLog) Since(t time.Time) []CommitEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []CommitEvent
	start := (l.head - l.size + l.cap) % l.cap
	for i := 0; i < l.size; i++ {
		e := l.ring[(start+i)%l.cap]
		if e.Timestamp.After(t) || e.Timestamp.Equal(t) {
			out = append(out, e)
		}
	}
	return out
}

// Subscribe registers a channel to receive every newly-appended CommitEvent.
// The returned channel is buffered (default 64); on overflow the bus drops
// the event for that subscriber. Used by pkg/admin to stream events live.
//
// Caller MUST eventually call Unsubscribe(ch) to release resources.
func (l *CommitLog) Subscribe() <-chan CommitEvent {
	ch := make(chan CommitEvent, 64)
	l.subMu.Lock()
	l.subs = append(l.subs, ch)
	l.subMu.Unlock()
	return ch
}

// Unsubscribe removes a channel previously registered via Subscribe and closes it.
func (l *CommitLog) Unsubscribe(ch <-chan CommitEvent) {
	l.subMu.Lock()
	defer l.subMu.Unlock()
	for i, c := range l.subs {
		if (<-chan CommitEvent)(c) == ch {
			l.subs = append(l.subs[:i], l.subs[i+1:]...)
			close(c)
			return
		}
	}
}
