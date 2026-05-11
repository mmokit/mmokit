package admin

import (
	"sync"
	"testing"
	"time"
)

func TestLogRing_AppendRecent(t *testing.T) {
	r := NewLogRing(4)
	r.Append(LogEntry{Host: "h1", Cat: "c", Msg: "1", T: time.Now()})
	r.Append(LogEntry{Host: "h1", Cat: "c", Msg: "2", T: time.Now()})

	got := r.Recent(10)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	// Newest first.
	if got[0].Msg != "2" || got[1].Msg != "1" {
		t.Fatalf("order = %v %v, want 2 1", got[0].Msg, got[1].Msg)
	}
}

func TestLogRing_Wraparound(t *testing.T) {
	r := NewLogRing(3)
	for i := 0; i < 5; i++ {
		r.Append(LogEntry{Msg: string(rune('a' + i)), T: time.Now()})
	}
	got := r.Recent(10)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (cap)", len(got))
	}
	// Newest-first over wrap: e, d, c (a + b overwritten).
	if got[0].Msg != "e" || got[1].Msg != "d" || got[2].Msg != "c" {
		t.Fatalf("wraparound order = %v %v %v, want e d c", got[0].Msg, got[1].Msg, got[2].Msg)
	}
}

func TestLogRing_RecentN(t *testing.T) {
	r := NewLogRing(10)
	for i := 0; i < 5; i++ {
		r.Append(LogEntry{Msg: string(rune('a' + i)), T: time.Now()})
	}
	got := r.Recent(2)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	if got[0].Msg != "e" || got[1].Msg != "d" {
		t.Fatalf("Recent(2) = %v %v, want e d", got[0].Msg, got[1].Msg)
	}
}

func TestLogRing_DefaultCap(t *testing.T) {
	r := NewLogRing(0)
	if r.cap != 4096 {
		t.Fatalf("cap=%d want 4096", r.cap)
	}
}

func TestLogRing_ConcurrentAppend(t *testing.T) {
	r := NewLogRing(1024)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Append(LogEntry{Msg: "x", T: time.Now()})
			}
		}()
	}
	wg.Wait()
	got := r.Recent(0)
	// 800 appends, cap 1024 — none should have wrapped.
	if len(got) != 800 {
		t.Fatalf("len=%d want 800", len(got))
	}
}
