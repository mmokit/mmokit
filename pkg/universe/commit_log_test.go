package universe

import (
	"testing"
	"time"
)

func TestCommitLog_AppendAndRecent(t *testing.T) {
	l := newCommitLog(4, nil)
	l.Append(CommitEvent{CommitID: 1, Step: "a"})
	l.Append(CommitEvent{CommitID: 1, Step: "b"})
	got := l.Recent(10)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Step != "a" || got[1].Step != "b" {
		t.Fatalf("order wrong: %v", got)
	}
}

func TestCommitLog_RingEviction(t *testing.T) {
	l := newCommitLog(3, nil)
	for i := 1; i <= 5; i++ {
		l.Append(CommitEvent{CommitID: uint64(i), Step: "x"})
	}
	got := l.Recent(10)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3 (cap)", len(got))
	}
	// Oldest should be CommitID=3 (1 and 2 evicted).
	if got[0].CommitID != 3 || got[2].CommitID != 5 {
		t.Fatalf("eviction wrong: %v", got)
	}
}

func TestCommitLog_ByCommitID(t *testing.T) {
	l := newCommitLog(10, nil)
	l.Append(CommitEvent{CommitID: 7, Step: "a"})
	l.Append(CommitEvent{CommitID: 8, Step: "a"})
	l.Append(CommitEvent{CommitID: 7, Step: "b"})
	got := l.ByCommitID(7)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestCommitLog_Since(t *testing.T) {
	l := newCommitLog(10, nil)
	l.Append(CommitEvent{CommitID: 1, Timestamp: time.Unix(1000, 0)})
	l.Append(CommitEvent{CommitID: 2, Timestamp: time.Unix(2000, 0)})
	l.Append(CommitEvent{CommitID: 3, Timestamp: time.Unix(3000, 0)})
	got := l.Since(time.Unix(1500, 0))
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}
