package universe

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// handleCommitLogEvents serves GET /events — a JSON dump of recent
// CommitEvent entries. Query params:
//   - n=<N>: return the last N events (default 100)
//   - commit=<id>: return all events for a given commit ID
//   - since=<duration>: return events newer than time.Now() - duration (e.g. "5m")
//   - cell=<cellID>: return events affecting a specific cell
func handleCommitLogEvents(log *CommitLog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if log == nil {
			http.Error(w, "commit log not enabled", http.StatusServiceUnavailable)
			return
		}
		var events []CommitEvent
		q := r.URL.Query()
		switch {
		case q.Get("commit") != "":
			id, err := strconv.ParseUint(q.Get("commit"), 10, 64)
			if err != nil {
				http.Error(w, "bad commit: "+err.Error(), http.StatusBadRequest)
				return
			}
			events = log.ByCommitID(id)
		case q.Get("cell") != "":
			events = log.ByCell(q.Get("cell"))
		case q.Get("since") != "":
			d, err := time.ParseDuration(q.Get("since"))
			if err != nil {
				http.Error(w, "bad since: "+err.Error(), http.StatusBadRequest)
				return
			}
			events = log.Since(time.Now().Add(-d))
		default:
			n := 100
			if s := q.Get("n"); s != "" {
				if v, err := strconv.Atoi(s); err == nil && v > 0 {
					n = v
				}
			}
			events = log.Recent(n)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(events)
	}
}
