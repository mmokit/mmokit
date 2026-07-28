package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// sseWriter is a Subscriber that fans events out to an HTTP client over
// the Server-Sent Events protocol. One sseWriter per dashboard tab.
//
// Deliver runs on the TopicBus dispatcher goroutine, not on the handler
// goroutine, and writes to the handler's http.ResponseWriter. s.mu orders
// concurrent Delivers against each other but cannot order them against
// net/http, which writes to the same connection once the handler returns. What
// keeps that safe is TopicBus.Unsubscribe joining the dispatcher before
// handleStream returns; s.closed only covers the post-write-error case.
type sseWriter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	ctx      context.Context
	topics   []string
	topicSet map[string]struct{}
	mu       sync.Mutex
	closed   bool
}

// newSSEWriter constructs a writer locked to the given topic filter. ctx is
// the request context — when it ends, Deliver short-circuits and the bus
// unsubscribes via the false return.
func newSSEWriter(w http.ResponseWriter, ctx context.Context, topics []string) *sseWriter {
	set := make(map[string]struct{}, len(topics))
	for _, t := range topics {
		set[t] = struct{}{}
	}
	flusher, _ := w.(http.Flusher)
	return &sseWriter{
		w:        w,
		flusher:  flusher,
		ctx:      ctx,
		topics:   append([]string(nil), topics...),
		topicSet: set,
	}
}

// writeHeaders emits the SSE response headers. Call before delivering events.
func (s *sseWriter) writeHeaders() {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering
	s.w.WriteHeader(http.StatusOK)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *sseWriter) Topics() []string { return s.topics }

func (s *sseWriter) Deliver(topic string, payload any, ts time.Time) bool {
	if s.ctx.Err() != nil {
		return false
	}
	if len(s.topicSet) > 0 {
		if _, ok := s.topicSet[topic]; !ok {
			return true // not for us; keep subscription
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return true // skip malformed payload but stay subscribed
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", topic, body); err != nil {
		s.closed = true
		return false
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return true
}

func (s *sseWriter) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}
