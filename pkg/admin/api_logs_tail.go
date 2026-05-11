package admin

import (
	"net/http"
	"strconv"
)

// handleLogsRecent — GET /admin/api/logs/recent?n=N
//
// Returns the most recent N log entries (newest first), capped at the
// ring size. Default N = 200. Same auth as /admin/api/audit.
func (s *Server) handleLogsRecent(w http.ResponseWriter, r *http.Request) {
	if s.logRing == nil {
		writeJSON(w, http.StatusOK, []LogEntry{})
		return
	}
	n := 200
	if raw := r.URL.Query().Get("n"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			n = parsed
		}
	}
	writeJSON(w, http.StatusOK, s.logRing.Recent(n))
}
