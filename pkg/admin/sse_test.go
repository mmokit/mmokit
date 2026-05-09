package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEWriter_DeliversEvents(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	w := newSSEWriter(rec, ctx, []string{"cells", "events"})

	if !w.Deliver("cells", map[string]int{"x": 1}, time.Now()) {
		t.Fatalf("Deliver returned false")
	}
	if !w.Deliver("events", "ping", time.Now()) {
		t.Fatalf("Deliver returned false")
	}
	cancel()
	w.Close()

	body := rec.Body.String()
	if !strings.Contains(body, "event: cells") {
		t.Fatalf("missing cells event:\n%s", body)
	}
	if !strings.Contains(body, "event: events") {
		t.Fatalf("missing events event:\n%s", body)
	}
	if !strings.Contains(body, `"x":1`) {
		t.Fatalf("missing payload:\n%s", body)
	}
}

func TestSSEWriter_FilterByTopic(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	w := newSSEWriter(rec, context.Background(), []string{"cells"})
	w.Deliver("cells", "ok", time.Now())
	w.Deliver("hosts", "should-not-appear", time.Now())
	w.Close()
	if strings.Contains(rec.Body.String(), "should-not-appear") {
		t.Fatalf("topic filter failed:\n%s", rec.Body.String())
	}
}
