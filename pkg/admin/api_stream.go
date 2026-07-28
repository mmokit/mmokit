package admin

import (
	"net/http"
	"strings"
)

// handleStream — GET /admin/api/stream?topics=cells,hosts,events
//
// Single multiplexed SSE connection. The query string scopes which topics
// arrive; client demultiplexes by `event:` line.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	rawTopics := r.URL.Query().Get("topics")
	if rawTopics == "" {
		writeJSONError(w, http.StatusBadRequest, "topics= query param required")
		return
	}
	topics := splitNonEmpty(rawTopics, ",")

	writer := newSSEWriter(w, r.Context(), topics)
	writer.writeHeaders()
	s.bus.Subscribe(writer, topics...)
	// Unsubscribe blocks until the bus dispatcher has stopped. That is required,
	// not incidental: w is only legal to write to until this handler returns, and
	// the dispatcher writes to it from another goroutine. Never make this async.
	defer s.bus.Unsubscribe(writer)

	// Block until the client disconnects.
	<-r.Context().Done()
}

func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
