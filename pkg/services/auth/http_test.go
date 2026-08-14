package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/service"
)

func newHTTPTestService(t *testing.T) (*Service, *inMemRepo, *http.ServeMux, HTTPOpts) {
	t.Helper()
	repo := newInMemRepo()
	opts := DefaultServiceOpts()
	opts.Repository = repo
	s := newService(&service.Context{
		KindName: "auth", InstanceID: "test-0",
		Logger: logger.New(), Roles: map[string]struct{}{"service": {}},
	}, opts).(*Service)
	if err := s.Init(s.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	mux := http.NewServeMux()
	httpOpts := DefaultHTTPOpts()
	httpOpts.CookieSecure = false // local test, no TLS
	s.RegisterHTTP(mux, httpOpts)
	return s, repo, mux, httpOpts
}

func TestHTTPRegisterSetsCookieAndOmitsTokenFromBody(t *testing.T) {
	_, _, mux, opts := newHTTPTestService(t)
	body, _ := json.Marshal(registerRequestJSON{Username: "alice", Password: "hunter22"})
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var sess *http.Cookie
	for _, c := range cookies {
		if c.Name == opts.CookieName {
			sess = c
		}
	}
	if sess == nil {
		t.Fatalf("Set-Cookie %q missing; got %v", opts.CookieName, cookies)
	}
	if !sess.HttpOnly {
		t.Errorf("HttpOnly flag not set")
	}
	if sess.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite: got %v, want Strict", sess.SameSite)
	}
	if sess.Value == "" {
		t.Error("cookie value empty")
	}
	if bytes.Contains(w.Body.Bytes(), []byte("session_token")) {
		t.Errorf("body leaked session_token: %s", w.Body.String())
	}
}

func TestHTTPLoginThenMeRoundTrip(t *testing.T) {
	_, _, mux, _ := newHTTPTestService(t)
	regBody, _ := json.Marshal(registerRequestJSON{Username: "bob", Password: "hunter22"})
	regReq := httptest.NewRequest("POST", "/auth/register", bytes.NewReader(regBody))
	regW := httptest.NewRecorder()
	mux.ServeHTTP(regW, regReq)
	cookies := regW.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie from register")
	}

	meReq := httptest.NewRequest("GET", "/auth/me", nil)
	for _, c := range cookies {
		meReq.AddCookie(c)
	}
	meW := httptest.NewRecorder()
	mux.ServeHTTP(meW, meReq)
	if meW.Code != http.StatusOK {
		t.Fatalf("/auth/me status: got %d, body=%s", meW.Code, meW.Body.String())
	}
	var resp loginResponseJSON
	if err := json.NewDecoder(meW.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Username != "bob" {
		t.Fatalf("username mismatch: %s", resp.Username)
	}
}

func TestHTTPMeWithNoCookieReturns401(t *testing.T) {
	_, _, mux, _ := newHTTPTestService(t)
	req := httptest.NewRequest("GET", "/auth/me", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d", w.Code)
	}
}

func TestHTTPLogoutClearsCookie(t *testing.T) {
	_, _, mux, opts := newHTTPTestService(t)
	regBody, _ := json.Marshal(registerRequestJSON{Username: "carol", Password: "hunter22"})
	regReq := httptest.NewRequest("POST", "/auth/register", bytes.NewReader(regBody))
	regW := httptest.NewRecorder()
	mux.ServeHTTP(regW, regReq)
	cookies := regW.Result().Cookies()

	logoutReq := httptest.NewRequest("POST", "/auth/logout", nil)
	for _, c := range cookies {
		logoutReq.AddCookie(c)
	}
	logoutW := httptest.NewRecorder()
	mux.ServeHTTP(logoutW, logoutReq)
	if logoutW.Code != http.StatusOK {
		t.Fatalf("logout status: %d", logoutW.Code)
	}
	cleared := false
	for _, c := range logoutW.Result().Cookies() {
		if c.Name == opts.CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout did not clear the cookie")
	}

	// Subsequent /auth/me with the original cookie should now 401
	// (server-side session was revoked).
	meReq := httptest.NewRequest("GET", "/auth/me", nil)
	for _, c := range cookies {
		meReq.AddCookie(c)
	}
	meW := httptest.NewRecorder()
	mux.ServeHTTP(meW, meReq)
	if meW.Code != http.StatusUnauthorized {
		t.Fatalf("/auth/me after logout: got %d", meW.Code)
	}
}

func TestHTTPLoginInvalidCredentialsReturns400(t *testing.T) {
	_, _, mux, _ := newHTTPTestService(t)
	body, _ := json.Marshal(loginRequestJSON{Username: "ghost", Password: "x"})
	req := httptest.NewRequest("POST", "/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d", w.Code)
	}
}
