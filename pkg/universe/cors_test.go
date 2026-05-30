package universe

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestCORS_AllowlistedOrigin(t *testing.T) {
	h := corsMiddleware("http://localhost:5174, https://play.example.com", okHandler())

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.Header.Set("Origin", "http://localhost:5174")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5174" {
		t.Fatalf("ACAO = %q, want echoed origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("ACAC = %q, want true", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestCORS_Preflight(t *testing.T) {
	h := corsMiddleware("http://localhost:5174", okHandler())

	req := httptest.NewRequest("OPTIONS", "/auth/login", nil)
	req.Header.Set("Origin", "http://localhost:5174")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatalf("missing Access-Control-Allow-Methods on preflight")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("preflight body = %q, want empty", rec.Body.String())
	}
}

func TestCORS_DisallowedOrigin(t *testing.T) {
	h := corsMiddleware("http://localhost:5174", okHandler())

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO = %q, want empty for disallowed origin", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (request still served)", rec.Code)
	}
}

func TestCORS_EmptyAllowlistIsPassthrough(t *testing.T) {
	inner := okHandler()
	h := corsMiddleware("", inner)
	// http.HandlerFunc is uncomparable (func value), so compare the
	// underlying function pointers to assert identity-return.
	if reflect.ValueOf(h).Pointer() != reflect.ValueOf(inner).Pointer() {
		t.Fatalf("empty allowlist must return the inner handler unchanged")
	}
}
