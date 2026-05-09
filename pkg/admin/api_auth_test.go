package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/services/auth"
)

type testLogger struct{}

func (testLogger) Log(string, string, ...any) {}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	hash, err := auth.HashPassword("p@ssw0rd!", auth.DefaultArgonParams())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		sessions: NewMemorySessionStore(),
		audit:    NewAuditLog(256),
		lockout:  NewLockout(5, 15*time.Minute),
		operators: map[string]OperatorConfig{
			"josh": {Username: "josh", PasswordHash: hash, Grants: []string{"*.*"}},
		},
		log: testLogger{},
		cfg: Config{
			SessionTTL: time.Hour,
			CookieOpts: defaultCookieOpts(),
		},
	}
}

func TestLogin_Success(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body, _ := json.Marshal(loginRequest{Username: "josh", Password: "p@ssw0rd!"})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), "admin_session=") {
		t.Fatalf("missing session cookie: %s", w.Header().Get("Set-Cookie"))
	}
}

func TestLogin_BadPassword(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body, _ := json.Marshal(loginRequest{Username: "josh", Password: "wrong"})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestLogin_UnknownUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body, _ := json.Marshal(loginRequest{Username: "nobody", Password: "x"})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
