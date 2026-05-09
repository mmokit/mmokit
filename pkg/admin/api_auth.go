package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/zenion/mmoserver/pkg/services/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	User      string    `json:"user"`
	Grants    []string  `json:"grants"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "username and password required")
		return
	}

	// Check current lockout state without counting this attempt yet —
	// pass success=false but the limiter only locks on a NEW failure.
	allowed, retry := s.lockout.Check(r, false)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSONError(w, http.StatusTooManyRequests, "too many attempts")
		return
	}

	op, ok := s.operators[req.Username]
	if !ok {
		// already counted as a failure by the Check above
		s.audit.Append(AuditEntry{
			Username: req.Username, IP: remoteAddr(r).String(), Verb: "auth.login",
			OK: false, Error: "unknown user",
			StartedAt: time.Now(), FinishedAt: time.Now(),
		})
		s.log.Log("admin", "login-fail user=%s ip=%s reason=unknown-user", req.Username, remoteAddr(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	ok2, err := auth.VerifyPassword(req.Password, op.PasswordHash)
	if err != nil || !ok2 {
		s.audit.Append(AuditEntry{
			Username: req.Username, IP: remoteAddr(r).String(), Verb: "auth.login",
			OK: false, Error: "bad password",
			StartedAt: time.Now(), FinishedAt: time.Now(),
		})
		s.log.Log("admin", "login-fail user=%s ip=%s reason=bad-password", req.Username, remoteAddr(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Successful login — reset the IP failure counter.
	_, _ = s.lockout.Check(r, true)

	expiresAt := time.Now().Add(s.cfg.SessionTTL)
	rec := SessionRecord{
		Username:  req.Username,
		Grants:    op.Grants,
		IP:        remoteAddr(r).String(),
		UserAgent: r.UserAgent(),
		ExpiresAt: expiresAt,
	}
	token, _, err := s.sessions.Create(rec)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "session create failed")
		return
	}
	setSessionCookie(w, s.cfg.CookieOpts, token, s.cfg.SessionTTL)
	s.audit.Append(AuditEntry{
		Username: req.Username, IP: rec.IP, Verb: "auth.login",
		OK: true, StartedAt: time.Now(), FinishedAt: time.Now(),
	})
	s.log.Log("admin", "login user=%s ip=%s", req.Username, rec.IP)
	writeJSON(w, http.StatusOK, loginResponse{User: req.Username, Grants: op.Grants, ExpiresAt: expiresAt})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	tok := readSessionToken(r)
	if tok != "" {
		_ = s.sessions.Delete(auth.HashToken(tok))
	}
	clearSessionCookie(w, s.cfg.CookieOpts)
	caller, _ := callerFrom(r)
	s.audit.Append(AuditEntry{
		Username: caller.ID, IP: remoteAddr(r).String(), Verb: "auth.logout",
		OK: true, StartedAt: time.Now(), FinishedAt: time.Now(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFrom(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "no session")
		return
	}
	hash := auth.HashToken(readSessionToken(r))
	rec, _ := s.sessions.Lookup(hash)
	writeJSON(w, http.StatusOK, loginResponse{
		User:      caller.ID,
		Grants:    rec.Grants,
		ExpiresAt: rec.ExpiresAt,
	})
}
