package admin

import (
	"testing"
	"time"
)

func TestAuditLog_AppendRecent(t *testing.T) {
	t.Parallel()
	a := NewAuditLog(4)
	now := time.Now()
	for i := 0; i < 6; i++ {
		a.Append(AuditEntry{Verb: "v", Username: "u", StartedAt: now.Add(time.Duration(i) * time.Millisecond)})
	}
	got := a.Recent(10)
	if len(got) != 4 {
		t.Fatalf("expected 4 (capacity), got %d", len(got))
	}
	if !got[0].StartedAt.After(got[3].StartedAt) {
		t.Fatalf("expected newest-first ordering")
	}
}
