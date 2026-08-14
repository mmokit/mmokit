package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/persist"
	"github.com/mmokit/mmokit/pkg/persist/persisttest"
	"github.com/mmokit/mmokit/pkg/services/auth"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	hash, err := auth.HashPassword("p@ssw0rd!", auth.DefaultArgonParams())
	if err != nil {
		t.Fatal(err)
	}
	repo := persisttest.NewAdminOperatorRepoMock()
	if err := repo.Create(context.Background(), &persist.AdminOperator{
		Username:     "josh",
		PasswordHash: hash,
		Grants:       []string{"*.*"},
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{
		sessions:     NewMemorySessionStore(),
		audit:        NewAuditLog(256),
		lockout:      NewLockout(5, 15*time.Minute),
		operatorRepo: repo,
		logger:       logger.New(),
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

func TestNewServer_SeedsDefaultOperator(t *testing.T) {
	t.Parallel()
	repo := persisttest.NewAdminOperatorRepoMock()

	// Build a minimal Server via NewServer so the seeding path fires.
	srv := NewServer(ServerOpts{
		SessionStore: NewMemorySessionStore(),
		Panels:       NewPanelRegistry(),
		Logger:       logger.New(),
		OperatorRepo: repo,
		Config:       Config{SessionTTL: time.Hour},
	})
	t.Cleanup(srv.Stop)

	got, err := repo.GetByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("default admin operator not seeded: %v", err)
	}
	if len(got.Grants) != 1 || got.Grants[0] != "*.*" {
		t.Errorf("default admin grants = %v, want [*.*]", got.Grants)
	}
	ok, verr := auth.VerifyPassword("admin", got.PasswordHash)
	if verr != nil {
		t.Fatalf("VerifyPassword: %v", verr)
	}
	if !ok {
		t.Errorf("default admin password should verify against 'admin'")
	}
}

func TestNewServer_DoesNotReseedWhenOperatorsExist(t *testing.T) {
	t.Parallel()
	repo := persisttest.NewAdminOperatorRepoMock()
	if err := repo.Create(context.Background(), &persist.AdminOperator{
		Username:     "alice",
		PasswordHash: "preexisting",
		Grants:       []string{"cell.*"},
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(ServerOpts{
		SessionStore: NewMemorySessionStore(),
		Panels:       NewPanelRegistry(),
		Logger:       logger.New(),
		OperatorRepo: repo,
		Config:       Config{SessionTTL: time.Hour},
	})
	t.Cleanup(srv.Stop)

	// "admin" should NOT have been seeded — non-empty table short-circuits.
	if _, err := repo.GetByUsername(context.Background(), "admin"); err == nil {
		t.Fatalf("seed fired even though table was non-empty")
	}
	// "alice" should still be there.
	if _, err := repo.GetByUsername(context.Background(), "alice"); err != nil {
		t.Fatalf("pre-existing alice operator vanished: %v", err)
	}
}

func TestNewServer_RegistersOperatorCommands(t *testing.T) {
	t.Parallel()
	repo := persisttest.NewAdminOperatorRepoMock()

	reg := cmdsys.NewRegistry()
	srv := NewServer(ServerOpts{
		SessionStore: NewMemorySessionStore(),
		Panels:       NewPanelRegistry(),
		Logger:       logger.New(),
		Registry:     reg,
		OperatorRepo: repo,
		Config:       Config{SessionTTL: time.Hour},
	})
	t.Cleanup(srv.Stop)

	for _, verb := range []string{
		"admin.operator.create",
		"admin.operator.delete",
		"admin.operator.password",
		"admin.operator.list",
	} {
		if _, ok := reg.Lookup(verb); !ok {
			t.Errorf("verb %q not registered", verb)
		}
	}
}
