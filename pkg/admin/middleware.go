package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/services/auth"
)

type ctxKey int

const ctxKeyCaller ctxKey = 1

// requireSession is the middleware applied to every /admin/api/* route except
// /admin/api/auth/login. It looks up the session, builds a cmdsys.Caller from
// the operator's grants, and stores it on the request context.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := readSessionToken(r)
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "missing session")
			return
		}
		hash := auth.HashToken(token)
		rec, ok := s.sessions.Lookup(hash)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		// Sliding TTL bump (rate-limited to once per minute per session).
		if time.Since(rec.LastSeenAt) > time.Minute {
			_ = s.sessions.Touch(hash, s.cfg.SessionTTL)
		}
		caller := cmdsys.Caller{
			ID:     rec.Username,
			Source: cmdsys.SourceAdminHTTP,
			Grants: parseGrants(rec.Grants),
		}
		ctx := context.WithValue(r.Context(), ctxKeyCaller, caller)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func parseGrants(strs []string) []cmdsys.Grant {
	out := make([]cmdsys.Grant, 0, len(strs))
	for _, s := range strs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		allow := true
		if strings.HasPrefix(s, "!") {
			allow = false
			s = s[1:]
		}
		out = append(out, cmdsys.Grant{Pattern: s, Allow: allow})
	}
	return out
}

func callerFrom(r *http.Request) (cmdsys.Caller, bool) {
	c, ok := r.Context().Value(ctxKeyCaller).(cmdsys.Caller)
	return c, ok
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
