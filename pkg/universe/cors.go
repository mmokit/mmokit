package universe

import (
	"net/http"
	"strings"
)

// corsMiddleware wraps next with credentialed-CORS handling for the
// gateway HTTP listener. originsCSV is a comma-separated allowlist of
// browser origins (e.g. "http://localhost:5174,https://play.example.com").
//
// For an allowlisted Origin the middleware echoes it in
// Access-Control-Allow-Origin (echo, not "*", is required alongside
// Access-Control-Allow-Credentials: true) and answers OPTIONS preflight
// with 204. Requests from non-allowlisted origins are passed through
// untouched (the browser blocks the response, but server logic still runs
// for same-origin/non-browser callers). When the allowlist is empty the
// inner handler is returned verbatim — zero overhead on the dev path.
func corsMiddleware(originsCSV string, next http.Handler) http.Handler {
	allowed := parseOrigins(originsCSV)
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// parseOrigins splits a comma-separated origin list into a set, trimming
// whitespace and dropping empties.
func parseOrigins(csv string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(csv, ",") {
		if s := strings.TrimSpace(p); s != "" {
			out[s] = true
		}
	}
	return out
}
